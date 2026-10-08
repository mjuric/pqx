package fmtx

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mjuric/pqx/go/internal/data"
)

// MaxSpecNumber is the largest width or precision a format spec may ask
// for (fmt.MAX_SPEC_NUMBER: a typo like .1000000f would hang the grid).
const maxSpecNumber = 64

// specPrecision finds a spec's precision (fmt._SPEC_PRECISION; \d is any
// Unicode decimal digit in Python).
var specPrecision = regexp.MustCompile(`\.(\p{Nd}+)`)

// stdSpec is fmt._STD_SPEC, a standard format spec.
var stdSpec = regexp.MustCompile(`^(?s)(?:.?[<>=^])?[-+ ]?z?#?0?(\p{Nd}*)[,_]?(?:\.(\p{Nd}+))?[a-zA-Z%]?$`)

// ndInt is Python's int() of a run of decimal digits, saturating at a
// value larger than any limit pqx checks.
func ndInt(s string) int {
	n := 0
	for _, r := range s {
		d := decimalValue(r)
		if d < 0 {
			break
		}
		if n > 1e15 {
			return math.MaxInt32
		}
		n = n*10 + d
	}
	return n
}

func defaultDigits(k Kind) int {
	if d, ok := fixedDigits(k); ok {
		return d
	}
	d, _ := sigDigits(k)
	return d
}

func stepOverride(o Override, k Kind, delta int) Override {
	if o.Set && o.Spec != "" {
		if m := specPrecision.FindStringSubmatchIndex(o.Spec); m != nil {
			n := max(0, min(MaxDigits, ndInt(o.Spec[m[2]:m[3]])+delta))
			return Override{Spec: o.Spec[:m[2]] + strconv.Itoa(n) + o.Spec[m[3]:], Set: true}
		}
		o = Override{}
	}
	cur := o.Digits
	if !o.Set {
		cur = defaultDigits(k)
		if cur == 0 {
			return Override{}
		}
	}
	lo := 1
	if _, ok := fixedDigits(k); ok {
		lo = 0
	}
	return Override{Digits: max(lo, min(MaxDigits, cur+delta)), Set: true}
}

func describeOverride(o Override, k Kind) string {
	switch {
	case !o.Set:
		return ""
	case o.Spec != "":
		return o.Spec
	}
	if _, ok := fixedDigits(k); ok {
		return "." + strconv.Itoa(o.Digits) + "f"
	}
	return strconv.Itoa(o.Digits) + " sig"
}

// samples are fmt._SAMPLES: what a spec is tried on for a kind.
var sampleTime = data.Timestamp{T: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Unit: time.Microsecond}

func kindSample(k Kind) data.Value {
	switch k {
	case KindInt:
		return int64(1)
	case KindStr:
		return "abc"
	case KindTime:
		return sampleTime
	}
	return 1.5
}

func overrideError(o Override, k Kind, sample data.Value) string {
	if !o.Set {
		return ""
	}
	if o.Spec == "" {
		if k != "" && defaultDigits(k) == 0 {
			return "a digit count applies only to float columns; use a format spec such as ,d"
		}
		if o.Digits > MaxDigits {
			return fmt.Sprintf("at most %d digits", MaxDigits)
		}
		return ""
	}
	value := o.Spec
	if m := stdSpec.FindStringSubmatch(value); m != nil && (ndInt(m[1]) > maxSpecNumber || ndInt(m[2]) > maxSpecNumber) {
		return fmt.Sprintf("widths and precisions are limited to %d", maxSpecNumber)
	}
	if noSpec(k) {
		return string(k) + " columns can't take a format spec"
	}
	var samples []data.Value
	switch {
	case sample != nil:
		samples = []data.Value{sample}
	case k != "":
		samples = []data.Value{kindSample(k)}
	default:
		samples = []data.Value{1.5, int64(1), "abc", sampleTime}
	}
	var errs []string
	for _, v := range samples {
		switch v.(type) {
		case data.Timestamp, data.Date, data.TimeOfDay:
			if !strings.Contains(value, "%") {
				errs = append(errs, "timestamps take strftime codes, e.g. %Y-%m-%d %H:%M")
				continue
			}
		}
		if _, err := pyFormat(v, value); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		return ""
	}
	return errs[0]
}

func parseOverride(text string) Override {
	text = strings.TrimFunc(text, unicode.IsSpace)
	if text == "" {
		return Override{}
	}
	ascii := true
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			ascii = false
			break
		}
	}
	if ascii {
		n, err := strconv.Atoi(text)
		if err != nil {
			n = math.MaxInt
		}
		return Override{Digits: n, Set: true}
	}
	return Override{Spec: text, Set: true}
}

func percent(part, whole float64) string {
	switch {
	case whole == 0 || math.IsNaN(whole):
		return ""
	case part <= 0:
		return "0%"
	case part >= whole:
		return "100%"
	}
	p := 100 * part / whole
	switch {
	case p < 0.01:
		return "<0.01%"
	case p > 99.99:
		return ">99.99%"
	case p >= 99.5:
		return pyFloatString(p, 'f', 2, false, false, false) + "%"
	}
	return pyFloatString(p, 'g', 3, false, false, false) + "%"
}

func humanCount(n float64) string {
	if math.IsNaN(n) {
		return "?"
	}
	n = math.Trunc(n) + 0 // (+0: no "-0")
	for _, u := range []struct {
		div    float64
		suffix string
	}{{1e12, "T"}, {1e9, "B"}, {1e6, "M"}, {1e3, "k"}} {
		if math.Abs(n) >= u.div {
			return pyFloatString(n/u.div, 'g', 3, false, false, false) + u.suffix
		}
	}
	return strconv.FormatFloat(n, 'f', 0, 64)
}

func humanBytes(n float64) string {
	if math.IsNaN(n) {
		return "?"
	}
	for _, unit := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
		if math.Abs(n) < 1024 || unit == "TiB" {
			if unit == "B" {
				return pyFloatString(n, 'f', 0, false, false, false) + " B"
			}
			return pyFloatString(n, 'f', 1, false, false, false) + " " + unit
		}
		n /= 1024
	}
	return ""
}
