# Releasing the Go pqx (0.3.x)

How to cut a release candidate of the Go pqx to TestPyPI, and later a release to
PyPI. Decisions D4 (one PyPI name, binary-only wheels, release candidates first),
D5 (cutover) and D6 (platforms) are in [go-port.md](../design/go-port.md).

Steps marked **[owner]** need the owner's approval or are done by the owner. The
release workflow never uploads to PyPI and never creates a GitHub Release by itself.

## What a release is

| artifact | name | where |
|---|---|---|
| wheel, one per platform | `pqx-<v>-py3-none-<plat>.whl` | TestPyPI / PyPI |
| archive, one per platform | `pqx-<v>-<target>.tar.gz` (`.zip` on Windows), with `pqx`, `LICENSE`, `README.md` | GitHub Release |

| target | wheel tag | built on | needs at run time |
|---|---|---|---|
| linux-amd64 | `manylinux_2_28_x86_64` | `ubuntu-24.04`, in `quay.io/pypa/manylinux_2_28_x86_64` | glibc 2.28+ (libc, libm, libpthread, libdl, libresolv) |
| linux-arm64 | `manylinux_2_28_aarch64` | `ubuntu-24.04-arm`, in `quay.io/pypa/manylinux_2_28_aarch64` | glibc 2.28+ |
| darwin-arm64 | `macosx_13_0_arm64` | `macos-14` | macOS 13+ |
| darwin-amd64 | `macosx_13_0_x86_64` | `macos-15-intel` | macOS 13+ |
| windows-amd64 | `win_amd64` | `windows-2022`, MinGW-Builds gcc 14.2.0 UCRT | Windows 10+ (UCRT) |

- **The wheel holds only the binary**, as `pqx-<v>.data/scripts/pqx`; pip, pipx and
  uv copy it to the environment's `bin/` (`Scripts\` on Windows). No Python code, no
  dependencies, no `Requires-Python`. Built by `go/packaging/make_wheel.py`.
- **No sdist.** Building pqx needs Go, a C/C++ toolchain and DuckDB's static
  libraries; an sdist would make pip on an unsupported platform try, and fail, to
  build from source. Without one pip reports "no matching distribution" instead.
  Caveat: pip's resolver falls back to the newest release that has a matching file,
  so on a platform outside the table (musl Linux, glibc < 2.28, Windows on ARM,
  macOS < 13) `pip install pqx` will install Python pqx 0.2.x, whose
  `py3-none-any` wheel matches everywhere.
- **Linux binaries** link libstdc++ and libgcc statically (`go/packaging/build.sh`),
  so they need only glibc; the `go-release` workflow fails if the binary needs
  another library or a glibc symbol newer than 2.28, and runs `auditwheel show`.
- **macOS binaries** are built with `MACOSX_DEPLOYMENT_TARGET=13.0` (Go 1.27's
  minimum; DuckDB's libraries need 11.0); the workflow checks the binary's `minos`.
  They are ad-hoc signed, not notarized: installs through pip/pipx/uv work, but an
  archive downloaded with a browser is quarantined by Gatekeeper
  (`xattr -d com.apple.quarantine pqx` clears it).
- **Sizes** (0.3.0.dev3, 2026-10-09; table in
  [go-port.md](../design/go-port.md#release-packaging-wp13a)): 82–95 MB installed,
  26–32 MB per wheel. PyPI's per-file limit is 100 MB and its per-project limit
  10 GB (about 147 MB per release, so roughly 65 releases before asking PyPI for
  more).

## Versions

- Versions are PEP 440, normalized: `0.3.0rc1`, `0.3.0`, `0.3.1`. The git tag is the
  version with a `v`: `v0.3.0rc1`.
- `pqx --version` prints the same version (`-X main.version`).
- Pull-request runs of the workflow build `0.3.0.dev<run number>`; a dispatch builds
  the version given; a release tag (once enabled) builds the tag minus its `v`.
- Release candidates (`rcN`) are pre-releases: pip, pipx and uv install them only
  when asked (`--pre`, `pqx==0.3.0rc1`).

## Local build (for testing)

```bash
export PATH=/root/sdk/go/bin:$PATH TMPDIR=/dev/shm/$USER-pqx   # not /tmp: big
go/packaging/build.sh 0.3.0.dev0 /dev/shm/$USER-pqx/pqx
python3 go/packaging/make_wheel.py --binary /dev/shm/$USER-pqx/pqx --version 0.3.0.dev0 \
    --plat manylinux_2_28_x86_64 --out-dir /dev/shm/$USER-pqx/dist
python3 go/packaging/check_install.py /dev/shm/$USER-pqx/dist/*.whl 0.3.0.dev0
python3 go/packaging/smoke.py /dev/shm/$USER-pqx/pqx go/testdata/fixtures/demo.parquet
```

Linux needs `libstdc++-static` (`dnf --enablerepo=crb install libstdc++-static` on
Rocky 10). A local build links against the local glibc (2.39 here), and Rocky 10's
static libstdc++ is compiled for x86-64-v3 (AVX2), so the wheel is **not** a valid
`manylinux_2_28` wheel: `auditwheel show` says so. Release wheels come only from the
workflow.

## One-time setup  **[owner]**

1. **TestPyPI trusted publisher.** On test.pypi.org, for project `pqx` (a pending
   publisher if the project doesn't exist there yet): owner `mjuric`, repository
   `pqx`, workflow `go-release.yml`, environment `testpypi`.
2. **GitHub environment** `testpypi` in the repository settings (optionally with the
   owner as required reviewer, so every upload waits for a click).
3. **Make the workflow dispatchable.** GitHub only offers "Run workflow" for
   workflows that exist on the default branch (`master`). Before the cutover, either
   - merge `.github/workflows/go-release.yml` alone into `master` (it then runs from
     whichever ref is dispatched, e.g. `native-port`); or
   - enable the `push: tags: ["v0.3.*"]` trigger on `native-port` and push a release
     candidate tag there (the tag runs the workflow file of the tagged commit). Note
     `ci.yml` also runs on `v*` tags.

## Release candidate to TestPyPI

1. **Build without uploading.** Check the run is green on all five platforms:
   ```bash
   gh workflow run go-release.yml --ref native-port -f version=0.3.0rc1 -f publish=none
   gh run watch
   ```
   Each platform job builds, prints the binary's libraries (`NEEDED`/`otool -L`/DLLs),
   runs `pqx --version`, `pqx --help` and a pty open of `demo.parquet`, makes the
   wheel and the archive, installs the wheel with pip and with `uvx`, and (Linux)
   runs `auditwheel show`. The `check` job runs `twine check --strict` and
   `check-wheel-contents`.
2. **Look at the artifacts** (`gh run download <run-id> -n wheels`): five wheels,
   sizes as in the table above, `python -m zipfile -l` shows one script and the
   dist-info files.
3. **Upload to TestPyPI.  [owner approves]**
   ```bash
   gh workflow run go-release.yml --ref native-port -f version=0.3.0rc1 -f publish=testpypi
   ```
   A version can be uploaded once only; a fix needs `rc2`.
4. **Check the installs on each platform** (Linux x86-64 and arm64, macOS arm64 and
   Intel, Windows): each must install the right wheel, print `pqx 0.3.0rc1`, and open
   a file.
   ```bash
   T=https://test.pypi.org/simple/
   pip install --index-url $T --pre pqx==0.3.0rc1          # in a fresh venv
   pipx install --index-url $T --pip-args=--pre pqx==0.3.0rc1
   uvx --default-index $T pqx@0.3.0rc1 --version
   pqx some.parquet
   ```
   Also check the TestPyPI project page renders the README.
5. **The owner's terminal checks** (iTerm2, GNU screen, macOS; suspend with Ctrl+Z
   during a heavy query) on the release-candidate binaries, as listed in
   go-port.md.

## Release to PyPI (after the cutover)  **[owner]**

Not wired up yet: `go-release.yml` has no PyPI job. At the cutover (D5), with the
owner's approval:

1. Merge `native-port` into `master` (the integration PR) **[owner]**.
2. In the same change: delete or retarget `publish.yml` (Python's). It runs on every
   published GitHub Release and would try to build the removed Python package.
3. Enable the `push: tags: ["v0.3.*"]` trigger in `go-release.yml` and add a `pypi`
   job like `testpypi` (environment `pypi`, no `repository-url`), gated on the tag.
4. On pypi.org, add `go-release.yml` with environment `pypi` as a second trusted
   publisher of `pqx` **[owner]**; remove `publish.yml`'s once 0.3.0 is out.
5. Tag and push: `git tag v0.3.0 && git push origin v0.3.0` **[owner approves]**.
   The workflow builds, checks and uploads the wheels.
6. Create the GitHub Release from the tag with the five archives from the run's
   artifacts **[owner approves]**:
   ```bash
   gh run download <run-id> -p 'pqx-*' -D release
   gh release create v0.3.0 release/*/*.tar.gz release/*/*.zip --verify-tag --notes-file notes.md
   ```
7. Repeat the install checks of step 4 above against pypi.org.

0.2.x fixes are released from the `python-0.2` branch with its own `publish.yml`
(D5).
