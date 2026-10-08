package opts

import (
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"
)

// description is the paragraph under the usage line.
const description = "Interactive terminal explorer for Parquet files: browse, filter (SQL), profile, " +
	"plot and export — lazily, so files of any size open instantly."

// Epilog closes --help, kept as written.
const Epilog = `Examples:
  pqx trips.parquet
  pqx trips.parquet -w "payment_type = 'card'"
  pqx trips.parquet -w "select vendor, count(*) from t group by 1"

Inside pqx, press ? for keys.

Report bugs at: <` + Homepage + `/issues>
pqx home page: <` + Homepage + `>
Written by ` + Author + `.`

// VersionText is --version's text (GNU style, plain ASCII), without the
// final newline.
func VersionText(version string) string {
	return "pqx " + version + `
Copyright (C) 2026 ` + Author + `
License BSD-3-Clause: <https://opensource.org/license/bsd-3-clause>
This is free software: you are free to change and redistribute it.
There is NO WARRANTY, to the extent permitted by law.

Written by ` + Author + `.`
}

// Width is the width argparse formats help for: $COLUMNS, else the width of
// the terminal on stdout, else 80; less 2 (shutil.get_terminal_size).
func Width(getenv func(string) string, stdout *os.File) int {
	cols, ok := pyInt(getenv("COLUMNS"))
	if !ok || cols <= 0 {
		cols = 0
		if stdout != nil {
			if w, _, err := term.GetSize(stdout.Fd()); err == nil {
				cols = w
			}
		}
		if cols <= 0 {
			cols = 80
		}
	}
	return cols - 2
}

// usageParts are the usage line's pieces for the options and the
// positional, as argparse writes them.
func usageParts() (opts, pos []string) {
	for i := range options {
		o := &options[i]
		switch o.long {
		case "--sample":
			opts = append(opts, "[--sample | --no-sample]")
		case "--no-sample":
		default:
			name := o.long
			if o.short != "" {
				name = o.short
			}
			if v := o.valueName(); v != "" {
				name += " " + v
			}
			opts = append(opts, "["+name+"]")
		}
	}
	return opts, []string{"path"}
}

// valueName is the option's metavar, or its choices in braces.
func (o *option) valueName() string {
	if o.choices != nil {
		return "{" + strings.Join(o.choices, ",") + "}"
	}
	return o.metavar
}

// invocation is the option as --help lists it ("-w WHERE, --where WHERE").
func (o *option) invocation() string {
	v := o.valueName()
	var names []string
	for _, n := range []string{o.short, o.long} {
		if n == "" {
			continue
		}
		if v != "" {
			n += " " + v
		}
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

// Usage is the usage message (argparse's format_usage), wrapped for width.
func Usage(width int) string {
	const prefix, prog = "usage: ", "pqx"
	optParts, posParts := usageParts()
	full := prog + " " + strings.Join(append(append([]string{}, optParts...), posParts...), " ")
	if n(prefix)+n(full) <= width {
		return prefix + full + "\n"
	}
	getLines := func(parts []string, indent string, withPrefix bool) []string {
		var lines, line []string
		lineLen := n(indent) - 1
		if withPrefix {
			lineLen = n(prefix) - 1
		}
		for _, p := range parts {
			if lineLen+1+n(p) > width && len(line) > 0 {
				lines = append(lines, indent+strings.Join(line, " "))
				line, lineLen = nil, n(indent)-1
			}
			line = append(line, p)
			lineLen += n(p) + 1
		}
		if len(line) > 0 {
			lines = append(lines, indent+strings.Join(line, " "))
		}
		if withPrefix {
			lines[0] = lines[0][len(indent):]
		}
		return lines
	}
	var lines []string
	if float64(n(prefix)+n(prog)) <= 0.75*float64(width) {
		indent := strings.Repeat(" ", n(prefix)+n(prog)+1)
		lines = getLines(append([]string{prog}, optParts...), indent, true)
		lines = append(lines, getLines(posParts, indent, false)...)
	} else {
		indent := strings.Repeat(" ", n(prefix))
		lines = getLines(append(append([]string{}, optParts...), posParts...), indent, false)
		if len(lines) > 1 {
			lines = append(getLines(optParts, indent, false), getLines(posParts, indent, false)...)
		}
		lines = append([]string{prog}, lines...)
	}
	return prefix + strings.Join(lines, "\n") + "\n"
}

// Help is the --help text (argparse's format_help), wrapped for width.
func Help(width int) string {
	var b strings.Builder
	b.WriteString(Usage(width))
	b.WriteString("\n")
	for _, l := range wrap(description, max(width, 11)) {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n")

	maxHelpPosition := min(24, max(width-20, 4))
	actionMax := n("path") + 2
	for i := range options {
		actionMax = max(actionMax, n(options[i].invocation())+2)
	}
	helpPosition := min(actionMax+2, maxHelpPosition)
	helpWidth := max(width-helpPosition, 11)
	actionWidth := helpPosition - 2 - 2
	item := func(header, help string) {
		indentFirst := 0
		if n(header) <= actionWidth {
			fmt.Fprintf(&b, "  %-*s  ", actionWidth, header)
		} else {
			fmt.Fprintf(&b, "  %s\n", header)
			indentFirst = helpPosition
		}
		lines := wrap(help, helpWidth)
		fmt.Fprintf(&b, "%*s%s\n", indentFirst, "", lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(&b, "%*s%s\n", helpPosition, "", l)
		}
	}
	b.WriteString("positional arguments:\n")
	item("path", "Parquet file to open")
	b.WriteString("\noptions:\n")
	for i := range options {
		item(options[i].invocation(), options[i].help)
	}
	b.WriteString("\n" + Epilog + "\n")
	return b.String()
}

// n is a string's length as Python counts it, in code points.
func n(s string) int { return utf8.RuneCountInString(s) }

// wrap is textwrap.wrap(text, width) after argparse collapses whitespace,
// for text without hyphens (textwrap would also break after a hyphen;
// pqx's help has none, which a test checks).
func wrap(text string, width int) []string {
	words := strings.Fields(text)
	// chunks, last first: words and the single spaces between them
	var chunks []string
	for i := len(words) - 1; i >= 0; i-- {
		chunks = append(chunks, words[i])
		if i > 0 {
			chunks = append(chunks, " ")
		}
	}
	var lines []string
	for len(chunks) > 0 {
		var cur []string
		curLen := 0
		if chunks[len(chunks)-1] == " " && len(lines) > 0 {
			chunks = chunks[:len(chunks)-1]
		}
		for len(chunks) > 0 {
			l := n(chunks[len(chunks)-1])
			if curLen+l > width {
				break
			}
			cur = append(cur, chunks[len(chunks)-1])
			chunks = chunks[:len(chunks)-1]
			curLen += l
		}
		if len(chunks) > 0 && n(chunks[len(chunks)-1]) > width {
			// a word longer than a line: break it to fill this one
			left := width - curLen
			if width < 1 {
				left = 1
			}
			r := []rune(chunks[len(chunks)-1])
			cur = append(cur, string(r[:left]))
			chunks[len(chunks)-1] = string(r[left:])
		}
		if len(cur) > 0 && strings.TrimSpace(cur[len(cur)-1]) == "" {
			cur = cur[:len(cur)-1]
		}
		if len(cur) > 0 {
			lines = append(lines, strings.Join(cur, ""))
		}
	}
	return lines
}

// pyRepr is Python's repr() of a string, as argparse quotes values in its
// errors: control and other unprintable characters escaped; an invalid
// UTF-8 byte as the surrogate Python decodes it to.
func pyRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&b, `\udc%02x`, s[i])
			i++
			continue
		}
		i += size
		switch {
		case r == '\\' || r == rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}
