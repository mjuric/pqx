package data

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
)

// Python's re.match(r"^\s*\[([^\]]*)\]\s*(.*)$", s) on each (from Python).
func TestFelis(t *testing.T) {
	for _, c := range []struct {
		s          string
		unit, rest string
		ok         bool
	}{
		{"[deg] Right ascension", "deg", "Right ascension", true},
		{"  [mag]  text\n", "mag", "text", true},
		{"[x]a\nb", "", "", false},
		{"[]empty", "", "empty", true},
		{"no unit", "", "", false},
		{"[unclosed text", "", "", false},
		{"　[nm] wave", "nm", "wave", true},
		{"\x1c[s] t", "s", "t", true},
		{"[a]b]c", "a", "b]c", true},
		{"[u] line\n\n", "", "", false},
		{"[u]\n", "u", "", true},
		{"[u] \n x", "u", "x", true},
		{"[mult\nline] ok", "mult\nline", "ok", true},
	} {
		unit, rest, ok := felis(c.s)
		if unit != c.unit || rest != c.rest || ok != c.ok {
			t.Errorf("felis(%q) = %q %q %v, want %q %q %v", c.s, unit, rest, ok, c.unit, c.rest, c.ok)
		}
	}
}

// Ported from tests/test_data.py::test_open_and_schema (units and
// descriptions).
func TestUnitsAndDescriptions(t *testing.T) {
	_, ds := demoDataset(t)
	want := map[string][2]string{
		"diaSourceId": {"", "Unique identifier."},
		"band":        {"", "Filter band."},
		"mag":         {"mag", "PSF magnitude."},
		"psfFlux":     {"nJy", "[ignored] Flux."}, // a unit key wins over the description's
		"ingestTime":  {"", ""},
		"ra":          {"deg", "Right ascension."},
		"dec":         {"deg", "Declination."},
	}
	for _, c := range ds.Columns() {
		if w, ok := want[c.Name]; ok && (c.Unit != w[0] || c.Description != w[1]) {
			t.Errorf("%s: %q %q, want %q %q", c.Name, c.Unit, c.Description, w[0], w[1])
		}
	}
	md := arrow.NewMetadata([]string{"comment", "description", "units", "unit"}, []string{"c", "", "u2", ""})
	if u, d := unitDesc(md); u != "u2" || d != "c" {
		t.Errorf("empty keys fall through: %q %q", u, d)
	}
}

func TestKeyValueMetadata(t *testing.T) {
	_, ds := demoDataset(t)
	kv := ds.KeyValueMetadata()
	raw := ds.md.FileMetaData.KeyValueMetadata
	if len(kv) != len(raw) || len(kv) < 2 {
		t.Fatalf("%d entries, the footer has %d", len(kv), len(raw))
	}
	// every footer entry, sorted by key as PyArrow gives them
	want := map[string]string{}
	for _, e := range raw {
		want[e.Key] = e.GetValue()
	}
	for i, e := range kv {
		if i > 0 && kv[i-1].Key >= e.Key {
			t.Errorf("not sorted at %d: %q after %q", i, e.Key, kv[i-1].Key)
		}
		if want[e.Key] != e.Value {
			t.Errorf("%q: value differs from the footer's", e.Key)
		}
	}
	// a repeated key: listed once, with its last value
	dup := *raw[0]
	v := "second"
	dup.Value = &v
	ds.md.FileMetaData.KeyValueMetadata = append(raw, &dup)
	kv2 := ds.KeyValueMetadata()
	if len(kv2) != len(kv) {
		t.Fatalf("repeated key: %+v", kv2)
	}
	for _, e := range kv2 {
		if e.Key == raw[0].Key && e.Value != "second" {
			t.Errorf("repeated key %q has %q, want its last value", e.Key, e.Value)
		}
	}
}
