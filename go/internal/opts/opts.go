// Package opts is pqx's command line: the options of Python pqx's cli.py,
// with the same names, defaults, environment variables, exclusions, errors
// and help text (argparse's, reproduced), parsed into Options for the app.
package opts

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

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

var negativeNumber = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

// arg is what match makes of one argument.
type arg struct {
	opt      *option // nil for an unknown option
	value    string  // a value attached to it ("--where=x", "-wx")
	hasValue bool
	eq       bool // the value was attached with "="
	isOpt    bool // false for a positional
}

// match is argparse's _parse_optional: the option an argument names. An
// argument is a positional if it doesn't start with "-", is "-" alone, looks
// like a negative number, or has a space in it.
func match(s string) (arg, error) {
	if len(s) < 2 || s[0] != '-' {
		return arg{}, nil
	}
	if o := lookup(s); o != nil {
		return arg{opt: o, isOpt: true}, nil
	}
	if name, v, ok := strings.Cut(s, "="); ok {
		if o := lookup(name); o != nil {
			return arg{o, v, true, true, true}, nil
		}
	}
	var found []arg
	var names []string
	prefix, v, eq := strings.Cut(s, "=")
	for i := range options {
		o := &options[i]
		switch {
		case s[1] != '-' && o.short == s[:2]: // -wVALUE
			found = append(found, arg{o, s[2:], true, false, true})
			names = append(names, o.short)
		case strings.HasPrefix(o.long, prefix): // an abbreviation
			found = append(found, arg{o, v, eq, eq, true})
			names = append(names, o.long)
		}
	}
	switch {
	case len(found) > 1:
		return arg{}, usageErr("ambiguous option: %s could match %s", s, strings.Join(names, ", "))
	case len(found) == 1:
		return found[0], nil
	}
	if negativeNumber.MatchString(s) || strings.Contains(s, " ") {
		return arg{}, nil
	}
	return arg{isOpt: true}, nil
}

// Parse parses the command line (without the program name) as Python pqx's
// argparse parser does, then applies the environment (getenv, normally
// os.Getenv). Options may follow the file name; "--" ends them.
func Parse(args []string, getenv func(string) string) (*Options, error) {
	var (
		o             Options
		accent, dim   string
		border        string
		formats       []string
		extras        []string
		sampleSeen    string // the --sample/--no-sample given first
		endOfOptions  bool
		pathSeen      bool
		addPositional = func(a string) {
			if !pathSeen {
				o.Path, pathSeen = a, true
			} else {
				extras = append(extras, a)
			}
		}
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if endOfOptions {
			addPositional(a)
			continue
		}
		if a == "--" {
			endOfOptions = true
			continue
		}
		m, err := match(a)
		if err != nil {
			return nil, err
		}
		opt, value := m.opt, m.value
		switch {
		case !m.isOpt:
			addPositional(a)
			continue
		case opt == nil:
			extras = append(extras, a)
			continue
		case !opt.takesValue() && m.hasValue:
			// "-hx": -h, then x as more short flags; -h runs first and exits
			if opt.short == "-h" && !m.eq && value[0] != '-' {
				return nil, ErrHelp
			}
			return nil, usageErr("argument %s: ignored explicit argument %s", opt.name(), pyRepr(value))
		case opt.takesValue() && !m.hasValue:
			next := ""
			if i+1 < len(args) {
				next = args[i+1]
			}
			nm, _ := match(next)
			if i+1 >= len(args) || next == "--" || nm.isOpt {
				return nil, usageErr("argument %s: expected one argument", opt.name())
			}
			i++
			value = next
		}
		if opt.choices != nil && !contains(opt.choices, value) {
			return nil, usageErr("argument %s: invalid choice: %s (choose from %s)",
				opt.name(), pyRepr(value), strings.Join(opt.choices, ", "))
		}
		switch opt.long {
		case "--help":
			return nil, ErrHelp
		case "--version":
			return nil, ErrVersion
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
				return nil, usageErr("argument %s: not allowed with argument %s", opt.long, sampleSeen)
			}
			sampleSeen = opt.long
			v := opt.long == "--sample"
			o.Sample = &v
		case "--threads":
			n, ok := pyInt(value)
			if !ok {
				return nil, usageErr("argument --threads: invalid int value: %s", pyRepr(value))
			}
			o.Threads = n
		case "--format":
			formats = append(formats, value)
		}
	}
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

// pyInt is Python's int() of a string: surrounding whitespace, a sign, and
// single underscores between digits allowed.
func pyInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	t := strings.TrimLeft(s, "+-")
	if len(s)-len(t) > 1 || t == "" || t[0] == '_' || t[len(t)-1] == '_' || strings.Contains(t, "__") {
		return 0, false
	}
	n, err := strconv.Atoi(s[:len(s)-len(t)] + strings.ReplaceAll(t, "_", ""))
	return n, err == nil
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
