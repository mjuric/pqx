"""Wrap a built pqx binary in a platform wheel for PyPI (standard library only).

    python make_wheel.py --binary bin/pqx --version 0.3.0rc1 --plat manylinux_2_28_x86_64 \\
        [--readme README.md] [--license LICENSE] [--out-dir dist]

The wheel, pqx-<version>-py3-none-<plat>.whl, holds only the binary, as
pqx-<version>.data/scripts/pqx (pqx.exe on Windows), with the executable bit in the
zip entry's external attributes; installers copy it to the environment's bin/ (or
Scripts\\) directory. There is no Python code and there are no dependencies: this is
the layout ruff and uv ship in. Beside it go METADATA (the README as the long
description), WHEEL, the licence under licenses/, and RECORD with sha256 hashes.

Entries are written in a fixed order with a fixed timestamp (SOURCE_DATE_EPOCH if
set, else 1980-01-01), so the same inputs give the same wheel.
"""
import argparse
import base64
import hashlib
import os
import re
import sys
import time
import zipfile

NAME = "pqx"
SUMMARY = "A fast, friendly terminal explorer for Parquet files"
URL = "https://github.com/mjuric/pqx"
# wheel platform tags the release builds for (go/packaging/README.md explains each)
PLATFORMS = {
    "manylinux_2_28_x86_64",
    "manylinux_2_28_aarch64",
    "macosx_13_0_arm64",
    "macosx_13_0_x86_64",
    "win_amd64",
}
# PEP 440 public versions: N(.N)*[{a|b|rc}N][.postN][.devN]
VERSION_RE = re.compile(r"^\d+(\.\d+)*((a|b|rc)\d+)?(\.post\d+)?(\.dev\d+)?$")


def record_hash(data):
    digest = hashlib.sha256(data).digest()
    return "sha256=" + base64.urlsafe_b64encode(digest).rstrip(b"=").decode()


def metadata(version, readme):
    lines = [
        "Metadata-Version: 2.4",
        f"Name: {NAME}",
        f"Version: {version}",
        f"Summary: {SUMMARY}",
        "Author: Mario Juric",
        "License-Expression: BSD-3-Clause",
        "License-File: LICENSE",
        f"Project-URL: Homepage, {URL}",
        f"Project-URL: Source, {URL}",
        f"Project-URL: Issues, {URL}/issues",
        "Keywords: parquet,tui,terminal,duckdb",
        "Classifier: Environment :: Console",
        "Classifier: Intended Audience :: Science/Research",
        "Classifier: Operating System :: MacOS",
        "Classifier: Operating System :: Microsoft :: Windows",
        "Classifier: Operating System :: POSIX :: Linux",
        "Classifier: Topic :: Database",
        "Classifier: Topic :: Scientific/Engineering",
        "Description-Content-Type: text/markdown",
    ]
    return ("\n".join(lines) + "\n\n" + readme).encode()


def wheel_file(plat):
    return (
        "Wheel-Version: 1.0\n"
        "Generator: pqx make_wheel.py\n"
        "Root-Is-Purelib: false\n"
        f"Tag: py3-none-{plat}\n"
    ).encode()


def build(binary, version, plat, readme_path, license_path, out_dir):
    if not VERSION_RE.match(version):
        raise SystemExit(f"make_wheel: {version!r} is not a normalized PEP 440 version (e.g. 0.3.0, 0.3.0rc1)")
    if plat not in PLATFORMS:
        raise SystemExit(f"make_wheel: unknown platform tag {plat!r}; expected one of {sorted(PLATFORMS)}")
    exe = "pqx.exe" if plat.startswith("win") else "pqx"
    with open(binary, "rb") as f:
        bin_data = f.read()
    with open(readme_path, encoding="utf-8") as f:
        readme = f.read()
    with open(license_path, "rb") as f:
        license_data = f.read()

    dist_info = f"{NAME}-{version}.dist-info"
    files = [  # (archive name, data, mode)
        (f"{NAME}-{version}.data/scripts/{exe}", bin_data, 0o755),
        (f"{dist_info}/METADATA", metadata(version, readme), 0o644),
        (f"{dist_info}/WHEEL", wheel_file(plat), 0o644),
        (f"{dist_info}/licenses/LICENSE", license_data, 0o644),
    ]
    record = "".join(f"{name},{record_hash(data)},{len(data)}\n" for name, data, _ in files)
    record += f"{dist_info}/RECORD,,\n"
    files.append((f"{dist_info}/RECORD", record.encode(), 0o644))

    epoch = int(os.environ.get("SOURCE_DATE_EPOCH") or 315532800)  # 1980-01-01, zip's earliest
    stamp = time.gmtime(max(epoch, 315532800))[:6]
    os.makedirs(out_dir, exist_ok=True)
    path = os.path.join(out_dir, f"{NAME}-{version}-py3-none-{plat}.whl")
    tmp = path + ".tmp"
    with zipfile.ZipFile(tmp, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
        for name, data, mode in files:
            info = zipfile.ZipInfo(name, date_time=stamp)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.create_system = 3  # Unix, so the mode bits below are honoured
            info.external_attr = (0o100000 | mode) << 16  # regular file with this mode
            zf.writestr(info, data)
    os.replace(tmp, path)
    return path


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    root = os.path.dirname(os.path.dirname(here))
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--binary", required=True, help="the built pqx executable")
    ap.add_argument("--version", required=True, help="PEP 440 version, e.g. 0.3.0rc1")
    ap.add_argument("--plat", required=True, help="wheel platform tag: " + ", ".join(sorted(PLATFORMS)))
    ap.add_argument("--readme", default=os.path.join(root, "README.md"))
    ap.add_argument("--license", default=os.path.join(root, "LICENSE"))
    ap.add_argument("--out-dir", default="dist")
    a = ap.parse_args()
    path = build(a.binary, a.version, a.plat, a.readme, a.license, a.out_dir)
    print(path)
    return 0


if __name__ == "__main__":
    sys.exit(main())
