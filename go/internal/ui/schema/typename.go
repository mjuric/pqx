package schema

import (
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
)

// arrowName is t's name as PyArrow's str(type) spells it ("double",
// "string", "timestamp[us, tz=UTC]", "list<element: int64>"): the Schema
// tab's description shows it, as Python pqx does. Types PyArrow names as
// arrow-go does fall through to t.String().
func arrowName(t arrow.DataType) string { return typeName("entries", t) }

// typeName is arrowName for the type of a field called name: PyArrow, reading
// a Parquet file, names a map's entries field after the map's own field (a
// column "mp" is "map<string, int64 ('mp')>"), which arrow-go doesn't keep.
func typeName(name string, t arrow.DataType) string {
	if t == nil {
		return "null"
	}
	switch tt := t.(type) {
	case *arrow.Float16Type:
		return "halffloat"
	case *arrow.Float32Type:
		return "float"
	case *arrow.Float64Type:
		return "double"
	case *arrow.StringType:
		return "string"
	case *arrow.LargeStringType:
		return "large_string"
	case *arrow.StringViewType:
		return "string_view"
	case *arrow.Date32Type:
		return "date32[day]"
	case *arrow.Date64Type:
		return "date64[ms]"
	case *arrow.TimestampType:
		if tt.TimeZone != "" {
			return "timestamp[" + tt.Unit.String() + ", tz=" + tt.TimeZone + "]"
		}
		return "timestamp[" + tt.Unit.String() + "]"
	case *arrow.Decimal32Type:
		return decimal("decimal32", tt.Precision, tt.Scale)
	case *arrow.Decimal64Type:
		return decimal("decimal64", tt.Precision, tt.Scale)
	case *arrow.Decimal128Type:
		return decimal("decimal128", tt.Precision, tt.Scale)
	case *arrow.Decimal256Type:
		return decimal("decimal256", tt.Precision, tt.Scale)
	case *arrow.ListType:
		return "list<" + field(tt.ElemField()) + ">"
	case *arrow.LargeListType:
		return "large_list<" + field(tt.ElemField()) + ">"
	case *arrow.ListViewType:
		return "list_view<" + field(tt.ElemField()) + ">"
	case *arrow.FixedSizeListType:
		return "fixed_size_list<" + field(tt.ElemField()) + ">[" + strconv.Itoa(int(tt.Len())) + "]"
	case *arrow.MapType:
		// as Arrow C++'s MapType::ToString: field names that aren't the
		// standard ones are shown
		named := func(name, std string) string {
			if name != std {
				return " ('" + name + "')"
			}
			return ""
		}
		k, it := tt.KeyField(), tt.ItemField()
		s := "map<" + arrowName(k.Type) + named(k.Name, "key") + ", " + arrowName(it.Type)
		if !it.Nullable {
			s += " not null"
		}
		s += named(it.Name, "value")
		if tt.KeysSorted {
			s += ", keys_sorted"
		}
		return s + named(name, "entries") + ">"
	case *arrow.StructType:
		parts := make([]string, len(tt.Fields()))
		for i, f := range tt.Fields() {
			parts[i] = field(f)
		}
		return "struct<" + strings.Join(parts, ", ") + ">"
	case *arrow.DictionaryType:
		ordered := "0"
		if tt.Ordered {
			ordered = "1"
		}
		return "dictionary<values=" + arrowName(tt.ValueType) + ", indices=" + arrowName(tt.IndexType) +
			", ordered=" + ordered + ">"
	case arrow.ExtensionType:
		return "extension<" + tt.ExtensionName() + ">"
	}
	return t.String()
}

func field(f arrow.Field) string {
	s := f.Name + ": " + typeName(f.Name, f.Type)
	if !f.Nullable {
		s += " not null"
	}
	return s
}

func decimal(name string, p, s int32) string {
	return name + "(" + strconv.Itoa(int(p)) + ", " + strconv.Itoa(int(s)) + ")"
}
