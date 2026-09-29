"""Command-line entry point: ``pqx FILE``."""
from __future__ import annotations

import argparse
import os
import sys

from . import __version__


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(
        prog="pqx",
        description="Interactive terminal explorer for Parquet files: browse, filter (SQL), profile, "
                    "plot and export — lazily, so files of any size open instantly.",
    )
    p.add_argument("path", help="Parquet file to open")
    p.add_argument("-w", "--where", default="",
                   help="initial filter: a SQL WHERE expression, or a full 'select … from t' query")
    p.add_argument("--theme", default=None, help="Textual theme (e.g. tokyo-night, nord, gruvbox, "
                                                 "textual-light); switch live with Ctrl+P")
    g = p.add_mutually_exclusive_group()
    g.add_argument("--sample", dest="sample", action="store_true", default=None,
                   help="sample rows for stats/plots (default: on above 200M rows or 8 GiB)")
    g.add_argument("--no-sample", dest="sample", action="store_false",
                   help="always scan every row for stats/plots")
    p.add_argument("--threads", type=int, default=None,
                   help="DuckDB worker threads (default: all cores; lower it on shared machines)")
    p.add_argument("--version", action="version", version=f"pqx {__version__}")
    a = p.parse_args(argv)

    if not os.path.exists(a.path):
        print(f"pqx: {a.path}: no such file", file=sys.stderr)
        return 2
    try:
        from .app import PqxApp
        app = PqxApp(a.path, where=a.where, theme=a.theme, sample=a.sample, threads=a.threads)
    except Exception as e:  # noqa: BLE001 — surface unreadable files cleanly
        print(f"pqx: cannot open {a.path}: {e}", file=sys.stderr)
        return 1
    app.run()
    return 0


if __name__ == "__main__":
    sys.exit(main())
