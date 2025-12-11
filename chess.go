package viamchess

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golang/geo/r3"

	"github.com/mitchellh/mapstructure"

	"go.viam.com/rdk/components/gripper"
	"go.viam.com/rdk/components/switch"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/robot/framesystem"
	generic "go.viam.com/rdk/services/generic"
	"go.viam.com/rdk/services/motion"
	"go.viam.com/rdk/services/vision"
	"go.viam.com/rdk/spatialmath"
	"go.viam.com/rdk/vision/viscapture"
)

var ChessModel = family.WithModel("chess")

func init() {
	resource.RegisterService(generic.API, ChessModel,
		resource.Registration[resource.Resource, *ChessConfig]{
			Constructor: newViamChessChess,
		},
	)
}

type ChessConfig struct {
	PieceFinder string `json:"piece-finder"`
	Gripper     string
	Motion      string

	PoseStart string `json:"pose-start"`
}

func (cfg *ChessConfig) Validate(path string) ([]string, []string, error) {
	if cfg.PieceFinder == "" {
		return nil, nil, fmt.Errorf("need a piece-finder")
	}
	if cfg.Gripper == "" {
		return nil, nil, fmt.Errorf("need a gripper")
	}
	if cfg.PoseStart == "" {
		return nil, nil, fmt.Errorf("need a pose-start")
	}

	return []string{cfg.PieceFinder, cfg.Gripper, cfg.PoseStart}, nil, nil
}

type viamChessChess struct {
	resource.AlwaysRebuild

	name resource.Name

	logger logging.Logger
	conf   *ChessConfig

	cancelCtx  context.Context
	cancelFunc func()

	pieceFinder vision.Service
	gripper     gripper.Gripper

	poseStart toggleswitch.Switch

	motion motion.Service
	rfs    framesystem.Service
}

func newViamChessChess(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (resource.Resource, error) {
	conf, err := resource.NativeConfig[*ChessConfig](rawConf)
	if err != nil {
		return nil, err
	}

	return NewChess(ctx, deps, rawConf.ResourceName(), conf, logger)

}

func NewChess(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *ChessConfig, logger logging.Logger) (resource.Resource, error) {
	var err error

	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	s := &viamChessChess{
		name:       name,
		logger:     logger,
		conf:       conf,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
	}

	s.pieceFinder, err = vision.FromProvider(deps, conf.PieceFinder)
	if err != nil {
		return nil, err
	}

	s.gripper, err = gripper.FromProvider(deps, conf.Gripper)
	if err != nil {
		return nil, err
	}

	s.poseStart, err = toggleswitch.FromProvider(deps, conf.PoseStart)
	if err != nil {
		return nil, err
	}
	err = s.goToStart(ctx)
	if err != nil {
		return nil, err
	}

	s.motion, err = motion.FromDependencies(deps, "builtin")
	if err != nil {
		return nil, err
	}

	s.rfs, err = framesystem.FromDependencies(deps)
	if err != nil {
		logger.Errorf("can't find framesystem: %v", err)
	}

	return s, nil
}

func (s *viamChessChess) Name() resource.Name {
	return s.name
}

// ----

type MoveCmd struct {
	From, To string
}

type cmdStruct struct {
	Move MoveCmd
}

func (s *viamChessChess) DoCommand(ctx context.Context, cmdMap map[string]interface{}) (map[string]interface{}, error) {
	defer func() {
		err := s.goToStart(ctx)
		if err != nil {
			s.logger.Warnf("can't go home: %v", err)
		}
	}()
	var cmd cmdStruct
	err := mapstructure.Decode(cmdMap, &cmd)
	if err != nil {
		return nil, err
	}

	if cmd.Move.To != "" && cmd.Move.From != "" {
		s.logger.Infof("move %v to %v", cmd.Move.From, cmd.Move.To)

		all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return nil, err
		}

		err = s.moveTo(ctx, all, cmd.Move.From)
		if err != nil {
			return nil, err
		}

		return nil, fmt.Errorf("finish me")
	}

	return nil, fmt.Errorf("bad cmd %v", cmdMap)
}

func (s *viamChessChess) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
}

func (s *viamChessChess) moveTo(ctx context.Context, data viscapture.VisCapture, pos string) error {
	for _, o := range data.Objects {
		if !strings.HasPrefix(o.Geometry.Label(), pos) {
			continue
		}
		s.logger.Infof(o.Geometry.Label())

		md := o.MetaData()
		s.logger.Infof("%v - %v", o.Geometry.Label(), md)

		center := md.Center()

		startPose, err := s.rfs.GetPose(ctx, s.conf.Gripper, "world", nil, nil)
		if err != nil {
			return err
		}

		_, err = s.motion.Move(ctx, motion.MoveReq{
			ComponentName: s.conf.Gripper,
			Destination: referenceframe.NewPoseInFrame("world",
				spatialmath.NewPose(
					r3.Vector{center.X, center.Y, 100},
					startPose.Pose().Orientation(),
				)),
		})
		if err != nil {
			return err
		}

	}

	return fmt.Errorf("Finis measd ")
}

func (s *viamChessChess) goToStart(ctx context.Context) error {
	err := s.poseStart.SetPosition(ctx, 2, nil)
	if err != nil {
		return err
	}
	time.Sleep(time.Second)
	return nil
}
