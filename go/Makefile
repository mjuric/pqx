# The duckdb_arrow tag enables duckdb-go's Arrow interface; always build with it.
TAGS := duckdb_arrow
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test vet bench clean
build:
	go build -tags $(TAGS) -ldflags "-X main.version=$(VERSION)" -o bin/pqx ./cmd/pqx
test:
	go test -tags $(TAGS) ./...
vet:
	go vet -tags $(TAGS) ./...
bench:
	go test -tags $(TAGS) -run '^$$' -bench . ./internal/data/
clean:
	rm -rf bin
