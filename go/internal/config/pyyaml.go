package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mjuric/pqx/go/internal/fmtx"
)

// PyYAML's safe_load, on yaml.v3's node tree: what Python pqx reads from a
// formats.yaml (YAML 1.1 resolution, merge keys, SafeConstructor's tags),
// and the errors where it can't read one. Go must never rewrite a file
// Python can't read, so anything PyYAML refuses is an error here too; where
// the two parsers might read the text differently (characters YAML 1.1
// takes as line breaks), the file is refused as well.

// The Python values of a document. Scalars are nil, bool, *big.Int,
// float64, string, pyTime and pyBytes; collections pyList, *pyDict, pySet.
type (
	pyTime  string // a date or datetime: its str()
	pyBytes []byte
	pyList  []any
	pySet   struct{}
	pyDict  struct {
		keys []any
		vals []any
		idx  map[string]int
	}
)

func (d *pyDict) set(k, v any) {
	id := hashKey(k)
	if i, ok := d.idx[id]; ok {
		d.vals[i] = v // Python keeps the first key, with the last value
		return
	}
	d.idx[id] = len(d.keys)
	d.keys = append(d.keys, k)
	d.vals = append(d.vals, v)
}

func (d *pyDict) get(k string) (any, bool) {
	i, ok := d.idx[hashKey(k)]
	if !ok {
		return nil, false
	}
	return d.vals[i], true
}

// hashKey is a dict key's identity: Python's 1, 1.0 and True are one key.
func hashKey(k any) string {
	switch x := k.(type) {
	case nil:
		return "n"
	case bool:
		if x {
			return "#1"
		}
		return "#0"
	case *big.Int:
		return "#" + x.Text(10)
	case float64:
		if x == math.Trunc(x) && !math.IsInf(x, 0) {
			b, _ := new(big.Float).SetFloat64(x).Int(nil)
			return "#" + b.Text(10)
		}
		if math.IsNaN(x) {
			return "nan" // PyYAML's one NaN object: equal to itself as a key
		}
		return "#" + strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return "s" + x
	case pyTime:
		return "t" + string(x)
	case pyBytes:
		return "b" + string(x)
	}
	return fmt.Sprintf("?%p", k)
}

// pyStr is str() of a key.
func pyStr(k any) string {
	switch x := k.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case *big.Int:
		return x.Text(10)
	case float64:
		switch {
		case math.IsNaN(x):
			return "nan"
		case math.IsInf(x, 1):
			return "inf"
		case math.IsInf(x, -1):
			return "-inf"
		}
		return fmtx.Format(x, fmtx.KindFloat, fmtx.Opts{Raw: true, Unsafe: true}) // repr
	case string:
		return x
	case pyTime:
		return string(x)
	case pyBytes:
		return bytesRepr(x)
	}
	return fmt.Sprint(k)
}

func bytesRepr(b []byte) string {
	q := byte('\'')
	if strings.IndexByte(string(b), '\'') >= 0 && strings.IndexByte(string(b), '"') < 0 {
		q = '"'
	}
	var s strings.Builder
	s.WriteString("b")
	s.WriteByte(q)
	for _, c := range b {
		switch {
		case c == q || c == '\\':
			s.WriteByte('\\')
			s.WriteByte(c)
		case c == '\t':
			s.WriteString(`\t`)
		case c == '\n':
			s.WriteString(`\n`)
		case c == '\r':
			s.WriteString(`\r`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&s, `\x%02x`, c)
		default:
			s.WriteByte(c)
		}
	}
	s.WriteByte(q)
	return s.String()
}

var errDocs = errors.New("expected a single document in the stream")

// safeLoad is yaml.safe_load(text).
func safeLoad(text []byte) (any, error) {
	if err := checkReadable(string(text)); err != nil {
		return nil, err
	}
	src := strings.TrimPrefix(string(text), "\ufeff")
	if strings.HasPrefix(src, "\ufeff") {
		// PyYAML strips one BOM; the next is text. yaml.v3 would strip it
		// too at the start, but not after a (blank) line.
		src = "\n" + src
	}
	dec := yaml.NewDecoder(strings.NewReader(yaml11(src)))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if err == io.EOF {
			return nil, nil // no document
		}
		return nil, err
	}
	var more yaml.Node
	if err := dec.Decode(&more); err != io.EOF {
		if err == nil {
			return nil, errDocs
		}
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	if err := checkTree(doc.Content[0], map[string]bool{}, 0); err != nil {
		return nil, err
	}
	c := constructor{memo: map[*yaml.Node]any{}, busy: map[*yaml.Node]bool{}, flat: map[*yaml.Node][][2]*yaml.Node{}}
	return c.construct(doc.Content[0])
}

// maxDepth is the deepest nesting read: deeper, Python pqx crashes
// (RecursionError), so the file is refused (and never rewritten).
const maxDepth = 300

// maxPairs bounds the mapping pairs merge keys may produce (a "billion
// laughs" of merges).
const maxPairs = 1_000_000

// checkTree refuses an anchor defined twice (PyYAML: "found duplicate
// anchor"; yaml.v3 redefines it) and nesting deeper than maxDepth.
func checkTree(n *yaml.Node, anchors map[string]bool, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("nested more than %d deep", maxDepth)
	}
	if n.Anchor != "" && n.Kind != yaml.AliasNode {
		if anchors[n.Anchor] {
			return fmt.Errorf("found duplicate anchor %q", n.Anchor)
		}
		anchors[n.Anchor] = true
	}
	for _, x := range n.Content {
		if err := checkTree(x, anchors, depth+1); err != nil {
			return err
		}
	}
	return nil
}

var yamlDirective = regexp.MustCompile(`(?m)^%YAML[ \t]+1\.[0-9]+[ \t]*$`)

// yaml11 makes a %YAML 1.x directive read %YAML 1.1: PyYAML reads any 1.x
// with its 1.1 rules; yaml.v3 refuses all but 1.1. (Only the directives
// before the document's "---" are touched.)
func yaml11(s string) string {
	if !strings.HasPrefix(s, "%") {
		return s
	}
	end := strings.Index(s, "\n---")
	if end < 0 {
		return s
	}
	return yamlDirective.ReplaceAllString(s[:end], "%YAML 1.1") + s[end:]
}

// checkReadable refuses what PyYAML's reader refuses (control
// characters), the characters YAML 1.1 takes as line breaks (NEL, LS, PS),
// which yaml.v3 reads as text, and tabs: PyYAML allows them in fewer places
// than yaml.v3 (not between a key and its value), and its own files have
// none (it writes \t).
func checkReadable(s string) error {
	for _, r := range s {
		ok := r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r <= 0x7E) || r == 0x85 ||
			(r >= 0xA0 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
		if !ok {
			return fmt.Errorf("unacceptable character #x%04x: special characters are not allowed", r)
		}
		if r == 0x85 || r == 0x2028 || r == 0x2029 || r == '\t' {
			return fmt.Errorf("character #x%04x: YAML 1.1 reads it differently from yaml.v3", r)
		}
	}
	return nil
}

type constructor struct {
	memo map[*yaml.Node]any
	busy map[*yaml.Node]bool
	// flat: each mapping's flattened pairs; pairs: how many were made
	flat  map[*yaml.Node][][2]*yaml.Node
	pairs int
}

// nodeTag is the tag PyYAML gives a node: an explicit one (short form), or
// the resolved one.
func nodeTag(n *yaml.Node) string {
	explicit := n.Style&yaml.TaggedStyle != 0 && n.Tag != "" && n.Tag != "!"
	if explicit {
		return n.Tag
	}
	switch n.Kind {
	case yaml.MappingNode:
		return "!!map"
	case yaml.SequenceNode:
		return "!!seq"
	}
	if n.Style&yaml.TaggedStyle != 0 || n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return "!!str" // "!" or quoted: text
	}
	return [...]string{tagStr: "!!str", tagBool: "!!bool", tagFloat: "!!float", tagInt: "!!int", tagMerge: "!!merge",
		tagNull: "!!null", tagTimestamp: "!!timestamp", tagValue: "!!value"}[implicit(n.Value)]
}

func (c *constructor) construct(n *yaml.Node) (any, error) {
	for n.Kind == yaml.AliasNode {
		if n.Alias == nil {
			return nil, errors.New("found undefined alias")
		}
		n = n.Alias
	}
	if v, ok := c.memo[n]; ok {
		return v, nil
	}
	if c.busy[n] {
		if n.Kind == yaml.SequenceNode {
			return pyList{}, nil // a recursive list: fine in Python
		}
		return nil, errors.New("found a recursive structure")
	}
	c.busy[n] = true
	defer delete(c.busy, n)
	v, err := c.build(n)
	if err != nil {
		return nil, err
	}
	c.memo[n] = v
	return v, nil
}

func kindName(k yaml.Kind) string {
	switch k {
	case yaml.MappingNode:
		return "mapping"
	case yaml.SequenceNode:
		return "sequence"
	}
	return "scalar"
}

func (c *constructor) build(n *yaml.Node) (any, error) {
	t := nodeTag(n)
	want := yaml.ScalarNode
	switch t {
	case "!!seq", "!!omap", "!!pairs":
		want = yaml.SequenceNode
	case "!!map", "!!set":
		want = yaml.MappingNode
	case "!!null", "!!bool", "!!int", "!!float", "!!binary", "!!timestamp", "!!str":
	default:
		return nil, fmt.Errorf("could not determine a constructor for the tag %q", t)
	}
	if n.Kind != want {
		return nil, fmt.Errorf("expected a %s node, but found %s", kindName(want), kindName(n.Kind))
	}
	switch t {
	case "!!null":
		return nil, nil
	case "!!str":
		return n.Value, nil
	case "!!bool":
		switch strings.ToLower(n.Value) {
		case "yes", "true", "on":
			return true, nil
		case "no", "false", "off":
			return false, nil
		}
		return nil, fmt.Errorf("not a bool: %q", n.Value)
	case "!!int":
		return yamlInt(n.Value)
	case "!!float":
		return yamlFloat(n.Value)
	case "!!timestamp":
		return yamlTimestamp(n.Value)
	case "!!binary":
		s := strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '=' {
				return r
			}
			return -1
		}, n.Value)
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64 data: %v", err)
		}
		return pyBytes(b), nil
	case "!!seq":
		l := pyList{}
		for _, x := range n.Content {
			v, err := c.construct(x)
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
		return l, nil
	case "!!omap", "!!pairs":
		l := pyList{}
		for _, x := range n.Content {
			for x.Kind == yaml.AliasNode && x.Alias != nil {
				x = x.Alias
			}
			if x.Kind != yaml.MappingNode || len(x.Content) != 2 {
				return nil, errors.New("expected a single mapping item")
			}
			k, err := c.construct(x.Content[0])
			if err != nil {
				return nil, err
			}
			v, err := c.construct(x.Content[1])
			if err != nil {
				return nil, err
			}
			l = append(l, pyList{k, v})
		}
		return l, nil
	}
	// !!map, !!set
	pairs, err := c.flatten(n)
	if err != nil {
		return nil, err
	}
	d := &pyDict{idx: map[string]int{}}
	for _, p := range pairs {
		k, err := c.construct(p[0])
		if err != nil {
			return nil, err
		}
		switch k.(type) {
		case pyList, *pyDict, pySet:
			return nil, errors.New("found unhashable key")
		}
		v, err := c.construct(p[1])
		if err != nil {
			return nil, err
		}
		d.set(k, v)
	}
	if t == "!!set" {
		return pySet{}, nil
	}
	return d, nil
}

// flatten is SafeConstructor.flatten_mapping: the pairs of a mapping with
// its merge keys ("<<") applied, merged pairs first.
func (c *constructor) flatten(n *yaml.Node) ([][2]*yaml.Node, error) {
	if f, ok := c.flat[n]; ok {
		return f, nil
	}
	f, err := c.flattenNode(n)
	if err != nil {
		return nil, err
	}
	if c.pairs += len(f); c.pairs > maxPairs {
		return nil, fmt.Errorf("merge keys make more than %d pairs", maxPairs)
	}
	c.flat[n] = f
	return f, nil
}

func (c *constructor) flattenNode(n *yaml.Node) ([][2]*yaml.Node, error) {
	var merge, own [][2]*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind == yaml.ScalarNode && nodeTag(k) == "!!merge" {
			for v.Kind == yaml.AliasNode && v.Alias != nil {
				v = v.Alias
			}
			switch v.Kind {
			case yaml.MappingNode:
				sub, err := c.flattenOnce(v)
				if err != nil {
					return nil, err
				}
				merge = append(merge, sub...)
			case yaml.SequenceNode:
				var subs [][][2]*yaml.Node
				for _, s := range v.Content {
					for s.Kind == yaml.AliasNode && s.Alias != nil {
						s = s.Alias
					}
					if s.Kind != yaml.MappingNode {
						return nil, errors.New("expected a mapping for merging")
					}
					sub, err := c.flattenOnce(s)
					if err != nil {
						return nil, err
					}
					subs = append(subs, sub)
				}
				for j := len(subs) - 1; j >= 0; j-- {
					merge = append(merge, subs[j]...)
				}
			default:
				return nil, errors.New("expected a mapping or list of mappings for merging")
			}
			continue
		}
		if k.Kind == yaml.ScalarNode && nodeTag(k) == "!!value" {
			k = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k.Value, Style: yaml.TaggedStyle}
		}
		own = append(own, [2]*yaml.Node{k, v})
	}
	return append(merge, own...), nil
}

// flattenOnce flattens a merged mapping, refusing a merge into itself.
func (c *constructor) flattenOnce(n *yaml.Node) ([][2]*yaml.Node, error) {
	if c.busy[n] {
		return nil, errors.New("found a recursive merge")
	}
	c.busy[n] = true
	defer delete(c.busy, n)
	return c.flatten(n)
}

// pyInt is Python's int(s, base) after PyYAML's sign handling: digits,
// and for base 8 an optional 0o prefix.
func pyInt(s string, base int) (*big.Int, bool) {
	if base == 8 {
		if t, ok := strings.CutPrefix(strings.ToLower(s), "0o"); ok {
			s = t
		}
	}
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "+-") {
		return nil, false
	}
	n, ok := new(big.Int).SetString(s, base)
	return n, ok
}

// yamlInt is SafeConstructor.construct_yaml_int.
func yamlInt(s string) (any, error) {
	v := strings.ReplaceAll(s, "_", "")
	bad := fmt.Errorf("invalid literal for int(): %q", s)
	if v == "" {
		return nil, bad
	}
	sign := int64(1)
	if v[0] == '-' {
		sign = -1
	}
	if v[0] == '+' || v[0] == '-' {
		v = v[1:]
	}
	var n *big.Int
	ok := true
	switch {
	case v == "0":
		n = new(big.Int)
	case strings.HasPrefix(v, "0b"):
		n, ok = pyInt(v[2:], 2)
	case strings.HasPrefix(v, "0x"):
		n, ok = pyInt(v[2:], 16)
	case strings.HasPrefix(v, "0"):
		n, ok = pyInt(v, 8)
	case strings.Contains(v, ":"):
		n = new(big.Int)
		base := big.NewInt(1)
		parts := strings.Split(v, ":")
		for i := len(parts) - 1; i >= 0; i-- {
			d, ok2 := pyInt(parts[i], 10)
			if !ok2 {
				return nil, bad
			}
			n.Add(n, d.Mul(d, base))
			base.Mul(base, big.NewInt(60))
		}
	default:
		n, ok = pyInt(v, 10)
	}
	if !ok {
		return nil, bad
	}
	return n.Mul(n, big.NewInt(sign)), nil
}

var pyFloatRE = regexp.MustCompile(`^\s*[+-]?(?:(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:e[+-]?[0-9]+)?|inf|infinity|nan)\s*$`)

// pyFloat is Python's float(s) of a lower-cased text.
func pyFloat(s string) (float64, bool) {
	if !pyFloatRE.MatchString(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return f, true
}

// yamlFloat is SafeConstructor.construct_yaml_float.
func yamlFloat(s string) (any, error) {
	v := strings.ToLower(strings.ReplaceAll(s, "_", ""))
	bad := fmt.Errorf("could not convert string to float: %q", s)
	if v == "" {
		return nil, bad
	}
	sign := 1.0
	if v[0] == '-' {
		sign = -1
	}
	if v[0] == '+' || v[0] == '-' {
		v = v[1:]
	}
	switch {
	case v == ".inf":
		return sign * math.Inf(1), nil
	case v == ".nan":
		return math.NaN(), nil
	case strings.Contains(v, ":"):
		parts := strings.Split(v, ":")
		x, base := 0.0, 1.0
		for i := len(parts) - 1; i >= 0; i-- {
			d, ok := pyFloat(parts[i])
			if !ok {
				return nil, bad
			}
			x += float64(d * base) // (rounded, not fused into the sum)
			base *= 60
		}
		return sign * x, nil
	}
	f, ok := pyFloat(v)
	if !ok {
		return nil, bad
	}
	return sign * f, nil
}

var timestampRE = regexp.MustCompile(`^([0-9]{4})-([0-9][0-9]?)-([0-9][0-9]?)(?:(?:[Tt]|[ \t]+)([0-9][0-9]?):([0-9][0-9]):([0-9][0-9])(?:\.([0-9]*))?(?:[ \t]*(Z|([-+])([0-9][0-9]?)(?::([0-9][0-9]))?))?)?$`)

// yamlTimestamp is SafeConstructor.construct_yaml_timestamp, as its str():
// a date, or a datetime with its UTC offset if it has one.
func yamlTimestamp(s string) (any, error) {
	m := timestampRE.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("not a timestamp: %q", s)
	}
	n := func(x string) int { v, _ := strconv.Atoi(x); return v }
	y, mo, d := n(m[1]), n(m[2]), n(m[3])
	if y < 1 || mo < 1 || mo > 12 || d < 1 || d > time.Date(y, time.Month(mo)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
		return nil, fmt.Errorf("day is out of range for month: %q", s)
	}
	date := fmt.Sprintf("%04d-%02d-%02d", y, mo, d)
	if m[4] == "" {
		return pyTime(date), nil
	}
	h, mi, sec := n(m[4]), n(m[5]), n(m[6])
	if h > 23 || mi > 59 || sec > 59 {
		return nil, fmt.Errorf("time out of range: %q", s)
	}
	out := date + fmt.Sprintf(" %02d:%02d:%02d", h, mi, sec)
	if f := m[7]; f != "" {
		if len(f) > 6 {
			f = f[:6]
		}
		f += strings.Repeat("0", 6-len(f))
		if f != "000000" {
			out += "." + f
		}
	}
	switch {
	case m[9] != "":
		off := n(m[10])*60 + n(m[11])
		if off >= 24*60 {
			return nil, fmt.Errorf("offset out of range: %q", s)
		}
		sign := "+"
		if m[9] == "-" && off != 0 {
			sign = "-"
		}
		out += fmt.Sprintf("%s%02d:%02d", sign, off/60, off%60)
	case m[8] == "Z":
		out += "+00:00"
	}
	return pyTime(out), nil
}
