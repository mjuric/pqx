package footer

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// PrettyJSON is s pretty-printed as Python's json.dumps(json.loads(s),
// indent=2, ensure_ascii=False) prints it (Rich's JSON, which the Metadata
// tab uses): keys in file order (a repeated key keeps its first place and
// its last value), integers exact, floats as Python's repr. ok is false if
// s isn't one JSON value. Python also reads NaN and Infinity; this doesn't.
func PrettyJSON(s string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := parseJSON(dec)
	if err != nil {
		return "", false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", false
	}
	var b strings.Builder
	writeJSON(&b, v, 0)
	return b.String(), true
}

type jsonObject struct {
	keys []string
	vals map[string]any
}

func parseJSON(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &jsonObject{vals: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := parseJSON(dec)
				if err != nil {
					return nil, err
				}
				if _, ok := o.vals[k]; !ok {
					o.keys = append(o.keys, k)
				}
				o.vals[k] = v
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			l := []any{}
			for dec.More() {
				v, err := parseJSON(dec)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return l, nil
		}
		return nil, errors.New("unexpected delimiter")
	}
	return tok, nil
}

func writeJSON(b *strings.Builder, v any, level int) {
	ind := func(n int) string { return strings.Repeat("  ", n) }
	switch t := v.(type) {
	case *jsonObject:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.keys {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(ind(level + 1))
			writeString(b, k)
			b.WriteString(": ")
			writeJSON(b, t.vals[k], level+1)
		}
		b.WriteString("\n" + ind(level) + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, x := range t {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(ind(level + 1))
			writeJSON(b, x, level+1)
		}
		b.WriteString("\n" + ind(level) + "]")
	case string:
		writeString(b, t)
	case json.Number:
		b.WriteString(pyNumber(string(t)))
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case nil:
		b.WriteString("null")
	}
}

// writeString writes s quoted as json.dumps(ensure_ascii=False) does.
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteString(strconv.FormatInt(int64(r)>>4, 16))
				b.WriteString(strconv.FormatInt(int64(r)&15, 16))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// pyNumber is a JSON number as Python prints what json.loads makes of it:
// an int exactly, a float as its repr.
func pyNumber(s string) string {
	if !strings.ContainsAny(s, ".eE") {
		if n, ok := new(big.Int).SetString(s, 10); ok {
			return n.String()
		}
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !math.IsInf(f, 0) {
		return s
	}
	return PyRepr(f)
}

// PyRepr is Python's repr of a float (json.dumps writes Infinity and NaN
// for the non-finite ones).
func PyRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // shortest: [-]d[.ddd]e±XX
	neg := strings.HasPrefix(e, "-")
	e = strings.TrimPrefix(e, "-")
	mant, exps, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(exps)
	digits := strings.Replace(mant, ".", "", 1)
	var out string
	if exp >= -4 && exp < 16 {
		// fixed notation
		if exp >= 0 {
			if len(digits) <= exp+1 {
				out = digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
			} else {
				out = digits[:exp+1] + "." + digits[exp+1:]
			}
		} else {
			out = "0." + strings.Repeat("0", -exp-1) + digits
		}
	} else {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		sign := "+"
		if exp < 0 {
			sign, exp = "-", -exp
		}
		es := strconv.Itoa(exp)
		if len(es) < 2 {
			es = "0" + es
		}
		out = m + "e" + sign + es
	}
	if neg {
		out = "-" + out
	}
	return out
}
