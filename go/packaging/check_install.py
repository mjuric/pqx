"""Install a pqx wheel into a fresh venv with pip and check `pqx --version` (standard library only).

    python check_install.py WHEEL VERSION

Exits non-zero if the install fails, pqx isn't in the venv's bin/ (Scripts\\ on
Windows) with its executable bit, or `pqx --version` doesn't print `pqx VERSION`.
Prints the installed size of the binary.
"""
import os
import subprocess
import sys
import tempfile
import venv


def main():
    wheel, version = sys.argv[1], sys.argv[2]
    with tempfile.TemporaryDirectory() as d:
        venv.create(d, with_pip=True)
        bindir = os.path.join(d, "Scripts" if os.name == "nt" else "bin")
        py = os.path.join(bindir, "python.exe" if os.name == "nt" else "python")
        subprocess.run([py, "-m", "pip", "install", "--no-index", "--disable-pip-version-check", "-q", wheel], check=True)
        exe = os.path.join(bindir, "pqx.exe" if os.name == "nt" else "pqx")
        if not os.path.isfile(exe):
            print(f"FAIL: {exe} not installed", file=sys.stderr)
            return 1
        if os.name != "nt" and not os.access(exe, os.X_OK):
            print(f"FAIL: {exe} is not executable", file=sys.stderr)
            return 1
        out = subprocess.run([exe, "--version"], check=True, capture_output=True, text=True).stdout
        first = out.splitlines()[0] if out else ""
        if first != f"pqx {version}":
            print(f"FAIL: pqx --version printed {first!r}, expected 'pqx {version}'", file=sys.stderr)
            return 1
        print(f"pip install OK: {exe} ({os.path.getsize(exe):,} bytes) prints {first!r}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
