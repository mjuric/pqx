// Command pqx is the Go prototype of pqx, a terminal explorer for Parquet files.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

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
	if err := fs.Parse(args); err != nil {
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
		fmt.Fprintf(stderr, "pqx: %s: %v\n", path, err)
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
