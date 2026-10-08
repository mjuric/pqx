package filter

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/apache/arrow-go/v18/arrow"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
)

// The filter box's hint has an example drawn from the file's own first row
// (Python pqx's filter_placeholder): the first numeric column with a plain
// name ("> its value") and the first string column ("= its value").

// hintCandidates is how many columns of each kind the hint reads.
const hintCandidates = 8

// placeholderStrMax is the longest string value the example quotes.
const placeholderStrMax = 20

var plainIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// keywords are SQL words a column name can't stand for bare in the example
// (Python asks DuckDB for its keyword list).
var keywords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`all and any array as asc between both by case cast check collate column
		constraint create default desc distinct do else end except false fetch for foreign from grant group having in
		initially intersect into is isnull join lateral leading like limit not notnull null offset on only or order
		placing primary references returning select some symmetric table then to trailing true union unique using
		variadic when where window with`) {
		keywords[w] = true
	}
}

func isPlainIdent(s string) bool { return plainIdent.MatchString(s) && !keywords[strings.ToLower(s)] }

type hinted struct{ hint string }

// startHint reads the first row of the candidate columns ("hint").
func (f *Filter) startHint() tea.Cmd {
	ds := f.env.DS
	lower := map[string]int{}
	for _, c := range ds.Columns() {
		lower[strings.ToLower(c.Name)]++
	}
	var cols []data.Column
	nums, strs := 0, 0
	for _, c := range ds.Columns() {
		if lower[strings.ToLower(c.Name)] != 1 || !isPlainIdent(c.Name) || c.Arrow == nil {
			continue
		}
		id := c.Arrow.ID()
		switch {
		case (arrow.IsInteger(id) || arrow.IsFloating(id)) && nums < hintCandidates:
			nums++
			cols = append(cols, c)
		case isString(c.Arrow) && strs < hintCandidates:
			strs++
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 || ds.NumRows() == 0 {
		return nil
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return f.env.Tasks.Run("hint", "", false, func(ctx context.Context) tea.Msg {
		w, err := ds.Fetch(ctx, data.View{}, 0, 1, names)
		if err != nil || w.Len == 0 {
			return hinted{}
		}
		row := make([]data.Value, len(cols))
		for i, n := range names {
			if v := w.Cols[n]; len(v) > 0 {
				row[i] = v[0]
			}
		}
		return hinted{hint: Placeholder(cols, row)}
	})
}

// Placeholder is the hint for columns cols whose first row is row.
func Placeholder(cols []data.Column, row []data.Value) string {
	var num, text string
	for i, c := range cols {
		if i >= len(row) || row[i] == nil || !isPlainIdent(c.Name) || c.Arrow == nil {
			continue
		}
		id := c.Arrow.ID()
		if num == "" && (arrow.IsInteger(id) || arrow.IsFloating(id)) {
			if lit, ok := shortNumber(row[i]); ok {
				num = c.Name + " > " + lit
			}
		} else if text == "" && isString(c.Arrow) {
			if s, ok := row[i].(string); ok {
				val := []rune(fmtx.Sanitize(strings.TrimSpace(s), false))
				if len(val) > placeholderStrMax {
					val = val[:placeholderStrMax]
				}
				if v := strings.TrimSpace(string(val)); v != "" {
					text = c.Name + " = '" + strings.ReplaceAll(v, "'", "''") + "'"
				}
			}
		}
		if num != "" && text != "" {
			break
		}
	}
	ex := example
	switch {
	case num != "" && text != "":
		ex = num + " and " + text
	case num != "":
		ex = num
	case text != "":
		ex = text
	}
	return "SQL WHERE expression, e.g. " + ex + " — or a full query: select … from t"
}

// shortNumber is v as a short SQL number literal for an example (Python's
// _short_number).
func shortNumber(v data.Value) (string, bool) {
	var f float64
	switch v := v.(type) {
	case int64:
		s := strconv.FormatInt(v, 10)
		return s, len(s) <= 12
	case uint64:
		s := strconv.FormatUint(v, 10)
		return s, len(s) <= 12
	case float64:
		f = v
	case float32:
		f = float64(v)
	default:
		return "", false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", false
	}
	s := pyG3(f)
	if strings.Contains(s, "e") && math.Abs(f) >= 1 && math.Abs(f) < 1e12 {
		s = strconv.FormatFloat(math.RoundToEven(f), 'f', 0, 64)
	}
	return s, true
}

// pyG3 is Python's f"{v:.3g}".
func pyG3(f float64) string {
	s := strconv.FormatFloat(f, 'g', 3, 64)
	if i := strings.IndexByte(s, 'e'); i >= 0 {
		mant, exp := s[:i], s[i+1:]
		sign := exp[0]
		digits := strings.TrimLeft(exp[1:], "0")
		for len(digits) < 2 {
			digits = "0" + digits
		}
		return mant + "e" + string(sign) + digits
	}
	return s
}

// isString reports whether t is text (dictionary-encoded too, as DuckDB
// reads it).
func isString(t arrow.DataType) bool {
	if d, ok := t.(*arrow.DictionaryType); ok {
		t = d.ValueType
	}
	switch t.ID() {
	case arrow.STRING, arrow.LARGE_STRING, arrow.STRING_VIEW:
		return true
	}
	return false
}
