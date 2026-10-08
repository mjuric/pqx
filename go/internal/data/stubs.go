package data

import (
	"context"
)

// Stubs for the contract's methods that wave 1 builds (docs/design/go-port.md:
// WP1 views, WP2 analysis and footer). Each WP replaces its stubs.

func (d *dataset) Info() FileInfo {
	return FileInfo{Path: d.path, Size: d.size, NumLeaves: d.md.Schema.NumColumns()} // WP2: the rest
}

func (d *dataset) KeyValueMetadata() []KeyValue { return nil } // WP2

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

func (d *dataset) ColumnStats(ctx context.Context, v View, col string, s Sample) (ColumnStats, error) {
	return ColumnStats{}, ErrNotImplemented // WP2
}

func (d *dataset) Histogram(ctx context.Context, v View, col string, o HistOptions) (Histogram, error) {
	return Histogram{}, ErrNotImplemented // WP2
}

func (d *dataset) SkyCounts(ctx context.Context, v View, lon, lat string, resDeg float64, s Sample) (Grid2D, error) {
	return Grid2D{}, ErrNotImplemented // WP2
}

func (d *dataset) XYCounts(ctx context.Context, v View, x, y string, nx, ny int, s Sample, xlim, ylim *[2]float64) (Grid2D, error) {
	return Grid2D{}, ErrNotImplemented // WP2
}

func (d *dataset) Export(ctx context.Context, v View, path string, f ExportFormat, cols []string) (int64, error) {
	return 0, ErrNotImplemented // WP2
}

func (d *dataset) FooterSummary(ctx context.Context) ([]ChunkSummary, error) {
	return nil, ErrNotImplemented // WP2
}

func (d *dataset) RowGroupInfo(ctx context.Context) ([]RowGroup, error) {
	return nil, ErrNotImplemented // WP2
}

func (d *dataset) Encodings(path string) []string { return nil } // WP2
