// Package fmtx is the port of Python pqx's fmt.py: formatting kinds, value
// text for the grid and the details pane, format overrides (digits and
// Python format specs), derived readings (MJD → UTC, RA → h:m:s, …),
// sanitizing, and human-readable numbers. Part of the contract
// (docs/design/go-port.md); WP3 implements it, with Python's outputs
// (go/testdata/golden/fmt.json) as the spec.
package fmtx

import (
	"math"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/styled"
)

// Kind is how a column's values are formatted (fmt.py's kinds).
type Kind string

const (
	KindInt     Kind = "int"
	KindFloat   Kind = "float"
	KindFloat32 Kind = "float32"
	KindErr     Kind = "err"
	KindMJD     Kind = "mjd"
	KindAngle   Kind = "angle"
	KindMag     Kind = "mag"
	KindFlux    Kind = "flux"
	KindBool    Kind = "bool"
	KindTime    Kind = "time"
	KindBinary  Kind = "binary"
	KindNested  Kind = "nested"
	KindStr     Kind = "str"
)

// Null is the text of SQL NULL.
const Null = "∅"

// MaxDigits is the most digits an override can ask for.
const MaxDigits = 17

// Override is a user's format for a column: a number of digits, or a
// Python format spec (".2f", ",d", ".1%", ">12", or strftime for times).
// The zero Override is automatic.
type Override struct {
	Digits int    // used when Spec is "" and Set
	Spec   string // a Python format spec; wins over Digits
	Set    bool
}

// Opts are format options.
type Opts struct {
	Raw      bool // full precision, no smart formatting ("f", details pane)
	Width    int  // cut text longer than this with "…"; 0 for no limit
	Override Override
	// Unsafe leaves control characters in text (for the clipboard, which
	// sanitizes afterwards); everything shown on screen is safe.
	Unsafe bool
}

// DefaultWidth is format_value's default width.
const DefaultWidth = 40

// KindFor is the kind of a column from its name, Arrow type and unit
// (fmt.kind_for).
func KindFor(name string, t arrow.DataType, unit string) Kind { return kindFor(name, t, unit) }

// Format is one value's text (fmt.format_value), sanitized unless
// o.Unsafe.
func Format(v data.Value, k Kind, o Opts) string {
	return formatValue(v, k, o.Raw, o.Width, o.Override, !o.Unsafe)
}

// RightJustified reports whether a kind's cells sit right (numbers).
func RightJustified(k Kind) bool {
	switch k {
	case KindMJD, KindAngle, KindMag, KindFlux, KindErr, KindFloat, KindFloat32, KindInt:
		return true
	}
	return false
}

// Cell is a grid cell's styled text (fmt.CellFormatter.__call__): NULL,
// NaN and ±∞ dim, true bold and false dim (centred), numbers right.
func Cell(v data.Value, k Kind, o Opts) styled.Text {
	t := styled.Text{Plain: Format(v, k, o)}
	if RightJustified(k) {
		t.Justify = styled.Right
	}
	switch x := v.(type) {
	case nil:
		t.Style.Dim = true
		return t
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			t.Style.Dim = true
			return t
		}
	case float32:
		if f := float64(x); math.IsNaN(f) || math.IsInf(f, 0) {
			t.Style.Dim = true
			return t
		}
	}
	if k == KindBool {
		t.Justify = styled.Center
		if truthy(v) {
			t.Style.Bold = true
		} else {
			t.Style.Dim = true
		}
	}
	return t
}

// truthy is Python's bool(v).
func truthy(v data.Value) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case uint64:
		return x != 0
	case float64:
		return x != 0
	case float32:
		return x != 0
	case string:
		return x != ""
	case []byte:
		return len(x) > 0
	case data.List:
		return len(x) > 0
	case data.Struct:
		return len(x) > 0
	case data.Map:
		return len(x) > 0
	case data.Decimal:
		return x.Unscaled != nil && x.Unscaled.Sign() != 0
	case data.Duration:
		return x != 0
	case data.Interval:
		return x != data.Interval{}
	}
	return true
}

// Derived is the reading shown below a value in the details pane
// ("· 2026-01-02 03:04:05 UTC", h:m:s, d:m:s, mas, AB mag), "" if none
// (fmt.derived).
func Derived(name string, k Kind, v data.Value, unit string) string { return derived(name, k, v, unit) }

// MJDToISO, DegToHMS and DegToDMS are fmt.py's conversions.
func MJDToISO(mjd float64) string            { return mjdToISO(mjd) }
func DegToHMS(deg float64) string            { return degToHMS(deg) }
func DegToDMS(deg float64, plus bool) string { return degToDMS(deg, plus) }

// DefaultDigits is the digits a kind shows automatically (decimals for
// mjd, angle and mag; significant digits for the others), or 0 if digits
// don't apply.
func DefaultDigits(k Kind) int { return defaultDigits(k) }

// StepOverride is "<" (delta -1) or ">" (+1) applied to o.
func StepOverride(o Override, k Kind, delta int) Override { return stepOverride(o, k, delta) }

// DescribeOverride is o in words for the header and notices ("3 digits", ".2f").
// The spec is the user's text: it comes back sanitized (Python shows it raw).
func DescribeOverride(o Override, k Kind) string { return Sanitize(describeOverride(o, k), false) }

// OverrideError is why value can't be a format for kind (checked against
// sample, a value of the column, if not nil), or "" if it can. Kind ""
// is Python's kind=None: the spec only has to suit some column. Messages
// quote the spec; they come back sanitized (Python's are raw).
func OverrideError(o Override, k Kind, sample data.Value) string {
	return Sanitize(overrideError(o, k, sample), false)
}

// ParseOverride is the format dialog's and formats.yaml's text: "3" is 3
// digits, "" automatic, anything else a spec (config.parse_override).
func ParseOverride(text string) Override { return parseOverride(text) }

// Sanitize shows control, C1, bidi and zero-width characters as visible
// symbols (ESC as ␛), keeping tab and newline if keepWS (fmt.sanitize).
// Everything from the file goes through it before reaching the terminal.
func Sanitize(s string, keepWS bool) string {
	if keepWS {
		return data.SanitizeKeepWS(s)
	}
	return data.Sanitize(s)
}

// HasControls reports whether Sanitize would change s.
func HasControls(s string, keepWS bool) bool {
	if keepWS {
		return data.SanitizeKeepWS(s) != s
	}
	return data.HasControls(s)
}

// Percent is part/whole as a percentage that never reads 0% or 100% for a
// partial share.
func Percent(part, whole float64) string { return percent(part, whole) }

// HumanCount is n as 1.2k, 3.4M, 5.6B, 7.8T.
func HumanCount(n float64) string { return humanCount(n) }

// HumanBytes is n bytes as KiB, MiB, ….
func HumanBytes(n float64) string { return humanBytes(n) }

// ShortType is a short type name for headers (f64, i32, ts[us,UTC], dict<…>).
func ShortType(t arrow.DataType) string { return shortType(t) }
