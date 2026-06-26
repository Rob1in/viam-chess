package viamchess

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/corentings/chess/v2"
	"github.com/mitchellh/mapstructure"

	"go.viam.com/rdk/vision/viscapture"
	"go.viam.com/utils/trace"
)

type MoveCmd struct {
	From, To string
	N        int
}

// hoverPickedCenterCmd is the payload of the "hover_over_picked_center" doCommand.
// For each square in Squares the arm hovers 20mm above the estimated pickup
// center of the piece there, dwells DwellSeconds (so you can eyeball it), then
// moves to the next. Raises to home between squares to clear taller pieces.
type hoverPickedCenterCmd struct {
	Squares      []string // required, e.g. ["e4","d5"]
	Method       string   // "top_n" | "top_band" | "highest_midpoint"; empty = config default
	N            int      // for top_n; <=0 = default 5
	BandMM       float64  `mapstructure:"band_mm"`       // for top_band; <=0 = default 20mm
	DwellSeconds float64  `mapstructure:"dwell_seconds"` // dwell per square; <=0 = default 10s

	// SquareInset overrides the per-square segmentation inset (px) for this
	// capture. nil = the piece-finder's configured inset. Negative enlarges the
	// bounds (capture more of a leaning piece top); positive shrinks.
	SquareInset *float64 `mapstructure:"square_inset"`
	// FilterBlob keeps only the most central, biggest blob of points per square
	// before estimating the center — useful with an enlarged bound, which may
	// pull in a neighbouring piece's points.
	FilterBlob bool `mapstructure:"filter_blob"`
	// ClusterRadiusMM is the XY connectivity radius for FilterBlob; <=0 = 8mm.
	ClusterRadiusMM float64 `mapstructure:"cluster_radius_mm"`
}

type cmdStruct struct {
	Move            MoveCmd
	Go              int
	Reset           bool
	Wipe            bool
	Difficulty      int
	Hover           string
	ClearCache      bool `mapstructure:"clear-cache"`
	Undo            int
	PlayFEN         string `mapstructure:"play-fen"`
	BoardSnapshot   bool   `mapstructure:"board-snapshot"`
	GameEvents      bool   `mapstructure:"game-events"`
	CompanionConfig bool   `mapstructure:"companion-config"`
	Auto            *bool  // pointer so explicit false is distinguishable from absent
	SetAnnounce     *bool  `mapstructure:"set-announce"` // pointer so explicit false is distinguishable from absent

	HoverPickedCenter *hoverPickedCenterCmd `mapstructure:"hover_over_picked_center"`
}

func (s *viamChessChess) DoCommand(ctx context.Context, cmdMap map[string]interface{}) (map[string]interface{}, error) {
	s.doCommandCount.Add(1)
	ctx, span := trace.StartSpan(ctx, "chess::DoCommand")
	defer span.End()

	// board-snapshot fast path: serve from cache without blocking on doCommandLock.
	// The board loop holds doCommandLock during makeAMove, so without this early
	// return polling clients would see stale state for the entire arm movement.
	if bs, _ := cmdMap["board-snapshot"].(bool); bs {
		s.boardCache.mu.RLock()
		if s.boardCache.ready {
			result := map[string]interface{}{
				"fen":             s.boardCache.fen,
				"camera_board":    s.boardCache.cameraBoard,
				"white_graveyard": s.boardCache.whiteGraveyard,
				"black_graveyard": s.boardCache.blackGraveyard,
				"auto":            s.autoEnabled.Load(),
				"captured_at_ms":  s.boardCache.capturedAt.UnixMilli(),
				"event":           s.boardCache.gameEvents.Event,
				"outcome":         s.boardCache.gameEvents.Outcome,
				"method":          s.boardCache.gameEvents.Method,
				"turn":            s.boardCache.gameEvents.Turn,
				"in_check":        s.boardCache.gameEvents.InCheck,
				"is_over":         s.boardCache.gameEvents.IsOver,
				"score_cp":        s.boardCache.gameEvents.ScoreCP,
				"score_mate":      s.boardCache.gameEvents.ScoreMate,
			}
			s.boardCache.mu.RUnlock()
			return result, nil
		}
		s.boardCache.mu.RUnlock()
	}

	s.doCommandLock.Lock()
	defer s.doCommandLock.Unlock()

	var cmd cmdStruct
	err := mapstructure.Decode(cmdMap, &cmd)
	if err != nil {
		return nil, err
	}

	if cmd.Wipe {
		s.clearSquareCache()
		err := s.wipe(ctx)
		s.invalidateBoardCache()
		return nil, err
	}
	if cmd.ClearCache {
		s.clearSquareCache()
		return nil, nil
	}
	if cmd.Difficulty != 0 {
		applied, err := s.applyElo(cmd.Difficulty)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"difficulty": applied}, nil
	}
	if cmd.Auto != nil {
		s.autoEnabled.Store(*cmd.Auto)
		return map[string]interface{}{"auto": *cmd.Auto}, nil
	}
	if cmd.SetAnnounce != nil {
		s.announceEnabled.Store(*cmd.SetAnnounce)
		s.logger.Infof("announce set to %v", *cmd.SetAnnounce)
		return map[string]interface{}{"announce": *cmd.SetAnnounce}, nil
	}
	if cmd.BoardSnapshot {
		// Fast path: read the loop-populated cache; no per-call capture.
		s.boardCache.mu.RLock()
		if s.boardCache.ready {
			result := map[string]interface{}{
				"fen":             s.boardCache.fen,
				"camera_board":    s.boardCache.cameraBoard,
				"white_graveyard": s.boardCache.whiteGraveyard,
				"black_graveyard": s.boardCache.blackGraveyard,
				"auto":            s.autoEnabled.Load(),
				"captured_at_ms":  s.boardCache.capturedAt.UnixMilli(),
				"event":           s.boardCache.gameEvents.Event,
				"outcome":         s.boardCache.gameEvents.Outcome,
				"method":          s.boardCache.gameEvents.Method,
				"turn":            s.boardCache.gameEvents.Turn,
				"in_check":        s.boardCache.gameEvents.InCheck,
				"is_over":         s.boardCache.gameEvents.IsOver,
				"score_cp":        s.boardCache.gameEvents.ScoreCP,
				"score_mate":      s.boardCache.gameEvents.ScoreMate,
			}
			s.boardCache.mu.RUnlock()
			return result, nil
		}
		s.boardCache.mu.RUnlock()
		// Cache empty (loop disabled or pre-first-tick) — capture inline.
		all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return nil, err
		}
		fen, cameraBoard, whiteGY, blackGY, events, err := s.buildSnapshotData(ctx, all)
		if err != nil {
			return nil, err
		}
		events.ScoreCP = int(s.lastScoreCP.Load())
		events.ScoreMate = int(s.lastScoreMate.Load())
		_ = s.refreshBoardCache(ctx, all)
		return map[string]interface{}{
			"fen":             fen,
			"camera_board":    cameraBoard,
			"white_graveyard": whiteGY,
			"black_graveyard": blackGY,
			"auto":            s.autoEnabled.Load(),
			"captured_at_ms":  time.Now().UnixMilli(),
			"event":           events.Event,
			"outcome":         events.Outcome,
			"method":          events.Method,
			"turn":            events.Turn,
			"in_check":        events.InCheck,
			"is_over":         events.IsOver,
			"score_cp":        events.ScoreCP,
			"score_mate":      events.ScoreMate,
		}, nil
	}

	if cmd.GameEvents {
		theState, err := s.getGame(ctx)
		if err != nil {
			return nil, err
		}
		result := gameEventsResult(theState.game)
		result.ScoreCP = int(s.lastScoreCP.Load())
		result.ScoreMate = int(s.lastScoreMate.Load())
		return result.Map(), nil
	}

	if cmd.CompanionConfig {
		return map[string]interface{}{
			"bad_state_delay_ms":    s.conf.companionBadStateDelayMs(),
			"welcome_revive_ms":     s.conf.companionWelcomeReviveMs(),
			"in_check_dismiss_ms":   s.conf.companionInCheckDismissMs(),
			"first_move_dismiss_ms": s.conf.companionFirstMoveDismissMs(),
		}, nil
	}

	if cmd.Hover != "" {
		err := s.goToStart(ctx)
		if err != nil {
			return nil, err
		}

		all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return nil, err
		}

		center, err := s.getCenterFor(all, cmd.Hover, nil)
		if err != nil {
			return nil, err
		}
		center.Z = max(15, center.Z) + 100

		err = s.setupGripper(ctx)
		if err != nil {
			return nil, err
		}

		err = s.moveGripper(ctx, center)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{"center": center}, nil
	}

	if cmd.HoverPickedCenter != nil {
		return s.hoverOverPickedCenter(ctx, cmd.HoverPickedCenter)
	}

	var videoFrom *time.Time
	var videoTags []string
	defer func() {
		err := s.goToStart(ctx)
		if err != nil {
			s.logger.Warnf("can't go home: %v", err)
		}
		if videoFrom != nil {
			s.saveVideo(ctx, *videoFrom, time.Now().UTC(), videoTags)
		}
		// Refresh cache so clients see post-command state without waiting
		// for the next loop tick.
		if all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil); err == nil {
			_ = s.refreshBoardCache(ctx, all)
		}
	}()

	if cmd.Move.To != "" && cmd.Move.From != "" {
		s.logger.Infof("move %v to %v", cmd.Move.From, cmd.Move.To)
		now := time.Now().UTC()
		videoFrom = &now
		videoTags = []string{"cmd=move", fmt.Sprintf("move=%s%s", cmd.Move.From, cmd.Move.To)}

		for x := range cmd.Move.N {
			err := s.goToStart(ctx)
			if err != nil {
				return nil, err
			}

			from, to := cmd.Move.From, cmd.Move.To
			if x%2 == 1 {
				to, from = from, to
			}
			all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
			if err != nil {
				return nil, err
			}

			err = s.movePiece(ctx, all, nil, from, to, nil, nil)
			if err != nil {
				return nil, err
			}
		}

		return nil, nil
	}

	if cmd.Go > 0 {
		now := time.Now().UTC()
		videoFrom = &now
		videoTags = []string{"cmd=go", fmt.Sprintf("go=%d", cmd.Go)}
		moves, err := s.makeNMoves(ctx, cmd.Go)
		for _, m := range moves {
			videoTags = append(videoTags, "move="+m.String())
		}
		if err != nil {
			return nil, err
		}
		last := moves[len(moves)-1]
		return map[string]interface{}{"move": last.String()}, nil
	}

	if cmd.Undo > 0 {
		err = s.undoMoves(ctx, cmd.Undo)
		return nil, err
	}

	if cmd.Reset {
		return nil, s.resetBoard(ctx)
	}

	if cmd.PlayFEN != "" {
		return nil, s.playFENFile(ctx, cmd.PlayFEN)
	}

	return nil, fmt.Errorf("bad cmd %v", cmdMap)
}

const (
	defaultHoverDwellSeconds = 10.0
	hoverApproachMM          = 100.0 // phase 1: hover 10cm above the picked center
	hoverFinalMM             = 30.0  // phase 2: descend straight down to 3cm above the highest point
	defaultClusterRadiusMM   = 8.0   // FilterBlob XY connectivity radius
)

// hoverOverPickedCenter captures the board once, then for each square estimates
// the pickup center of the piece there (using the chosen method) and descends in
// two phases — approach 10cm above the picked center, then straight down to 3cm
// above the highest point — dwelling there so you can eyeball the alignment
// before moving on. Raises to home between squares to clear taller pieces.
// Invalid or empty squares are skipped (recorded in the result) rather than
// aborting the sweep. Pure observation — no game-state change.
func (s *viamChessChess) hoverOverPickedCenter(ctx context.Context, c *hoverPickedCenterCmd) (map[string]interface{}, error) {
	if len(c.Squares) == 0 {
		return nil, fmt.Errorf("hover_over_picked_center: squares is required (e.g. [\"e4\",\"d5\"])")
	}

	method := s.conf.pickupCenterMethod()
	if c.Method != "" {
		method = pickupCenterMethod(c.Method)
	}
	switch method {
	case methodTopN, methodTopBand, methodHighestMidpoint:
	default:
		return nil, fmt.Errorf("hover_over_picked_center: unknown method %q (want top_n|top_band|highest_midpoint)", c.Method)
	}

	n := c.N
	if n <= 0 {
		n = defaultTopN
	}
	bandMM := c.BandMM
	if bandMM <= 0 {
		bandMM = defaultTopBandMM
	}
	dwell := time.Duration(defaultHoverDwellSeconds * float64(time.Second))
	if c.DwellSeconds > 0 {
		dwell = time.Duration(c.DwellSeconds * float64(time.Second))
	}
	clusterRadius := c.ClusterRadiusMM
	if clusterRadius <= 0 {
		clusterRadius = defaultClusterRadiusMM
	}

	// Per-call square-inset override flows to the piece finder via extra.
	var extra map[string]interface{}
	if c.SquareInset != nil {
		extra = map[string]interface{}{"square_inset": *c.SquareInset}
	}

	if err := s.goToStart(ctx); err != nil {
		return nil, err
	}

	all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, extra)
	if err != nil {
		return nil, err
	}

	results := make([]interface{}, 0, len(c.Squares))
	firstHover := true
	for _, raw := range c.Squares {
		sq := strings.TrimSpace(raw)
		if len(sq) != 2 || sq[0] < 'a' || sq[0] > 'h' || sq[1] < '1' || sq[1] > '8' {
			s.logger.Warnf("hover_over_picked_center: skipping invalid square %q", raw)
			results = append(results, map[string]interface{}{"square": raw, "error": "invalid square (want a1..h8)"})
			continue
		}

		o := s.findObject(all, sq)
		if o == nil {
			s.logger.Warnf("hover_over_picked_center: no object found for %s; skipping", sq)
			results = append(results, map[string]interface{}{"square": sq, "error": "no object found"})
			continue
		}
		if strings.HasSuffix(o.Geometry.Label(), "-0") {
			s.logger.Warnf("hover_over_picked_center: %s looks empty (no piece detected); hovering over board center", sq)
		}

		blobKept, blobTotal := 0, 0
		if c.FilterBlob {
			o, blobKept, blobTotal = filterCentralBlob(o, clusterRadius, s.logger)
		}

		// Raise to home between hovers so we don't drag the gripper across taller
		// pieces; the first hover relies on the goToStart above.
		if !firstHover {
			if err := s.goToStart(ctx); err != nil {
				return nil, err
			}
		}
		firstHover = false

		center := GetPickupCenterWith(o, method, n, bandMM)

		// Two-phase descent with a strictly vertical gripper. Approach 10cm above
		// the picked center, close the gripper, then drop straight down (same XY)
		// to 3cm above the highest point.
		approach := center
		approach.Z += hoverApproachMM
		if err := s.moveGripperVertical(ctx, approach); err != nil {
			return nil, err
		}

		if _, err := s.gripper.Grab(ctx, nil); err != nil {
			return nil, err
		}

		hoverPos := center
		hoverPos.Z += hoverFinalMM
		if err := s.moveGripperVertical(ctx, hoverPos); err != nil {
			return nil, err
		}

		s.logger.Infof("hover_over_picked_center: square=%s method=%s center=%v approach_z=%.1f hover_z=%.1f (dwell %s)", sq, method, center, approach.Z, hoverPos.Z, dwell)
		res := map[string]interface{}{
			"square":     sq,
			"center":     map[string]interface{}{"x": center.X, "y": center.Y, "z": center.Z},
			"approach_z": approach.Z,
			"hover_z":    hoverPos.Z,
		}
		if c.FilterBlob {
			res["blob_points"] = blobKept
			res["total_points"] = blobTotal
		}
		results = append(results, res)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(dwell):
		}
	}

	out := map[string]interface{}{
		"method":        string(method),
		"dwell_seconds": dwell.Seconds(),
		"filter_blob":   c.FilterBlob,
		"results":       results,
	}
	if c.SquareInset != nil {
		out["square_inset"] = *c.SquareInset
	}
	if c.FilterBlob {
		out["cluster_radius_mm"] = clusterRadius
	}
	return out, nil
}

const videoSaverTimeFormat = "2006-01-02_15-04-05"

// buildSnapshotData turns a camera capture + saved game state into the
// board-snapshot wire payload.
func (s *viamChessChess) buildSnapshotData(ctx context.Context, all viscapture.VisCapture) (
	fen string,
	cameraBoard map[string]interface{},
	whiteGY []interface{},
	blackGY []interface{},
	events GameEventsResult,
	err error,
) {
	theState, err := s.getGame(ctx)
	if err != nil {
		return
	}
	cameraBoard = map[string]interface{}{}
	for _, o := range all.Objects {
		label := o.Geometry.Label()
		if idx := strings.LastIndex(label, "-"); idx != -1 {
			cameraBoard[label[:idx]] = label[idx+1:]
		}
	}
	whiteGY = make([]interface{}, 0, len(theState.whiteGraveyard))
	for _, p := range theState.whiteGraveyard {
		if pStr := pieceIntToFEN(p); pStr != "" {
			whiteGY = append(whiteGY, pStr)
		}
	}
	blackGY = make([]interface{}, 0, len(theState.blackGraveyard))
	for _, p := range theState.blackGraveyard {
		if pStr := pieceIntToFEN(p); pStr != "" {
			blackGY = append(blackGY, pStr)
		}
	}
	fen = theState.game.FEN()
	events = gameEventsResult(theState.game)
	return
}

// refreshBoardCache rebuilds the snapshot cache from the given camera capture.
func (s *viamChessChess) refreshBoardCache(ctx context.Context, all viscapture.VisCapture) error {
	fen, cb, wg, bg, events, err := s.buildSnapshotData(ctx, all)
	if err != nil {
		return err
	}
	events.ScoreCP = int(s.lastScoreCP.Load())
	events.ScoreMate = int(s.lastScoreMate.Load())
	s.boardCache.mu.Lock()
	defer s.boardCache.mu.Unlock()
	s.boardCache.ready = true
	s.boardCache.fen = fen
	s.boardCache.cameraBoard = cb
	s.boardCache.whiteGraveyard = wg
	s.boardCache.blackGraveyard = bg
	s.boardCache.capturedAt = time.Now()
	s.boardCache.gameEvents = events
	return nil
}

// invalidateBoardCache marks the cache stale so the next reader re-captures.
// Used by wipe, which doesn't go through the post-command refresh defer.
func (s *viamChessChess) invalidateBoardCache() {
	s.boardCache.mu.Lock()
	defer s.boardCache.mu.Unlock()
	s.boardCache.ready = false
}

// GameEventsResult holds the current game-state events returned by the
// "game-events" DoCommand.
type GameEventsResult struct {
	// Event is the highest-priority active event: "checkmate", "stalemate",
	// "draw", "check", or "none".
	Event string `json:"event"`
	// Outcome is the game result: "in_progress", "white_won", "black_won", or "draw".
	Outcome string `json:"outcome"`
	// Method is how the game ended, or "none" while in progress: "checkmate",
	// "stalemate", "threefold_repetition", "fifty_move_rule",
	// "insufficient_material", "draw_offer", or "resignation".
	Method string `json:"method"`
	// Turn is whose move it is: "white" or "black".
	Turn string `json:"turn"`
	// InCheck is true when the side to move is currently in check (non-terminal).
	InCheck bool `json:"in_check"`
	// IsOver is true when the game has ended.
	IsOver bool `json:"is_over"`
	// ScoreCP is the engine evaluation in centipawns, white-relative.
	// Positive = white is ahead. 0 before the first engine move or when
	// no engine is configured.
	ScoreCP int `json:"score_cp"`
	// ScoreMate is the engine-detected moves to forced mate, white-relative.
	// Positive = white mates in N moves, negative = black mates in N moves,
	// 0 = no forced mate detected.
	ScoreMate int `json:"score_mate"`
}

// Map converts the result to the map[string]interface{} format required by DoCommand.
func (r GameEventsResult) Map() map[string]interface{} {
	return map[string]interface{}{
		"event":      r.Event,
		"outcome":    r.Outcome,
		"method":     r.Method,
		"turn":       r.Turn,
		"in_check":   r.InCheck,
		"is_over":    r.IsOver,
		"score_cp":   r.ScoreCP,
		"score_mate": r.ScoreMate,
	}
}

// gameEventsResult computes the current game-state events from a chess.Game.
// It is pure-read: no board mutations.
func gameEventsResult(game *chess.Game) GameEventsResult {
	outcome := game.Outcome()
	method := game.Method()

	// Detect check: the last played move carries the Check tag when it puts
	// the opponent (the side now to move) in check.
	inCheck := false
	if outcome == chess.NoOutcome {
		moves := game.Moves()
		if len(moves) > 0 {
			inCheck = moves[len(moves)-1].HasTag(chess.Check)
		}
	}

	var event string
	switch {
	case method == chess.Checkmate:
		event = "checkmate"
	case method == chess.Stalemate:
		event = "stalemate"
	case outcome == chess.Draw:
		event = "draw"
	case inCheck:
		event = "check"
	default:
		event = "none"
	}

	var outcomeStr string
	switch outcome {
	case chess.WhiteWon:
		outcomeStr = "white_won"
	case chess.BlackWon:
		outcomeStr = "black_won"
	case chess.Draw:
		outcomeStr = "draw"
	default:
		outcomeStr = "in_progress"
	}

	var methodStr string
	switch method {
	case chess.Checkmate:
		methodStr = "checkmate"
	case chess.Stalemate:
		methodStr = "stalemate"
	case chess.ThreefoldRepetition:
		methodStr = "threefold_repetition"
	case chess.FiftyMoveRule:
		methodStr = "fifty_move_rule"
	case chess.InsufficientMaterial:
		methodStr = "insufficient_material"
	case chess.DrawOffer:
		methodStr = "draw_offer"
	case chess.Resignation:
		methodStr = "resignation"
	default:
		methodStr = "none"
	}

	turn := "white"
	if game.Position().Turn() == chess.Black {
		turn = "black"
	}

	return GameEventsResult{
		Event:   event,
		Outcome: outcomeStr,
		Method:  methodStr,
		Turn:    turn,
		InCheck: inCheck,
		IsOver:  outcome != chess.NoOutcome,
	}
}

func (s *viamChessChess) saveVideo(ctx context.Context, from, to time.Time, tags []string) {
	if s.videoSaver == nil {
		return
	}
	_, err := s.videoSaver.DoCommand(ctx, map[string]interface{}{
		"command": "save",
		"from":    from.UTC().Format(videoSaverTimeFormat) + "Z",
		"to":      to.UTC().Format(videoSaverTimeFormat) + "Z",
		"tags":    tags,
		"async":   true,
	})
	if err != nil {
		s.logger.Warnf("video save failed: %v", err)
	}
}
