// Command pqx is the Go prototype of pqx, a terminal explorer for Parquet files.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui"
)

var version = "0.0.0-proto" // set with -ldflags "-X main.version=..."

const usage = `usage: pqx [OPTIONS] FILE

Explore a Parquet file in the terminal (Go prototype).

options:
  --threads N   DuckDB threads (default: DuckDB's own choice)
  --version     print the version and exit
  -h, --help    print this help and exit

keys: / filter, x clear filter, g go to row (1234, 1.5M, 50%, -1),
arrows PgUp PgDn Home End Ctrl+Home Ctrl+End move, Esc cancel, q quit.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pqx", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	threads := fs.Int("threads", 0, "")
	showVersion := fs.Bool("version", false, "")
	help := fs.Bool("help", false, "")
	fs.BoolVar(help, "h", false, "")
	if err := fs.Parse(flagsFirst(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		fmt.Fprintf(stderr, "pqx: %v\n\n%s", err, usage)
		return 2
	}
	switch {
	case *help:
		fmt.Fprint(stdout, usage)
		return 0
	case *showVersion:
		fmt.Fprintf(stdout, "pqx (Go prototype) %s\n", version)
		return 0
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "pqx: expected one FILE\n\n%s", usage)
		return 2
	}
	if *threads < 0 {
		fmt.Fprintln(stderr, "pqx: --threads must be 0 or more")
		return 2
	}
	path := fs.Arg(0)
	ds, err := data.Open(path, data.Options{Threads: *threads})
	if err != nil {
		// For file-system errors, say "pqx: FILE: reason" once; Open's own
		// message would repeat the (absolute) path. Other errors from Open already
		// name the file.
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		if msg := err.Error(); strings.Contains(msg, path) {
			fmt.Fprintf(stderr, "pqx: %s\n", msg) // already sanitized and names the file
		} else {
			fmt.Fprintf(stderr, "pqx: %s: %s\n", data.Sanitize(path), msg)
		}
		return 1
	}
	defer ds.Close()
	p := tea.NewProgram(ui.New(ds))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(stderr, "pqx: %v\n", err)
		return 1
	}
	return 0
}

// flagsFirst moves options ahead of the file name, so that "pqx FILE
// --threads 4" works as well as "pqx --threads 4 FILE" (the flag package
// stops at the first argument that isn't an option). "--" ends the options.
func flagsFirst(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			rest = append(rest, args[i+1:]...)
			i = len(args)
		case len(a) > 1 && a[0] == '-':
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if name == "threads" && i+1 < len(args) { // the one option with a value
				i++
				flags = append(flags, args[i])
			}
		default:
			rest = append(rest, a)
		}
	}
	return append(append(flags, "--"), rest...)
}
