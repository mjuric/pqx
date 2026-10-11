package dialogs

// A copy of internal/ui/rowspec.go (the prototype's, which WP7 is moving with
// the grid): the go-to dialog parses with it as Python's GotoScreen does.

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// parseRowSpec turns what the user typed after g into a 0-based row index of
// a view with total rows: "1234", "1_000" or "1,000", "1.5M", "2k", "3b"/"3g",
// "50%", or "-10" (counted from the end). The result is clamped to
// [0, total-1]. It is a port of parse_row_spec in pqx/data.py.
func parseRowSpec(text string, total int64) (int64, error) {
	s := strings.TrimSpace(text)
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "_", "")
	if s == "" {
		return 0, errors.New("empty row number")
	}
	last := max(total-1, 0)
	if strings.HasSuffix(s, "%") {
		p, err := parseFloat(s[:len(s)-1], text)
		if err != nil {
			return 0, err
		}
		// Python: max(0, min(total - 1, int(p / 100 * total)))
		v := p / 100.0 * float64(total)
		return clampRow(v, total-1), nil
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'k', 'K':
		mult = 1e3
	case 'm', 'M':
		mult = 1e6
	case 'b', 'B', 'g', 'G':
		mult = 1e9
	}
	if mult != 1 {
		s = s[:len(s)-1]
	}
	f, err := parseFloat(s, text)
	if err != nil {
		return 0, err
	}
	v := f * mult
	if v < 0 {
		v = float64(total) + math.Trunc(v)
	}
	return clampRow(v, last), nil
}

func parseFloat(s, orig string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("not a row number: %s", orig)
	}
	return f, nil
}

// clampRow truncates v toward zero (Python's int()) and clamps it to [0, hi].
func clampRow(v float64, hi int64) int64 {
	v = math.Trunc(v)
	if v <= 0 || hi <= 0 {
		return 0
	}
	if v >= float64(hi) {
		return hi
	}
	return int64(v)
}
