"""Send the SfM do-command to the chess module on a remote machine.

Usage:
    export VIAM_API_KEY=...
    export VIAM_API_KEY_ID=...
    python run_sfm.py --piece knight --radius 500 --num-rings 5 --num-azimuths 4
"""

import argparse
import asyncio
import os

from viam.robot.client import RobotClient
from viam.services.generic import Generic

MACHINE_ADDRESS = "chess2-main.oeq47g5p1m.viam.cloud"
CHESS_SERVICE_NAME = "chess"


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="Run SfM data collection on a Viam machine.")
    p.add_argument("--piece", required=True, help="name of the piece being photographed")
    p.add_argument("--radius", type=float, default=500.0, help="hemisphere radius in mm")
    p.add_argument("--num-rings", type=int, default=5, help="number of elevation rings")
    p.add_argument("--num-azimuths", type=int, default=4, help="azimuth shots per ring")
    p.add_argument("--output-dir", default="/home/viam/robin/chess-dataset", help="output dir on the machine")
    p.add_argument("--skip-pcd", action="store_true", help="skip saving point clouds")
    return p.parse_args()


async def main(args: argparse.Namespace) -> None:
    api_key = os.environ.get("VIAM_API_KEY")
    api_key_id = os.environ.get("VIAM_API_KEY_ID")
    if not api_key or not api_key_id:
        raise SystemExit(
            "VIAM_API_KEY and VIAM_API_KEY_ID must be set. Get them from "
            "app.viam.com -> your machine -> Connect tab -> Code sample."
        )
    opts = RobotClient.Options.with_api_key(
        api_key=api_key,
        api_key_id=api_key_id,
    )
    machine = await RobotClient.at_address(MACHINE_ADDRESS, opts)
    try:
        chess = Generic.from_robot(machine, name=CHESS_SERVICE_NAME)
        result = await chess.do_command({"sfm": {
            "piece": args.piece,
            "radius": args.radius,
            "num-rings": args.num_rings,
            "num-azimuths": args.num_azimuths,
            "output-dir": args.output_dir,
            "skip-pcd": args.skip_pcd,
        }})
        print("result:", result)
    finally:
        await machine.close()


if __name__ == "__main__":
    asyncio.run(main(parse_args()))
