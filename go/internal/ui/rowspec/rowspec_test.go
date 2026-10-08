package rowspec

import "testing"

func TestParseRowSpec(t *testing.T) {
	ok := []struct {
		spec  string
		total int64
		want  int64
	}{
		// the cases in tests/test_data.py
		{"1.5M", 10_000_000, 1_500_000},
		{"50%", 1000, 500},
		{"-1", 1000, 999},
		{"1,234", 1_000_000, 1234},
		{"10k", 100, 99},
		// more
		{"1234", 10_000, 1234},
		{" 1_000 ", 10_000, 1000},
		{"2k", 10_000, 2000},
		{"2K", 10_000, 2000},
		{"1b", 2e9, 1e9},
		{"1g", 2e9, 1e9},
		{"0", 10, 0},
		{"-10", 100, 90},
		{"-1000", 100, 0},
		{"100%", 1000, 999},
		{"0%", 1000, 0},
		{"150%", 1000, 999},
		{"-5%", 1000, 0},
		{"12.9", 100, 12},
		{"1e3", 10_000, 1000},
		{"5", 0, 0},
		{"50%", 0, 0},
	}
	for _, c := range ok {
		got, err := ParseRowSpec(c.spec, c.total)
		if err != nil || got != c.want {
			t.Errorf("ParseRowSpec(%q, %d) = %d, %v; want %d", c.spec, c.total, got, err, c.want)
		}
	}
	for _, spec := range []string{"abc", "", "  ", "%", "k", "nan", "inf", "-inf%", "1.2.3", "12x"} {
		if got, err := ParseRowSpec(spec, 10); err == nil {
			t.Errorf("ParseRowSpec(%q) = %d, want an error", spec, got)
		}
	}
}
