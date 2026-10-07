"""Per-column display formats remembered between runs.

They live in ``$XDG_CONFIG_HOME/pqx/formats.yaml`` (``~/.config/pqx/formats.yaml``
by default), keyed by column name and shared by every file::

    columns:
      ra: .4f        # a Python format spec
      psfFlux: 3     # digits: decimals for MJD/angle/mag columns, significant digits otherwise

Each change takes a lock, re-reads the file, merges itself in and replaces the
file atomically, so concurrent pqx sessions don't clobber each other's columns.
"""
from __future__ import annotations

import contextlib
import os
import tempfile
from pathlib import Path

import yaml

from .fmt import override_error

try:
    import fcntl
except ImportError:  # Windows: no advisory locks; saves are still atomic
    fcntl = None

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
    if isinstance(v, bool) or not isinstance(v, (int, str)) or v == "" or (isinstance(v, int) and v < 0):
        return False
    return override_error(v) is None


def load_formats(path: Path | None = None) -> dict[str, int | str]:
    """The saved overrides; ``{}`` if there is no file. Raises :class:`ConfigError` if it is unreadable.

    Entries that aren't a digit count (0–17) or a usable format spec are skipped."""
    path = path or formats_path()
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return {}
    except (OSError, UnicodeDecodeError) as e:
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
    path = (path or formats_path()).resolve()  # a symlinked file (dotfiles) is updated, not replaced
    path.parent.mkdir(parents=True, exist_ok=True)
    with _locked(path):
        cols = load_formats(path)
        if cols.get(name) == value:
            return path
        if value is None:
            cols.pop(name, None)
        else:
            cols[name] = value
        body = yaml.safe_dump({"columns": dict(sorted(cols.items()))}, sort_keys=False, allow_unicode=True,
                              default_flow_style=False)
        try:
            mode = path.stat().st_mode & 0o777
        except FileNotFoundError:
            umask = os.umask(0)
            os.umask(umask)
            mode = 0o666 & ~umask
        fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".formats.", suffix=".tmp")
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                f.write(HEADER + body)
                f.flush()
                os.fsync(f.fileno())
            os.chmod(tmp, mode)
            os.replace(tmp, path)
        except BaseException:
            with contextlib.suppress(OSError):
                os.unlink(tmp)
            raise
    return path


@contextlib.contextmanager
def _locked(path: Path):
    """Hold an exclusive lock on a sidecar file while reading and rewriting ``path``."""
    if fcntl is None:
        yield
        return
    with open(path.with_name(f".{path.name}.lock"), "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(lock, fcntl.LOCK_UN)


def parse_override(text: str) -> int | str | None:
    """``"3"`` → 3 digits, ``".2e"`` → a spec, ``""`` → None (automatic)."""
    text = text.strip()
    if not text:
        return None
    return int(text) if text.isascii() and text.isdigit() else text
