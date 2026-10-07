"""Time pqx's startup: until the grid is usable, and until the Schema/Metadata tabs are built.

    python bench/startup_bench.py FILE [FILE ...]
    python bench/startup_bench.py --cols 300      # 300 float columns x 5000 rows, generated
    python bench/startup_bench.py --profile FILE  # cProfile (pqx frames) up to tabs-ready

For each file: wall and CPU (process time: all threads) from constructing the
app to (1) the first page in the grid, (2) the Schema and Metadata tabs filled,
(3) no work left; how long a key press (→) sent as the first page appears takes
to move the cursor; and the longest the event loop went unresponsive before and
after the first page (what a key press would have waited). Headless at 200x50
through Textual's pilot; run from the repo root where `import pqx` resolves to
this checkout.
"""
from __future__ import annotations

import argparse
import asyncio
import cProfile
import os
import pstats
import tempfile
import time

from textual import events
from textual.widgets import DataTable
from textual.worker import WorkerState

from pqx.app import GridTable, PqxApp


def _tabs_ready(app) -> bool:
    try:
        schema = app.query_one("#schema-table", DataTable)
        rgs = app.query_one("#rowgroups", DataTable)
    except Exception:  # noqa: BLE001
        return False
    return (schema.row_count == len(app.ds.columns)
            and rgs.row_count == min(5000, app.ds.meta.num_row_groups))


def _idle(app) -> bool:
    return not app._busy and not any(w.state in (WorkerState.PENDING, WorkerState.RUNNING) for w in app.workers)


async def run(path: str) -> dict[str, float]:
    out: dict[str, float] = {}
    stalls: list[tuple[float, float]] = []  # (end, length) of each gap the event loop didn't run
    done = asyncio.Event()

    async def watch_loop():  # gaps between 10 ms sleeps: the event loop was busy
        last = time.perf_counter()
        while not done.is_set():
            await asyncio.sleep(0.01)
            now = time.perf_counter()
            stalls.append((now, now - last - 0.01))
            last = now

    w0, c0 = time.perf_counter(), time.process_time()
    watcher = asyncio.create_task(watch_loop())
    app = PqxApp(path)
    out["init wall"], out["init cpu"] = time.perf_counter() - w0, time.process_time() - c0
    async with app.run_test(size=(200, 50)) as pilot:
        grid = app.query_one(GridTable)
        key: list = []  # (sent at, cursor column then): a → once the first page is in

        def key_done() -> bool:
            if key and grid.cursor_column != key[1]:
                out["first key"] = time.perf_counter() - key[0]
                return True
            return False

        marks = {"grid": lambda: app.page is not None, "tabs": lambda: _tabs_ready(app),
                 "idle": lambda: _tabs_ready(app) and _idle(app), "key": key_done}
        t_grid = None
        while marks:
            for k, f in list(marks.items()):
                if f():
                    if k != "key":
                        out[f"{k} wall"], out[f"{k} cpu"] = time.perf_counter() - w0, time.process_time() - c0
                    del marks[k]
                    if k == "grid":
                        t_grid = time.perf_counter()
                        # straight to the driver, as a terminal would: pilot.press waits for the whole
                        # process (worker threads included) to go idle first
                        key[:] = [time.perf_counter(), grid.cursor_column]
                        app._driver.send_message(events.Key("right", None))
            await pilot.pause(0.005)
        done.set()
        await watcher
        out["stall to grid"] = max((s for t, s in stalls if t <= t_grid), default=0)
        out["stall after"] = max((s for t, s in stalls if t > t_grid), default=0)
    return out


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("files", nargs="*")
    p.add_argument("--cols", type=int, default=0, help="also a generated file this wide (5000 rows)")
    p.add_argument("--profile", action="store_true", help="cProfile pqx frames up to idle")
    a = p.parse_args()
    os.environ.setdefault("XDG_CONFIG_HOME", tempfile.mkdtemp())  # never read the user's saved formats
    with tempfile.TemporaryDirectory() as tmp:
        files = list(a.files)
        if a.cols:
            from grid_bench import make_wide
            path = os.path.join(tmp, f"wide{a.cols}.parquet")
            make_wide(path, a.cols, 5000)
            files.append(path)
        for path in files:
            prof = cProfile.Profile() if a.profile else None
            if prof:
                prof.enable()
            res = asyncio.run(run(path))
            if prof:
                prof.disable()
                pstats.Stats(prof).sort_stats("cumulative").print_stats(r"pqx/", 25)
            print(f"{os.path.basename(path)}:  " + "  ".join(
                f"{k} {v * 1000:.0f}" for k, v in res.items()) + "  (ms)")


if __name__ == "__main__":
    main()
