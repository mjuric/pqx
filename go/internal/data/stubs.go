package data

import (
	"context"
)

// Stubs for the contract's methods that wave 1 builds (docs/design/go-port.md:
// WP1 views, WP2 analysis and footer). Each WP replaces its stubs.

// SetupErr is DuckDB's error binding the file, if any.
func (d *dataset) SetupErr() error {
	<-d.bound
	if d.bindErr != nil {
		return duckError(d.bindErr)
	}
	return nil
}

// Validate checks a filter as CheckWhere does; sorts and SQL views are WP1's.
func (d *dataset) Validate(ctx context.Context, v View) ([]Column, error) {
	if v.IsSQL() || len(v.OrderBy) > 0 {
		return nil, ErrNotImplemented // WP1
	}
	if err := d.checkWhere(ctx, v.Where); err != nil {
		return nil, err
	}
	return d.Columns(), nil
}

func (d *dataset) FetchColumns(ctx context.Context, fileRows []int64, cols []string) (Window, error) {
	return Window{}, ErrNotImplemented // WP1
}

func (d *dataset) FindRow(ctx context.Context, v View, fileRow int64) (int64, bool, error) {
	if v.Plain() {
		return fileRow, fileRow >= 0 && fileRow < d.numRows, nil
	}
	return 0, false, ErrNotImplemented // WP1
}

func (d *dataset) FetchAround(ctx context.Context, v View, fileRow, pos, start int64, n int, cols []string) (Window, error) {
	return Window{}, ErrNotImplemented // WP1
}
