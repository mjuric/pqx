"""Pack a built pqx binary with LICENSE and README.md for a GitHub Release (standard library only).

    python make_archive.py --binary bin/pqx --version 0.3.0rc1 --target linux-amd64 [--out-dir dist]

Writes pqx-<version>-<target>.tar.gz (or .zip for windows-* targets) holding the
directory pqx-<version>-<target>/ with pqx (pqx.exe), LICENSE and README.md. Like
make_wheel.py, the entries have a fixed order, owner and timestamp.
"""
import argparse
import gzip
import io
import os
import sys
import tarfile
import time
import zipfile

TARGETS = {"linux-amd64", "linux-arm64", "darwin-arm64", "darwin-amd64", "windows-amd64"}


def build(binary, version, target, readme, license_path, out_dir):
    if target not in TARGETS:
        raise SystemExit(f"make_archive: unknown target {target!r}; expected one of {sorted(TARGETS)}")
    win = target.startswith("windows")
    top = f"pqx-{version}-{target}"
    members = [  # (name, source file, mode)
        ("pqx.exe" if win else "pqx", binary, 0o755),
        ("LICENSE", license_path, 0o644),
        ("README.md", readme, 0o644),
    ]
    epoch = max(int(os.environ.get("SOURCE_DATE_EPOCH") or 315532800), 315532800)
    os.makedirs(out_dir, exist_ok=True)
    path = os.path.join(out_dir, top + (".zip" if win else ".tar.gz"))
    tmp = path + ".tmp"
    if win:
        with zipfile.ZipFile(tmp, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
            for name, src, mode in members:
                info = zipfile.ZipInfo(f"{top}/{name}", date_time=time.gmtime(epoch)[:6])
                info.compress_type = zipfile.ZIP_DEFLATED
                info.create_system = 3
                info.external_attr = (0o100000 | mode) << 16
                with open(src, "rb") as f:
                    zf.writestr(info, f.read())
    else:
        # gzip header with mtime 0 and no file name, so the archive is reproducible
        with open(tmp, "wb") as raw, gzip.GzipFile(
            filename="", mode="wb", fileobj=raw, compresslevel=9, mtime=0
        ) as gz, tarfile.open(fileobj=gz, mode="w", format=tarfile.PAX_FORMAT) as tf:
            for name, src, mode in members:
                with open(src, "rb") as f:
                    data = f.read()
                info = tarfile.TarInfo(f"{top}/{name}")
                info.size, info.mode, info.mtime = len(data), mode, epoch
                info.uid = info.gid = 0
                info.uname = info.gname = ""
                tf.addfile(info, io.BytesIO(data))
    os.replace(tmp, path)
    return path


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    root = os.path.dirname(os.path.dirname(here))
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--binary", required=True)
    ap.add_argument("--version", required=True)
    ap.add_argument("--target", required=True, help=", ".join(sorted(TARGETS)))
    ap.add_argument("--readme", default=os.path.join(root, "README.md"))
    ap.add_argument("--license", default=os.path.join(root, "LICENSE"))
    ap.add_argument("--out-dir", default="dist")
    a = ap.parse_args()
    print(build(a.binary, a.version, a.target, a.readme, a.license, a.out_dir))
    return 0


if __name__ == "__main__":
    sys.exit(main())
