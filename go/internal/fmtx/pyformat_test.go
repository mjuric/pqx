package fmtx

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mjuric/pqx/go/internal/golden"
)

// TestPyFormat checks format(v, spec) against Python's outputs on ~20,000
// specs, values and strftime formats (testdata/pyformat.json, written by
// testdata/make_pyformat.py).
func TestPyFormat(t *testing.T) {
	b, err := os.ReadFile("testdata/pyformat.json")
	if err != nil {
		t.Fatal(err)
	}
	var recs []struct {
		V     golden.Value `json:"v"`
		Spec  string       `json:"spec"`
		Out   *string      `json:"out"`
		Error string       `json:"error"`
	}
	if err := json.Unmarshal(b, &recs); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, r := range recs {
		v := toValue(t, r.V)
		got, gerr := pyFormat(v, r.Spec)
		if gerr == errTooBig && r.Out == nil {
			continue // Python fails too, with a message of its own (pqx's checks stop at 64 first)
		}
		switch {
		case r.Out != nil:
			if gerr != nil || got != *r.Out {
				t.Errorf("format(%v, %q) = %q, %v; want %q", r.V, r.Spec, got, gerr, *r.Out)
				bad++
			}
		default:
			typ, msg, _ := strings.Cut(r.Error, ": ")
			switch {
			case gerr == nil:
				t.Errorf("format(%v, %q) = %q; want %s", r.V, r.Spec, got, r.Error)
				bad++
			case typ != "OverflowError" && gerr.Error() != msg:
				t.Errorf("format(%v, %q): error %q; want %q", r.V, r.Spec, gerr, msg)
				bad++
			}
		}
		if bad > 100000 {
			t.Fatal("too many")
		}
	}
	t.Logf("%d records", len(recs))
}

// TestPyValues checks format_value, derived and the human numbers against
// Python on random numbers (testdata/pyvalues.json).
func TestPyValues(t *testing.T) {
	b, err := os.ReadFile("testdata/pyvalues.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		FormatValue []struct {
			V        golden.Value    `json:"v"`
			Kind     string          `json:"kind"`
			Raw      bool            `json:"raw"`
			Override json.RawMessage `json:"override"`
			Out      string          `json:"out"`
		} `json:"format_value"`
		Derived []struct {
			Name, Kind, Unit, Out string
			V                     golden.Value
		} `json:"derived"`
		Human []struct {
			N           float64 `json:"n"`
			Count       string  `json:"count"`
			B           float64 `json:"b"`
			Bytes       string  `json:"bytes"`
			Part, Whole float64
			PercentText string `json:"percent"`
		} `json:"human"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	for _, r := range d.FormatValue {
		o, _ := golden.DecodeOverride(r.Override)
		got := Format(toValue(t, r.V), Kind(r.Kind), Opts{Raw: r.Raw, Width: DefaultWidth, Override: toOverride(o)})
		if got != r.Out {
			t.Errorf("format_value(%v, %s, raw=%v, %v) = %q, want %q", r.V, r.Kind, r.Raw, o, got, r.Out)
		}
	}
	for _, r := range d.Derived {
		if got := Derived(r.Name, Kind(r.Kind), toValue(t, r.V), r.Unit); got != r.Out {
			t.Errorf("derived(%q, %s, %v, %q) = %q, want %q", r.Name, r.Kind, r.V, r.Unit, got, r.Out)
		}
	}
	for _, r := range d.Human {
		if got := HumanCount(r.N); got != r.Count {
			t.Errorf("human_count(%v) = %q, want %q", r.N, got, r.Count)
		}
		if got := HumanBytes(r.B); got != r.Bytes {
			t.Errorf("human_bytes(%v) = %q, want %q", r.B, got, r.Bytes)
		}
		if got := Percent(r.Part, r.Whole); got != r.PercentText {
			t.Errorf("percent(%v, %v) = %q, want %q", r.Part, r.Whole, got, r.PercentText)
		}
	}
}
