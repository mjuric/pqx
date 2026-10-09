package fmtx

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Python's datetime.strftime on Linux: Python replaces %z, %:z, %Z and %f
// itself, then glibc's strftime (C locale) does the rest, with its flags
// (_ - 0 ^ #), field widths and E/O modifiers. An unknown directive is
// copied as is.

// pyTime is what strftime sees: a broken-down time, and what Python knows
// of the zone and the microseconds.
type pyTime struct {
	t      time.Time // the wall clock, in UTC
	zoned  bool      // an aware datetime in UTC
	micros int
}

var (
	weekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	months   = []string{"January", "February", "March", "April", "May", "June", "July", "August",
		"September", "October", "November", "December"}
)

// strftime is datetime.strftime(format).
func strftime(pt pyTime, format string) string {
	// Python's wrap_strftime: %z, %:z, %Z and %f (without flags) are its own.
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(format) {
			b.WriteByte('%')
			break
		}
		switch format[i+1] {
		case 'z':
			if pt.zoned {
				b.WriteString("+0000")
			}
		case 'Z':
			if pt.zoned {
				b.WriteString("UTC")
			}
		case 'f':
			fmt.Fprintf(&b, "%06d", pt.micros)
		case ':':
			if i+2 < len(format) && format[i+2] == 'z' {
				if pt.zoned {
					b.WriteString("+00:00")
				}
				i++
				break
			}
			b.WriteString("%:")
		default:
			b.WriteByte('%')
			b.WriteByte(format[i+1])
		}
		i++
	}
	f := b.String()
	return strftimeLimit(pt.t, f, outputLimit(utf8.RuneCountInString(f)))
}

// outputLimit is the buffer Python's time.strftime ends with for a format
// of n characters: from 1024, doubled until it is at least 256 × n. Output
// that doesn't fit (with its terminating NUL) comes back as "".
func outputLimit(n int) int {
	i := 1024
	for i < 256*n {
		i *= 2
	}
	return i
}

// glibcStrftime is glibc's strftime in the C locale (__strftime_internal).
func glibcStrftime(t time.Time, f string) string { return strftimeLimit(t, f, math.MaxInt32) }

// strftimeLimit is glibcStrftime, or "" if the output would take limit
// characters or more (it stops as soon as it knows).
func strftimeLimit(t time.Time, f string, limit int) string {
	var out strings.Builder
	over := func() bool {
		return out.Len() >= limit && utf8.RuneCountInString(out.String()) >= limit
	}
	for i := 0; i < len(f); i++ {
		if over() {
			return ""
		}
		if f[i] != '%' {
			out.WriteByte(f[i])
			continue
		}
		start := i
		var padc byte
		upper, changeCase := false, false
		j := i + 1
	flags:
		for ; j < len(f); j++ {
			switch f[j] {
			case '_', '-', '0':
				padc = f[j]
			case '^':
				upper = true
			case '#':
				changeCase = true
			default:
				break flags
			}
		}
		width := -1
		if j < len(f) && f[j] >= '0' && f[j] <= '9' {
			width = 0
			for ; j < len(f) && f[j] >= '0' && f[j] <= '9'; j++ {
				if width < 1<<20 {
					width = width*10 + int(f[j]-'0')
				}
			}
		}
		var modifier byte
		if j < len(f) && (f[j] == 'E' || f[j] == 'O') {
			modifier = f[j]
			j++
		}
		if width >= limit {
			return "" // at least width characters: too many
		}
		g := glibcField{width: width, pad: padc, upper: upper}
		bad := func() {
			if j >= len(f) { // % at the end: copy what there is
				j = len(f) - 1
			}
			if f[j] >= 0x80 {
				// a non-ASCII character: the directive is copied without
				// it, and the character follows as text
				g.text(&out, f[start:j])
				j--
				return
			}
			g.text(&out, f[start:j+1])
		}
		if j >= len(f) {
			bad()
			i = j
			continue
		}
		c := f[j]
		yday := t.YearDay() - 1
		wday := int(t.Weekday())
		hour12 := t.Hour() % 12
		if hour12 == 0 {
			hour12 = 12
		}
		switch c {
		case '%':
			g.text(&out, "%")
		case 'a', 'A':
			if modifier != 0 {
				bad()
				break
			}
			if changeCase {
				g.upper = true
			}
			name := weekdays[wday]
			if c == 'a' {
				name = name[:3]
			}
			g.text(&out, name)
		case 'b', 'h', 'B':
			if changeCase {
				g.upper = true // (before the check: a bad %#Eb is copied in capitals)
			}
			if modifier == 'E' {
				bad()
				break
			}
			name := months[t.Month()-1]
			if c != 'B' {
				name = name[:3]
			}
			g.text(&out, name)
		case 'c':
			if modifier == 'O' {
				bad()
				break
			}
			g.sub(&out, t, "%a %b %e %H:%M:%S %Y")
		case 'C':
			y := t.Year()
			c := y / 100
			if y%100 < 0 {
				c--
			}
			g.number(&out, 1, int64(c), false)
		case 'x', 'D':
			if (c == 'x' && modifier == 'O') || (c == 'D' && modifier != 0) {
				bad()
				break
			}
			g.sub(&out, t, "%m/%d/%y")
		case 'X', 'T':
			if c == 'X' && modifier == 'O' {
				bad()
				break
			}
			g.sub(&out, t, "%H:%M:%S")
		case 'r':
			g.sub(&out, t, "%I:%M:%S %p")
		case 'R':
			g.sub(&out, t, "%H:%M")
		case 'F':
			if modifier != 0 {
				bad()
				break
			}
			g.sub(&out, t, "%Y-%m-%d")
		case 'd', 'e', 'H', 'I', 'k', 'l', 'j', 'm', 'M', 'S', 'U', 'W', 'V', 'g', 'G', 'u', 'w', 'y', 'Y':
			if modifier == 'E' && c != 'y' && c != 'Y' && c != 'u' || modifier == 'O' && c == 'Y' {
				bad()
				break
			}
			var d int
			var v int64
			space := false
			switch c {
			case 'd':
				d, v = 2, int64(t.Day())
			case 'e':
				d, v, space = 2, int64(t.Day()), true
			case 'H':
				d, v = 2, int64(t.Hour())
			case 'I':
				d, v = 2, int64(hour12)
			case 'k':
				d, v, space = 2, int64(t.Hour()), true
			case 'l':
				d, v, space = 2, int64(hour12), true
			case 'j':
				d, v = 3, int64(yday+1)
			case 'm':
				d, v = 2, int64(t.Month())
			case 'M':
				d, v = 2, int64(t.Minute())
			case 'S':
				d, v = 2, int64(t.Second())
			case 'U':
				d, v = 2, int64((yday-wday+7)/7)
			case 'W':
				d, v = 2, int64((yday-(wday-1+7)%7+7)/7)
			case 'V':
				_, w := t.ISOWeek()
				d, v = 2, int64(w)
			case 'g':
				y, _ := t.ISOWeek()
				d, v = 2, int64((y%100+100)%100)
			case 'G':
				y, _ := t.ISOWeek()
				d, v = 1, int64(y)
			case 'u':
				d, v = 1, int64((wday-1+7)%7+1)
			case 'w':
				d, v = 1, int64(wday)
			case 'y':
				d, v = 2, int64((t.Year()%100+100)%100)
			case 'Y':
				d, v = 1, int64(t.Year())
			}
			g.number(&out, d, v, space)
		case 'n':
			g.text(&out, "\n")
		case 't':
			g.text(&out, "\t")
		case 'p', 'P':
			s := "AM"
			if t.Hour() >= 12 {
				s = "PM"
			}
			if c == 'P' || changeCase {
				s = strings.ToLower(s)
				g.upper = false
			}
			g.text(&out, s)
		case 's':
			// mktime: the broken-down time read as local time
			u := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local).Unix()
			g.number(&out, 1, u, false)
		case 'z':
			// (with flags: Python leaves it to glibc, which writes nothing:
			// Python's struct_time has tm_isdst -1)
		case 'Z':
			if padc != '-' { // (an empty zone: the - flag leaves even the padding out)
				g.text(&out, "")
			}
		default:
			bad()
		}
		i = j
	}
	if over() {
		return ""
	}
	return out.String()
}

// glibcField is one directive's flags.
type glibcField struct {
	width int
	pad   byte
	upper bool
}

// text is glibc's cpy: s padded on the left to the width (with zeros for
// the 0 flag, else spaces).
func (g glibcField) text(out *strings.Builder, s string) {
	if g.upper {
		s = strings.ToUpper(s)
	}
	if d := g.width - len(s); d > 0 {
		c := " "
		if g.pad == '0' {
			c = "0"
		}
		out.WriteString(strings.Repeat(c, d))
	}
	out.WriteString(s)
}

// sub is a directive that stands for a format of its own (%c, %x, %T, …).
func (g glibcField) sub(out *strings.Builder, t time.Time, f string) {
	g.text(out, glibcStrftime(t, f))
}

// number is glibc's DO_NUMBER (DO_NUMBER_SPACEPAD if space): v with at
// least d digits, padded per the flags.
func (g glibcField) number(out *strings.Builder, d int, v int64, space bool) {
	digits := max(d, g.width)
	if space && g.pad != '0' && g.pad != '-' {
		g.pad = '_'
	}
	neg := v < 0
	num := strconv.FormatInt(v, 10)
	if neg {
		num = num[1:]
	}
	s := num
	if neg {
		s = "-" + num
	}
	if g.pad != '-' {
		if padding := digits - len(s); padding > 0 {
			if g.pad == '_' {
				out.WriteString(strings.Repeat(" ", padding))
				g.width = max(g.width-padding, 0)
			} else {
				if neg {
					out.WriteByte('-')
					s = num
				}
				out.WriteString(strings.Repeat("0", padding))
				g.width = 0
			}
		}
	}
	g.upper = false
	g.text(out, s)
}
