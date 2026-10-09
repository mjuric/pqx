package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Python pqx reads formats.yaml with PyYAML, which follows YAML 1.1: a
// plain "yes" is a bool, "017" an octal int, ".5" a float. These are its
// implicit resolvers (yaml/resolver.py), so the Go port reads a value as
// Python does, and quotes what Python would read as something else.

type tag int

const (
	tagStr tag = iota
	tagBool
	tagFloat
	tagInt
	tagMerge
	tagNull
	tagTimestamp
	tagValue
)

var resolvers = []struct {
	t  tag
	re *regexp.Regexp
}{
	{tagBool, regexp.MustCompile(`^(?:yes|Yes|YES|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF)$`)},
	{tagFloat, regexp.MustCompile(`^(?:[-+]?(?:[0-9][0-9_]*)\.[0-9_]*(?:[eE][-+][0-9]+)?|\.[0-9][0-9_]*(?:[eE][-+][0-9]+)?|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)},
	{tagInt, regexp.MustCompile(`^(?:[-+]?0b[0-1_]+|[-+]?0[0-7_]+|[-+]?(?:0|[1-9][0-9_]*)|[-+]?0x[0-9a-fA-F_]+|[-+]?[1-9][0-9_]*(?::[0-5]?[0-9])+)$`)},
	{tagMerge, regexp.MustCompile(`^(?:<<)$`)},
	{tagNull, regexp.MustCompile(`^(?:~|null|Null|NULL|)$`)},
	{tagTimestamp, regexp.MustCompile(`^(?:[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]|[0-9][0-9][0-9][0-9]-[0-9][0-9]?-[0-9][0-9]?(?:[Tt]|[ \t]+)[0-9][0-9]?:[0-9][0-9]:[0-9][0-9](?:\.[0-9]*)?(?:[ \t]*(?:Z|[-+][0-9][0-9]?(?::[0-9][0-9])?))?)$`)},
	{tagValue, regexp.MustCompile(`^(?:=)$`)},
}

// implicit is the tag PyYAML gives a plain scalar.
func implicit(s string) tag {
	for _, r := range resolvers {
		if r.re.MatchString(s) {
			return r.t
		}
	}
	return tagStr
}

// scalar is s as PyYAML's emitter writes it (with allow_unicode): plain
// when that reads back as the same text, else single-quoted, else
// double-quoted with escapes. Unlike PyYAML, a value with line breaks is
// double-quoted (PyYAML single-quotes it over several lines) and long
// values are not folded: Python reads both the same.
func scalar(s string, simpleKey bool) string {
	a := analyze(s)
	if implicit(s) == tagStr && !(simpleKey && (a.empty || a.multiline)) && a.allowBlockPlain {
		return s
	}
	if a.allowSingleQuoted && !a.multiline {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return doubleQuoted(s)
}

type analysis struct {
	empty, multiline                   bool
	allowBlockPlain, allowSingleQuoted bool
}

func isBreak(r rune) bool { return r == '\n' || r == 0x85 || r == 0x2028 || r == 0x2029 }

func isSpaceOrBreak(r rune) bool { return r == 0 || r == ' ' || r == '\t' || r == '\r' || isBreak(r) }

// analyze is PyYAML's Emitter.analyze_scalar (the parts that decide the style).
func analyze(s string) analysis {
	if s == "" {
		return analysis{empty: true, allowBlockPlain: true, allowSingleQuoted: true}
	}
	rs := []rune(s)
	blockInd, flowInd, lineBreaks, special := false, false, false, false
	leadingSpace, leadingBreak, trailingSpace, trailingBreak := false, false, false, false
	breakSpace, spaceBreak := false, false
	if strings.HasPrefix(s, "---") || strings.HasPrefix(s, "...") {
		blockInd, flowInd = true, true
	}
	precededByWS := true
	followedByWS := len(rs) == 1 || isSpaceOrBreak(rs[1])
	prevSpace, prevBreak := false, false
	for i, ch := range rs {
		if i == 0 {
			if strings.ContainsRune("#,[]{}&*!|>'\"%@`", ch) {
				flowInd, blockInd = true, true
			}
			if ch == '?' || ch == ':' {
				flowInd = true
				if followedByWS {
					blockInd = true
				}
			}
			if ch == '-' && followedByWS {
				flowInd, blockInd = true, true
			}
		} else {
			if strings.ContainsRune(",?[]{}", ch) {
				flowInd = true
			}
			if ch == ':' {
				flowInd = true
				if followedByWS {
					blockInd = true
				}
			}
			if ch == '#' && precededByWS {
				flowInd, blockInd = true, true
			}
		}
		if isBreak(ch) {
			lineBreaks = true
		}
		if !(ch == '\n' || (ch >= 0x20 && ch <= 0x7E)) {
			unicodeOK := (ch == 0x85 || (ch >= 0xA0 && ch <= 0xD7FF) || (ch >= 0xE000 && ch <= 0xFFFD) ||
				(ch >= 0x10000 && ch < 0x10FFFF)) && ch != 0xFEFF
			if !unicodeOK {
				special = true
			}
		}
		switch {
		case ch == ' ':
			if i == 0 {
				leadingSpace = true
			}
			if i == len(rs)-1 {
				trailingSpace = true
			}
			if prevBreak {
				breakSpace = true
			}
			prevSpace, prevBreak = true, false
		case isBreak(ch):
			if i == 0 {
				leadingBreak = true
			}
			if i == len(rs)-1 {
				trailingBreak = true
			}
			if prevSpace {
				spaceBreak = true
			}
			prevSpace, prevBreak = false, true
		default:
			prevSpace, prevBreak = false, false
		}
		precededByWS = isSpaceOrBreak(ch)
		followedByWS = i+2 >= len(rs) || isSpaceOrBreak(rs[i+2])
	}
	a := analysis{multiline: lineBreaks, allowBlockPlain: true, allowSingleQuoted: true}
	if leadingSpace || leadingBreak || trailingSpace || trailingBreak {
		a.allowBlockPlain = false
	}
	if breakSpace {
		a.allowBlockPlain, a.allowSingleQuoted = false, false
	}
	if spaceBreak || special {
		a.allowBlockPlain, a.allowSingleQuoted = false, false
	}
	if lineBreaks || blockInd {
		a.allowBlockPlain = false
	}
	_ = flowInd // (flow style is never used)
	return a
}

var escapes = map[rune]string{0: "0", 0x07: "a", 0x08: "b", 0x09: "t", 0x0A: "n", 0x0B: "v", 0x0C: "f",
	0x0D: "r", 0x1B: "e", '"': "\"", '\\': "\\", 0x85: "N", 0xA0: "_", 0x2028: "L", 0x2029: "P"}

// doubleQuoted is PyYAML's write_double_quoted (allow_unicode, no folding).
func doubleQuoted(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, ch := range s {
		plain := (ch >= 0x20 && ch <= 0x7E) || (ch >= 0xA0 && ch <= 0xD7FF) || (ch >= 0xE000 && ch <= 0xFFFD)
		if ch == '"' || ch == '\\' || ch == 0x85 || ch == 0x2028 || ch == 0x2029 || ch == 0xFEFF || !plain {
			switch e, ok := escapes[ch]; {
			case ok:
				b.WriteString("\\" + e)
			case ch <= 0xFF:
				fmt.Fprintf(&b, "\\x%02X", ch)
			case ch <= 0xFFFF:
				fmt.Fprintf(&b, "\\u%04X", ch)
			default:
				fmt.Fprintf(&b, "\\U%08X", ch)
			}
			continue
		}
		b.WriteRune(ch)
	}
	b.WriteByte('"')
	return b.String()
}
