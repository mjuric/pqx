package filter

import (
	"regexp"
	"strings"

	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/sqllit"
)

// Completion of the identifier under the cursor (Python pqx's
// ColumnSuggester): a column name goes in as SQL needs it, bare if it is a
// plain identifier and not a keyword, else quoted, so a name from the file
// never becomes SQL of its own. Names with control characters aren't
// offered (no input box should hold those; "=" can still filter on such a
// column).

// sqlWords are the keywords and functions offered (Python's SQL_WORDS).
var sqlWords = []string{"and", "or", "not", "is", "null", "between", "in", "like", "ilike", "select", "from", "where",
	"group by", "order by", "limit", "count(*)", "avg(", "min(", "max(", "sum(", "distinct",
	"regexp_matches(", "abs(", "isnan(", "desc", "asc", "having"}

// wordSQL is a completion: what the typed word matches, and what goes in.
type wordSQL struct{ match, text string }

// lastIdent is the identifier the text ends with.
var lastIdent = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*$`)

// completions are the file's columns (by DuckDB's names) and the SQL
// words, made on first use.
func (f *Filter) completions() []wordSQL {
	if f.words == nil {
		f.words = []wordSQL{}
		for _, c := range f.env.DS.Columns() {
			name := c.SQLName
			if name == "" {
				name = c.Name
			}
			if !fmtx.HasControls(name, false) {
				f.words = append(f.words, wordSQL{name, sqllit.Ident(name)})
			}
		}
		for _, w := range sqlWords {
			f.words = append(f.words, wordSQL{w, w})
		}
	}
	return f.words
}

// suggest is the text completed: the identifier it ends with replaced by
// the first column name or word that starts with it (ignoring case) and is
// longer; "" if there is none.
func (f *Filter) suggest(value string) string {
	loc := lastIdent.FindStringIndex(value)
	if loc == nil {
		return ""
	}
	tok := value[loc[0]:]
	low := strings.ToLower(tok)
	n := len([]rune(tok))
	for _, w := range f.completions() {
		if strings.HasPrefix(strings.ToLower(w.match), low) && len([]rune(w.match)) > n {
			return value[:loc[0]] + w.text
		}
	}
	return ""
}
