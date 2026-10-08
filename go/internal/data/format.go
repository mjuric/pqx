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
