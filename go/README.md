# pqx Go prototype

A prototype of pqx in Go, to measure startup, paging and cancellation against
the Python version. See `docs/design/go-prototype.md` for the plan and
`docs/design/native-port.md` for the background. Not the shipped pqx.

```
make build && bin/pqx file.parquet
make test
```

Needs Go 1.27 and a C compiler (cgo, for DuckDB).
