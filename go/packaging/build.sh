#!/usr/bin/env bash
# Build a release pqx binary for the host platform.
#
#   go/packaging/build.sh VERSION OUTPUT
#
# VERSION is the PEP 440 version the wheel carries (e.g. 0.3.0rc1); `pqx --version`
# prints it. The binary is stripped (-s -w) and built with -trimpath.
#
# Linux: libstdc++ and libgcc are linked statically, so the binary needs only glibc
# (libc, libm, libresolv, ld-linux). duckdb-go-bindings asks for -lstdc++ itself,
# which -static-libstdc++ doesn't cover, so the static archive is put first on the
# library path. Release builds run in a manylinux_2_28 container (glibc 2.28); a
# build on a newer system needs that system's glibc.
# macOS: MACOSX_DEPLOYMENT_TARGET defaults to 13.0, the oldest macOS Go 1.27 runs on
# (DuckDB's prebuilt libraries target 11.0).
# Windows: needs MinGW-w64 gcc 14.2 for UCRT (MinGW-Builds posix-seh-ucrt) on PATH,
# the toolchain DuckDB's prebuilt Windows libraries are built with; MSYS2's newer
# gcc doesn't link them, nor does an MSVCRT MinGW.
set -euo pipefail

if [ $# -ne 2 ]; then
	echo "usage: $0 VERSION OUTPUT" >&2
	exit 2
fi
version=$1
out=$(cd "$(dirname "$2")" && pwd)/$(basename "$2")
cd "$(dirname "$0")/.."   # go/

export CGO_ENABLED=1
extld=""
case "$(go env GOOS)" in
linux)
	lib=$(${CC:-gcc} -print-file-name=libstdc++.a)
	if [ ! -f "$lib" ]; then
		echo "build.sh: no static libstdc++.a (install libstdc++-static)" >&2
		exit 1
	fi
	static=$(mktemp -d)
	trap 'rm -rf "$static"' EXIT
	ln -s "$lib" "$static/libstdc++.a"
	export CGO_LDFLAGS="${CGO_LDFLAGS:+$CGO_LDFLAGS }-L$static"
	extld="-static-libstdc++ -static-libgcc"
	;;
darwin)
	export MACOSX_DEPLOYMENT_TARGET=${MACOSX_DEPLOYMENT_TARGET:-13.0}
	;;
esac

ldflags="-s -w -X main.version=$version"
if [ -n "$extld" ]; then
	ldflags="$ldflags -extldflags '$extld'"
fi
go build -tags duckdb_arrow -trimpath -ldflags "$ldflags" -o "$out" ./cmd/pqx
