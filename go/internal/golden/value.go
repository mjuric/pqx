package golden

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Kinds of typed values, as in the "t" field of the encoding.
const (
	KindNull   = "null"
	KindInt    = "int"
	KindUint   = "uint"
	KindF64    = "f64"
	KindF32    = "f32"
	KindBool   = "bool"
	KindStr    = "str"
	KindBytes  = "bytes"
	KindTS     = "ts"
	KindDate   = "date"
	KindTime   = "time"
	KindDur    = "dur"
	KindDec    = "dec"
	KindUUID   = "uuid"
	KindList   = "list"
	KindStruct = "struct"
	KindMap    = "map"
)

// Kinds lists every kind of typed value.
var Kinds = []string{KindNull, KindInt, KindUint, KindF64, KindF32, KindBool, KindStr, KindBytes, KindTS,
	KindDate, KindTime, KindDur, KindDec, KindUUID, KindList, KindStruct, KindMap}

// Value is a decoded typed value. Kind says which fields are set:
//
//	int, uint     Int
//	f64           Float
//	f32           Float32, and Float (the same value widened)
//	bool          Bool
//	str, uuid     Str
//	bytes         Bytes
//	ts            Time (UTC; a naive timestamp also reads as UTC), Unit, TZ ("UTC" or "" for naive), Str (the text)
//	date          Time (midnight UTC), Str
//	time          Nanos (since midnight), Str
//	dur           Int (nanoseconds)
//	dec           Int (unscaled), Scale, Precision: the value is Int / 10^Scale
//	list          List
//	struct        Fields
//	map           Entries
type Value struct {
	Kind      string
	Int       *big.Int
	Float     float64
	Float32   float32
	Bool      bool
	Str       string
	Bytes     []byte
	Time      time.Time
	Unit      string
	TZ        string
	Nanos     int64
	Scale     int
	Precision int
	List      []Value
	Fields    []Field
	Entries   []Entry
}

// Field is one field of a struct value.
type Field struct {
	Name  string
	Value Value
}

// Entry is one entry of a map value.
type Entry struct {
	Key, Value Value
}

// IsNull reports whether v is NULL.
func (v Value) IsNull() bool { return v.Kind == KindNull }

// Int64 is an int, uint or dur value as an int64; ok is false for other kinds
// or if it doesn't fit.
func (v Value) Int64() (int64, bool) {
	if v.Int == nil || v.Kind == KindDec || !v.Int.IsInt64() {
		return 0, false
	}
	return v.Int.Int64(), true
}

// Uint64 is an int or uint value as a uint64; ok is false for other kinds or if
// it doesn't fit.
func (v Value) Uint64() (uint64, bool) {
	if v.Int == nil || v.Kind == KindDec || !v.Int.IsUint64() {
		return 0, false
	}
	return v.Int.Uint64(), true
}

// Duration is a dur value as a time.Duration.
func (v Value) Duration() (time.Duration, bool) {
	if v.Kind != KindDur {
		return 0, false
	}
	n, ok := v.Int64()
	return time.Duration(n), ok
}

// Rat is a dec value as an exact rational.
func (v Value) Rat() (*big.Rat, bool) {
	if v.Kind != KindDec {
		return nil, false
	}
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(v.Scale)), nil)
	return new(big.Rat).SetFrac(v.Int, den), true
}

// String is a short readable form, for test messages.
func (v Value) String() string {
	switch v.Kind {
	case KindNull:
		return "NULL"
	case KindInt, KindUint, KindDur:
		return v.Kind + ":" + v.Int.String()
	case KindF64:
		return "f64:" + strconv.FormatFloat(v.Float, 'g', -1, 64)
	case KindF32:
		return "f32:" + strconv.FormatFloat(float64(v.Float32), 'g', -1, 32)
	case KindBool:
		return strconv.FormatBool(v.Bool)
	case KindStr:
		return strconv.Quote(v.Str)
	case KindBytes:
		return "0x" + hex.EncodeToString(v.Bytes)
	case KindTS:
		tz := ""
		if v.TZ != "" {
			tz = " " + v.TZ
		}
		return fmt.Sprintf("ts[%s]:%s%s", v.Unit, v.Str, tz)
	case KindDate, KindTime, KindUUID:
		return v.Kind + ":" + v.Str
	case KindDec:
		return fmt.Sprintf("dec(%d,%d):%se-%d", v.Precision, v.Scale, v.Int, v.Scale)
	case KindList:
		parts := make([]string, len(v.List))
		for i, x := range v.List {
			parts[i] = x.String()
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case KindStruct:
		parts := make([]string, len(v.Fields))
		for i, f := range v.Fields {
			parts[i] = strconv.Quote(f.Name) + ": " + f.Value.String()
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case KindMap:
		parts := make([]string, len(v.Entries))
		for i, e := range v.Entries {
			parts[i] = e.Key.String() + " => " + e.Value.String()
		}
		return "map{" + strings.Join(parts, ", ") + "}"
	}
	return "?" + v.Kind
}

type rawValue struct {
	T         string          `json:"t"`
	V         json.RawMessage `json:"v"`
	Unit      *string         `json:"unit"`
	TZ        *string         `json:"tz"`
	Scale     *int            `json:"scale"`
	Precision *int            `json:"precision"`
}

const (
	tsLayout   = "2006-01-02T15:04:05.000000000"
	dateLayout = "2006-01-02"
)

// UnmarshalJSON decodes the typed value encoding (testdata/golden/README.md).
func (v *Value) UnmarshalJSON(b []byte) error {
	var r rawValue
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return fmt.Errorf("typed value %s: %w", b, err)
	}
	out, err := decodeRaw(r)
	if err != nil {
		return fmt.Errorf("typed value %.200s: %w", b, err)
	}
	*v = out
	return nil
}

func decodeRaw(r rawValue) (Value, error) {
	v := Value{Kind: r.T}
	str := func() (string, error) {
		var s string
		err := json.Unmarshal(r.V, &s)
		return s, err
	}
	switch r.T {
	case KindNull:
		if len(r.V) != 0 {
			return v, fmt.Errorf("null with a value")
		}
		return v, nil
	case KindInt, KindUint, KindDur:
		s, err := str()
		if err != nil {
			return v, err
		}
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return v, fmt.Errorf("bad integer %q", s)
		}
		if r.T == KindUint && n.Sign() < 0 {
			return v, fmt.Errorf("negative uint %q", s)
		}
		v.Int = n
	case KindF64, KindF32:
		s, err := str()
		if err != nil {
			return v, err
		}
		x, err := parseFloat(s)
		if err != nil {
			return v, err
		}
		v.Float = x
		if r.T == KindF32 {
			v.Float32 = float32(x)
			if float64(v.Float32) != x && !math.IsNaN(x) {
				return v, fmt.Errorf("f32 %q is not a float32 value", s)
			}
		}
	case KindBool:
		if err := json.Unmarshal(r.V, &v.Bool); err != nil {
			return v, err
		}
	case KindStr, KindUUID:
		s, err := str()
		if err != nil {
			return v, err
		}
		v.Str = s
		if r.T == KindUUID && (len(s) != 36 || strings.Count(s, "-") != 4) {
			return v, fmt.Errorf("bad uuid %q", s)
		}
	case KindBytes:
		s, err := str()
		if err != nil {
			return v, err
		}
		if v.Bytes, err = hex.DecodeString(s); err != nil {
			return v, err
		}
		if v.Bytes == nil {
			v.Bytes = []byte{}
		}
	case KindTS:
		s, err := str()
		if err != nil {
			return v, err
		}
		if v.Time, err = time.ParseInLocation(tsLayout, s, time.UTC); err != nil {
			return v, err
		}
		v.Str = s
		if r.Unit == nil {
			return v, fmt.Errorf("ts without unit")
		}
		switch *r.Unit {
		case "s", "ms", "us", "ns":
			v.Unit = *r.Unit
		default:
			return v, fmt.Errorf("ts with unit %q", *r.Unit)
		}
		if r.TZ != nil {
			if *r.TZ != "UTC" {
				return v, fmt.Errorf("ts with tz %q", *r.TZ)
			}
			v.TZ = *r.TZ
		}
	case KindDate:
		s, err := str()
		if err != nil {
			return v, err
		}
		if v.Time, err = time.ParseInLocation(dateLayout, s, time.UTC); err != nil {
			return v, err
		}
		v.Str = s
	case KindTime:
		s, err := str()
		if err != nil {
			return v, err
		}
		n, err := parseTimeOfDay(s)
		if err != nil {
			return v, err
		}
		v.Str, v.Nanos = s, n
	case KindDec:
		s, err := str()
		if err != nil {
			return v, err
		}
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return v, fmt.Errorf("bad decimal %q", s)
		}
		if r.Scale == nil || r.Precision == nil {
			return v, fmt.Errorf("dec without scale or precision")
		}
		v.Int, v.Scale, v.Precision = n, *r.Scale, *r.Precision
	case KindList:
		if err := json.Unmarshal(r.V, &v.List); err != nil {
			return v, err
		}
		if v.List == nil {
			v.List = []Value{}
		}
	case KindStruct:
		var pairs [][2]json.RawMessage
		if err := json.Unmarshal(r.V, &pairs); err != nil {
			return v, err
		}
		v.Fields = make([]Field, len(pairs))
		for i, p := range pairs {
			if err := json.Unmarshal(p[0], &v.Fields[i].Name); err != nil {
				return v, err
			}
			if err := json.Unmarshal(p[1], &v.Fields[i].Value); err != nil {
				return v, err
			}
		}
	case KindMap:
		var pairs [][2]Value
		if err := json.Unmarshal(r.V, &pairs); err != nil {
			return v, err
		}
		v.Entries = make([]Entry, len(pairs))
		for i, p := range pairs {
			v.Entries[i] = Entry{Key: p[0], Value: p[1]}
		}
	default:
		return v, fmt.Errorf("unknown kind %q", r.T)
	}
	return v, nil
}

// parseFloat reads Python's repr of a float: nan, inf, -inf, or a number.
func parseFloat(s string) (float64, error) {
	switch s {
	case "nan":
		return math.NaN(), nil
	case "inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	}
	return strconv.ParseFloat(s, 64)
}

// parseTimeOfDay reads HH:MM:SS.fffffffff into nanoseconds since midnight.
func parseTimeOfDay(s string) (int64, error) {
	if len(s) != 18 || s[2] != ':' || s[5] != ':' || s[8] != '.' {
		return 0, fmt.Errorf("bad time %q", s)
	}
	var parts [4]int64
	for i, f := range []string{s[0:2], s[3:5], s[6:8], s[9:]} {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bad time %q", s)
		}
		parts[i] = n
	}
	if parts[0] > 23 || parts[1] > 59 || parts[2] > 59 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	return ((parts[0]*60+parts[1])*60+parts[2])*1e9 + parts[3], nil
}
