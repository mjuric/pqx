// Package config reads and writes ~/.config/pqx/formats.yaml (or under
// $XDG_CONFIG_HOME if that is absolute), the column formats remembered by
// column name. The file format, locking and atomic writes are Python pqx's
// (config.py), so both versions can share the file. Part of the contract
// (docs/design/go-port.md).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/mjuric/pqx/go/internal/fmtx"
)

// ErrConfig wraps a formats.yaml that can't be read or parsed.
var ErrConfig = errors.New("formats.yaml")

// configError is config.ConfigError: "<path>: <what>", matching ErrConfig.
type configError struct{ msg string }

func (e *configError) Error() string        { return e.msg }
func (e *configError) Is(target error) bool { return target == ErrConfig }

func errorf(format string, a ...any) error { return &configError{fmt.Sprintf(format, a...)} }

// header starts the file (config.HEADER).
const header = `# pqx column display formats, keyed by column name (shared by all files).
# A value is either a Python format spec (.4f, .2e, ",d") or an integer number
# of digits: decimals for MJD / angle / magnitude columns, significant digits
# for other floats. pqx rewrites this file when you press < > or F in the grid,
# so comments other than this header are not kept.
`

// Path is the formats file's path.
func Path() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "~"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "pqx", "formats.yaml")
}

// LoadFormats reads the formats file at path ("" for Path()): column name
// to override, skipping invalid entries. A missing file is no formats and
// no error; an unreadable or unparsable one is an error wrapping ErrConfig.
func LoadFormats(path string) (map[string]fmtx.Override, error) {
	if path == "" {
		path = Path()
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]fmtx.Override{}, nil
	}
	if err != nil {
		return nil, errorf("%s: %v", path, err)
	}
	if !utf8.Valid(b) {
		return nil, errorf("%s: not UTF-8 text", path)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, errorf("%s: %v", path, err)
	}
	out := map[string]fmtx.Override{}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return out, nil
		}
		root = root.Content[0]
	}
	root = deref(root)
	if root.Kind != yaml.MappingNode {
		return out, nil // not a mapping: no columns (Python's doc.get on a dict only)
	}
	var cols *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if k := deref(root.Content[i]); k.Kind == yaml.ScalarNode && k.Value == "columns" && resolve(k) == tagStr {
			cols = deref(root.Content[i+1])
		}
	}
	if cols == nil || (cols.Kind == yaml.ScalarNode && resolve(cols) == tagNull) {
		return out, nil
	}
	if cols.Kind != yaml.MappingNode {
		return nil, errorf("%s: 'columns' must be a mapping", path)
	}
	for i := 0; i+1 < len(cols.Content); i += 2 {
		k, v := deref(cols.Content[i]), deref(cols.Content[i+1])
		if k.Kind != yaml.ScalarNode {
			return nil, errorf("%s: a column name that isn't text", path)
		}
		if o, ok := valid(v); ok {
			out[pyStrKey(k)] = o
		}
	}
	return out, nil
}

func deref(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// valid is config._valid: an int of digits (0 or more, at most 17) or a
// non-empty spec that suits some column.
func valid(v *yaml.Node) (fmtx.Override, bool) {
	if v.Kind != yaml.ScalarNode {
		return fmtx.Override{}, false
	}
	var o fmtx.Override
	switch resolve(v) {
	case tagInt:
		n, ok := yamlInt(v.Value)
		if !ok || n < 0 {
			return o, false
		}
		o = fmtx.Override{Digits: n, Set: true}
	case tagStr:
		if v.Value == "" {
			return o, false
		}
		o = fmtx.Override{Spec: v.Value, Set: true}
	default:
		return o, false
	}
	return o, fmtx.OverrideError(o, "", nil) == ""
}

// pyStrKey is Python's str() of a key as PyYAML reads it.
func pyStrKey(k *yaml.Node) string {
	switch resolve(k) {
	case tagNull:
		return "None"
	case tagBool:
		if yamlBool(k.Value) {
			return "True"
		}
		return "False"
	case tagInt:
		if n, ok := yamlInt(k.Value); ok {
			return fmt.Sprint(n)
		}
	}
	return k.Value
}

// SaveFormat sets (or, for the zero Override, removes) one column's format
// in the file at path ("" for Path()), under a lock, merging with what is
// there, and writing atomically. It returns the path written.
func SaveFormat(name string, o fmtx.Override, path string) (string, error) {
	if path == "" {
		path = Path()
	}
	path, err := resolvePath(path) // a symlinked file (dotfiles) is updated, not replaced
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	if haveLock {
		lf, err := os.OpenFile(filepath.Join(dir, "."+filepath.Base(path)+".lock"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
		if err != nil {
			return "", err
		}
		defer lf.Close()
		if err := lock(lf); err != nil {
			return "", err
		}
		defer unlock(lf)
	}
	cols, err := LoadFormats(path)
	if err != nil {
		return "", err
	}
	if cur, ok := cols[name]; (ok && cur == o) || (!ok && !o.Set) {
		return path, nil
	}
	if o.Set {
		cols[name] = o
	} else {
		delete(cols, name)
	}
	mode := newFileMode()
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".formats.*.tmp")
	if err != nil {
		return "", err
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.WriteString(header + dump(cols)); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	done = true
	return path, nil
}

// resolvePath is pathlib's resolve(strict=False): an absolute path with
// its symlinks followed as far as they exist (a dangling link to its
// target).
func resolvePath(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	for range 40 {
		r, err := filepath.EvalSymlinks(p)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return p, nil
		}
		if st, lerr := os.Lstat(p); lerr == nil && st.Mode()&fs.ModeSymlink != 0 {
			t, err := os.Readlink(p)
			if err != nil {
				return p, nil
			}
			if !filepath.IsAbs(t) {
				t = filepath.Join(filepath.Dir(p), t)
			}
			p = filepath.Clean(t)
			continue
		}
		dir := filepath.Dir(p)
		if dir == p {
			return p, nil
		}
		rd, err := resolvePath(dir)
		if err != nil {
			return p, nil
		}
		return filepath.Join(rd, filepath.Base(p)), nil
	}
	return p, nil
}

// dump is yaml.safe_dump({"columns": cols}, sort_keys=False,
// allow_unicode=True, default_flow_style=False) with the columns sorted.
func dump(cols map[string]fmtx.Override) string {
	if len(cols) == 0 {
		return "columns: {}\n"
	}
	names := make([]string, 0, len(cols))
	for k := range cols {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("columns:\n")
	for _, k := range names {
		o := cols[k]
		if k == "" || len([]rune(k)) >= 128 || strings.ContainsAny(k, "\n\u0085\u2028\u2029") {
			// not a simple key: PyYAML writes "? key" and ": value"
			b.WriteString("  ? ")
			b.WriteString(scalar(k, false))
			b.WriteString("\n  : ")
		} else {
			b.WriteString("  ")
			b.WriteString(scalar(k, true))
			b.WriteString(": ")
		}
		if o.Spec != "" {
			b.WriteString(scalar(o.Spec, false))
		} else {
			fmt.Fprint(&b, o.Digits)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
