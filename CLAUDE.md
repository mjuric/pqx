# Working in this repository

pqx is a terminal Parquet explorer written in Go (`go/`). Python pqx 0.2.x, the
version it was ported from, is no longer here: its code is the `v0.2.0` tag and
the `python-0.2` branch (made by the owner when 0.2.0 final is released; before
that, `master` before the cutover), and it stays the reference the port is checked against
(golden files, the pty parity harness). Plan and history of the port:
`docs/design/go-port.md`.

## Layout

- `go/cmd/pqx`: the program (options, terminal setup, wiring the UI parts).
- `go/internal/...`: `data` (arrow-go and DuckDB), `fmtx`, `cells`, `plots`,
  `config`, `theme`, and the UI under `ui/` (`app` the root model, `kit` the
  parts' shared interfaces, one package per part).
- `go/testdata`: fixtures and golden files (outputs of Python pqx 0.2.x);
  `go/testdata/README.md` says how to regenerate them.
- `go/bench/parity`: the pty parity harness (Python pqx vs Go pqx);
  `go/bench/pty`: the pty driver and benchmarks; `go/bench/screenshots`: the
  README screenshots.
- `go/packaging`: the wheel and archive builders; `.github/workflows/go-release.yml`
  builds releases; `docs/runbooks/release.md` is the release runbook.
- `tools/`: `uv run` scripts (synthetic files, Python pqx's own benchmarks).

## Checks before every commit

In `go/` (Go 1.27 and a C/C++ compiler for cgo; the `Makefile` sets the
`duckdb_arrow` tag that every build and test needs):

```sh
test -z "$(gofmt -l .)"
make vet
make test
go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 -tags duckdb_arrow ./...
```

and `ruff check .` (settings in `ruff.toml`) when Python changed. CI (`go.yml`)
runs these on Linux and macOS, plus actionlint and the harness selftests.

- Tests are network-free and use the fixtures in `go/testdata` and `t.TempDir()`.
  Tests that need Python pqx (`go/internal/config`) skip unless `PQX_REF_PYTHON`
  names a Python where `import pqx` gives Python pqx 0.2.x.
- Behaviour follows Python pqx 0.2.x unless a decision in `go-port.md` says
  otherwise. A change to what the user sees gets a parity scenario
  (`go/bench/parity/scenarios/`), checked against Python with
  `run.py --a python --b python` first.
- DuckDB threads in tests at most 8.

## Feature work with subagents

For a feature (or set of features) big enough to split up:
1. Plan it and get my approval before starting.
2. Open an integration branch with a draft PR into `master`; commit the design and plan there (`docs/design/<feature>.md`).
3. Subagents each work on their own branch started from the integration branch (in separate worktrees), commit and push after each logical unit, keep the checks above green, and open a PR into the integration branch when done.
4. You integrate: review each PR, have it independently reviewed by a separate agent if it's large, send fixes back, merge into the integration branch (a merge commit, never a squash), and run the full test suite after each merge.
5. When everything is in and CI is green, ask me before merging the integration branch into `master`.

## Releases

Never push a release tag, upload to (Test)PyPI or publish a GitHub Release without
my approval. A `v0.3.*` tag uploads to PyPI and drafts a GitHub Release; a dispatch
of `go-release.yml` with `publish: testpypi` uploads to TestPyPI
(`docs/runbooks/release.md`).
