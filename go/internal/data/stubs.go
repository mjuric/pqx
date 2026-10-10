package data

// SetupErr is DuckDB's error binding the file, if any.
func (d *dataset) SetupErr() error {
	<-d.bound
	if d.bindErr != nil {
		return duckError(d.bindErr)
	}
	return nil
}
