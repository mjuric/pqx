package data

import "context"

// Unimplemented implements every Dataset method with ErrNotImplemented (or
// zero values). Fakes in tests embed it and override what they need.
type Unimplemented struct{}

func (Unimplemented) Path() string                 { return "" }
func (Unimplemented) Info() FileInfo               { return FileInfo{} }
func (Unimplemented) NumRows() int64               { return 0 }
func (Unimplemented) Columns() []Column            { return nil }
func (Unimplemented) RowGroups() []int64           { return nil }
func (Unimplemented) KeyValueMetadata() []KeyValue { return nil }
func (Unimplemented) SetupErr() error              { return nil }
func (Unimplemented) Validate(context.Context, View) ([]Column, error) {
	return nil, ErrNotImplemented
}
func (Unimplemented) Fetch(context.Context, View, int64, int, []string) (Window, error) {
	return Window{}, ErrNotImplemented
}
func (Unimplemented) FetchColumns(context.Context, []int64, []string) (Window, error) {
	return Window{}, ErrNotImplemented
}
func (Unimplemented) Count(context.Context, View) (int64, error) { return 0, ErrNotImplemented }
func (Unimplemented) FindRow(context.Context, View, int64) (int64, bool, error) {
	return 0, false, ErrNotImplemented
}
func (Unimplemented) FetchAround(context.Context, View, int64, int64, int64, int, []string) (Window, error) {
	return Window{}, ErrNotImplemented
}
func (Unimplemented) ColumnStats(context.Context, View, string, Sample) (ColumnStats, error) {
	return ColumnStats{}, ErrNotImplemented
}
func (Unimplemented) Histogram(context.Context, View, string, HistOptions) (Histogram, error) {
	return Histogram{}, ErrNotImplemented
}
func (Unimplemented) SkyCounts(context.Context, View, string, string, float64, Sample) (Grid2D, error) {
	return Grid2D{}, ErrNotImplemented
}
func (Unimplemented) XYCounts(context.Context, View, string, string, int, int, Sample, *[2]float64, *[2]float64) (Grid2D, error) {
	return Grid2D{}, ErrNotImplemented
}
func (Unimplemented) Export(context.Context, View, string, ExportFormat, []string) (int64, error) {
	return 0, ErrNotImplemented
}
func (Unimplemented) FooterSummary(context.Context) ([]ChunkSummary, error) {
	return nil, ErrNotImplemented
}
func (Unimplemented) RowGroupInfo(context.Context) ([]RowGroup, error) {
	return nil, ErrNotImplemented
}
func (Unimplemented) Encodings(string) []string { return nil }
func (Unimplemented) Close() error              { return nil }

var _ Dataset = Unimplemented{}
