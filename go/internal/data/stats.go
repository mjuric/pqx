package data

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// sampleSlices is how many evenly spaced row groups a sample reads from.
const sampleSlices = 16

// statQuantiles are the quantiles ColumnStats computes.
var statQuantiles = []float64{0.01, 0.05, 0.25, 0.5, 0.75, 0.95, 0.99}

// topK is how many of the most frequent values ColumnStats lists.
const topK = 10

// sampleCondition is a file_row_number condition that selects about rows
// rows (pqx's sample_condition): up to slices runs, each at the start of one
// of evenly spaced row groups, so DuckDB prunes the other row groups instead
// of reading them. "" for all rows (rows is 0 or at least the file's, or the
// file has a column of its own named file_row_number).
func (d *dataset) sampleCondition(rows int64, slices int) string {
	n := d.numRows
	if rows <= 0 || !d.hasRowNum || rows >= n {
		return ""
	}
	nrg := len(d.rgRows)
	k := max(1, min(slices, nrg))
	chunk := max(1, (rows+int64(k)-1)/int64(k))
	var picks []int
	seen := map[int]bool{}
	for i := range k {
		// Python's round: half to even
		p := int(math.RoundToEven(float64(i) * float64(nrg-1) / float64(max(k-1, 1))))
		if !seen[p] {
			seen[p] = true
			picks = append(picks, p)
		}
	}
	// (picks increase with i, so they are sorted)
	parts := make([]string, len(picks))
	for j, i := range picks {
		a := d.rgStart[i]
		b := a + min(chunk, d.rgRows[i])
		parts[j] = fmt.Sprintf("(file_row_number >= %d AND file_row_number < %d)", a, b)
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// sqlBody is a SQL view's query, trimmed of trailing semicolons, as pqx has it.
func sqlBody(v View) string {
	return strings.TrimRight(strings.TrimSpace(v.SQL), ";")
}

// colRef is column col of view v quoted for SQL: DuckDB's name for a column
// of the file, or the name itself for a SQL view's result column.
func (d *dataset) colRef(v View, col string) (string, error) {
	if v.IsSQL() {
		return quoteIdent(col)
	}
	j, ok := d.byName[col]
	if !ok {
		return "", fmt.Errorf("no column %s in this file", Sanitize(col))
	}
	return quoteIdent(d.cols[j].SQLName)
}

// relationSQL is a query for view v's rows, columns cols (all if nil), for
// stats, plots and export (pqx's relation_sql). Order is left out: it
// doesn't matter to aggregates. With a sample, a view of the file reads
// only the rows of sampleCondition (then filters them); a SQL view takes a
// reservoir sample of its result. A SQL view's query is put on lines of its
// own, so a trailing -- comment in it can't swallow what follows.
func (d *dataset) relationSQL(v View, cols []string, s Sample) (string, error) {
	if v.IsSQL() {
		base := sqlBody(v)
		if s.Rows > 0 {
			base = "SELECT * FROM (\n" + base + "\n) USING SAMPLE reservoir(" + strconv.FormatInt(s.Rows, 10) + " ROWS) REPEATABLE (42)"
		}
		if len(cols) > 0 {
			sel, err := d.selectCols(v, cols)
			if err != nil {
				return "", err
			}
			return "SELECT " + sel + " FROM (\n" + base + "\n)", nil
		}
		return base, nil
	}
	sel, err := d.selectCols(v, cols)
	if err != nil {
		return "", err
	}
	var conds []string
	if c := d.sampleCondition(s.Rows, sampleSlices); c != "" {
		conds = append(conds, c)
	}
	if strings.TrimSpace(v.Where) != "" {
		w, err := whereSQL(v.Where)
		if err != nil {
			return "", err
		}
		conds = append(conds, w)
	}
	q := "SELECT " + sel + " FROM " + d.src
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	return q, nil
}

// selectCols is cols (all of the file's if none), quoted, for a select list.
func (d *dataset) selectCols(v View, cols []string) (string, error) {
	if len(cols) == 0 {
		if v.IsSQL() {
			return "*", nil
		}
		cols = make([]string, len(d.cols))
		for i, c := range d.cols {
			cols[i] = c.Name
		}
	}
	parts := make([]string, len(cols))
	for i, c := range cols {
		q, err := d.colRef(v, c)
		if err != nil {
			return "", err
		}
		parts[i] = q
	}
	return strings.Join(parts, ", "), nil
}

// prepare waits for DuckDB's bind and, for a SQL view, creates the view t
// its query reads.
func (d *dataset) prepare(ctx context.Context, v View) error {
	if err := d.waitBound(ctx); err != nil {
		return err
	}
	if !v.IsSQL() {
		return nil
	}
	return d.ensureT(ctx)
}

// ensureT creates the view t over the file, which SQL views query (as in
// pqx), once for the database (every connection sees it).
func (d *dataset) ensureT(ctx context.Context) error {
	a := &d.an
	a.tMu.Lock()
	defer a.tMu.Unlock()
	if a.tReady {
		return nil
	}
	_, err := d.db.ExecContext(ctx, "CREATE OR REPLACE VIEW t AS SELECT * FROM "+readParquet(d.path, false))
	if err != nil {
		return duckError(err)
	}
	a.tReady = true
	return nil
}

// columnType is column col of view v: how pqx sorts it (by the file's Arrow
// type for a view of the file, by DuckDB's type for a SQL view's result, as
// pqx's app does) and DuckDB's type.
func (d *dataset) columnType(ctx context.Context, v View, col string) (typeClass, string, error) {
	if !v.IsSQL() {
		j, ok := d.byName[col]
		if !ok {
			return typeClass{}, "", fmt.Errorf("no column %s in this file", Sanitize(col))
		}
		return arrowClass(d.cols[j].Arrow), d.cols[j].Type, nil
	}
	q := "SELECT column_name, column_type FROM (DESCRIBE SELECT * FROM (\n" + sqlBody(v) + "\n))"
	rows, err := d.queryRows(ctx, q)
	if err != nil {
		return typeClass{}, "", err
	}
	for _, r := range rows {
		if name, _ := r[0].(string); name == col {
			t, _ := r[1].(string)
			return duckClass(t), t, nil
		}
	}
	return typeClass{}, "", fmt.Errorf("no column %s in the query's result", Sanitize(col))
}

// typeClass is what ColumnStats needs to know of a column's type (pqx's
// ColumnInfo properties).
type typeClass struct {
	numeric, float, decimal, nested, boolean bool
}

func arrowClass(t arrow.DataType) typeClass {
	if t == nil {
		return typeClass{}
	}
	return typeClass{
		numeric: isNumeric(t),
		float:   isFloat(t),
		decimal: arrow.IsDecimal(t.ID()),
		nested:  isNested(t),
		boolean: t.ID() == arrow.BOOL,
	}
}

// duckClass sorts a DuckDB type the way arrowClass sorts the Arrow type
// DuckDB returns it as (HUGEINT is a decimal there).
func duckClass(duck string) typeClass {
	t := strings.ToUpper(strings.TrimSpace(duck))
	switch {
	case strings.HasSuffix(t, "]") || strings.HasPrefix(t, "STRUCT(") || strings.HasPrefix(t, "MAP(") || strings.HasPrefix(t, "UNION("):
		return typeClass{nested: true}
	case t == "FLOAT" || t == "DOUBLE":
		return typeClass{numeric: true, float: true}
	case strings.HasPrefix(t, "DECIMAL") || t == "HUGEINT" || t == "UHUGEINT":
		return typeClass{numeric: true, decimal: true}
	case t == "BOOLEAN":
		return typeClass{boolean: true}
	}
	switch t {
	case "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT":
		return typeClass{numeric: true}
	}
	return typeClass{}
}

// queryRows runs q and returns its rows of Values, each column converted as
// convs says (ValueAt where nil or missing).
func (d *dataset) queryRows(ctx context.Context, q string, convs ...func(arrow.Array, int) Value) ([][]Value, error) {
	var out [][]Value
	err := d.query(ctx, q, func(rec arrow.RecordBatch) error {
		nc := int(rec.NumCols())
		for i := range int(rec.NumRows()) {
			row := make([]Value, nc)
			for k := range nc {
				conv := ValueAt
				if k < len(convs) && convs[k] != nil {
					conv = convs[k]
				}
				row[k] = conv(rec.Column(k), i)
			}
			out = append(out, row)
		}
		return nil
	})
	return out, err
}

// ColumnStats profiles column col of view v (pqx's column_stats). Min, max,
// mean and std leave out NaN (mean and std also ±inf), which would poison
// them; NaNs are counted. The most frequent values are listed unless the
// column is nested, a float column with more than 1000 distinct values, or
// nearly unique; when that list is shorter than 10 it is the whole
// distribution, and the distinct count is exact.
func (d *dataset) ColumnStats(ctx context.Context, v View, col string, s Sample) (ColumnStats, error) {
	if err := d.prepare(ctx, v); err != nil {
		return ColumnStats{}, err
	}
	cl, typ, err := d.columnType(ctx, v, col)
	if err != nil {
		return ColumnStats{}, err
	}
	q, err := d.colRef(v, col)
	if err != nil {
		return ColumnStats{}, err
	}
	base, err := d.relationSQL(v, []string{col}, s)
	if err != nil {
		return ColumnStats{}, err
	}
	rel := "(SELECT " + q + " AS v FROM (\n" + base + "\n))"
	st := ColumnStats{Name: col, NaNs: -1, Distinct: -1, Sampled: s.Rows > 0}
	numeric := cl.numeric && !cl.decimal
	w, wf := "v", "v"
	if cl.float {
		w = "CASE WHEN isnan(v) THEN NULL ELSE v END"
		wf = "CASE WHEN isfinite(v) THEN v END"
	}
	aggs := []string{"count(*)", "count(v)"}
	if cl.nested {
		aggs = append(aggs, "NULL", "NULL")
	} else {
		aggs = append(aggs, "min("+w+")", "max("+w+")")
	}
	if !cl.nested && !cl.boolean {
		aggs = append(aggs, "approx_count_distinct(v)")
	} else {
		aggs = append(aggs, "NULL")
	}
	if numeric {
		aggs = append(aggs, "avg("+wf+")", "stddev_samp("+wf+")")
		if cl.float {
			aggs = append(aggs, "count(*) FILTER (WHERE isnan(v))")
		} else {
			aggs = append(aggs, "NULL")
		}
		qs := make([]string, len(statQuantiles))
		for i, x := range statQuantiles {
			qs[i] = pyRepr(x)
		}
		aggs = append(aggs, "approx_quantile("+wf+", ["+strings.Join(qs, ", ")+"])")
	}
	conv := duckValueFunc(typ)
	rows, err := d.queryRows(ctx, "SELECT "+strings.Join(aggs, ", ")+" FROM "+rel, nil, nil, conv, conv)
	if err != nil {
		return ColumnStats{}, err
	}
	if len(rows) != 1 {
		return ColumnStats{}, fmt.Errorf("the statistics query returned %d rows", len(rows))
	}
	r := rows[0]
	st.Count = asInt(r[0])
	nonNull := asInt(r[1])
	st.Nulls = st.Count - nonNull
	st.Min, st.Max = r[2], r[3]
	if r[4] != nil {
		st.Distinct = min(asInt(r[4]), nonNull)
	}
	if numeric {
		st.Mean, st.Std = asFloatPtr(r[5]), asFloatPtr(r[6])
		if cl.float {
			st.NaNs = asInt(r[7])
		}
		if l, ok := r[8].(List); ok {
			st.Quantiles = make(map[float64]float64, len(l))
			for i, x := range l {
				if i < len(statQuantiles) {
					if f := asFloatPtr(x); f != nil {
						st.Quantiles[statQuantiles[i]] = *f
					}
				}
			}
		}
	}
	distinct := max(st.Distinct, 0)
	nearUnique := float64(distinct) > math.Max(1000, 0.5*float64(nonNull))
	if !cl.nested && !(cl.float && distinct > 1000) && !nearUnique {
		top, err := d.queryRows(ctx, "SELECT v, count(*) AS n FROM "+rel+" GROUP BY v ORDER BY n DESC, v LIMIT "+strconv.Itoa(topK), conv)
		if err != nil {
			return ColumnStats{}, err
		}
		st.Top = make([]ValueCount, len(top))
		nonNullTop := int64(0)
		for i, t := range top {
			st.Top[i] = ValueCount{Value: t[0], Count: asInt(t[1])}
			if t[0] != nil {
				nonNullTop++
			}
		}
		if len(top) < topK {
			st.Distinct = nonNullTop
			st.DistinctExact = true
		}
	}
	return st, nil
}

// asInt is an integer Value (count, approx_count_distinct) as an int64.
func asInt(v Value) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case uint64:
		return int64(x)
	case float64:
		return int64(x)
	}
	return 0
}

// asFloatPtr is a numeric Value as a *float64; nil for NULL.
func asFloatPtr(v Value) *float64 {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case float32:
		f = float64(x)
	case int64:
		f = float64(x)
	case uint64:
		f = float64(x)
	case Decimal: // HUGEINT's quantiles
		if x.Unscaled == nil {
			return nil
		}
		f, _ = new(big.Rat).SetFrac(x.Unscaled, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(x.Scale)), nil)).Float64()
	default:
		return nil
	}
	return &f
}

// int64At is integer column arr's value i (DuckDB's ::INT is int32, count(*) int64).
func int64At(arr arrow.Array, i int) (int64, bool) {
	if arr.IsNull(i) {
		return 0, false
	}
	switch a := arr.(type) {
	case *array.Int32:
		return int64(a.Value(i)), true
	case *array.Int64:
		return a.Value(i), true
	}
	return 0, false
}

// float64At is double column arr's value i.
func float64At(arr arrow.Array, i int) (float64, bool) {
	if arr.IsNull(i) {
		return 0, false
	}
	if a, ok := arr.(*array.Float64); ok {
		return a.Value(i), true
	}
	return 0, false
}
