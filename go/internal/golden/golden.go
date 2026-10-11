// Package golden loads the golden files in go/testdata/golden: outputs of Python
// pqx, the reference implementation, that the Go tests compare against.
//
// A file is a header and named sections of records (see testdata/golden/README.md).
// Records stay raw JSON until a test asks for a field, so each test decodes only
// what it uses: with the accessors on Record, or into its own structs with
// Record.Decode or LoadInto (Value and Text implement json.Unmarshaler).
//
// Value is this package's own representation of the typed value encoding,
// independent of the data layer's types; tests convert between the two.
package golden

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// Dir is the absolute path of go/testdata/golden.
func Dir() string {
	return filepath.Join(testdata(), "golden")
}

// FixturesDir is the absolute path of go/testdata/fixtures.
func FixturesDir() string {
	return filepath.Join(testdata(), "fixtures")
}

// Fixture is the absolute path of a fixture, e.g. Fixture("demo.parquet").
func Fixture(name string) string {
	return filepath.Join(FixturesDir(), name)
}

func testdata() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("golden: can't find the source directory")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata")
}

// Files lists the golden files (base names, sorted).
func Files() ([]string, error) {
	m, err := filepath.Glob(filepath.Join(Dir(), "*.json"))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(m))
	for i, p := range m {
		out[i] = filepath.Base(p)
	}
	sort.Strings(out)
	return out, nil
}

// Header is a file's header: what made it, with which versions.
type Header struct {
	File          string            `json:"file"`
	Generator     string            `json:"generator"`
	Encoding      string            `json:"encoding"`
	Versions      map[string]string `json:"versions"`
	Fixture       string            `json:"fixture,omitempty"`        // data_*.json: the fixture file
	DuckDBThreads int               `json:"duckdb_threads,omitempty"` // data_*.json
}

// File is one golden file.
type File struct {
	Name     string
	Header   Header
	Order    []string            // section names in file order
	Sections map[string][]Record // records by section
}

// Record is one record: field name to raw JSON.
type Record map[string]json.RawMessage

// LoadFile reads and parses golden file name (e.g. "fmt.json").
func LoadFile(name string) (*File, error) {
	b, err := os.ReadFile(filepath.Join(Dir(), name))
	if err != nil {
		return nil, err
	}
	f := &File{Name: name, Sections: map[string][]Record{}}
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := expectDelim(dec, '{'); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		key, _ := tok.(string)
		if key == "header" {
			if err := dec.Decode(&f.Header); err != nil {
				return nil, fmt.Errorf("%s: header: %w", name, err)
			}
			continue
		}
		var recs []Record
		if err := dec.Decode(&recs); err != nil {
			return nil, fmt.Errorf("%s: section %q: %w", name, key, err)
		}
		f.Order = append(f.Order, key)
		f.Sections[key] = recs
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return f, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

// Load reads golden file name, failing the test if it can't.
func Load(t testing.TB, name string) *File {
	t.Helper()
	f, err := LoadFile(name)
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	return f
}

// LoadInto decodes the whole of golden file name into v (a struct whose fields
// name the sections, or a map).
func LoadInto(t testing.TB, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(Dir(), name))
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("golden: %s: %v", name, err)
	}
}

// Section returns the records of a section, failing the test if there is none.
func (f *File) Section(t testing.TB, name string) []Record {
	t.Helper()
	recs, ok := f.Sections[name]
	if !ok {
		t.Fatalf("golden: %s has no section %q (has %v)", f.Name, name, f.Order)
	}
	return recs
}

// ID is the record's id ("<section>/<n>").
func (r Record) ID() string {
	var s string
	_ = json.Unmarshal(r["id"], &s)
	return s
}

// Has reports whether the record has field key (a JSON null counts).
func (r Record) Has(key string) bool {
	_, ok := r[key]
	return ok
}

// IsNull reports whether field key is missing or JSON null.
func (r Record) IsNull(key string) bool {
	raw, ok := r[key]
	return !ok || string(bytes.TrimSpace(raw)) == "null"
}

// Err returns the record's error ("<Python exception type>: <message>") and
// whether it has one: Python raised instead of returning "out".
func (r Record) Err() (string, bool) {
	raw, ok := r["error"]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// DecodeErr decodes field key into v.
func (r Record) DecodeErr(key string, v any) error {
	raw, ok := r[key]
	if !ok {
		return fmt.Errorf("%s: no field %q", r.ID(), key)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: field %q: %w", r.ID(), key, err)
	}
	return nil
}

// Decode decodes field key into v, failing the test if it can't.
func (r Record) Decode(t testing.TB, key string, v any) {
	t.Helper()
	if err := r.DecodeErr(key, v); err != nil {
		t.Fatalf("golden: %v", err)
	}
}

// String is field key as a string.
func (r Record) String(t testing.TB, key string) string {
	t.Helper()
	var s string
	r.Decode(t, key, &s)
	return s
}

// Int is field key as an int.
func (r Record) Int(t testing.TB, key string) int {
	t.Helper()
	var n int
	r.Decode(t, key, &n)
	return n
}

// Float is field key (a plain JSON number) as a float64.
func (r Record) Float(t testing.TB, key string) float64 {
	t.Helper()
	var x float64
	r.Decode(t, key, &x)
	return x
}

// Bool is field key as a bool.
func (r Record) Bool(t testing.TB, key string) bool {
	t.Helper()
	var b bool
	r.Decode(t, key, &b)
	return b
}

// Value is field key as a typed value.
func (r Record) Value(t testing.TB, key string) Value {
	t.Helper()
	var v Value
	r.Decode(t, key, &v)
	return v
}

// Text is field key as Rich text.
func (r Record) Text(t testing.TB, key string) Text {
	t.Helper()
	var x Text
	r.Decode(t, key, &x)
	return x
}

// Override is field key as a format override: nil (none), an int (digits) or a
// string (a format spec).
func (r Record) Override(t testing.TB, key string) any {
	t.Helper()
	o, err := DecodeOverride(r[key])
	if err != nil {
		t.Fatalf("golden: %s: field %q: %v", r.ID(), key, err)
	}
	return o
}

// DecodeOverride decodes an override: null, an int or a string.
func DecodeOverride(raw json.RawMessage) (any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		err := json.Unmarshal(raw, &s)
		return s, err
	}
	var n int
	err := json.Unmarshal(raw, &n)
	return n, err
}
