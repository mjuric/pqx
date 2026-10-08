package data

import (
	"math/big"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// Value is one cell. It holds one of:
//
//	nil        SQL NULL
//	int64      signed integers of any width
//	uint64     unsigned integers of any width
//	float64    DOUBLE
//	float32    FLOAT (and float16)
//	bool
//	string     text (VARCHAR, JSON, ENUM); raw, never sanitized
//	[]byte     binary
//	Timestamp
//	Date
//	TimeOfDay
//	Duration
//	Interval
//	Decimal
//	UUID
//	List
//	Struct
//	Map
//
// Both readers (arrow-go and DuckDB) give the same Value for the same cell.
// Values own their memory (nothing points into Arrow or DuckDB buffers).
type Value = any

// Timestamp is a point in time. T is in UTC. Zoned is set for a timestamp
// with a time zone (Python pqx shows these with a Z); Unit is the stored
// resolution (time.Second, time.Millisecond, time.Microsecond or
// time.Nanosecond), which decides how many fractional digits there are.
type Timestamp struct {
	T     time.Time
	Zoned bool
	Unit  time.Duration
}

// Date is a calendar date, as days since 1970-01-01.
type Date int32

// Time is the date at midnight UTC.
func (d Date) Time() time.Time { return time.Unix(int64(d)*86400, 0).UTC() }

// TimeOfDay is a time of day, as nanoseconds since midnight.
type TimeOfDay int64

// Duration is an Arrow duration (nanoseconds, whatever the stored unit).
type Duration time.Duration

// Interval is DuckDB's INTERVAL.
type Interval struct {
	Months, Days int32
	Nanos        int64
}

// Decimal is an exact decimal: Unscaled × 10^-Scale, of the column's
// Precision. Decimals wider than 38 digits stay exact.
type Decimal struct {
	Unscaled  *big.Int
	Scale     int32
	Precision int32
}

// UUID is a UUID's 16 bytes.
type UUID [16]byte

// List is a list (or fixed-size list) value.
type List []Value

// Field is one field of a Struct.
type Field struct {
	Name  string // raw
	Value Value
}

// Struct is a struct value, fields in order.
type Struct []Field

// KV is one entry of a Map.
type KV struct {
	Key, Value Value
}

// Map is a map value, entries in order.
type Map []KV

// ValueAt is value i of arr as a Value. It copies everything it keeps
// (strings and bytes from DuckDB's results point into memory DuckDB frees
// with the batch).
func ValueAt(arr arrow.Array, i int) Value {
	if arr.IsNull(i) {
		return nil
	}
	switch a := arr.(type) {
	case *array.Int8:
		return int64(a.Value(i))
	case *array.Int16:
		return int64(a.Value(i))
	case *array.Int32:
		return int64(a.Value(i))
	case *array.Int64:
		return a.Value(i)
	case *array.Uint8:
		return uint64(a.Value(i))
	case *array.Uint16:
		return uint64(a.Value(i))
	case *array.Uint32:
		return uint64(a.Value(i))
	case *array.Uint64:
		return a.Value(i)
	case *array.Float16:
		return a.Value(i).Float32()
	case *array.Float32:
		return a.Value(i)
	case *array.Float64:
		return a.Value(i)
	case *array.Boolean:
		return a.Value(i)
	case *array.String:
		return strings.Clone(a.Value(i))
	case *array.LargeString:
		return strings.Clone(a.Value(i))
	case *array.StringView:
		return strings.Clone(a.Value(i))
	case *array.Binary:
		return cloneBytes(a.Value(i))
	case *array.LargeBinary:
		return cloneBytes(a.Value(i))
	case *array.BinaryView:
		return cloneBytes(a.Value(i))
	case *array.FixedSizeBinary:
		return cloneBytes(a.Value(i))
	case *array.Timestamp:
		t := a.DataType().(*arrow.TimestampType)
		return Timestamp{T: a.Value(i).ToTime(t.Unit).UTC(), Zoned: t.TimeZone != "", Unit: t.Unit.Multiplier()}
	case *array.Date32:
		return Date(a.Value(i))
	case *array.Date64:
		return Date(int64(a.Value(i)) / 86400000)
	case *array.Time32:
		u := a.DataType().(*arrow.Time32Type).Unit
		return TimeOfDay(int64(a.Value(i)) * int64(u.Multiplier()))
	case *array.Time64:
		u := a.DataType().(*arrow.Time64Type).Unit
		return TimeOfDay(int64(a.Value(i)) * int64(u.Multiplier()))
	case *array.Duration:
		u := a.DataType().(*arrow.DurationType).Unit
		return Duration(int64(a.Value(i)) * int64(u.Multiplier()))
	case *array.MonthDayNanoInterval:
		v := a.Value(i)
		return Interval{Months: v.Months, Days: v.Days, Nanos: v.Nanoseconds}
	case *array.Decimal128:
		t := a.DataType().(*arrow.Decimal128Type)
		return Decimal{Unscaled: a.Value(i).BigInt(), Scale: t.Scale, Precision: t.Precision}
	case *array.Decimal256:
		t := a.DataType().(*arrow.Decimal256Type)
		return Decimal{Unscaled: a.Value(i).BigInt(), Scale: t.Scale, Precision: t.Precision}
	case *array.Dictionary:
		return ValueAt(a.Dictionary(), a.GetValueIndex(i))
	case *array.Map: // (before List: a Map is a List)
		lo, hi := a.ValueOffsets(i)
		keys, items := a.Keys(), a.Items()
		m := make(Map, 0, hi-lo)
		for j := lo; j < hi; j++ {
			m = append(m, KV{ValueAt(keys, int(j)), ValueAt(items, int(j))})
		}
		return m
	case array.ListLike:
		lo, hi := a.ValueOffsets(i)
		vals := a.ListValues()
		l := make(List, 0, hi-lo)
		for j := lo; j < hi; j++ {
			l = append(l, ValueAt(vals, int(j)))
		}
		return l
	case *array.Struct:
		st := a.DataType().(*arrow.StructType)
		s := make(Struct, st.NumFields())
		for k := range s {
			s[k] = Field{Name: st.Field(k).Name, Value: ValueAt(a.Field(k), i)}
		}
		return s
	case *array.Null:
		return nil
	case array.ExtensionArray:
		if fb, ok := a.Storage().(*array.FixedSizeBinary); ok && a.ExtensionType().ExtensionName() == "arrow.uuid" && len(fb.Value(i)) == 16 {
			return UUID(fb.Value(i))
		}
		return ValueAt(a.Storage(), i)
	}
	return arr.ValueStr(i) // a type pqx doesn't know: its text
}

func cloneBytes(b []byte) []byte { return append([]byte{}, b...) }

func isNumeric(t arrow.DataType) bool {
	if t == nil {
		return false
	}
	if d, ok := t.(*arrow.DictionaryType); ok {
		return isNumeric(d.ValueType)
	}
	return arrow.IsInteger(t.ID()) || arrow.IsFloating(t.ID()) || arrow.IsDecimal(t.ID())
}

func isFloat(t arrow.DataType) bool {
	if t == nil {
		return false
	}
	if d, ok := t.(*arrow.DictionaryType); ok {
		return isFloat(d.ValueType)
	}
	return arrow.IsFloating(t.ID())
}

func isTemporal(t arrow.DataType) bool {
	if t == nil {
		return false
	}
	switch t.ID() {
	case arrow.TIMESTAMP, arrow.DATE32, arrow.DATE64, arrow.TIME32, arrow.TIME64:
		return true
	}
	return false
}

func isNested(t arrow.DataType) bool {
	if t == nil {
		return false
	}
	return arrow.IsNested(t.ID())
}
