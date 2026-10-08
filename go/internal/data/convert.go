package data

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// valueColumn is rows [lo, hi) of arr as Values, through conv (ValueAt if nil).
func valueColumn(arr arrow.Array, lo, hi int, conv func(arrow.Array, int) Value) []Value {
	if conv == nil {
		conv = ValueAt
	}
	out := make([]Value, hi-lo)
	for i := lo; i < hi; i++ {
		out[i-lo] = conv(arr, i)
	}
	return out
}

// plainValueFunc is how the plain view (arrow-go) converts a column of Arrow
// type at so its values are the ones DuckDB (the other views) gives, given
// DuckDB's type for the column; nil means ValueAt as is.
//   - Text that arrow-go reads as binary (Parquet ENUM and JSON): a string.
//   - Nanosecond timestamps that DuckDB reads as microseconds (INT96, and
//     zone-aware ones): cut to microseconds the way DuckDB does it (INT96
//     rounded down, others toward zero), zoned as DuckDB's type says.
func plainValueFunc(at arrow.DataType, duck string, int96 bool) func(arrow.Array, int) Value {
	switch at := at.(type) {
	case *arrow.BinaryType, *arrow.LargeBinaryType, *arrow.BinaryViewType:
		if duck != "VARCHAR" && duck != "JSON" && !strings.HasPrefix(duck, "ENUM") {
			return nil
		}
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			switch a := arr.(type) {
			case *array.Binary:
				return string(a.Value(i))
			case *array.LargeBinary:
				return string(a.Value(i))
			case *array.BinaryView:
				return string(a.Value(i))
			}
			return ValueAt(arr, i)
		}
	case *arrow.FixedSizeBinaryType:
		if duck != "UUID" || at.ByteWidth != 16 {
			return nil
		}
		return func(arr arrow.Array, i int) Value {
			if a, ok := arr.(*array.FixedSizeBinary); ok && !a.IsNull(i) {
				return UUID(a.Value(i))
			}
			return ValueAt(arr, i)
		}
	case *arrow.TimestampType:
		if at.Unit != arrow.Nanosecond || (duck != "TIMESTAMP" && duck != "TIMESTAMP WITH TIME ZONE") {
			return nil
		}
		zoned := duck == "TIMESTAMP WITH TIME ZONE"
		return func(arr arrow.Array, i int) Value {
			a, ok := arr.(*array.Timestamp)
			if !ok || a.IsNull(i) {
				return ValueAt(arr, i)
			}
			v := int64(a.Value(i))
			us := v / 1000
			if int96 && v%1000 < 0 {
				us--
			}
			return Timestamp{T: time.UnixMicro(us).UTC(), Zoned: zoned, Unit: time.Microsecond}
		}
	}
	return nil
}

// duckValueFunc converts DuckDB's Arrow results for a column of DuckDB type
// duck where DuckDB's Arrow type loses the type: UUIDs come back as text.
// nil means ValueAt as is.
func duckValueFunc(duck string) func(arrow.Array, int) Value {
	if duck != "UUID" {
		return nil
	}
	return func(arr arrow.Array, i int) Value {
		v := ValueAt(arr, i)
		if s, ok := v.(string); ok {
			if u, err := parseUUID(s); err == nil {
				return u
			}
		}
		return v
	}
}

func parseUUID(s string) (UUID, error) {
	var u UUID
	h := strings.ReplaceAll(s, "-", "")
	if len(h) != 32 {
		return u, fmt.Errorf("not a UUID: %q", s)
	}
	_, err := hex.Decode(u[:], []byte(h))
	return u, err
}
