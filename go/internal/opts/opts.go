// Package opts is pqx's command line: the options of Python pqx's cli.py,
// with the same names, defaults, environment variables, exclusions, errors
// and help text (argparse's, reproduced), parsed into Options for the app.
package opts

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/theme"
)

// Author and Homepage are the credits in --version and --help.
const (
	Author   = "Mario Juric"
	Homepage = "https://github.com/mjuric/pqx"
)

// Options are the parsed command line, with the environment applied.
type Options struct {
	Path  string // the file to open
	Where string // -w: a WHERE expression or a full query; "" for none
	// The look, resolved as Python pqx resolves them: the option, else the
	// environment variable, else the default; an invalid value from the
	// environment is the default.
	Accent string // one of theme.Accents (--accent, PQX_ACCENT)
	Dim    string // one of theme.DimModes (--dim, PQX_DIM)
	Border string // "ansi_NAME" or "#hex" (--border, PQX_BORDER)
	Theme  string // "" for the terminal's palette, or one of theme.Names
	// Sample is --sample (true), --no-sample (false), or nil: sample above
	// the size limits.
	Sample *bool
	// Threads is DuckDB's thread count; 0 for all cores. Negative values are
	// passed on (opening fails, as in Python pqx).
	Threads int
	// Formats are the --format overrides by column name.
	Formats map[string]fmtx.Override
}

// ErrHelp and ErrVersion are returned by Parse for -h/--help and --version:
// print Help or VersionText to stdout and exit 0.
var (
	ErrHelp    = errors.New("help requested")
	ErrVersion = errors.New("version requested")
)

// UsageError is a command-line error: print Usage to stderr, then
// "pqx: error: " and the message, and exit 2 (argparse's p.error).
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

func usageErr(format string, a ...any) error {
	return &UsageError{fmtx.Sanitize(fmt.Sprintf(format, a...), false)}
}

// option is one option of the parser, in --help order.
type option struct {
	short, long string   // "-w", "--where"; short may be ""
	metavar     string   // for options with a value; "" for flags
	choices     []string // the allowed values, if limited
	help        string
}

var options = []option{
	{short: "-h", long: "--help", help: "show this help message and exit"},
	{short: "-w", long: "--where", metavar: "WHERE",
		help: "initial filter: a SQL WHERE expression, or a full 'select … from t' query"},
	{long: "--accent", choices: theme.Accents,
		help: "focus colour, from the terminal's palette (default: blue; env PQX_ACCENT)"},
	{long: "--dim", choices: theme.DimModes,
		help: "how secondary text is dimmed: the faint attribute (default) or ANSI bright black " +
			"(env PQX_DIM)"},
	{long: "--border", metavar: "BORDER",
		help: "colour of unfocused panel borders, an ANSI name such as bright_black (default) or white " +
			"(env PQX_BORDER)"},
	{long: "--theme", metavar: "THEME", help: "use a Textual theme instead of the terminal's own colours"},
	{long: "--sample", help: "sample rows for stats/plots (default: on above 200M rows or 8 GiB)"},
	{long: "--no-sample", help: "always scan every row for stats/plots"},
	{long: "--threads", metavar: "THREADS",
		help: "DuckDB worker threads (default: all cores; lower it on shared machines)"},
	{long: "--format", metavar: "COL=SPEC",
		help: "display format for a column this session: a Python spec (.3f, .2e, ,d) or a number " +
			"of digits; repeatable. Formats set in the grid (< > F) are saved to " +
			"$XDG_CONFIG_HOME/pqx/formats.yaml (default ~/.config/pqx/formats.yaml)"},
	{long: "--version", help: "show the version and licence, and exit"},
}

// takesValue reports whether the option has a value.
func (o *option) takesValue() bool { return o.metavar != "" || o.choices != nil }

// name is the option as argparse names it in errors ("-w/--where").
func (o *option) name() string {
	if o.short != "" {
		return o.short + "/" + o.long
	}
	return o.long
}

// lookup finds an option by its exact name.
func lookup(s string) *option {
	for i := range options {
		if options[i].short == s || options[i].long == s {
			return &options[i]
		}
	}
	return nil
}

// optionStrings are the option strings in the order argparse registers
// them (-h, --help, -w, --where, --accent, …), the order its errors list
// them in.
func optionStrings() []string {
	var names []string
	for _, o := range options {
		if o.short != "" {
			names = append(names, o.short)
		}
		names = append(names, o.long)
	}
	return names
}

// tuple is one reading of an option argument (argparse's option tuple).
type tuple struct {
	opt      *option // nil for an unknown option
	name     string  // the option string matched
	sep      bool    // the value was attached with "="
	explicit *string // the value attached to it, if any
}

// match is argparse's _parse_optional: the readings of an argument, or nil
// for a positional (it doesn't start with "-", is "-" alone, looks like a
// negative number, or has a space in it). More than one reading is an
// ambiguous abbreviation; one with opt nil is an unknown option.
func match(s string) []tuple {
	if len(s) < 2 || s[0] != '-' {
		return nil
	}
	if o := lookup(s); o != nil {
		return []tuple{{opt: o, name: s}}
	}
	if name, v, ok := strings.Cut(s, "="); ok {
		if o := lookup(name); o != nil {
			return []tuple{{o, name, true, &v}}
		}
	}
	var found []tuple
	prefix, v, eq := strings.Cut(s, "=")
	var explicit *string
	if eq {
		explicit = &v
	}
	for _, name := range optionStrings() {
		switch {
		case s[1] != '-' && name == s[:2]: // -wVALUE
			rest := s[2:]
			found = append(found, tuple{lookup(name), name, false, &rest})
		case (s[1] == '-') == strings.HasPrefix(name, "--") && strings.HasPrefix(name, prefix):
			found = append(found, tuple{lookup(name), name, eq, explicit})
		case s[1] != '-' && strings.HasPrefix(name, prefix): // "-" alone before "="
			found = append(found, tuple{lookup(name), name, eq, explicit})
		}
	}
	if len(found) > 0 {
		return found
	}
	if isNegativeNumber(s) || strings.Contains(s, " ") {
		return nil
	}
	return []tuple{{name: s}}
}

// isNegativeNumber is argparse's ^-\d+$|^-\d*\.\d+$ (\d is any decimal
// digit, as in Python).
func isNegativeNumber(s string) bool {
	r := []rune(s[1:])
	digits := func(r []rune) bool {
		for _, c := range r {
			if !unicode.IsDigit(c) {
				return false
			}
		}
		return true
	}
	if len(r) > 0 && digits(r) {
		return true
	}
	for i, c := range r {
		if c == '.' {
			return digits(r[:i]) && i+1 < len(r) && digits(r[i+1:])
		}
	}
	return false
}

// action is an option to apply, with its value.
type action struct {
	opt   *option
	value string
}

// Parse parses the command line (without the program name) as Python pqx's
// argparse parser does, then applies the environment (getenv, normally
// os.Getenv). Options may follow the file name; "--" ends them.
func Parse(args []string, getenv func(string) string) (*Options, error) {
	var (
		o                   Options
		accent, dim, border string
		formats             []string
		extras              []string
		sampleSeen          string // the --sample/--no-sample given first
		pathSeen            bool
	)
	// argparse's pattern: O an option, A an argument, - the first "--"
	// (everything after it is an argument)
	pattern := make([]byte, len(args))
	readings := make(map[int][]tuple)
	for i, a := range args {
		if a == "--" {
			pattern[i] = '-'
			for j := i + 1; j < len(args); j++ {
				pattern[j] = 'A'
			}
			break
		}
		if t := match(a); t != nil {
			pattern[i], readings[i] = 'O', t
		} else {
			pattern[i] = 'A'
		}
	}
	// consumePath matches the positional (-*A-*) at i; it returns where it stopped
	consumePath := func(i int) int {
		if pathSeen {
			return i
		}
		j := i
		for j < len(pattern) && pattern[j] == '-' {
			j++
		}
		if j == len(pattern) || pattern[j] != 'A' {
			return i
		}
		j++
		for j < len(pattern) && pattern[j] == '-' {
			j++
		}
		vals := append([]string(nil), args[i:j]...)
		if strings.Contains(string(pattern[i:j]), "-") { // the first "--" goes
			for k, v := range vals {
				if v == "--" {
					vals = append(vals[:k], vals[k+1:]...)
					break
				}
			}
		}
		o.Path, pathSeen = vals[0], true
		return j
	}
	apply := func(a action) error {
		opt, value := a.opt, a.value
		if opt.choices != nil && !contains(opt.choices, value) {
			return usageErr("argument %s: invalid choice: %s (choose from %s)",
				opt.name(), pyRepr(value), strings.Join(opt.choices, ", "))
		}
		switch opt.long {
		case "--help":
			return ErrHelp
		case "--version":
			return ErrVersion
		case "--where":
			o.Where = value
		case "--accent":
			accent = value
		case "--dim":
			dim = value
		case "--border":
			border = value
		case "--theme":
			o.Theme = value
		case "--sample", "--no-sample":
			if sampleSeen != "" && sampleSeen != opt.long {
				return usageErr("argument %s: not allowed with argument %s", opt.long, sampleSeen)
			}
			sampleSeen = opt.long
			v := opt.long == "--sample"
			o.Sample = &v
		case "--threads":
			n, ok := pyInt(value)
			if !ok {
				return usageErr("argument --threads: invalid int value: %s", pyRepr(value))
			}
			o.Threads = n
		case "--format":
			formats = append(formats, value)
		}
		return nil
	}
	// consumeOption is argparse's consume_optional: it applies the option at
	// i (and any short options run together with it, "-hw") and returns
	// where it stopped.
	consumeOption := func(i int) (int, error) {
		ts := readings[i]
		if len(ts) > 1 {
			names := make([]string, len(ts))
			for k, t := range ts {
				names[k] = t.name
			}
			return 0, usageErr("ambiguous option: %s could match %s", args[i], strings.Join(names, ", "))
		}
		t := ts[0]
		var todo []action
		stop := i + 1
		for {
			if t.opt == nil {
				extras = append(extras, args[i])
				break
			}
			if t.explicit != nil {
				ex := *t.explicit
				if !t.opt.takesValue() && t.name[1] != '-' && ex != "" {
					if t.sep || ex[0] == '-' {
						return 0, usageErr("argument %s: ignored explicit argument %s", t.opt.name(), pyRepr(ex))
					}
					todo = append(todo, action{opt: t.opt})
					next := "-" + ex[:1]
					o := lookup(next)
					if o == nil {
						extras = append(extras, "-"+ex)
						break
					}
					rest := ex[1:]
					t = tuple{opt: o, name: next}
					switch {
					case rest == "":
					case rest[0] == '=':
						rest = rest[1:]
						t.sep, t.explicit = true, &rest
					default:
						t.explicit = &rest
					}
					continue
				}
				if t.opt.takesValue() {
					todo = append(todo, action{t.opt, ex})
					break
				}
				return 0, usageErr("argument %s: ignored explicit argument %s", t.opt.name(), pyRepr(ex))
			}
			if !t.opt.takesValue() {
				todo = append(todo, action{opt: t.opt})
				break
			}
			if stop >= len(pattern) || pattern[stop] != 'A' {
				return 0, usageErr("argument %s: expected one argument", t.opt.name())
			}
			todo = append(todo, action{t.opt, args[stop]})
			stop++
			break
		}
		for _, a := range todo {
			if err := apply(a); err != nil {
				return 0, err
			}
		}
		return stop, nil
	}

	last := -1
	for i := range pattern {
		if pattern[i] == 'O' {
			last = i
		}
	}
	i := 0
	for i <= last {
		next := i
		for pattern[next] != 'O' {
			next++
		}
		if i != next {
			if j := consumePath(i); j > i {
				i = j
				continue
			}
		}
		if pattern[i] != 'O' {
			extras = append(extras, args[i:next]...)
			i = next
		}
		var err error
		if i, err = consumeOption(i); err != nil {
			return nil, err
		}
	}
	i = consumePath(i)
	extras = append(extras, args[i:]...)
	if !pathSeen {
		return nil, usageErr("the following arguments are required: path")
	}
	if len(extras) > 0 {
		return nil, usageErr("unrecognized arguments: %s", strings.Join(extras, " "))
	}
	o.Formats = map[string]fmtx.Override{}
	for _, item := range formats {
		name, spec, eq := strings.Cut(item, "=")
		name = strings.TrimSpace(name)
		if !eq || name == "" || strings.TrimSpace(spec) == "" {
			return nil, usageErr("--format expects COL=SPEC, got %s", pyRepr(item))
		}
		ov := fmtx.ParseOverride(strings.TrimSpace(spec))
		if e := fmtx.OverrideError(ov, "", nil); e != "" {
			return nil, usageErr("--format %s: %s", fmtx.Sanitize(item, false), fmtx.Sanitize(e, false))
		}
		o.Formats[name] = ov
	}

	// the look: the option, else the environment, else the default
	o.Accent = strings.ToLower(firstOf(accent, getenv("PQX_ACCENT"), theme.Accents[0]))
	if !contains(theme.Accents, o.Accent) {
		o.Accent = theme.Accents[0]
	}
	o.Dim = strings.ToLower(firstOf(dim, getenv("PQX_DIM"), theme.DimModes[0]))
	if !contains(theme.DimModes, o.Dim) {
		o.Dim = theme.DimModes[0]
	}
	o.Border = firstOf(border, getenv("PQX_BORDER"), theme.DefaultBorder)
	if !strings.HasPrefix(o.Border, "ansi_") && !strings.HasPrefix(o.Border, "#") {
		o.Border = "ansi_" + o.Border
	}
	if _, err := theme.ParseBorder(o.Border); err != nil {
		what := "argument --border"
		if border == "" {
			what = "PQX_BORDER"
		}
		return nil, usageErr("%s: %v", what, err)
	}
	if o.Theme != "" && !contains(theme.Names, o.Theme) {
		return nil, usageErr("argument --theme: invalid choice: %s (choose from %s)",
			pyRepr(o.Theme), strings.Join(theme.Names, ", "))
	}
	return &o, nil
}

// pyInt is Python's int() of a string: surrounding whitespace, a sign,
// decimal digits of any script, and single underscores between digits. A
// value beyond int's range is clamped (ok stays true: Python accepts it).
func pyInt(s string) (n int, ok bool) {
	t := []rune(strings.TrimFunc(s, unicode.IsSpace))
	neg := false
	if len(t) > 0 && (t[0] == '+' || t[0] == '-') {
		neg = t[0] == '-'
		t = t[1:]
	}
	if len(t) == 0 {
		return 0, false
	}
	const limit = math.MaxInt / 10
	over := false
	for i, r := range t {
		if r == '_' {
			if i == 0 || i == len(t)-1 || t[i-1] == '_' {
				return 0, false
			}
			continue
		}
		d := digitValue(r)
		if d < 0 {
			return 0, false
		}
		if n > limit {
			over = true
		} else {
			n = n*10 + d
		}
	}
	if over || n < 0 {
		n = math.MaxInt
	}
	if neg {
		n = -n
	}
	return n, true
}

// digitValue is the value of a decimal digit of any script, or -1.
func digitValue(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	if !unicode.IsDigit(r) {
		return -1
	}
	// decimal digits come in runs of ten from zero, some runs back to back
	z := r
	for unicode.IsDigit(z - 1) {
		z--
	}
	return int(r-z) % 10
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
