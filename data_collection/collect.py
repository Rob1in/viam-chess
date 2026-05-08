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
    upload_every_n_moves: int = 5  # drain the frame buffer every N moves
    manual: bool = False  # prompt to move pieces by hand instead of using the robot
    seed: int | None = None  # None → fresh OS-entropy seed each run, logged for reproducibility


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
    seed = cfg.seed if cfg.seed is not None else random.SystemRandom().randrange(2**31)
    log.info("rng seed: %d", seed)
    rng = random.Random(seed)
    board = starting_board()
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

        for i in range(cfg.n_moves):
            src, dst = random_move(board, rng)
            piece = board[src]
            tall = piece.endswith("-king") or piece.endswith("-queen")
            log.info("move %d/%d: %s -> %s (%s, tall=%s)",
                     i + 1, cfg.n_moves, src, dst, piece, tall)

            if cfg.manual:
                # Home FIRST so the arm is out of the way while the user moves
                # the piece. After the previous iteration's capture cycle, the
                # arm is parked at the last perturbed pose — without this, the
                # user would be reaching around it.
                await go_home(home_switch)
                await asyncio.to_thread(
                    input,
                    f"  → move {piece} from {src} to {dst}, press ENTER when done: ",
                )
            else:
                await do_move(chess_svc, src, dst, tall)
            board[dst] = board.pop(src)

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
    p.add_argument("--seed", type=int, default=None,
                   help="RNG seed; omit for OS entropy (logged at startup so you can reproduce)")
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
        seed=args.seed,
    )


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    log = logging.getLogger("collect")
    cfg = parse_args()
    asyncio.run(run(cfg, log))


if __name__ == "__main__":
    main()
