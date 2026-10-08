package data

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"math/big"
	"sort"
	"time"

	"github.com/apache/arrow-go/v18/arrow/float16"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/metadata"
	"github.com/apache/arrow-go/v18/parquet/schema"
)

// footerScan is the result of one pass over every column chunk in the footer
// (pqx's _footer_scan): the summary per leaf path, and the compressed bytes of
// each row group.
type footerScan struct {
	summary []ChunkSummary
	rgComp  []int64
}

// FooterSummary is the storage and statistics of each leaf column over all
// row groups, from one scan of the footer shared with RowGroupInfo.
func (d *dataset) FooterSummary(ctx context.Context) ([]ChunkSummary, error) {
	fs, err := d.footerScan(ctx)
	if err != nil {
		return nil, err
	}
	return append([]ChunkSummary(nil), fs.summary...), nil
}

// RowGroupInfo is each row group's rows and sizes: compressed as the sum of
// its column chunks', uncompressed as the footer's total_byte_size.
func (d *dataset) RowGroupInfo(ctx context.Context) ([]RowGroup, error) {
	fs, err := d.footerScan(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RowGroup, len(d.md.RowGroups))
	for i, rg := range d.md.RowGroups {
		out[i] = RowGroup{Index: i, Start: d.rgStart[i], Rows: rg.GetNumRows(), Compressed: fs.rgComp[i], Uncompressed: rg.GetTotalByteSize()}
	}
	return out, nil
}

// footerScan is the cached scan, made by the first call that needs it; calls
// meanwhile wait for it (or for their ctx). A scan stopped by its ctx caches
// nothing, and the next call starts over.
func (d *dataset) footerScan(ctx context.Context) (*footerScan, error) {
	a := &d.an
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		a.mu.Lock()
		if a.footer != nil {
			fs := a.footer
			a.mu.Unlock()
			return fs, nil
		}
		if wait := a.scanning; wait != nil {
			a.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		a.scanning = done
		a.mu.Unlock()

		fs, err := d.scanFooter(ctx)

		a.mu.Lock()
		if err == nil {
			a.footer = fs
		}
		a.scanning = nil
		close(done)
		a.mu.Unlock()
		return fs, err
	}
}

// leafStats is how the scan reads one leaf column's statistics.
type leafStats struct {
	conv     func([]byte) (Value, bool) // a min or max as a Value; false if malformed
	typed    bool                       // min_value/max_value (type-defined order), not the legacy min/max
	sort     schema.SortOrder
	physical parquet.Type
	logical  schema.LogicalType
}

// scanFooter goes over every column chunk once, from the parsed thrift
// footer: sizes and null counts as they are, min and max decoded as the
// column's logical type (pqx's _scan_footer, which PyArrow's statistics
// decode). Leaves sharing a path add up. ctx is checked every row group.
func (d *dataset) scanFooter(ctx context.Context) (*footerScan, error) {
	md := d.md
	n := md.Schema.NumColumns()
	paths := make([]string, n)
	slot := make([]int, n)
	first := make(map[string]int, n)
	leaves := make([]leafStats, n)
	logical := make(map[string]string, n)
	for i := range n {
		c := md.Schema.Column(i)
		paths[i] = c.Path()
		k, ok := first[paths[i]]
		if !ok {
			k = i
			first[paths[i]] = i
		}
		slot[i] = k
		leaves[i] = leafStats{
			conv:     statConverter(c),
			typed:    c.ColumnOrder() == parquet.ColumnOrders.TypeDefinedOrder,
			sort:     c.SortOrder(),
			physical: c.PhysicalType(),
			logical:  c.LogicalType(),
		}
		logical[paths[i]] = logicalString(c.LogicalType())
	}
	type acc struct {
		comp, unc, nulls int64
		lo, hi           Value
		has, seen        bool
		phys, codec      string
	}
	accs := make([]acc, n)
	for i := range accs {
		accs[i].has = true
	}
	version := md.WriterVersion()
	rgComp := make([]int64, 0, len(md.RowGroups))
	for _, g := range md.RowGroups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var total int64
		for i, c := range g.GetColumns() {
			if i >= n {
				break
			}
			cm := c.GetMetaData()
			a := &accs[slot[i]]
			if cm == nil { // encrypted column metadata: nothing to read
				a.has = false
				continue
			}
			if !a.seen {
				a.seen = true
				a.phys = parquet.Type(cm.GetType()).String()
				a.codec = codecName(int32(cm.GetCodec()))
			}
			size := cm.GetTotalCompressedSize()
			total += size
			a.comp += size
			a.unc += cm.GetTotalUncompressedSize()

			st := cm.GetStatistics()
			lf := &leaves[i]
			if st == nil || lf.sort == schema.SortUNKNOWN {
				a.has = false
				continue
			}
			var minB, maxB []byte
			var hasMin, hasMax bool
			if lf.typed {
				minB, maxB, hasMin, hasMax = st.MinValue, st.MaxValue, st.IsSetMinValue(), st.IsSetMaxValue()
			} else {
				minB, maxB, hasMin, hasMax = st.Min, st.Max, st.IsSetMin(), st.IsSetMax()
			}
			enc := metadata.EncodedStatistics{HasMin: hasMin, Min: minB, HasMax: hasMax, Max: maxB}
			if !version.HasCorrectStatistics(lf.physical, lf.logical, enc, lf.sort) {
				a.has = false // PyArrow has no statistics for this chunk
				continue
			}
			hasNulls := st.IsSetNullCount()
			if hasMin && hasMax {
				lo, ok1 := lf.conv(minB)
				hi, ok2 := lf.conv(maxB)
				if ok1 && ok2 {
					if a.lo == nil {
						a.lo = lo
					} else if less(lo, a.lo) {
						a.lo = lo
					}
					if a.hi == nil {
						a.hi = hi
					} else if less(a.hi, hi) {
						a.hi = hi
					}
				}
				if hasNulls {
					a.nulls += st.GetNullCount()
				}
			} else if hasNulls {
				a.nulls += st.GetNullCount()
			} else {
				a.has = false
			}
		}
		rgComp = append(rgComp, total)
	}
	var out []ChunkSummary
	for i := range n {
		a := &accs[i]
		if slot[i] != i || !a.seen {
			continue
		}
		out = append(out, ChunkSummary{
			Path: paths[i], Physical: a.phys, Logical: logical[paths[i]], Compression: a.codec,
			Compressed: a.comp, Uncompressed: a.unc, Min: ownValue(a.lo), Max: ownValue(a.hi),
			Nulls: a.nulls, HasStats: a.has,
		})
	}
	return &footerScan{summary: out, rgComp: rgComp}, nil
}

// codecName is PyArrow's name for a Parquet compression codec: thrift's LZ4
// (Hadoop's framing) is LZ4_HADOOP here (PyArrow says UNKNOWN), LZ4_RAW is LZ4.
func codecName(c int32) string {
	switch c {
	case 0:
		return "UNCOMPRESSED"
	case 1:
		return "SNAPPY"
	case 2:
		return "GZIP"
	case 3:
		return "LZO"
	case 4:
		return "BROTLI"
	case 5:
		return "LZ4_HADOOP"
	case 6:
		return "ZSTD"
	case 7:
		return "LZ4"
	}
	return "UNKNOWN"
}

// logicalString is the logical type as PyArrow's str() of it gives it.
func logicalString(t schema.LogicalType) string {
	if t == nil {
		return "None"
	}
	return t.String()
}

// ownValue copies a []byte that points into the footer's buffers.
func ownValue(v Value) Value {
	if b, ok := v.([]byte); ok {
		return cloneBytes(b)
	}
	return v
}

// statConverter decodes a column's plain-encoded min or max as a Value of
// its logical type, as PyArrow's Statistics.min does (min_raw for plain
// columns): dates, times, timestamps, decimals, unsigned integers, text.
func statConverter(c *schema.Column) func([]byte) (Value, bool) {
	lt := c.LogicalType()
	switch c.PhysicalType() {
	case parquet.Types.Boolean:
		return func(b []byte) (Value, bool) {
			if len(b) < 1 {
				return nil, false
			}
			return b[0]&1 == 1, true
		}
	case parquet.Types.Int32:
		conv := func(v int32) Value { return int64(v) }
		switch t := lt.(type) {
		case schema.IntLogicalType:
			if !t.IsSigned() {
				conv = func(v int32) Value { return uint64(uint32(v)) }
			}
		case schema.DateLogicalType:
			conv = func(v int32) Value { return Date(v) }
		case schema.TimeLogicalType:
			mult := unitNanos(t.TimeUnit())
			conv = func(v int32) Value { return TimeOfDay(int64(v) * mult) }
		case schema.DecimalLogicalType:
			conv = func(v int32) Value {
				return Decimal{Unscaled: big.NewInt(int64(v)), Scale: t.Scale(), Precision: t.Precision()}
			}
		}
		return func(b []byte) (Value, bool) {
			if len(b) != 4 {
				return nil, false
			}
			return conv(int32(binary.LittleEndian.Uint32(b))), true
		}
	case parquet.Types.Int64:
		conv := func(v int64) Value { return v }
		switch t := lt.(type) {
		case schema.IntLogicalType:
			if !t.IsSigned() {
				conv = func(v int64) Value { return uint64(v) }
			}
		case schema.TimeLogicalType:
			mult := unitNanos(t.TimeUnit())
			conv = func(v int64) Value { return TimeOfDay(v * mult) }
		case schema.TimestampLogicalType:
			return timestampConverter(t.TimeUnit(), t.IsAdjustedToUTC())
		case schema.DecimalLogicalType:
			conv = func(v int64) Value {
				return Decimal{Unscaled: big.NewInt(v), Scale: t.Scale(), Precision: t.Precision()}
			}
		}
		return func(b []byte) (Value, bool) {
			if len(b) != 8 {
				return nil, false
			}
			return conv(int64(binary.LittleEndian.Uint64(b))), true
		}
	case parquet.Types.Float:
		return func(b []byte) (Value, bool) {
			if len(b) != 4 {
				return nil, false
			}
			return math.Float32frombits(binary.LittleEndian.Uint32(b)), true
		}
	case parquet.Types.Double:
		return func(b []byte) (Value, bool) {
			if len(b) != 8 {
				return nil, false
			}
			return math.Float64frombits(binary.LittleEndian.Uint64(b)), true
		}
	case parquet.Types.ByteArray, parquet.Types.FixedLenByteArray:
		width := -1
		if c.PhysicalType() == parquet.Types.FixedLenByteArray {
			width = c.TypeLength()
		}
		fixed := func(b []byte) bool { return width < 0 || len(b) == width }
		switch t := lt.(type) {
		case schema.StringLogicalType, schema.EnumLogicalType, schema.JSONLogicalType:
			return func(b []byte) (Value, bool) { return string(b), fixed(b) }
		case schema.DecimalLogicalType:
			return func(b []byte) (Value, bool) {
				if !fixed(b) {
					return nil, false
				}
				return Decimal{Unscaled: bigFromTwos(b), Scale: t.Scale(), Precision: t.Precision()}, true
			}
		case schema.UUIDLogicalType:
			return func(b []byte) (Value, bool) {
				if len(b) != 16 {
					return nil, false
				}
				return UUID(b), true
			}
		case schema.Float16LogicalType:
			return func(b []byte) (Value, bool) {
				if len(b) != 2 {
					return nil, false
				}
				return float16.FromBits(binary.LittleEndian.Uint16(b)).Float32(), true
			}
		}
		return func(b []byte) (Value, bool) { return b, fixed(b) }
	}
	return func([]byte) (Value, bool) { return nil, false } // INT96: no sort order, no statistics
}

func timestampConverter(u schema.TimeUnitType, utc bool) func([]byte) (Value, bool) {
	unit := time.Duration(unitNanos(u))
	return func(b []byte) (Value, bool) {
		if len(b) != 8 {
			return nil, false
		}
		v := int64(binary.LittleEndian.Uint64(b))
		var t time.Time
		switch unit {
		case time.Millisecond:
			t = time.UnixMilli(v)
		case time.Microsecond:
			t = time.UnixMicro(v)
		default:
			t = time.Unix(0, v)
		}
		return Timestamp{T: t.UTC(), Zoned: utc, Unit: unit}, true
	}
}

func unitNanos(u schema.TimeUnitType) int64 {
	switch u {
	case schema.TimeUnitMillis:
		return 1e6
	case schema.TimeUnitMicros:
		return 1e3
	}
	return 1
}

// bigFromTwos is a big-endian two's complement integer.
func bigFromTwos(b []byte) *big.Int {
	v := new(big.Int).SetBytes(b)
	if len(b) > 0 && b[0]&0x80 != 0 {
		v.Sub(v, new(big.Int).Lsh(big.NewInt(1), uint(8*len(b))))
	}
	return v
}

// less is a < b for two statistics of one column (Python's <: false for NaN,
// and for values it can't order).
func less(a, b Value) bool {
	switch x := a.(type) {
	case bool:
		y, ok := b.(bool)
		return ok && !x && y
	case int64:
		y, ok := b.(int64)
		return ok && x < y
	case uint64:
		y, ok := b.(uint64)
		return ok && x < y
	case float32:
		y, ok := b.(float32)
		return ok && x < y
	case float64:
		y, ok := b.(float64)
		return ok && x < y
	case string:
		y, ok := b.(string)
		return ok && x < y
	case []byte:
		y, ok := b.([]byte)
		return ok && bytes.Compare(x, y) < 0
	case Date:
		y, ok := b.(Date)
		return ok && x < y
	case TimeOfDay:
		y, ok := b.(TimeOfDay)
		return ok && x < y
	case Timestamp:
		y, ok := b.(Timestamp)
		return ok && x.T.Before(y.T)
	case Decimal:
		y, ok := b.(Decimal)
		return ok && x.Scale == y.Scale && x.Unscaled.Cmp(y.Unscaled) < 0
	case UUID:
		y, ok := b.(UUID)
		return ok && bytes.Compare(x[:], y[:]) < 0
	}
	return false
}

// Encodings are the encodings leaf column path uses in any row group, sorted.
func (d *dataset) Encodings(path string) []string {
	a := &d.an
	a.mu.Lock()
	if e, ok := a.enc[path]; ok {
		a.mu.Unlock()
		return append([]string(nil), e...)
	}
	a.mu.Unlock()
	md := d.md
	var idx []int
	for i := range md.Schema.NumColumns() {
		if md.Schema.Column(i).Path() == path {
			idx = append(idx, i)
		}
	}
	set := map[string]bool{}
	for _, g := range md.RowGroups {
		cols := g.GetColumns()
		for _, i := range idx {
			if i < len(cols) {
				if cm := cols[i].GetMetaData(); cm != nil {
					for _, e := range cm.GetEncodings() {
						set[parquet.Encoding(e).String()] = true
					}
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for e := range set {
		out = append(out, e)
	}
	sort.Strings(out)
	a.mu.Lock()
	if a.enc == nil {
		a.enc = map[string][]string{}
	}
	a.enc[path] = out
	a.mu.Unlock()
	return append([]string(nil), out...)
}
