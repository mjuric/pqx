package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	cases := []struct {
		args     []string
		code     int
		out, err string
	}{
		{[]string{"--version"}, 0, "pqx (Go prototype) " + version + "\n", ""},
		{[]string{"-version"}, 0, "pqx (Go prototype) ", ""},
		{[]string{"--help"}, 0, "usage: pqx [OPTIONS] FILE", ""},
		{[]string{"-h"}, 0, "usage: pqx", ""},
		{[]string{}, 2, "", "expected one FILE"},
		{[]string{"a", "b"}, 2, "", "expected one FILE"},
		{[]string{"--bogus", "f"}, 2, "", "flag provided but not defined"},
		{[]string{"--threads", "x", "f"}, 2, "", "invalid value"},
		{[]string{"--threads", "-1", "f"}, 2, "", "--threads must be 0 or more"},
		{[]string{"--threads", "4", "/nonexistent/file.parquet"}, 1, "", "pqx: /nonexistent/file.parquet: "},
		{[]string{"/nonexistent/file.parquet", "--threads", "4"}, 1, "", "pqx: /nonexistent/file.parquet: "},
		{[]string{"/nonexistent/file.parquet", "--threads=4"}, 1, "", "pqx: /nonexistent/file.parquet: "},
		{[]string{"/nonexistent/f", "--version"}, 0, "pqx (Go prototype)", ""},
		{[]string{"/nonexistent/f", "--threads", "-1"}, 2, "", "--threads must be 0 or more"},
		{[]string{"--", "-odd-name.parquet"}, 1, "", "pqx: -odd-name.parquet: "},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		code := run(c.args, &out, &errb)
		if code != c.code || !strings.Contains(out.String(), c.out) || !strings.Contains(errb.String(), c.err) {
			t.Errorf("run(%q) = %d, stdout %q, stderr %q", c.args, code, out.String(), errb.String())
		}
	}
	var out bytes.Buffer
	run([]string{"--version"}, &out, &out)
	for _, b := range out.Bytes() {
		if b >= 0x80 {
			t.Errorf("--version output isn't ASCII: %q", out.String())
		}
	}
}
