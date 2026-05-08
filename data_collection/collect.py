"""Auto-annotated chess piece data collection.

Drives the chess robot through random non-capturing moves, captures multiple
camera frames per board state from slightly perturbed home poses, and uploads
each frame with auto-derived bounding-box labels to a Viam dataset.

Capture and upload alternate in chunks: every N moves the frame buffer is
drained, then capture resumes. The board state is snapshotted into each
buffered frame so deferred upload uses the position that was on the board at
capture time, not the live state which keeps changing.

Ground truth is the script's internal piece map — initialized to the standard
starting position and updated after each move.
"""
from __future__ import annotations

import argparse
import asyncio
import logging
import os
import random
import sys
from dataclasses import dataclass
from datetime import datetime, timezone

from viam.app.viam_client import ViamClient
from viam.components.arm import Arm
from viam.components.switch import Switch
from viam.proto.common import Pose
from viam.robot.client import RobotClient
from viam.rpc.dial import Credentials, DialOptions
from viam.services.generic import Generic as GenericService
from viam.services.vision import VisionClient


FILES = "abcdefgh"
RANKS = "12345678"
ALL_SQUARES = [f + r for f in FILES for r in RANKS]

PIECE_BY_FILE = {
    "a": "rook", "b": "knight", "c": "bishop", "d": "queen",
    "e": "king", "f": "bishop", "g": "knight", "h": "rook",
}

def starting_board() -> dict[str, str]:
    """Standard chess starting position as {square: 'white-pawn'|...}."""
    board: dict[str, str] = {}
    for f in FILES:
        board[f"{f}1"] = f"white-{PIECE_BY_FILE[f]}"
        board[f"{f}2"] = "white-pawn"
        board[f"{f}7"] = "black-pawn"
        board[f"{f}8"] = f"black-{PIECE_BY_FILE[f]}"
    return board


def random_move(board: dict[str, str], rng: random.Random) -> tuple[str, str]:
    """Pick a random source (occupied) and dest (empty) square. No captures."""
    occupied = list(board.keys())
    empty = [s for s in ALL_SQUARES if s not in board]
    return rng.choice(occupied), rng.choice(empty)


@dataclass
class Config:
    api_key: str
    api_key_id: str
    machine_address: str
    part_id: str
    dataset_id: str
    chess_service: str = "chess"
    vision_service: str = "piece-finder"
    arm_component: str = "arm"
    camera_component: str = "cam"
    home_switch: str = "hack-pose-look-straight-down"
    n_moves: int = 50
    k_poses: int = 4
    perturb_xy_mm: float = 12.0
    perturb_z_mm: float = 8.0
    perturb_theta_deg: float = 3.0
    upload_concurrency: int = 10
    upload_every_n_moves: int = 3  # drain the frame buffer every N moves
    manual: bool = False  # prompt to move pieces by hand instead of using the robot
    wipe_on_start: bool = True  # start from clean game_state (default)


@dataclass
class CapturedFrame:
    """One image + boxes pending upload. Self-contained so deferred upload
    uses the board state that existed at capture time, not the live state."""
    image_bytes: bytes
    width: int
    height: int
    per_square: dict[str, tuple[int, tuple[int, int, int, int]]]
    board_snapshot: dict[str, str]
    tags: list[str]
    capture_time: datetime


# ---------------------------------------------------------------------------
# Connection helpers
# ---------------------------------------------------------------------------

async def connect_machine(cfg: Config) -> RobotClient:
    opts = RobotClient.Options.with_api_key(
        api_key=cfg.api_key,
        api_key_id=cfg.api_key_id,
    )
    return await RobotClient.at_address(cfg.machine_address, opts)


async def connect_app(cfg: Config) -> ViamClient:
    creds = Credentials(type="api-key", payload=cfg.api_key)
    dial = DialOptions(credentials=creds, auth_entity=cfg.api_key_id)
    return await ViamClient.create_from_dial_options(dial)


# ---------------------------------------------------------------------------
# Robot interactions
# ---------------------------------------------------------------------------

async def go_home(switch: Switch) -> None:
    """Drive the arm to the saved 'home' joint pose via the arm-position-saver
    toggleswitch (position 2 = 'go to' on erh:vmodutils:arm-position-saver)."""
    await switch.set_position(2)


async def do_move(chess_svc: GenericService, src: str, dst: str, tall: bool) -> None:
    """Send a single move via the chess service's DoCommand.

    `tall=True` forces grabZTall pickup — required for King/Queen since the
    chess module can't infer piece type without an engine-tracked board.
    """
    await chess_svc.do_command({
        "move": {"from": src, "to": dst, "n": 1, "tall": tall},
    })


def uci_move_to_src_dst(move: str) -> tuple[str, str]:
    """Parse UCI move like 'e2e4' (or 'e7e8q') to (src, dst)."""
    move = move.strip()
    if len(move) < 4:
        raise ValueError(f"bad move {move!r}")
    src, dst = move[:2], move[2:4]
    if src[0] not in FILES or src[1] not in RANKS or dst[0] not in FILES or dst[1] not in RANKS:
        raise ValueError(f"bad move {move!r}")
    return src, dst


def board_from_fen(fen: str) -> dict[str, str]:
    """Convert FEN into this script's {square: 'white-pawn'|...} board dict."""
    placement = fen.split()[0]
    ranks = placement.split("/")
    if len(ranks) != 8:
        raise ValueError(f"bad fen placement {placement!r}")

    piece_name = {
        "p": "pawn",
        "n": "knight",
        "b": "bishop",
        "r": "rook",
        "q": "queen",
        "k": "king",
    }
    out: dict[str, str] = {}
    for rank_idx, r in enumerate(ranks):
        file_idx = 0
        for ch in r:
            if ch.isdigit():
                file_idx += int(ch)
                continue
            if file_idx >= 8:
                raise ValueError(f"bad fen rank {r!r} in {placement!r}")
            color = "white" if ch.isupper() else "black"
            p = piece_name.get(ch.lower())
            if not p:
                raise ValueError(f"bad fen piece {ch!r} in {placement!r}")
            sq = f"{FILES[file_idx]}{8 - rank_idx}"
            out[sq] = f"{color}-{p}"
            file_idx += 1
        if file_idx != 8:
            raise ValueError(f"bad fen rank {r!r} in {placement!r}")
    return out


async def get_board_snapshot(chess_svc: GenericService) -> dict:
    """Fetch the chess module's snapshot (includes `fen`)."""
    res = await chess_svc.do_command({"board-snapshot": True})
    if not isinstance(res, dict) or "fen" not in res:
        raise RuntimeError(f"unexpected board-snapshot response: {res!r}")
    return res


async def do_go(chess_svc: GenericService, n: int = 1) -> tuple[str, str]:
    """Ask the chess service to play `n` moves. Returns (src, dst) of the last move."""
    res = await chess_svc.do_command({"go": n})
    move = res.get("move") if isinstance(res, dict) else None
    if not isinstance(move, str):
        raise RuntimeError(f"unexpected go response: {res!r}")
    return uci_move_to_src_dst(move)


async def do_go_retry(chess_svc: GenericService, n: int = 1, attempts: int = 5) -> tuple[str, str]:
    """Run `go` with a few retries for occasional vision noise at sanity-check time."""
    last_err: Exception | None = None
    for attempt in range(1, attempts + 1):
        try:
            return await do_go(chess_svc, n)
        except Exception as e:
            last_err = e
            msg = str(e)
            retryable = "no valid moves from:" in msg or "can't find object for square" in msg
            if not retryable or attempt == attempts:
                raise
            # Clear square cache and try again after a short pause.
            try:
                await chess_svc.do_command({"clear-cache": True})
            except Exception:
                # If cache-clear itself fails, we still retry `go` after sleep.
                pass
            await asyncio.sleep(0.4 * attempt)
    assert last_err is not None
    raise last_err


def perturb_pose(home: Pose, rng: random.Random, cfg: Config) -> Pose:
    """Return a Pose offset slightly from home in XY/Z and yawed by theta."""
    return Pose(
        x=home.x + rng.uniform(-cfg.perturb_xy_mm, cfg.perturb_xy_mm),
        y=home.y + rng.uniform(-cfg.perturb_xy_mm, cfg.perturb_xy_mm),
        z=home.z + rng.uniform(-cfg.perturb_z_mm, cfg.perturb_z_mm),
        o_x=home.o_x,
        o_y=home.o_y,
        o_z=home.o_z,
        theta=home.theta + rng.uniform(-cfg.perturb_theta_deg, cfg.perturb_theta_deg),
    )


async def capture(vision: VisionClient, camera_name: str):
    """Atomic image + detections from PieceFinder."""
    return await vision.capture_all_from_camera(
        camera_name,
        return_image=True,
        return_detections=True,
    )


# ---------------------------------------------------------------------------
# Detection parsing + validation
# ---------------------------------------------------------------------------

def parse_square_detections(detections) -> dict[str, tuple[int, tuple[int, int, int, int]]]:
    """Filter PieceFinder output to per-square (color, bbox).

    PieceFinder emits two detections per square: the square box (label
    "<sq>-<color>") and a tiny center marker (label "x-<sq>-<color>"). We keep
    only the square boxes.
    """
    out: dict[str, tuple[int, tuple[int, int, int, int]]] = {}
    for d in detections:
        name = d.class_name
        if name.startswith("x-"):
            continue
        sq, _, color_str = name.rpartition("-")
        if len(sq) != 2 or sq[0] not in FILES or sq[1] not in RANKS:
            continue
        try:
            color = int(color_str)
        except ValueError:
            continue
        out[sq] = (color, (d.x_min, d.y_min, d.x_max, d.y_max))
    return out


# ---------------------------------------------------------------------------
# Upload
# ---------------------------------------------------------------------------

async def upload_one(data_client, cfg: Config, frame: CapturedFrame) -> str:
    """Upload one image + add all of its boxes (boxes added in parallel)."""
    binary_id = await data_client.binary_data_capture_upload(
        binary_data=frame.image_bytes,
        part_id=cfg.part_id,
        component_type="rdk:component:camera",
        component_name=cfg.camera_component,
        method_name="ReadImage",
        file_extension=".jpg",
        tags=frame.tags,
        dataset_ids=[cfg.dataset_id],
        data_request_times=(frame.capture_time, frame.capture_time),
    )

    async def add_box(sq: str, label: str) -> None:
        _, (xmin, ymin, xmax, ymax) = frame.per_square[sq]
        await data_client.add_bounding_box_to_image_by_id(
            binary_id=binary_id,
            label=label,
            x_min_normalized=xmin / frame.width,
            y_min_normalized=ymin / frame.height,
            x_max_normalized=xmax / frame.width,
            y_max_normalized=ymax / frame.height,
        )

    await asyncio.gather(*(add_box(sq, lbl) for sq, lbl in frame.board_snapshot.items()))
    return binary_id


async def upload_batch(
    data_client,
    cfg: Config,
    log: logging.Logger,
    frames: list[CapturedFrame],
) -> None:
    """Upload all buffered frames in parallel, capped at cfg.upload_concurrency."""
    if not frames:
        return
    log.info("uploading %d frames (concurrency=%d)", len(frames), cfg.upload_concurrency)
    sem = asyncio.Semaphore(cfg.upload_concurrency)
    done = 0
    done_lock = asyncio.Lock()

    async def go(f: CapturedFrame) -> None:
        nonlocal done
        async with sem:
            await upload_one(data_client, cfg, f)
        async with done_lock:
            done += 1
            if done % 10 == 0 or done == len(frames):
                log.info("uploaded %d/%d", done, len(frames))

    await asyncio.gather(*(go(f) for f in frames))


# ---------------------------------------------------------------------------
# Main loop
# ---------------------------------------------------------------------------

async def run(cfg: Config, log: logging.Logger) -> None:
    rng = random.Random()
    frames: list[CapturedFrame] = []

    machine = await connect_machine(cfg)
    app = await connect_app(cfg)
    try:
        chess_svc = GenericService.from_robot(robot=machine, name=cfg.chess_service)
        vision = VisionClient.from_robot(robot=machine, name=cfg.vision_service)
        arm = Arm.from_robot(robot=machine, name=cfg.arm_component)
        home_switch = Switch.from_robot(robot=machine, name=cfg.home_switch)
        data_client = app.data_client

        # Drive the arm to the saved home pose at startup. In auto mode this
        # also happens implicitly via the chess module's defer goToStart on the
        # first move, but homing here means the perturbations on every iteration
        # (including the first) are referenced to a deterministic pose.
        log.info("homing arm via switch %r", cfg.home_switch)
        await go_home(home_switch)

        if cfg.wipe_on_start:
            # Start from a clean server-side game_state so `go` isn't continuing a
            # prior run's position/move history.
            log.info("wiping chess game_state")
            await chess_svc.do_command({"wipe": True})
        else:
            log.info("resuming from existing chess game_state (no wipe)")
        snap0 = await get_board_snapshot(chess_svc)
        board = board_from_fen(str(snap0["fen"]))

        skill = rng.randrange(0, 100)
        await chess_svc.do_command({"skill": skill})
        log.info("setting skill to %d", skill)

        for i in range(cfg.n_moves):
            if cfg.manual:
                raise NotImplementedError("manual mode is not supported with do_go()")
            else:
                src, dst = await do_go_retry(chess_svc, 1)
                log.info("move %d/%d: %s -> %s", i + 1, cfg.n_moves, src, dst)
                snap = await get_board_snapshot(chess_svc)
                board = board_from_fen(str(snap["fen"]))

            # Either the chess module's defer goToStart ran (auto), or we
            # just homed via the switch (manual) — arm is at canonical home.
            home = await arm.get_end_position()
            captured = await capture_k(
                cfg, rng, arm, vision, home, board,
                move_index=i, src=src, dst=dst,
            )
            frames.extend(captured)
            log.info("buffered %d frames so far", len(frames))

            # Drain in chunks: capture N moves, pause to upload, then resume.
            if (i + 1) % cfg.upload_every_n_moves == 0:
                await upload_batch(data_client, cfg, log, frames)
                frames = []

        # Drain any remainder (n_moves not divisible by upload_every_n_moves).
        await upload_batch(data_client, cfg, log, frames)
    finally:
        await machine.close()
        app.close()


async def capture_k(
    cfg: Config,
    rng: random.Random,
    arm: Arm,
    vision: VisionClient,
    home: Pose,
    board: dict[str, str],
    move_index: int,
    src: str,
    dst: str,
) -> list[CapturedFrame]:
    """Capture k perturbed frames at the current board state; return them."""
    base_tags = [f"move_index={move_index}", f"move={src}{dst}"]
    out: list[CapturedFrame] = []

    for k in range(cfg.k_poses):
        if k == 0:
            await arm.move_to_position(home)
        else:
            await arm.move_to_position(perturb_pose(home, rng, cfg))

        result = await capture(vision, cfg.camera_component)
        per_square = parse_square_detections(result.detections)
        img = result.image

        out.append(CapturedFrame(
            image_bytes=img.data,
            width=img.width,
            height=img.height,
            per_square=per_square,
            board_snapshot=dict(board),
            tags=base_tags + [f"pose_index={k}"],
            capture_time=datetime.now(timezone.utc),
        ))
    return out


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def env_required(name: str) -> str:
    val = os.environ.get(name)
    if not val:
        sys.exit(f"missing required env var: {name}")
    return val


def parse_args() -> Config:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--n-moves", type=int, default=50)
    p.add_argument("--k-poses", type=int, default=4)
    p.add_argument("--perturb-xy-mm", type=float, default=12.0)
    p.add_argument("--perturb-z-mm", type=float, default=8.0)
    p.add_argument("--perturb-theta-deg", type=float, default=3.0)
    p.add_argument("--upload-concurrency", type=int, default=10)
    p.add_argument("--upload-every-n-moves", type=int, default=5,
                   help="capture N moves, then pause to drain the buffer, then resume")
    p.add_argument("--manual", action="store_true",
                   help="prompt the user to move pieces by hand instead of using the robot; "
                        "arm must be at home pose when the script starts")
    p.add_argument("--chess-service", default="chess")
    p.add_argument("--vision-service", default="piece-finder")
    p.add_argument("--arm-component", default="arm")
    p.add_argument("--camera-component", default="cam")
    p.add_argument("--home-switch", default="hack-pose-look-straight-down",
                   help="name of the arm-position-saver toggleswitch component used to home the arm")
    p.add_argument("--resume", action="store_true",
                   help="resume from the chess module's last saved game_state (skip wipe at startup)")
    args = p.parse_args()

    return Config(
        api_key=env_required("VIAM_API_KEY"),
        api_key_id=env_required("VIAM_API_KEY_ID"),
        machine_address=env_required("VIAM_MACHINE_ADDRESS"),
        part_id=env_required("VIAM_PART_ID"),
        dataset_id=env_required("VIAM_DATASET_ID"),
        chess_service=args.chess_service,
        vision_service=args.vision_service,
        arm_component=args.arm_component,
        camera_component=args.camera_component,
        home_switch=args.home_switch,
        n_moves=args.n_moves,
        k_poses=args.k_poses,
        perturb_xy_mm=args.perturb_xy_mm,
        perturb_z_mm=args.perturb_z_mm,
        perturb_theta_deg=args.perturb_theta_deg,
        upload_concurrency=args.upload_concurrency,
        upload_every_n_moves=args.upload_every_n_moves,
        manual=args.manual,
        wipe_on_start=not args.resume,
    )


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    log = logging.getLogger("collect")
    cfg = parse_args()
    asyncio.run(run(cfg, log))


if __name__ == "__main__":
    main()
