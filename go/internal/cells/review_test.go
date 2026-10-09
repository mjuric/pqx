package cells

import (
	"testing"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
)

// At 4*k values or fewer, all of them, sorted; one more, and the 4*k picks.
func TestWidestCandidatesAtFourK(t *testing.T) {
	vals := []data.Value{5.0, -1.0, 3.0, -4.0, 2.0, 0.5, -0.25, 9.0}
	got := WidestCandidates(vals, fmtx.KindFloat, false, 2)
	if !sameValues(got, []data.Value{-4.0, -1.0, -0.25, 0.5, 2.0, 3.0, 5.0, 9.0}) {
		t.Errorf("8 values, k=2: %v", got)
	}
	got = WidestCandidates(append(vals, 7.0), fmtx.KindFloat, false, 2)
	if !sameValues(got, []data.Value{-4.0, -1.0, -1.0, -0.25, 0.5, 2.0, 7.0, 9.0}) {
		t.Errorf("9 values, k=2: %v", got)
	}
	strs := []data.Value{"a", "bbbb", "cc", "ddd", "e", "fffff", "gg", "h", "ii"}
	got = WidestCandidates(strs, fmtx.KindStr, false, 2)
	if !sameValues(got, []data.Value{"fffff", "bbbb"}) {
		t.Errorf("strings: %v", got)
	}
}

// The longest 4·k strings by length, then the widest by cells: a short
// string of wide characters beyond the first 2·k still wins.
func TestWidestCandidatesWideString(t *testing.T) {
	vals := []data.Value{"aaaaa", "bbbbb", "ccccc", "日日日", "d"}
	got := WidestCandidates(vals, fmtx.KindStr, false, 1)
	if !sameValues(got, []data.Value{"日日日"}) {
		t.Errorf("got %v", got)
	}
}
