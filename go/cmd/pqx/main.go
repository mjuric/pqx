// Command pqx is a terminal explorer for Parquet files.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/opts"
	"github.com/mjuric/pqx/go/internal/term"
	"github.com/mjuric/pqx/go/internal/theme"
	"github.com/mjuric/pqx/go/internal/ui"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/chrome"
	"github.com/mjuric/pqx/go/internal/ui/dialogs"
	"github.com/mjuric/pqx/go/internal/ui/footer"
	"github.com/mjuric/pqx/go/internal/ui/kit"
	"github.com/mjuric/pqx/go/internal/ui/meta"
	"github.com/mjuric/pqx/go/internal/ui/plot"
	"github.com/mjuric/pqx/go/internal/ui/schema"
	"github.com/mjuric/pqx/go/internal/ui/stats"
)

var version = "0.0.0.dev0" // set with -ldflags "-X main.version=..."

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var out *os.File
	if f, ok := stdout.(*os.File); ok {
		out = f
	}
	width := opts.Width(os.Getenv, out)
	o, err := opts.Parse(args, os.Getenv)
	var ue *opts.UsageError
	switch {
	case errors.Is(err, opts.ErrHelp):
		fmt.Fprint(stdout, opts.Help(width))
		return 0
	case errors.Is(err, opts.ErrVersion):
		fmt.Fprintln(stdout, opts.VersionText(version))
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "%spqx: error: %s\n", opts.Usage(width), ue.Msg)
		return 2
	case err != nil:
		fmt.Fprintf(stderr, "pqx: %s\n", fmtx.Sanitize(err.Error(), false))
		return 2
	}
	path := fmtx.Sanitize(o.Path, false)
	st, err := os.Stat(o.Path)
	if err != nil { // as os.path.exists
		fmt.Fprintf(stderr, "pqx: %s: no such file\n", path)
		return 2
	}
	cantOpen := func(err error) int {
		// a file-system error names the file again; say it once
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		fmt.Fprintf(stderr, "pqx: cannot open %s: %s\n", path, fmtx.Sanitize(err.Error(), false))
		return 1
	}
	switch {
	case o.Threads < 0:
		return cantOpen(errors.New("--threads must be at least 1 (or 0 for all cores)"))
	case o.Threads > maxThreads:
		return cantOpen(fmt.Errorf("--threads must be at most %d", maxThreads))
	}
	ds, err := data.Open(o.Path, data.Options{Threads: o.Threads})
	if err != nil {
		return cantOpen(err)
	}
	defer ds.Close()
	look, err := theme.New(o.Accent, o.Dim, o.Border, o.Theme)
	if err != nil {
		return cantOpen(err) // (Parse has checked these)
	}
	sampling := ds.NumRows() > autoSampleRows || st.Size() > autoSampleBytes
	if o.Sample != nil {
		sampling = *o.Sample
	}
	env := &kit.Env{
		DS: ds,
		Opts: kit.Options{
			Where:          o.Where,
			Sampling:       sampling,
			SessionFormats: o.Formats,
			Version:        version,
		},
		Look:  look,
		State: &kit.State{Total: ds.NumRows(), Columns: ds.Columns(), FileRow: 0},
		Tasks: kit.NewTasks(),
	}
	env.Dialogs = dialogs.New(env)
	parts := app.Parts{
		Chrome: chrome.New(env),
		Legacy: ui.Legacy{M: ui.New(ds)},
		Schema: schema.New(env, footer.New(env)),
		Meta:   meta.New(env),
		Stats:  stats.New(env),
		Plot:   plot.New(env),
	}
	return runApp(app.New(env, parts), stderr)
}

// maxThreads is the most DuckDB threads --threads may ask for.
const maxThreads = 4096

// Sampling is on by default above this many rows or this file size
// (app.py's AUTO_SAMPLE_ROWS and AUTO_SAMPLE_BYTES).
const (
	autoSampleRows  = 200_000_000
	autoSampleBytes = 8 << 30
)

// runApp runs the app on the terminal, through the input filter and the
// output wrapper, and puts the terminal back however it ends.
func runApp(m tea.Model, stderr io.Writer) (code int) {
	s, err := term.Open()
	if err != nil {
		fmt.Fprintf(stderr, "pqx: %v\n", err)
		return 1
	}
	defer s.Close()
	defer func() {
		// Bubble Tea recovers panics in the app and its commands; this is
		// for the rest of this goroutine
		if r := recover(); r != nil {
			s.Close()
			fmt.Fprintf(stderr, "pqx: internal error: %v\n\n%s", r, debug.Stack())
			code = 1
		}
	}()
	if _, err := tea.NewProgram(m, s.ProgramOptions()...).Run(); err != nil {
		s.Close()
		if !errors.Is(err, tea.ErrProgramPanic) { // Bubble Tea has printed the panic
			fmt.Fprintf(stderr, "pqx: %s\n", fmtx.Sanitize(err.Error(), false))
		}
		return 1
	}
	return 0
}
