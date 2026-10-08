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

func parseUUID(s string) (UUID, error) {
	var u UUID
	h := strings.ReplaceAll(s, "-", "")
	if len(h) != 32 {
		return u, fmt.Errorf("not a UUID: %q", s)
	}
	_, err := hex.Decode(u[:], []byte(h))
	return u, err
}
