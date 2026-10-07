"""Command-line entry point: ``pqx FILE``."""
from __future__ import annotations

import argparse
import os
import sys

from . import __version__
from .config import parse_override
from .fmt import override_error


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(
        prog="pqx",
        description="Interactive terminal explorer for Parquet files: browse, filter (SQL), profile, "
                    "plot and export — lazily, so files of any size open instantly.",
    )
    p.add_argument("path", help="Parquet file to open")
    p.add_argument("-w", "--where", default="",
                   help="initial filter: a SQL WHERE expression, or a full 'select … from t' query")
    p.add_argument("--accent", choices=["blue", "cyan", "magenta", "green", "yellow"], default=None,
                   help="focus colour, from the terminal's palette (default: blue; env PQX_ACCENT)")
    p.add_argument("--dim", choices=["faint", "bright-black"], default=None,
                   help="how secondary text is dimmed: the faint attribute (default) or ANSI bright black "
                        "(env PQX_DIM)")
    p.add_argument("--border", default=None,
                   help="colour of unfocused panel borders, an ANSI name such as bright_black (default) or white "
                        "(env PQX_BORDER)")
    p.add_argument("--theme", default=None, help="use a Textual theme instead of the terminal's own colours")
    g = p.add_mutually_exclusive_group()
    g.add_argument("--sample", dest="sample", action="store_true", default=None,
                   help="sample rows for stats/plots (default: on above 200M rows or 8 GiB)")
    g.add_argument("--no-sample", dest="sample", action="store_false",
                   help="always scan every row for stats/plots")
    p.add_argument("--threads", type=int, default=None,
                   help="DuckDB worker threads (default: all cores; lower it on shared machines)")
    p.add_argument("--format", dest="formats", action="append", default=[], metavar="COL=SPEC",
                   help="display format for a column this session: a Python spec (.3f, .2e, ,d) or a number "
                        "of digits; repeatable. Formats set in the grid (< > F) are saved to "
                        "$XDG_CONFIG_HOME/pqx/formats.yaml (default ~/.config/pqx/formats.yaml)")
    p.add_argument("--version", action="version", version=f"pqx {__version__}")
    a = p.parse_args(argv)
    formats = {}
    for item in a.formats:
        name, eq, spec = item.partition("=")
        name = name.strip()
        if not eq or not name or not spec.strip():
            p.error(f"--format expects COL=SPEC, got {item!r}")
        formats[name] = parse_override(spec)
        err = override_error(formats[name])
        if err:
            p.error(f"--format {item}: {err}")

    if not os.path.exists(a.path):
        print(f"pqx: {a.path}: no such file", file=sys.stderr)
        return 2
    try:
        from .app import PqxApp
        app = PqxApp(a.path, where=a.where, theme=a.theme, sample=a.sample, threads=a.threads,
                     accent=a.accent, dim=a.dim, border=a.border, formats=formats)
    except Exception as e:  # noqa: BLE001 — surface unreadable files cleanly
        print(f"pqx: cannot open {a.path}: {e}", file=sys.stderr)
        return 1
    app.run()
    return 0


if __name__ == "__main__":
    sys.exit(main())
