package fmtx

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
)

// Struct field names reach ShortType from the file; the result is shown in
// headers, the Schema tab and Stats, so it must never carry control
// characters.
func TestShortTypeIsSafe(t *testing.T) {
	st := arrow.StructOf(arrow.Field{Name: "\x1b]0;PWN\x07f\u009b31m\u202e", Type: arrow.PrimitiveTypes.Int64})
	for _, typ := range []arrow.DataType{st, arrow.ListOf(st), arrow.MapOf(arrow.BinaryTypes.String, st)} {
		got := ShortType(typ)
		if HasControls(got, false) || got != Sanitize(got, false) {
			t.Fatalf("ShortType(%s) = %q", typ, got)
		}
	}
}
