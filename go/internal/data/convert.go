package data

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
)

// valueColumn is rows [lo, hi) of arr as Values, through conv (ValueAt if nil).
func valueColumn(arr arrow.Array, lo, hi int, conv func(arrow.Array, int) Value) []Value {
	if conv == nil {
		conv = ValueAt
	}
	out := make([]Value, hi-lo)
	for i := lo; i < hi; i++ {
		out[i-lo] = conv(arr, i)
	}
	return out
}

// duckValueFunc converts DuckDB's Arrow results for a column of DuckDB type
// duck where DuckDB's Arrow type loses the type: UUIDs come back as text.
// nil means ValueAt as is. (Top level only; duckCell handles any depth.)
func duckValueFunc(duck string) func(arrow.Array, int) Value {
	if duck != "UUID" {
		return nil
	}
	return func(arr arrow.Array, i int) Value {
		v := ValueAt(arr, i)
		if s, ok := v.(string); ok {
			if u, err := parseUUID(s); err == nil {
				return u
			}
		}
		return v
	}
}

func parseUUID(s string) (UUID, error) {
	var u UUID
	h := strings.ReplaceAll(s, "-", "")
	if len(h) != 32 {
		return u, fmt.Errorf("not a UUID: %q", s)
	}
	_, err := hex.Decode(u[:], []byte(h))
	return u, err
}
