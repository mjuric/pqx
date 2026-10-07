"""Time pqx's data grid on a wide file.

    python bench/grid_bench.py                    # 300 float columns x 5000 rows, generated
    python bench/grid_bench.py --cols 16          # narrower
    python bench/grid_bench.py --file x.parquet   # your own file
    python bench/grid_bench.py --profile right    # cProfile one operation

Reports CPU time per operation (process time, so waits don't count), driving the
app headless at 200x50 through Textual's pilot. Run it from the repo root, in an
environment where `import pqx` resolves to this checkout.
"""
from __future__ import annotations

import argparse
import asyncio
import cProfile
import os
import pstats
import tempfile
import time

import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq
from textual.worker import WorkerState

from pqx.app import GridTable, PqxApp

OPS = [  # (name, keys, repeats)
    ("down", ["down"], 30),
    ("right", ["right"], 30),
    ("pagedown", ["pagedown"], 10),
    ("ctrl+end", ["ctrl+end"], 1),
    ("ctrl+home", ["ctrl+home"], 1),
    ("end", ["end"], 1),
    ("home", ["home"], 1),
    ("f", ["f"], 2),
]


def make_wide(path: str, cols: int, rows: int) -> None:
    rng = np.random.default_rng(1)
    data = {"id": np.arange(rows, dtype=np.int64)}
    for i in range(cols - 1):
        data[f"c{i:03d}"] = rng.normal(scale=10 ** (i % 7 - 2), size=rows)
    pq.write_table(pa.table(data), path, row_group_size=1000)


async def settle(pilot, app) -> None:
    await pilot.pause(0.05)
    while app._busy or any(w.state in (WorkerState.PENDING, WorkerState.RUNNING) for w in app.workers):
        await pilot.pause(0.02)
    await pilot.pause(0.05)


async def run(path: str, profile: str | None) -> dict[str, float]:
    out = {}
    t0 = time.process_time()
    app = PqxApp(path)
    async with app.run_test(size=(200, 50)) as pilot:
        await settle(pilot, app)
        out["startup"] = (time.process_time() - t0) * 1000
        g = app.query_one(GridTable)
        print(f"{os.path.basename(path)}: window {g.window} rows x {len(app.cols_shown)} columns")

        async def timed(name, keys, n):
            prof = cProfile.Profile() if profile == name else None
            t = time.process_time()
            if prof:
                prof.enable()
            for _ in range(n):
                await pilot.press(*keys)
                await pilot.pause(0)
            await settle(pilot, app)
            if prof:
                prof.disable()
                pstats.Stats(prof).sort_stats("cumulative").print_stats(30)
            out[name] = (time.process_time() - t) / n * 1000

        for name, keys, n in OPS:
            await timed(name, keys, n)
        await pilot.press("d")
        await settle(pilot, app)
        await timed("down+detail", ["down"], 20)
    return out


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--file")
    p.add_argument("--cols", type=int, default=300)
    p.add_argument("--rows", type=int, default=5000)
    p.add_argument("--profile", metavar="OP", help="cProfile this operation: " + ", ".join(
        [o[0] for o in OPS] + ["down+detail"]))
    a = p.parse_args()
    os.environ.setdefault("XDG_CONFIG_HOME", tempfile.mkdtemp())  # never read the user's saved formats
    with tempfile.TemporaryDirectory() as tmp:
        path = a.file
        if not path:
            path = os.path.join(tmp, f"wide{a.cols}.parquet")
            make_wide(path, a.cols, a.rows)
        res = asyncio.run(run(path, a.profile))
    print(f"{'operation':14s} {'cpu ms/op':>10s}")
    for k, v in res.items():
        print(f"{k:14s} {v:10.1f}")


if __name__ == "__main__":
    main()
