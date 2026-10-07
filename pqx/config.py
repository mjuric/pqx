"""Per-column display formats remembered between runs.

They live in ``$XDG_CONFIG_HOME/pqx/formats.yaml`` (``~/.config/pqx/formats.yaml``
by default), keyed by column name and shared by every file::

    columns:
      ra: .4f        # a Python format spec
      psfFlux: 3     # digits: decimals for MJD/angle/mag columns, significant digits otherwise

Each change re-reads the file, merges itself in and replaces the file atomically,
so concurrent pqx sessions don't clobber each other's columns.
"""
from __future__ import annotations

import os
import tempfile
from pathlib import Path

import yaml

HEADER = """\
# pqx column display formats, keyed by column name (shared by all files).
# A value is either a Python format spec (.4f, .2e, ",d") or an integer number
# of digits: decimals for MJD / angle / magnitude columns, significant digits
# for other floats. pqx rewrites this file when you press < > or F in the grid,
# so comments other than this header are not kept.
"""


class ConfigError(Exception):
    """The formats file exists but can't be read or parsed."""


def formats_path() -> Path:
    base = os.environ.get("XDG_CONFIG_HOME", "")
    root = Path(base) if base and os.path.isabs(base) else Path.home() / ".config"
    return root / "pqx" / "formats.yaml"


def _valid(v) -> bool:
    return (isinstance(v, int) and not isinstance(v, bool) and v >= 0) or (isinstance(v, str) and v != "")


def load_formats(path: Path | None = None) -> dict[str, int | str]:
    """The saved overrides; ``{}`` if there is no file. Raises :class:`ConfigError` if it is unreadable.

    Entries with a value that is neither a non-negative int nor a non-empty string are skipped."""
    path = path or formats_path()
    try:
        text = path.read_text()
    except FileNotFoundError:
        return {}
    except OSError as e:
        raise ConfigError(f"{path}: {e}") from e
    try:
        doc = yaml.safe_load(text) or {}
    except yaml.YAMLError as e:
        raise ConfigError(f"{path}: {e}") from e
    cols = doc.get("columns") if isinstance(doc, dict) else None
    if cols is None:
        cols = {}
    if not isinstance(cols, dict):
        raise ConfigError(f"{path}: 'columns' must be a mapping")
    return {str(k): v for k, v in cols.items() if _valid(v)}


def save_format(name: str, value: int | str | None, path: Path | None = None) -> Path:
    """Set (or with ``None`` remove) one column's override in the file. Raises :class:`ConfigError`
    rather than overwrite a file it can't parse, and ``OSError`` if it can't write."""
    path = path or formats_path()
    cols = load_formats(path)
    if value is None:
        cols.pop(name, None)
    else:
        cols[name] = value
    path.parent.mkdir(parents=True, exist_ok=True)
    body = yaml.safe_dump({"columns": dict(sorted(cols.items()))}, sort_keys=False, allow_unicode=True,
                          default_flow_style=False)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".formats.", suffix=".tmp")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(HEADER + body)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
    return path


def parse_override(text: str) -> int | str | None:
    """``"3"`` → 3 digits, ``".2e"`` → a spec, ``""`` → None (automatic)."""
    text = text.strip()
    if not text:
        return None
    return int(text) if text.isdigit() else text
