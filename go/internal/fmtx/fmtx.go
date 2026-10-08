// Package fmtx is the port of Python pqx's fmt.py: formatting kinds, value
// text for the grid and the details pane, format overrides (digits and
// Python format specs), derived readings (MJD → UTC, RA → h:m:s, …),
// sanitizing, and human-readable numbers. Part of the contract
// (docs/design/go-port.md); WP3 implements it, with Python's outputs
// (go/testdata/golden/fmt.json) as the spec.
//
// The bodies here are starters so the rest of pqx builds: WP3 replaces them.
package fmtx

import (
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
func KindFor(name string, t arrow.DataType, unit string) Kind {
	switch {
	case t == nil:
		return KindStr
	case arrow.IsInteger(t.ID()):
		return KindInt
	case t.ID() == arrow.FLOAT32 || t.ID() == arrow.FLOAT16:
		return KindFloat32
	case arrow.IsFloating(t.ID()) || arrow.IsDecimal(t.ID()):
		return KindFloat
	case t.ID() == arrow.BOOL:
		return KindBool
	case t.ID() == arrow.TIMESTAMP || t.ID() == arrow.DATE32 || t.ID() == arrow.DATE64 || t.ID() == arrow.TIME32 || t.ID() == arrow.TIME64:
		return KindTime
	case t.ID() == arrow.BINARY || t.ID() == arrow.LARGE_BINARY || t.ID() == arrow.FIXED_SIZE_BINARY || t.ID() == arrow.BINARY_VIEW:
		return KindBinary
	case arrow.IsNested(t.ID()):
		return KindNested
	}
	return KindStr
}

// Format is one value's text (fmt.format_value), sanitized unless
// o.Unsafe. Starter: the prototype's formatting.
func Format(v data.Value, k Kind, o Opts) string {
	s := starterFormat(v)
	if !o.Unsafe {
		s = Sanitize(s, true)
	}
	if o.Width > 0 && !o.Raw {
		if r := []rune(s); len(r) > o.Width {
			s = string(r[:o.Width-1]) + "…"
		}
	}
	return s
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
	if v == nil {
		t.Style.Dim = true
	}
	return t
}

// Derived is the reading shown below a value in the details pane
// ("· 2026-01-02 03:04:05 UTC", h:m:s, d:m:s, mas, AB mag), "" if none
// (fmt.derived). Starter: none.
func Derived(name string, k Kind, v data.Value, unit string) string { return "" }

// MJDToISO, DegToHMS and DegToDMS are fmt.py's conversions.
func MJDToISO(mjd float64) string            { return "" }
func DegToHMS(deg float64) string            { return "" }
func DegToDMS(deg float64, plus bool) string { return "" }

// DefaultDigits is the digits a kind shows automatically (decimals for
// mjd, angle and mag; significant digits for the others), or 0 if digits
// don't apply.
func DefaultDigits(k Kind) int { return 0 }

// StepOverride is "<" (delta -1) or ">" (+1) applied to o.
func StepOverride(o Override, k Kind, delta int) Override { return o }

// DescribeOverride is o in words for the header and notices ("3 digits", ".2f").
func DescribeOverride(o Override, k Kind) string { return o.Spec }

// OverrideError is why value can't be a format for kind (checked against
// sample, a value of the column, if not nil), or "" if it can.
func OverrideError(o Override, k Kind, sample data.Value) string { return "" }

// ParseOverride is the format dialog's and formats.yaml's text: "3" is 3
// digits, "" automatic, anything else a spec (config.parse_override).
func ParseOverride(text string) Override {
	if text == "" {
		return Override{}
	}
	return Override{Spec: text, Set: true}
}

// Sanitize shows control, C1, bidi and zero-width characters as visible
// symbols (ESC as ␛), keeping tab and newline if keepWS (fmt.sanitize).
// Everything from the file goes through it before reaching the terminal.
func Sanitize(s string, keepWS bool) string { return data.Sanitize(s) }

// HasControls reports whether Sanitize would change s.
func HasControls(s string, keepWS bool) bool { return Sanitize(s, keepWS) != s }

// Percent is part/whole as a percentage that never reads 0% or 100% for a
// partial share.
func Percent(part, whole float64) string { return "" }

// HumanCount is n as 1.2k, 3.4M, 5.6B, 7.8T.
func HumanCount(n float64) string { return "" }

// HumanBytes is n bytes as KiB, MiB, ….
func HumanBytes(n float64) string { return "" }

// ShortType is a short type name for headers (f64, i32, ts[us,UTC], dict<…>).
func ShortType(t arrow.DataType) string {
	if t == nil {
		return ""
	}
	return t.String()
}
