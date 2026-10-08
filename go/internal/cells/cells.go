// Package cells is the port of Python pqx's cells.py helpers the grid
// needs: terminal widths of text and the values most likely to be the
// widest when formatted. Part of the contract (docs/design/go-port.md);
// WP3 implements it.
package cells

import (
	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
)

// Placeholder is the text of a cell not loaded yet; FailedMark of one that
// couldn't be loaded.
const (
	Placeholder = "…"
	FailedMark  = "✗"
)

// Width is the number of terminal cells s takes (East Asian wide
// characters 2, combining marks 0; cells.text_width). Starter: runes.
func Width(s string) int { return len([]rune(s)) }

// WidestCandidates are the few values of vals whose text is likely the
// widest for kind (cells.widest_candidates: numbers' extremes and the values
// nearest zero; the longest strings), or nil if kind can't be guessed this
// way and every value must be formatted. Starter: nil.
func WidestCandidates(vals []data.Value, k fmtx.Kind, raw bool, n int) []data.Value { return nil }
