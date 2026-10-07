"""Command-line entry point: ``pqx FILE``."""
from __future__ import annotations

import argparse
import os
import sys

from . import AUTHOR, HOMEPAGE, __version__
from .config import parse_override
from .fmt import override_error, sanitize


VERSION_TEXT = f"""\
pqx {__version__}
Copyright (C) 2026 {AUTHOR}
License BSD-3-Clause: <https://opensource.org/license/bsd-3-clause>
This is free software: you are free to change and redistribute it.
There is NO WARRANTY, to the extent permitted by law.

Written by {AUTHOR}."""

EPILOG = f"""\
Examples:
  pqx trips.parquet
  pqx trips.parquet -w "payment_type = 'card'"
  pqx trips.parquet -w "select vendor, count(*) from t group by 1"

Inside pqx, press ? for keys.

Report bugs at: <{HOMEPAGE}/issues>
pqx home page: <{HOMEPAGE}>
Written by {AUTHOR}."""


class _HelpFormatter(argparse.HelpFormatter):
    """Description and option help wrapped as usual; the epilog (the text with line breaks)
    kept as written."""

    def _fill_text(self, text, width, indent):
        if "\n" not in text:
            return super()._fill_text(text, width, indent)
        return "".join(indent + line for line in text.splitlines(keepends=True))


class _VersionAction(argparse.Action):
    """``--version``: GNU-style version and licence text (argparse's own would rewrap it)."""

    def __init__(self, option_strings, dest=argparse.SUPPRESS, default=argparse.SUPPRESS, help=None):
        super().__init__(option_strings, dest=dest, default=default, nargs=0, help=help)

    def __call__(self, parser, namespace, values, option_string=None):
        print(VERSION_TEXT)
        parser.exit()


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(
        prog="pqx",
        description="Interactive terminal explorer for Parquet files: browse, filter (SQL), profile, "
                    "plot and export — lazily, so files of any size open instantly.",
        epilog=EPILOG,
        formatter_class=_HelpFormatter,
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
    p.add_argument("--version", action=_VersionAction, help="show the version and licence, and exit")
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
            p.error(f"--format {sanitize(item)}: {sanitize(err)}")

    if not os.path.exists(a.path):
        print(f"pqx: {sanitize(a.path)}: no such file", file=sys.stderr)
        return 2
    try:
        from .app import PqxApp
        app = PqxApp(a.path, where=a.where, theme=a.theme, sample=a.sample, threads=a.threads,
                     accent=a.accent, dim=a.dim, border=a.border, formats=formats)
    except Exception as e:  # noqa: BLE001 — surface unreadable files cleanly
        print(f"pqx: cannot open {sanitize(a.path)}: {sanitize(str(e))}", file=sys.stderr)  # (both may hold the file's name)
        return 1
    app.run()
    return 0


if __name__ == "__main__":
    sys.exit(main())
