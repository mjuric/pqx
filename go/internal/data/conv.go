package data

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/duckdb/duckdb-go/v2"
	"github.com/duckdb/duckdb-go/v2/mapping"
)

// cellFunc converts cell i of an array to a Value.
type cellFunc func(arrow.Array, int) Value

// Both readers give the same Value for the same cell (data.go). DuckDB's
// values are the reference, as in Python pqx (whose direct reader casts
// PyArrow's columns to DuckDB's types, _castable and _convert):
//
//   - DuckDB's own values are ValueAt of its Arrow results, with UUIDs (which
//     DuckDB hands over as text) turned back into UUID (duckCell).
//   - arrow-go's values are converted to what ValueAt gives for DuckDB's
//     Arrow type of the column (directCell): timestamps and times in DuckDB's
//     unit (cut toward zero, INT96 rounded down, as DuckDB does), Parquet text that arrow-go reads as binary (ENUM,
//     JSON) as strings, fixed-size lists as lists, dictionaries decoded.
//
// Decimals wider than 38 digits are the exception: DuckDB reads them as
// wrong doubles, so arrow-go's exact Decimal is the reference, and windows
// DuckDB serves get those columns from arrow-go by file row (wideCols).

// directCell returns how the plain view converts arrow-go's values of type s
// (as it reads the column from the file) to DuckDB's: d is DuckDB's Arrow type
// for the column and ti its DuckDB type (nil if unknown). ok is false if there
// is no exact conversion: DuckDB then reads that column.
func directCell(s, d arrow.DataType, ti duckdb.TypeInfo, int96 bool) (cellFunc, bool) {
	return convFor(s, d, ti, int96, true)
}

func convFor(s, d arrow.DataType, ti duckdb.TypeInfo, int96, top bool) (cellFunc, bool) {
	if s == nil || d == nil {
		return nil, false
	}
	// DuckDB's ENUM comes as a dictionary: ValueAt reads through it.
	if dd, ok := d.(*arrow.DictionaryType); ok {
		return convFor(s, dd.ValueType, ti, int96, top)
	}
	if _, ok := d.(arrow.ExtensionType); ok {
		return nil, false
	}
	isUUID := ti != nil && ti.InternalType() == duckdb.TYPE_UUID
	switch st := s.(type) {
	case arrow.ExtensionType:
		if st.ExtensionName() == "arrow.uuid" {
			if !isUUID {
				return nil, false
			}
			return func(arr arrow.Array, i int) Value {
				if arr.IsNull(i) {
					return nil
				}
				ext := arr.(array.ExtensionArray)
				return uuidOf(ext.Storage().(*array.FixedSizeBinary).Value(i))
			}, true
		}
		inner, ok := convFor(st.StorageType(), d, ti, int96, top)
		if !ok {
			return nil, false
		}
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			return inner(arr.(array.ExtensionArray).Storage(), i)
		}, true
	case *arrow.DictionaryType:
		inner, ok := convFor(st.ValueType, d, ti, int96, top)
		if !ok {
			return nil, false
		}
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			a := arr.(*array.Dictionary)
			return inner(a.Dictionary(), a.GetValueIndex(i))
		}, true
	case *arrow.NullType:
		return func(arrow.Array, int) Value { return nil }, true
	case *arrow.DurationType:
		// arrow-go (v18.8) can't read duration columns ("no support for
		// reading columns of type: duration"); DuckDB reads their stored
		// counts as BIGINT.
		return nil, false
	}
	if isUUID {
		fs, ok := s.(*arrow.FixedSizeBinaryType)
		if !ok || fs.ByteWidth != 16 {
			return nil, false
		}
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			return uuidOf(arr.(*array.FixedSizeBinary).Value(i))
		}, true
	}
	if hasWideDecimal(s) {
		// DuckDB's type is a DOUBLE where s has the wide decimal: arrow-go
		// is the only reader that reads the column right.
		return ValueAt, true
	}
	if !hasUUID(ti) && arrow.TypeEqual(s, d) {
		return ValueAt, true
	}

	switch dt := d.(type) {
	case *arrow.StructType:
		st, ok := s.(*arrow.StructType)
		if !ok || st.NumFields() != dt.NumFields() {
			return nil, false
		}
		var entries []duckdb.StructEntry
		if sd, ok := details[*duckdb.StructDetails](ti); ok {
			entries = sd.Entries
		}
		fields := make([]cellFunc, st.NumFields())
		names := make([]string, st.NumFields())
		for k := range fields {
			if st.Field(k).Name != dt.Field(k).Name {
				return nil, false
			}
			var cti duckdb.TypeInfo
			if k < len(entries) {
				cti = entries[k].Info()
			}
			f, ok := convFor(st.Field(k).Type, dt.Field(k).Type, cti, int96, false)
			if !ok {
				return nil, false
			}
			fields[k], names[k] = f, dt.Field(k).Name
		}
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			a := arr.(*array.Struct)
			out := make(Struct, len(fields))
			for k, f := range fields {
				out[k] = Field{Name: names[k], Value: f(a.Field(k), i)}
			}
			return out
		}, true
	case *arrow.MapType:
		sm, ok := s.(*arrow.MapType)
		if !ok {
			return nil, false
		}
		var kti, vti duckdb.TypeInfo
		if md, ok := details[*duckdb.MapDetails](ti); ok {
			kti, vti = md.Key, md.Value
		}
		kf, ok1 := convFor(sm.KeyType(), dt.KeyType(), kti, int96, false)
		vf, ok2 := convFor(sm.ItemType(), dt.ItemType(), vti, int96, false)
		if !ok1 || !ok2 {
			return nil, false
		}
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			a := arr.(*array.Map)
			lo, hi := a.ValueOffsets(i)
			keys, items := a.Keys(), a.Items()
			m := make(Map, 0, hi-lo)
			for j := lo; j < hi; j++ {
				m = append(m, KV{kf(keys, int(j)), vf(items, int(j))})
			}
			return m
		}, true
	case *arrow.ListType, *arrow.LargeListType, *arrow.FixedSizeListType, *arrow.ListViewType, *arrow.LargeListViewType:
		se, ok := listElem(s)
		if !ok {
			return nil, false
		}
		de, _ := listElem(d)
		var cti duckdb.TypeInfo
		if ld, ok := details[*duckdb.ListDetails](ti); ok {
			cti = ld.Child
		} else if ad, ok := details[*duckdb.ArrayDetails](ti); ok {
			cti = ad.Child
		}
		ef, ok := convFor(se, de, cti, int96, false)
		if !ok {
			return nil, false
		}
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			a := arr.(array.ListLike)
			lo, hi := a.ValueOffsets(i)
			vals := a.ListValues()
			l := make(List, 0, hi-lo)
			for j := lo; j < hi; j++ {
				l = append(l, ef(vals, int(j)))
			}
			return l
		}, true
	}

	switch {
	case arrow.IsFloating(s.ID()) && arrow.IsFloating(d.ID()):
		if floatBits(s) > floatBits(d) {
			return nil, false
		}
		if d.ID() == arrow.FLOAT32 {
			return func(arr arrow.Array, i int) Value {
				if arr.IsNull(i) {
					return nil
				}
				return float32Of(ValueAt(arr, i))
			}, true
		}
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			return float64(float32Of(ValueAt(arr, i)))
		}, true
	case arrow.IsInteger(s.ID()) && arrow.IsInteger(d.ID()):
		// ValueAt makes every width int64 (signed) or uint64 (unsigned).
		if arrow.IsUnsignedInteger(s.ID()) != arrow.IsUnsignedInteger(d.ID()) {
			return nil, false
		}
		return ValueAt, true
	case s.ID() == arrow.BOOL && d.ID() == arrow.BOOL:
		return ValueAt, true
	case isStringType(s) && isStringType(d):
		return ValueAt, true
	case isBinaryType(s) && isStringType(d):
		// Parquet ENUM and JSON, which arrow-go reads as binary
		if s.ID() == arrow.FIXED_SIZE_BINARY {
			return nil, false
		}
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			return string(ValueAt(arr, i).([]byte))
		}, true
	case isBinaryType(s) && isBinaryType(d):
		return ValueAt, true
	case s.ID() == arrow.TIMESTAMP && d.ID() == arrow.TIMESTAMP:
		return timestampCell(s.(*arrow.TimestampType), d.(*arrow.TimestampType), int96)
	case (s.ID() == arrow.TIME32 || s.ID() == arrow.TIME64) && (d.ID() == arrow.TIME32 || d.ID() == arrow.TIME64):
		dunit := timeUnit(d)
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			ns := int64(ValueAt(arr, i).(TimeOfDay))
			m := int64(dunit.Multiplier())
			return TimeOfDay(ns / m * m)
		}, true
	case (s.ID() == arrow.DATE32 || s.ID() == arrow.DATE64) && (d.ID() == arrow.DATE32 || d.ID() == arrow.DATE64):
		return ValueAt, true
	case arrow.IsDecimal(s.ID()) && arrow.IsDecimal(d.ID()):
		sd, dd := s.(arrow.DecimalType), d.(arrow.DecimalType)
		if sd.GetScale() != dd.GetScale() {
			return nil, false
		}
		prec := dd.GetPrecision()
		return func(arr arrow.Array, i int) Value {
			if arr.IsNull(i) {
				return nil
			}
			v := ValueAt(arr, i).(Decimal)
			v.Precision = prec
			return v
		}, true
	}
	return nil, false
}

// details is ti's details as a T, if ti has such details.
func details[T duckdb.TypeDetails](ti duckdb.TypeInfo) (T, bool) {
	var zero T
	if ti == nil {
		return zero, false
	}
	d, ok := ti.Details().(T)
	return d, ok
}

func listElem(t arrow.DataType) (arrow.DataType, bool) {
	switch t := t.(type) {
	case *arrow.ListType:
		return t.Elem(), true
	case *arrow.LargeListType:
		return t.Elem(), true
	case *arrow.FixedSizeListType:
		return t.Elem(), true
	case *arrow.ListViewType:
		return t.Elem(), true
	case *arrow.LargeListViewType:
		return t.Elem(), true
	}
	return nil, false
}

func floatBits(t arrow.DataType) int {
	switch t.ID() {
	case arrow.FLOAT16:
		return 16
	case arrow.FLOAT32:
		return 32
	}
	return 64
}

func float32Of(v Value) float32 {
	switch v := v.(type) {
	case float32:
		return v
	case float64:
		return float32(v)
	}
	return 0
}

func isStringType(t arrow.DataType) bool {
	switch t.ID() {
	case arrow.STRING, arrow.LARGE_STRING, arrow.STRING_VIEW:
		return true
	}
	return false
}

func isBinaryType(t arrow.DataType) bool {
	switch t.ID() {
	case arrow.BINARY, arrow.LARGE_BINARY, arrow.BINARY_VIEW, arrow.FIXED_SIZE_BINARY:
		return true
	}
	return false
}

func timeUnit(t arrow.DataType) arrow.TimeUnit {
	switch t := t.(type) {
	case *arrow.Time32Type:
		return t.Unit
	case *arrow.Time64Type:
		return t.Unit
	}
	return arrow.Nanosecond
}

// timestampCell converts timestamps of type s to DuckDB's d: the same
// instant in d's unit, cut toward zero as DuckDB does when it reads a finer
// unit (INT96, which DuckDB decodes itself, rounded down). A zone-aware and
// a naive timestamp don't convert.
func timestampCell(s, d *arrow.TimestampType, int96 bool) (cellFunc, bool) {
	if (s.TimeZone == "") != (d.TimeZone == "") {
		return nil, false
	}
	sm, dm := int64(s.Unit.Multiplier()), int64(d.Unit.Multiplier())
	zoned := d.TimeZone != ""
	unit := d.Unit.Multiplier()
	return func(arr arrow.Array, i int) Value {
		if arr.IsNull(i) {
			return nil
		}
		v := int64(arr.(*array.Timestamp).Value(i))
		switch {
		case sm > dm:
			v *= sm / dm // (overflow only past the year 292,000)
		case sm < dm:
			k := dm / sm
			q := v / k
			if int96 && v%k < 0 {
				q--
			}
			v = q
		}
		return Timestamp{T: arrow.Timestamp(v).ToTime(d.Unit).UTC(), Zoned: zoned, Unit: unit}
	}, true
}

func uuidOf(b []byte) Value {
	if len(b) != 16 {
		return cloneBytes(b)
	}
	return UUID(b)
}

// hasWideDecimal reports whether t is or holds a decimal of more than 38
// digits (which DuckDB reads as wrong doubles).
func hasWideDecimal(t arrow.DataType) bool {
	switch t := t.(type) {
	case arrow.DecimalType:
		return t.GetPrecision() > 38
	case *arrow.DictionaryType:
		return hasWideDecimal(t.ValueType)
	case arrow.ExtensionType:
		return hasWideDecimal(t.StorageType())
	case arrow.NestedType:
		for _, f := range t.Fields() {
			if hasWideDecimal(f.Type) {
				return true
			}
		}
	}
	return false
}

// hasUUID reports whether DuckDB type ti is or holds a UUID.
func hasUUID(ti duckdb.TypeInfo) bool {
	if ti == nil {
		return false
	}
	switch ti.InternalType() {
	case duckdb.TYPE_UUID:
		return true
	case duckdb.TYPE_LIST:
		d, _ := details[*duckdb.ListDetails](ti)
		return d != nil && hasUUID(d.Child)
	case duckdb.TYPE_ARRAY:
		d, _ := details[*duckdb.ArrayDetails](ti)
		return d != nil && hasUUID(d.Child)
	case duckdb.TYPE_MAP:
		d, _ := details[*duckdb.MapDetails](ti)
		return d != nil && (hasUUID(d.Key) || hasUUID(d.Value))
	case duckdb.TYPE_STRUCT:
		d, _ := details[*duckdb.StructDetails](ti)
		if d == nil {
			return false
		}
		for _, e := range d.Entries {
			if hasUUID(e.Info()) {
				return true
			}
		}
	}
	return false
}

// duckCell is how values of DuckDB's Arrow results for a column of DuckDB
// type ti are read: ValueAt, with the UUIDs (text in DuckDB's Arrow results)
// made UUID wherever they are in the type.
func duckCell(ti duckdb.TypeInfo) cellFunc {
	if !hasUUID(ti) {
		return ValueAt
	}
	fix := uuidFixer(ti)
	return func(arr arrow.Array, i int) Value { return fix(ValueAt(arr, i)) }
}

// uuidFixer turns the UUID text in a value of DuckDB type ti into UUIDs.
func uuidFixer(ti duckdb.TypeInfo) func(Value) Value {
	if !hasUUID(ti) {
		return func(v Value) Value { return v }
	}
	switch ti.InternalType() {
	case duckdb.TYPE_UUID:
		return func(v Value) Value {
			if s, ok := v.(string); ok {
				if u, err := parseUUID(s); err == nil {
					return u
				}
			}
			return v
		}
	case duckdb.TYPE_LIST, duckdb.TYPE_ARRAY:
		var child duckdb.TypeInfo
		if d, ok := details[*duckdb.ListDetails](ti); ok {
			child = d.Child
		} else if d, ok := details[*duckdb.ArrayDetails](ti); ok {
			child = d.Child
		}
		f := uuidFixer(child)
		return func(v Value) Value {
			if l, ok := v.(List); ok {
				for i := range l {
					l[i] = f(l[i])
				}
			}
			return v
		}
	case duckdb.TYPE_MAP:
		d, _ := details[*duckdb.MapDetails](ti)
		kf, vf := uuidFixer(d.Key), uuidFixer(d.Value)
		return func(v Value) Value {
			if m, ok := v.(Map); ok {
				for i := range m {
					m[i] = KV{kf(m[i].Key), vf(m[i].Value)}
				}
			}
			return v
		}
	case duckdb.TYPE_STRUCT:
		d, _ := details[*duckdb.StructDetails](ti)
		fs := make([]func(Value) Value, len(d.Entries))
		for k, e := range d.Entries {
			fs[k] = uuidFixer(e.Info())
		}
		return func(v Value) Value {
			if s, ok := v.(Struct); ok && len(s) == len(fs) {
				for k := range s {
					s[k].Value = fs[k](s[k].Value)
				}
			}
			return v
		}
	}
	return func(v Value) Value { return v }
}

// duckTypeName is DuckDB's name for type ti, as DESCRIBE spells it
// (keywords: DuckDB's keywords, which struct field names are quoted for).
func duckTypeName(ti duckdb.TypeInfo, keywords map[string]bool) string {
	if ti == nil {
		return ""
	}
	if a := ti.Alias(); a != "" {
		return strings.ToUpper(a)
	}
	switch ti.InternalType() {
	case duckdb.TYPE_BOOLEAN:
		return "BOOLEAN"
	case duckdb.TYPE_TINYINT:
		return "TINYINT"
	case duckdb.TYPE_SMALLINT:
		return "SMALLINT"
	case duckdb.TYPE_INTEGER, duckdb.TYPE_SQLNULL:
		return "INTEGER"
	case duckdb.TYPE_BIGINT:
		return "BIGINT"
	case duckdb.TYPE_UTINYINT:
		return "UTINYINT"
	case duckdb.TYPE_USMALLINT:
		return "USMALLINT"
	case duckdb.TYPE_UINTEGER:
		return "UINTEGER"
	case duckdb.TYPE_UBIGINT:
		return "UBIGINT"
	case duckdb.TYPE_FLOAT:
		return "FLOAT"
	case duckdb.TYPE_DOUBLE:
		return "DOUBLE"
	case duckdb.TYPE_TIMESTAMP:
		return "TIMESTAMP"
	case duckdb.TYPE_DATE:
		return "DATE"
	case duckdb.TYPE_TIME:
		return "TIME"
	case mapping.TypeTimeNS: // (duckdb-go has no name for it yet)
		return "TIME_NS"
	case duckdb.TYPE_INTERVAL:
		return "INTERVAL"
	case duckdb.TYPE_HUGEINT:
		return "HUGEINT"
	case duckdb.TYPE_UHUGEINT:
		return "UHUGEINT"
	case duckdb.TYPE_VARCHAR:
		return "VARCHAR"
	case duckdb.TYPE_BLOB:
		return "BLOB"
	case duckdb.TYPE_DECIMAL:
		if d, ok := details[*duckdb.DecimalDetails](ti); ok {
			return fmt.Sprintf("DECIMAL(%d,%d)", d.Width, d.Scale)
		}
		return "DECIMAL"
	case duckdb.TYPE_TIMESTAMP_S:
		return "TIMESTAMP_S"
	case duckdb.TYPE_TIMESTAMP_MS:
		return "TIMESTAMP_MS"
	case duckdb.TYPE_TIMESTAMP_NS:
		return "TIMESTAMP_NS"
	case duckdb.TYPE_TIMESTAMP_TZ:
		return "TIMESTAMP WITH TIME ZONE"
	case duckdb.TYPE_TIME_TZ:
		return "TIME WITH TIME ZONE"
	case duckdb.TYPE_UUID:
		return "UUID"
	case duckdb.TYPE_BIT:
		return "BIT"
	case duckdb.TYPE_BIGNUM:
		return "BIGNUM"
	case duckdb.TYPE_ENUM:
		d, _ := details[*duckdb.EnumDetails](ti)
		if d == nil {
			return "ENUM"
		}
		vals := make([]string, len(d.Values))
		for i, v := range d.Values {
			vals[i] = quoteStr(v)
		}
		return "ENUM(" + strings.Join(vals, ", ") + ")"
	case duckdb.TYPE_LIST:
		d, _ := details[*duckdb.ListDetails](ti)
		if d == nil {
			return "LIST"
		}
		return duckTypeName(d.Child, keywords) + "[]"
	case duckdb.TYPE_ARRAY:
		d, _ := details[*duckdb.ArrayDetails](ti)
		if d == nil {
			return "ARRAY"
		}
		return duckTypeName(d.Child, keywords) + "[" + strconv.FormatUint(d.Size, 10) + "]"
	case duckdb.TYPE_MAP:
		d, _ := details[*duckdb.MapDetails](ti)
		if d == nil {
			return "MAP"
		}
		return "MAP(" + duckTypeName(d.Key, keywords) + ", " + duckTypeName(d.Value, keywords) + ")"
	case duckdb.TYPE_STRUCT:
		d, _ := details[*duckdb.StructDetails](ti)
		if d == nil {
			return "STRUCT"
		}
		parts := make([]string, len(d.Entries))
		for i, e := range d.Entries {
			parts[i] = optionallyQuoted(e.Name(), keywords) + " " + duckTypeName(e.Info(), keywords)
		}
		return "STRUCT(" + strings.Join(parts, ", ") + ")"
	case duckdb.TYPE_UNION:
		d, _ := details[*duckdb.UnionDetails](ti)
		if d == nil {
			return "UNION"
		}
		parts := make([]string, len(d.Members))
		for i, m := range d.Members {
			parts[i] = optionallyQuoted(m.Name, keywords) + " " + duckTypeName(m.Type, keywords)
		}
		return "UNION(" + strings.Join(parts, ", ") + ")"
	case duckdb.TYPE_GEOMETRY:
		return "GEOMETRY"
	case duckdb.TYPE_VARIANT:
		return "VARIANT"
	}
	return "UNKNOWN"
}

// optionallyQuoted is name as DuckDB writes it in a type: bare if it is
// lower-case letters, digits and underscores, not starting with a digit,
// and not a keyword; else quoted.
func optionallyQuoted(name string, keywords map[string]bool) string {
	plain := name != "" && !keywords[name]
	for i := 0; i < len(name) && plain; i++ {
		c := name[i]
		plain = c == '_' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9'
	}
	if plain {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
