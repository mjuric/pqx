package fmtx

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
)

// fmt.py's name patterns. Python's "$" also matches before a final
// newline; "\n?$" keeps that.
var (
	reMJD        = regexp.MustCompile(`(?i)mjd|(^|_)jd(\n?$|_)|^jd|epoch|tai\n?$|utc\n?$`)
	reAngle      = regexp.MustCompile(`(?i)(^|_)(ra|dec|decl|lon|lat|glon|glat|elon|elat|lambda|beta|longitude|latitude)(\n?$|_)`)
	reAngleCamel = regexp.MustCompile(`^(ra|dec|decl)(\n?$|[A-Z0-9_])|(Ra|Dec|RA|DEC)\n?$`)
	reVizierRA   = regexp.MustCompile(`^RA(J2000|B1950|_ICRS|\n?$|_)`)
	reVizierDE   = regexp.MustCompile(`^DE(J2000|B1950|_ICRS|\n?$|_)`)
	reRA         = regexp.MustCompile(`(?i)(^|_)ra(\n?$|_)`)
	reRACamel    = regexp.MustCompile(`^ra(\n?$|[A-Z0-9_])|(Ra|RA)\n?$`)
	reLat        = regexp.MustCompile(`(?i)(^|_)(dec|decl|lat|glat|elat|beta|latitude)(\n?$|_)`)
	reLatCamel   = regexp.MustCompile(`^(dec|decl)(\n?$|[A-Z0-9_])|(Dec|DEC)\n?$`)
	reErr        = regexp.MustCompile(`(?i)err|sigma|unc|std|rms|cov`)
	reMag        = regexp.MustCompile(`(?i)mag(\n?$|[A-Z_])|^mag|Mag`)
	reFlux       = regexp.MustCompile(`(?i)flux`)
)

func isDegUnit(u string) bool { return u == "deg" || u == "degree" || u == "degrees" }

func normUnit(unit string) string { return strings.ToLower(strings.TrimSpace(unit)) }

func kindFor(name string, t arrow.DataType, unit string) Kind {
	if t == nil {
		return KindStr
	}
	u := normUnit(unit)
	id := t.ID()
	switch {
	case arrow.IsFloating(id) || arrow.IsDecimal(id):
		switch {
		case reErr.MatchString(name):
			return KindErr
		case u == "d" || u == "day" || u == "days" || u == "mjd" || reMJD.MatchString(name):
			return KindMJD
		case isDegUnit(u) || reAngleCamel.MatchString(name) || reAngle.MatchString(name) ||
			reVizierRA.MatchString(name) || reVizierDE.MatchString(name):
			return KindAngle
		case u == "mag" || u == "mag(ab)" || u == "abmag" || reMag.MatchString(name):
			return KindMag
		case u == "njy" || u == "jy" || u == "mjy" || u == "ujy" || reFlux.MatchString(name):
			return KindFlux
		case id == arrow.FLOAT32 || id == arrow.FLOAT16:
			return KindFloat32
		}
		return KindFloat
	case arrow.IsInteger(id):
		return KindInt
	case id == arrow.BOOL:
		return KindBool
	case id == arrow.TIMESTAMP || id == arrow.DATE32 || id == arrow.DATE64 || id == arrow.TIME32 || id == arrow.TIME64:
		return KindTime
	case id == arrow.BINARY || id == arrow.LARGE_BINARY || id == arrow.FIXED_SIZE_BINARY || id == arrow.BINARY_VIEW:
		return KindBinary
	}
	switch id {
	case arrow.LIST, arrow.LARGE_LIST, arrow.FIXED_SIZE_LIST, arrow.LIST_VIEW, arrow.LARGE_LIST_VIEW,
		arrow.STRUCT, arrow.SPARSE_UNION, arrow.DENSE_UNION, arrow.MAP:
		return KindNested
	}
	return KindStr
}

// shortTypes are short_type's names for PyArrow's type names.
var shortTypes = map[string]string{"double": "f64", "float": "f32", "halffloat": "f16", "int64": "i64",
	"int32": "i32", "int16": "i16", "int8": "i8", "uint64": "u64", "uint32": "u32", "uint16": "u16",
	"uint8": "u8", "string": "str", "large_string": "str", "bool": "bool"}

func shortType(t arrow.DataType) string {
	if t == nil {
		return ""
	}
	switch x := t.(type) {
	case *arrow.DictionaryType:
		return "dict<" + shortType(x.ValueType) + ">"
	case *arrow.TimestampType:
		s := "ts[" + x.Unit.String()
		if x.TimeZone != "" {
			s += "," + x.TimeZone
		}
		return Sanitize(s+"]", false)
	}
	s := Sanitize(pyTypeString(t), false)
	if n, ok := shortTypes[s]; ok {
		return n
	}
	return s
}

// pyTypeString is PyArrow's str() of an Arrow type ("double",
// "list<item: int64>", "timestamp[us, tz=UTC]", "map<string, int64>"), raw
// (not sanitized).
func pyTypeString(t arrow.DataType) string {
	switch x := t.(type) {
	case nil:
		return ""
	case *arrow.TimestampType:
		s := "timestamp[" + x.Unit.String()
		if x.TimeZone != "" {
			s += ", tz=" + x.TimeZone
		}
		return s + "]"
	case *arrow.Time32Type:
		return "time32[" + x.Unit.String() + "]"
	case *arrow.Time64Type:
		return "time64[" + x.Unit.String() + "]"
	case *arrow.DurationType:
		return "duration[" + x.Unit.String() + "]"
	case *arrow.FixedSizeBinaryType:
		return "fixed_size_binary[" + strconv.Itoa(x.ByteWidth) + "]"
	case arrow.DecimalType:
		name := map[arrow.Type]string{arrow.DECIMAL32: "decimal32", arrow.DECIMAL64: "decimal64",
			arrow.DECIMAL128: "decimal128", arrow.DECIMAL256: "decimal256"}[t.ID()]
		return name + "(" + strconv.Itoa(int(x.GetPrecision())) + ", " + strconv.Itoa(int(x.GetScale())) + ")"
	case *arrow.DictionaryType:
		ordered := "0"
		if x.Ordered {
			ordered = "1"
		}
		return "dictionary<values=" + pyTypeString(x.ValueType) + ", indices=" + pyTypeString(x.IndexType) +
			", ordered=" + ordered + ">"
	case *arrow.ListType:
		return "list<" + pyField(x.ElemField()) + ">"
	case *arrow.LargeListType:
		return "large_list<" + pyField(x.ElemField()) + ">"
	case *arrow.ListViewType:
		return "list_view<" + pyField(x.ElemField()) + ">"
	case *arrow.LargeListViewType:
		return "large_list_view<" + pyField(x.ElemField()) + ">"
	case *arrow.FixedSizeListType:
		return "fixed_size_list<" + pyField(x.ElemField()) + ">[" + strconv.Itoa(int(x.Len())) + "]"
	case *arrow.MapType:
		var b strings.Builder
		b.WriteString("map<")
		k, v := x.KeyField(), x.ItemField()
		b.WriteString(pyTypeString(k.Type))
		if k.Name != "key" {
			b.WriteString(" ('" + k.Name + "')")
		}
		b.WriteString(", ")
		b.WriteString(pyTypeString(v.Type))
		if v.Name != "value" {
			b.WriteString(" ('" + v.Name + "')")
		}
		if x.KeysSorted {
			b.WriteString(", keys_sorted")
		}
		if e := x.ElemField(); e.Name != "entries" {
			b.WriteString(" ('" + e.Name + "')")
		}
		b.WriteString(">")
		return b.String()
	case *arrow.StructType:
		parts := make([]string, x.NumFields())
		for i := range parts {
			parts[i] = pyField(x.Field(i))
		}
		return "struct<" + strings.Join(parts, ", ") + ">"
	case *arrow.RunEndEncodedType:
		return "run_end_encoded<run_ends: " + pyTypeString(x.RunEnds()) + ", values: " + pyTypeString(x.Encoded()) + ">"
	case arrow.UnionType:
		parts := make([]string, len(x.Fields()))
		codes := x.TypeCodes()
		for i, f := range x.Fields() {
			parts[i] = pyField(f) + "=" + strconv.Itoa(int(codes[i]))
		}
		return x.Name() + "<" + strings.Join(parts, ", ") + ">"
	case arrow.ExtensionType:
		return "extension<" + x.ExtensionName() + ">"
	}
	switch t.ID() {
	case arrow.FLOAT64:
		return "double"
	case arrow.FLOAT32:
		return "float"
	case arrow.FLOAT16:
		return "halffloat"
	case arrow.BOOL:
		return "bool"
	case arrow.STRING:
		return "string"
	case arrow.LARGE_STRING:
		return "large_string"
	case arrow.STRING_VIEW:
		return "string_view"
	case arrow.BINARY_VIEW:
		return "binary_view"
	case arrow.DATE32:
		return "date32[day]"
	case arrow.DATE64:
		return "date64[ms]"
	case arrow.NULL:
		return "null"
	case arrow.INTERVAL_MONTH_DAY_NANO:
		return "month_day_nano_interval"
	case arrow.INTERVAL_MONTHS:
		return "month_interval"
	case arrow.INTERVAL_DAY_TIME:
		return "day_time_interval"
	}
	return t.Name() // int8 … uint64, binary, large_binary: the same names
}

func pyField(f arrow.Field) string {
	s := f.Name + ": " + pyTypeString(f.Type)
	if !f.Nullable {
		s += " not null"
	}
	return s
}
