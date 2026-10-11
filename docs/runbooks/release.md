# Releasing the Go pqx (0.3.x)

How to cut a release candidate of the Go pqx to TestPyPI, and later a release to
PyPI. Decisions D4 (one PyPI name, binary-only wheels, release candidates first),
D5 (cutover) and D6 (platforms) are in [go-port.md](../design/go-port.md).

Steps marked **[owner]** need the owner's approval or are done by the owner. The
release workflow (`.github/workflows/go-release.yml`) uploads to PyPI only when a
`v0.3.*` tag is pushed, to TestPyPI only when dispatched with `publish: testpypi`,
and never publishes a GitHub Release: on a tag it creates a draft, which the owner
publishes.

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
  the version given; a `v0.3.*` tag builds the tag minus its `v` (the workflow fails
  unless that is a normalized PEP 440 0.3.x version and every binary prints it).
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

**Protect the environments first (required), then add the trusted publishers.**
A PyPI trusted publisher trusts any run of the named workflow that names the named
environment. An unprotected environment can be named by a run on any ref: a
same-repository pull request that edits `go-release.yml`, or a dispatch from any
branch. Such a run gets an OIDC token PyPI accepts and can upload whatever it
built. The environment's protection rules are what limit the token to reviewed
release runs.

1. **GitHub environment `pypi`** (exists; `publish.yml` uses it too). Settings →
   Environments → `pypi`:
   - **Required reviewers:** the owner, so every upload waits for a click (also
     "prevent self-review" off, so the owner can approve their own tag).
   - **Deployment branches and tags:** "Selected branches and tags", with only the
     tag rule `v*`. That covers `go-release.yml` on `refs/tags/v0.3.*` and
     `publish.yml` on `refs/tags/v0.2.*` (python-0.2), and nothing on a branch or a
     pull request.
   - Optionally, a tag ruleset (Settings → Rules → Rulesets) on `v*` that lets only
     the owner create, update or delete such tags, so nobody else can push a tag that
     reaches the environment.
2. **GitHub environment `testpypi`** (new), the same way: required reviewer the
   owner; deployment branches: only `master` (dispatches run on the dispatched
   branch).
3. **PyPI trusted publisher**, only after step 1. On pypi.org, project `pqx`, add a
   second trusted publisher: owner `mjuric`, repository `pqx`, workflow
   `go-release.yml`, environment `pypi`. Keep the existing one (`publish.yml`,
   environment `pypi`): the `python-0.2` branch uses it for 0.2.x fixes.
4. **TestPyPI trusted publisher**, only after step 2. On test.pypi.org, for project
   `pqx` (a pending publisher if the project doesn't exist there yet): owner
   `mjuric`, repository `pqx`, workflow `go-release.yml`, environment `testpypi`.
5. **Dispatch.** GitHub only offers "Run workflow" for workflows on the default
   branch. Once `native-port` is merged, `go-release.yml` is on `master` and can be
   dispatched (the `testpypi` environment then admits only runs on `master`).

## Release candidate to TestPyPI

1. **Build without uploading.** Check the run is green on all five platforms:
   ```bash
   gh workflow run go-release.yml --ref master -f version=0.3.0rc1 -f publish=none
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
   gh workflow run go-release.yml --ref master -f version=0.3.0rc1 -f publish=testpypi
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

## The cutover (prepared in the `cutover` PR into `native-port`, 2026-10-11)

Done in that PR, so `master` is Go-only once `native-port` merges (D5):

- [x] Python pqx removed: `pqx/`, `tests/`, `pyproject.toml`, `MANIFEST.in`; its
  benchmarks moved to `tools/` as `uv run` scripts. The golden-file generators and
  the parity harness take Python pqx 0.2.x from PyPI (`go/testdata/README.md`,
  `go/bench/parity/README.md`).
- [x] `ci.yml` and `publish.yml` removed: `publish.yml` ran on every published
  GitHub Release and would have tried to build the removed package.
- [x] `go-release.yml` runs on `v0.3.*` tags on `master`, with a `test` job (the
  Go tests on the tagged commit), a `pypi` job (environment `pypi`, trusted
  publishing) and a `github-release` job (a draft Release with the five archives).
- [x] `go.yml` is the CI: Go tests on Linux and macOS, ruff, actionlint and the
  harness selftests.

Left for the owner, in this order:

1. **Python 0.2.0 final**, from `master` *before* `native-port` merges (it is still
   Python there): publish the GitHub Release `v0.2.0` as for the release candidates
   (`publish.yml` uploads it). Then keep a branch for 0.2.x fixes:
   ```bash
   git fetch origin --tags
   git branch python-0.2 v0.2.0 && git push origin python-0.2
   ```
   The branch has Python pqx with its own `ci.yml` and `publish.yml`.
2. The one-time setup above: protect `pypi` and `testpypi` first, then add the
   trusted publishers for `go-release.yml`.
3. **Merge `native-port` into `master`** (the integration PR).
4. **Release candidate**: `git tag v0.3.0rc1 origin/master && git push origin
   v0.3.0rc1` (below).
5. **0.3.0 final** the same way, after the checks on the release candidate.

## Release to PyPI  **[owner]**

A tag runs the whole release; there is nothing to upload by hand, and v0.3.x
GitHub Releases are never created by hand (the tag push drafts it; a hand-made
Release, or one made with `gh release create`, would also create the tag on
whatever commit it names).

1. **Tag and push** **[owner approves]**: `git tag v0.3.0 origin/master && git push
   origin v0.3.0` (or `v0.3.0rc1` for a release candidate). The tagged commit must
   be on `master`: the `version` job fails otherwise, so a tag on `native-port` or
   any other branch publishes nothing.
2. **The `Go release` run** (`gh run watch`): `version` derives `0.3.0` from the tag
   and checks it; `test` runs `make vet` and `make test` on the tagged commit; the
   five platform builds and `check` run as for a candidate; then
   - `pypi` uploads the five wheels, taken straight from the platform builds'
     artifacts, to pypi.org, after the owner approves the `pypi` environment's
     deployment. A version can be uploaded once only: a broken release needs a new
     version (`0.3.1`, or `rc2`). If the upload is cut off part-way, re-run the
     failed `pypi` job ("Re-run failed jobs"): it skips the wheels already on PyPI
     (`skip-existing`) and uploads the rest.
   - `github-release` creates a **draft** GitHub Release for the tag, titled
     `pqx 0.3.0`, with the five archives and generated notes, targeting the tagged
     commit, marked pre-release for `aN`/`bN`/`rcN`/`.devN` versions. Re-running the
     job replaces the files of its own draft; it refuses a published Release, a draft
     made from another commit (delete that draft first), and fails if the Release
     can't be looked up.
3. **Publish the GitHub Release** **[owner]**: edit the draft's notes on GitHub and
   publish it (or `gh release edit v0.3.0 --draft=false`). Nothing runs when a
   Release is published.
4. **Check the installs** against pypi.org as in step 4 of the release candidate
   (`pip install pqx==0.3.0`, `pipx`, `uvx pqx@0.3.0`); a release candidate needs
   `--pre` or the exact version.

A `v0.2.*` tag, any other tag, and a published Release run nothing from
`go-release.yml`.

## Python pqx 0.2.x fixes

Released from the `python-0.2` branch, which keeps Python pqx's `ci.yml` and
`publish.yml` (unchanged from `master` before the cutover):

1. Commit the fix to `python-0.2` (through a PR into `python-0.2`); its `ci.yml`
   runs the Python tests.
2. Publish a GitHub Release with a new tag on that branch, not marked latest
   **[owner approves]**:
   ```bash
   gh release create v0.2.1 --target python-0.2 --latest=false --notes-file notes.md
   ```
   `publish.yml` runs from the tagged commit, builds Python pqx (setuptools-scm takes
   `0.2.1` from the tag) and uploads it to PyPI with the existing trusted publisher.
   `go-release.yml` doesn't run (not a `v0.3.*` tag).
3. pip keeps installing 0.3.x where a 0.3 wheel matches, and 0.2.1 elsewhere (the
   fallback in "What a release is").
