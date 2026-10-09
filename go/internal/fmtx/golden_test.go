package fmtx

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/extensions"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/golden"
)

// toValue is a golden typed value as the data layer holds it.
func toValue(t testing.TB, g golden.Value) data.Value {
	t.Helper()
	switch g.Kind {
	case golden.KindNull:
		return nil
	case golden.KindInt:
		return g.Int.Int64()
	case golden.KindUint:
		return g.Int.Uint64()
	case golden.KindF64:
		return g.Float
	case golden.KindF32:
		return g.Float32
	case golden.KindBool:
		return g.Bool
	case golden.KindStr:
		return g.Str
	case golden.KindBytes:
		return g.Bytes
	case golden.KindTS:
		units := map[string]time.Duration{"s": time.Second, "ms": time.Millisecond, "us": time.Microsecond, "ns": time.Nanosecond}
		return data.Timestamp{T: g.Time.UTC(), Zoned: g.TZ != "", Unit: units[g.Unit]}
	case golden.KindDate:
		return data.Date(g.Time.Unix() / 86400)
	case golden.KindTime:
		return data.TimeOfDay(g.Nanos)
	case golden.KindDur:
		return data.Duration(g.Int.Int64())
	case golden.KindDec:
		return data.Decimal{Unscaled: g.Int, Scale: int32(g.Scale), Precision: int32(g.Precision)}
	case golden.KindUUID:
		b, err := hex.DecodeString(strings.ReplaceAll(g.Str, "-", ""))
		if err != nil || len(b) != 16 {
			t.Fatalf("bad uuid %q", g.Str)
		}
		var u data.UUID
		copy(u[:], b)
		return u
	case golden.KindList:
		l := data.List{}
		for _, x := range g.List {
			l = append(l, toValue(t, x))
		}
		return l
	case golden.KindStruct:
		s := data.Struct{}
		for _, f := range g.Fields {
			s = append(s, data.Field{Name: f.Name, Value: toValue(t, f.Value)})
		}
		return s
	case golden.KindMap:
		m := data.Map{}
		for _, e := range g.Entries {
			m = append(m, data.KV{Key: toValue(t, e.Key), Value: toValue(t, e.Value)})
		}
		return m
	}
	t.Fatalf("unknown golden kind %q", g.Kind)
	return nil
}

// toOverride is a golden override (nil, int or string).
func toOverride(o any) Override {
	switch x := o.(type) {
	case int:
		return Override{Digits: x, Set: true}
	case string:
		return Override{Spec: x, Set: true}
	}
	return Override{}
}

// parseType makes an Arrow type from PyArrow's str() of it.
func parseType(t testing.TB, s string) arrow.DataType {
	t.Helper()
	typ, rest := parseTypeAt(t, s)
	if rest != "" {
		t.Fatalf("parseType(%q): left %q", s, rest)
	}
	return typ
}

var simpleTypes = map[string]arrow.DataType{
	"double": arrow.PrimitiveTypes.Float64, "float": arrow.PrimitiveTypes.Float32, "halffloat": arrow.FixedWidthTypes.Float16,
	"int8": arrow.PrimitiveTypes.Int8, "int16": arrow.PrimitiveTypes.Int16, "int32": arrow.PrimitiveTypes.Int32,
	"int64": arrow.PrimitiveTypes.Int64, "uint8": arrow.PrimitiveTypes.Uint8, "uint16": arrow.PrimitiveTypes.Uint16,
	"uint32": arrow.PrimitiveTypes.Uint32, "uint64": arrow.PrimitiveTypes.Uint64, "bool": arrow.FixedWidthTypes.Boolean,
	"string": arrow.BinaryTypes.String, "large_string": arrow.BinaryTypes.LargeString, "binary": arrow.BinaryTypes.Binary,
	"large_binary": arrow.BinaryTypes.LargeBinary, "date32[day]": arrow.FixedWidthTypes.Date32,
	"date64[ms]": arrow.FixedWidthTypes.Date64, "null": arrow.Null,
	"extension<arrow.uuid>": extensions.NewUUIDType(),
}

var timeUnits = map[string]arrow.TimeUnit{"s": arrow.Second, "ms": arrow.Millisecond, "us": arrow.Microsecond, "ns": arrow.Nanosecond}

// splitTop splits s at sep outside <>, [] and ().
func splitTop(s, sep string) []string {
	var out []string
	depth, last := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<', '[', '(':
			depth++
		case '>', ']', ')':
			depth--
		}
		if depth == 0 && strings.HasPrefix(s[i:], sep) {
			out = append(out, s[last:i])
			last = i + len(sep)
			i += len(sep) - 1
		}
	}
	return append(out, s[last:])
}

// closing is the index of the '>' closing the '<' at s[open].
func closing(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func parseField(t testing.TB, s string) arrow.Field {
	i := strings.Index(s, ": ")
	name, ts := s[:i], s[i+2:]
	nullable := true
	if strings.HasSuffix(ts, " not null") {
		ts, nullable = strings.TrimSuffix(ts, " not null"), false
	}
	return arrow.Field{Name: name, Type: parseType(t, ts), Nullable: nullable}
}

func parseTypeAt(t testing.TB, s string) (arrow.DataType, string) {
	t.Helper()
	for name, typ := range simpleTypes {
		if s == name {
			return typ, ""
		}
	}
	if s == "extension<arrow.json>" {
		j, err := extensions.NewJSONType(arrow.BinaryTypes.String)
		if err != nil {
			t.Fatal(err)
		}
		return j, ""
	}
	var a, b int
	var unit, tz string
	switch {
	case strings.HasPrefix(s, "timestamp["):
		in := strings.TrimSuffix(strings.TrimPrefix(s, "timestamp["), "]")
		unit, tz, _ = strings.Cut(in, ", tz=")
		return &arrow.TimestampType{Unit: timeUnits[unit], TimeZone: tz}, ""
	case strings.HasPrefix(s, "time32["):
		return &arrow.Time32Type{Unit: timeUnits[s[7:len(s)-1]]}, ""
	case strings.HasPrefix(s, "time64["):
		return &arrow.Time64Type{Unit: timeUnits[s[7:len(s)-1]]}, ""
	case strings.HasPrefix(s, "duration["):
		return &arrow.DurationType{Unit: timeUnits[s[9:len(s)-1]]}, ""
	case strings.HasPrefix(s, "fixed_size_binary["):
		n, _ := strconv.Atoi(s[18 : len(s)-1])
		return &arrow.FixedSizeBinaryType{ByteWidth: n}, ""
	case strings.HasPrefix(s, "decimal128("):
		fmt.Sscanf(s, "decimal128(%d, %d)", &a, &b)
		return &arrow.Decimal128Type{Precision: int32(a), Scale: int32(b)}, ""
	case strings.HasPrefix(s, "decimal256("):
		fmt.Sscanf(s, "decimal256(%d, %d)", &a, &b)
		return &arrow.Decimal256Type{Precision: int32(a), Scale: int32(b)}, ""
	}
	open := strings.IndexByte(s, '<')
	if open < 0 {
		t.Fatalf("parseType: unknown %q", s)
	}
	end := closing(s, open)
	head, in, tail := s[:open], s[open+1:end], s[end+1:]
	switch head {
	case "list":
		return arrow.ListOfField(parseField(t, in)), tail
	case "large_list":
		return arrow.LargeListOfField(parseField(t, in)), tail
	case "fixed_size_list":
		n, _ := strconv.Atoi(strings.Trim(tail, "[]"))
		return arrow.FixedSizeListOfField(int32(n), parseField(t, in)), ""
	case "struct":
		var fs []arrow.Field
		for _, p := range splitTop(in, ", ") {
			fs = append(fs, parseField(t, p))
		}
		return arrow.StructOf(fs...), tail
	case "map":
		parts := splitTop(in, ", ")
		// arrow-go names a map's fields key, value and entries, always: a
		// map whose entries are named otherwise (" ('map')") can't be made.
		item, _, _ := strings.Cut(parts[1], " ('")
		return arrow.MapOf(parseType(t, parts[0]), parseType(t, item)), tail
	case "dictionary":
		parts := splitTop(in, ", ")
		vt := parseType(t, strings.TrimPrefix(parts[0], "values="))
		it := parseType(t, strings.TrimPrefix(parts[1], "indices="))
		return &arrow.DictionaryType{IndexType: it, ValueType: vt, Ordered: parts[2] == "ordered=1"}, tail
	}
	t.Fatalf("parseType: unknown %q", s)
	return nil, ""
}

// arrowGoType is PyArrow's type text as arrow-go can hold the type: without
// a map's own entries name (" ('map')").
func arrowGoType(s string) string { return strings.ReplaceAll(s, " ('map')", "") }

var nan = math.NaN()

func loadFmt(t *testing.T) *golden.File { return golden.Load(t, "fmt.json") }

func TestGoldenKindFor(t *testing.T) {
	for _, r := range loadFmt(t).Section(t, "kind_for") {
		typ := parseType(t, r.String(t, "type"))
		if got, want := pyTypeString(typ), arrowGoType(r.String(t, "type")); got != want {
			t.Errorf("%s: pyTypeString = %q, want %q", r.ID(), got, want)
		}
		got := KindFor(r.String(t, "name"), typ, r.String(t, "unit"))
		if want := r.String(t, "out"); string(got) != want {
			t.Errorf("%s: KindFor(%q, %s, %q) = %q, want %q", r.ID(), r.String(t, "name"), r.String(t, "type"), r.String(t, "unit"), got, want)
		}
	}
}

func TestGoldenShortType(t *testing.T) {
	for _, r := range loadFmt(t).Section(t, "short_type") {
		typ := parseType(t, r.String(t, "type"))
		if got, want := ShortType(typ), arrowGoType(r.String(t, "out")); got != want {
			t.Errorf("%s: ShortType(%s) = %q, want %q", r.ID(), r.String(t, "type"), got, want)
		}
	}
}

// f32RawCase reports whether a format_value case is a float32 value shown
// raw: Python shows the value widened to a double ("0.10000000149011612"),
// the Go port its shortest float32 text ("0.1"), as Python's details pane
// does (fmt.shortest). An intended difference.
func f32RawCase(v golden.Value, raw bool) bool { return v.Kind == golden.KindF32 && raw }

func TestGoldenFormatValue(t *testing.T) {
	n, skipped := 0, 0
	for _, r := range loadFmt(t).Section(t, "format_value") {
		gv := r.Value(t, "v")
		v := toValue(t, gv)
		k := Kind(r.String(t, "kind"))
		var cases [][]json.RawMessage
		r.Decode(t, "cases", &cases)
		for _, c := range cases {
			var raw bool
			var width int
			json.Unmarshal(c[0], &raw)
			json.Unmarshal(c[1], &width)
			o, err := golden.DecodeOverride(c[2])
			if err != nil {
				t.Fatal(err)
			}
			var want string
			if err := json.Unmarshal(c[3], &want); err != nil {
				t.Fatalf("%s: an error case: %s", r.ID(), c[3])
			}
			got := Format(v, k, Opts{Raw: raw, Width: width, Override: toOverride(o)})
			n++
			if f32RawCase(gv, raw) {
				f := float64(gv.Float32)
				if got != pyRepr(f, 32) {
					t.Errorf("%s: float32 raw = %q", r.ID(), got)
				}
				skipped++
				continue
			}
			if got != want {
				t.Errorf("%s %s kind=%s raw=%v width=%d override=%v: got %q, want %q", r.ID(), gv, k, raw, width, o, got, want)
			}
		}
	}
	t.Logf("%d cases, %d float32 raw cases checked against the shortest float32 text", n, skipped)
}

func TestGoldenFormatValueUnsafe(t *testing.T) {
	for _, r := range loadFmt(t).Section(t, "format_value_unsafe") {
		v := toValue(t, r.Value(t, "v"))
		got := Format(v, Kind(r.String(t, "kind")), Opts{Raw: r.Bool(t, "raw"), Width: r.Int(t, "width"), Unsafe: true})
		if want := r.String(t, "out"); got != want {
			t.Errorf("%s: got %q, want %q", r.ID(), got, want)
		}
	}
}

func TestGoldenDerived(t *testing.T) {
	for _, r := range loadFmt(t).Section(t, "derived") {
		v := toValue(t, r.Value(t, "v"))
		got := Derived(r.String(t, "name"), Kind(r.String(t, "kind")), v, r.String(t, "unit"))
		if want := r.String(t, "out"); got != want {
			t.Errorf("%s: Derived(%q, %s, %v, %q) = %q, want %q", r.ID(), r.String(t, "name"), r.String(t, "kind"), r.Value(t, "v"), r.String(t, "unit"), got, want)
		}
	}
}

func TestGoldenConversions(t *testing.T) {
	f := loadFmt(t)
	check := func(sec string, fn func(r golden.Record) string) {
		for _, r := range f.Section(t, sec) {
			want := ""
			if e, ok := r.Err(); ok {
				want = "error: " + e
			} else {
				want = r.String(t, "out")
			}
			if got := fn(r); got != want {
				t.Errorf("%s: v=%v got %q, want %q", r.ID(), r.Value(t, "v"), got, want)
			}
		}
	}
	check("mjd_to_iso", func(r golden.Record) string { return MJDToISO(r.Value(t, "v").Float) })
	check("deg_to_hms", func(r golden.Record) string { return DegToHMS(r.Value(t, "v").Float) })
	check("deg_to_dms", func(r golden.Record) string { return DegToDMS(r.Value(t, "v").Float, r.Bool(t, "plus")) })
}

func TestGoldenOverrides(t *testing.T) {
	f := loadFmt(t)
	for _, r := range f.Section(t, "step_override") {
		got := StepOverride(toOverride(r.Override(t, "override")), Kind(r.String(t, "kind")), r.Int(t, "delta"))
		if want := toOverride(r.Override(t, "out")); got != want {
			t.Errorf("%s: got %+v, want %+v", r.ID(), got, want)
		}
	}
	for _, r := range f.Section(t, "describe_override") {
		got := DescribeOverride(toOverride(r.Override(t, "override")), Kind(r.String(t, "kind")))
		if want := r.String(t, "out"); got != want {
			t.Errorf("%s: got %q, want %q", r.ID(), got, want)
		}
	}
	for _, r := range f.Section(t, "override_error") {
		var kind string
		if !r.IsNull("kind") {
			kind = r.String(t, "kind")
		}
		var sample data.Value
		if !r.IsNull("sample") {
			sample = toValue(t, r.Value(t, "sample"))
		}
		if v, ok := r.Override(t, "value").(string); ok && v == "" {
			continue // an empty spec: Override can't hold one (ParseOverride makes it automatic)
		}
		got := OverrideError(toOverride(r.Override(t, "value")), Kind(kind), sample)
		want := ""
		if !r.IsNull("out") {
			want = r.String(t, "out")
		}
		if got != want {
			t.Errorf("%s: OverrideError(%v, %q, %v) = %q, want %q", r.ID(), r.Override(t, "value"), kind, sample, got, want)
		}
	}
	for _, r := range f.Section(t, "default_digits") {
		want := 0
		if !r.IsNull("out") {
			want = r.Int(t, "out")
		}
		if got := DefaultDigits(Kind(r.String(t, "kind"))); got != want {
			t.Errorf("%s: got %d, want %d", r.ID(), got, want)
		}
	}
}

func TestGoldenHuman(t *testing.T) {
	f := loadFmt(t)
	for _, r := range f.Section(t, "percent") {
		if got, want := Percent(r.Float(t, "part"), r.Float(t, "whole")), r.String(t, "out"); got != want {
			t.Errorf("%s: got %q, want %q", r.ID(), got, want)
		}
	}
	num := func(r golden.Record) float64 {
		if r.IsNull("n") {
			return nan
		}
		return r.Float(t, "n")
	}
	for _, r := range f.Section(t, "human_count") {
		if got, want := HumanCount(num(r)), r.String(t, "out"); got != want {
			t.Errorf("%s: got %q, want %q", r.ID(), got, want)
		}
	}
	for _, r := range f.Section(t, "human_bytes") {
		if got, want := HumanBytes(num(r)), r.String(t, "out"); got != want {
			t.Errorf("%s: got %q, want %q", r.ID(), got, want)
		}
	}
}

func TestGoldenSanitize(t *testing.T) {
	for _, r := range loadFmt(t).Section(t, "sanitize") {
		s, keep := r.String(t, "s"), r.Bool(t, "keep_ws")
		if got, want := Sanitize(s, keep), r.String(t, "out"); got != want {
			t.Errorf("%s: Sanitize(%q, %v) = %q, want %q", r.ID(), s, keep, got, want)
		}
		if got, want := HasControls(s, keep), r.Bool(t, "has_controls"); got != want {
			t.Errorf("%s: HasControls(%q, %v) = %v, want %v", r.ID(), s, keep, got, want)
		}
	}
}

func TestGoldenCellFormatter(t *testing.T) {
	type gcase struct {
		V       golden.Value `json:"v"`
		Raw     bool         `json:"raw"`
		Plain   string       `json:"plain"`
		Style   string       `json:"style"`
		Justify string       `json:"justify"`
		Error   string       `json:"error"`
	}
	for _, r := range loadFmt(t).Section(t, "cell_formatter") {
		typ := parseType(t, r.String(t, "type"))
		k := KindFor(r.String(t, "name"), typ, r.String(t, "unit"))
		if want := r.String(t, "kind"); string(k) != want {
			t.Errorf("%s: kind %q, want %q", r.ID(), k, want)
		}
		if want := r.Bool(t, "right"); RightJustified(k) != want {
			t.Errorf("%s: right %v, want %v", r.ID(), !want, want)
		}
		o := toOverride(r.Override(t, "override"))
		var cases []gcase
		r.Decode(t, "cases", &cases)
		for _, c := range cases {
			if c.Error != "" {
				t.Errorf("%s: an error case: %s", r.ID(), c.Error)
				continue
			}
			got := Cell(toValue(t, c.V), k, Opts{Raw: c.Raw, Width: DefaultWidth, Override: o})
			if f32RawCase(c.V, c.Raw) {
				continue
			}
			style := ""
			switch {
			case got.Style.Bold:
				style = "bold"
			case got.Style.Dim:
				style = "dim"
			}
			justify := map[int]string{0: "left", 1: "right", 2: "center"}[int(got.Justify)]
			if got.Plain != c.Plain || style != c.Style || justify != c.Justify {
				t.Errorf("%s %s %v raw=%v: got %q %q %q, want %q %q %q", r.ID(), r.String(t, "name"), c.V, c.Raw,
					got.Plain, style, justify, c.Plain, c.Style, c.Justify)
			}
		}
	}
}
