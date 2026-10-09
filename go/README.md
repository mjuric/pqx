# pqx in Go

The Go source of pqx: the program in `cmd/pqx`, everything else under
`internal/`. The [README at the top of the repository](../README.md#development)
describes the layout; the port's plan and progress are in
[`docs/design/go-port.md`](../docs/design/go-port.md).

```
make build && bin/pqx file.parquet
make test
make vet
```

Needs Go 1.27 and a C/C++ compiler (cgo, for DuckDB). The `Makefile` sets the
`duckdb_arrow` build tag that every build and test needs.
