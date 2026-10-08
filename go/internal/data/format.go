package data

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// maxNestedText caps the summary text of a nested value (list, struct, map).
const maxNestedText = 200

// FormatCell is the cell text of value i of arr: integers as is, floats with
// 6 significant digits ('g'), booleans as true/false, timestamps in UTC as
// "2006-01-02 15:04:05.000001" (with a Z for zone-aware ones), NULL as Null,
// binary as hex (first 16 bytes and the size), and strings and anything else
// through Sanitize. Both readers (arrow-go and DuckDB's Arrow results) go
// through it, so a value reads the same whichever served it.
func FormatCell(arr arrow.Array, i int) string {
	if arr.IsNull(i) {
		return Null
	}
	switch a := arr.(type) {
	case *array.Int8:
		return strconv.FormatInt(int64(a.Value(i)), 10)
	case *array.Int16:
		return strconv.FormatInt(int64(a.Value(i)), 10)
	case *array.Int32:
		return strconv.FormatInt(int64(a.Value(i)), 10)
	case *array.Int64:
		return strconv.FormatInt(a.Value(i), 10)
	case *array.Uint8:
		return strconv.FormatUint(uint64(a.Value(i)), 10)
	case *array.Uint16:
		return strconv.FormatUint(uint64(a.Value(i)), 10)
	case *array.Uint32:
		return strconv.FormatUint(uint64(a.Value(i)), 10)
	case *array.Uint64:
		return strconv.FormatUint(a.Value(i), 10)
	case *array.Float16:
		return strconv.FormatFloat(float64(a.Value(i).Float32()), 'g', 6, 32)
	case *array.Float32:
		return strconv.FormatFloat(float64(a.Value(i)), 'g', 6, 32)
	case *array.Float64:
		return strconv.FormatFloat(a.Value(i), 'g', 6, 64)
	case *array.Boolean:
		return strconv.FormatBool(a.Value(i))
	case *array.String:
		return textCell(a.Value(i))
	case *array.LargeString:
		return textCell(a.Value(i))
	case *array.StringView:
		return textCell(a.Value(i))
	case *array.Binary:
		return formatBytes(a.Value(i))
	case *array.LargeBinary:
		return formatBytes(a.Value(i))
	case *array.BinaryView:
		return formatBytes(a.Value(i))
	case *array.FixedSizeBinary:
		return formatBytes(a.Value(i))
	case *array.Timestamp:
		t := a.DataType().(*arrow.TimestampType)
		return formatTimestamp(a.Value(i).ToTime(t.Unit), t.TimeZone != "")
	case *array.Date32:
		return a.Value(i).ToTime().UTC().Format(time.DateOnly)
	case *array.Date64:
		return a.Value(i).ToTime().UTC().Format(time.DateOnly)
	case *array.Time32:
		u := a.DataType().(*arrow.Time32Type).Unit
		return formatClock(a.Value(i).ToTime(u))
	case *array.Time64:
		u := a.DataType().(*arrow.Time64Type).Unit
		return formatClock(a.Value(i).ToTime(u))
	case *array.Dictionary:
		return FormatCell(a.Dictionary(), a.GetValueIndex(i))
	case *array.Null:
		return Null
	}
	s := arr.ValueStr(i)
	if len(s) > maxNestedText {
		cut := maxNestedText
		for cut > 0 && !isRuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return Sanitize(s)
}

// textCell is Sanitize(v) in memory of its own: arrow-go's string values
// point into the array's buffers, which for DuckDB's results are C memory
// that DuckDB frees with the batch.
func textCell(v string) string {
	s := Sanitize(v)
	if len(s) > 0 && unsafe.StringData(s) == unsafe.StringData(v) {
		return strings.Clone(s)
	}
	return s
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// formatTimestamp is Python's isoformat(sep=" "): fractional seconds only if
// there are any (6 digits, or 9 if there are nanoseconds), and Z for UTC.
func formatTimestamp(t time.Time, zoned bool) string {
	t = t.UTC()
	s := t.Format(time.DateTime)
	s += fraction(t.Nanosecond())
	if zoned {
		s += "Z"
	}
	return s
}

func formatClock(t time.Time) string {
	return t.UTC().Format(time.TimeOnly) + fraction(t.Nanosecond())
}

func fraction(ns int) string {
	switch {
	case ns == 0:
		return ""
	case ns%1000 == 0:
		return "." + pad(ns/1000, 6)
	default:
		return "." + pad(ns, 9)
	}
}

func pad(v, width int) string {
	s := strconv.Itoa(v)
	return strings.Repeat("0", width-len(s)) + s
}

// formatBytes is pqx's text for binary values: 0x and the first 16 bytes in
// hex, … if there are more, and the size.
func formatBytes(b []byte) string {
	n := min(len(b), 16)
	more := ""
	if len(b) > 16 {
		more = "…"
	}
	return "0x" + hex.EncodeToString(b[:n]) + more + " (" + strconv.Itoa(len(b)) + " B)"
}

// formatColumn is the cell text of rows [lo, hi) of arr.
func formatColumn(arr arrow.Array, lo, hi int) []string {
	out := make([]string, hi-lo)
	for i := lo; i < hi; i++ {
		out[i-lo] = FormatCell(arr, i)
	}
	return out
}

// plainCellFunc is how the plain view (arrow-go) formats a column of Arrow
// type at so its cells read as DuckDB's (filtered views) do, given DuckDB's
// type for the column; nil means FormatCell as is.
//   - Text that arrow-go reads as binary (Parquet ENUM and JSON): text.
//   - Nanosecond timestamps that DuckDB reads as microseconds (INT96, and
//     zone-aware ones): cut to microseconds the way DuckDB does it (INT96
//     rounded down, others toward zero), and zoned (a Z) as DuckDB's type
//     says.
func plainCellFunc(at arrow.DataType, duck string, int96 bool) func(arrow.Array, int) string {
	switch at := at.(type) {
	case *arrow.BinaryType, *arrow.LargeBinaryType, *arrow.BinaryViewType:
		if duck != "VARCHAR" && duck != "JSON" && !strings.HasPrefix(duck, "ENUM") {
			return nil
		}
		return func(arr arrow.Array, i int) string {
			if arr.IsNull(i) {
				return Null
			}
			var b []byte
			switch a := arr.(type) {
			case *array.Binary:
				b = a.Value(i)
			case *array.LargeBinary:
				b = a.Value(i)
			case *array.BinaryView:
				b = a.Value(i)
			default:
				return FormatCell(arr, i)
			}
			return Sanitize(string(b)) // (string() copies)
		}
	case *arrow.TimestampType:
		if at.Unit != arrow.Nanosecond || (duck != "TIMESTAMP" && duck != "TIMESTAMP WITH TIME ZONE") {
			return nil
		}
		zoned := duck == "TIMESTAMP WITH TIME ZONE"
		return func(arr arrow.Array, i int) string {
			a, ok := arr.(*array.Timestamp)
			if !ok || a.IsNull(i) {
				return FormatCell(arr, i)
			}
			v := int64(a.Value(i))
			us := v / 1000
			if int96 && v%1000 < 0 {
				us--
			}
			return formatTimestamp(time.UnixMicro(us), zoned)
		}
	}
	return nil
}
