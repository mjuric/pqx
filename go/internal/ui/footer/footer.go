// Package footer runs the pass over the file's footer that the Schema and
// Metadata tabs need (sizes and statistics of every column chunk, the row
// groups), and holds the small text helpers both tabs share.
//
// The pass can take seconds on a file with thousands of row groups, so it
// waits for the grid's first page (Python pqx's FOOTER_WAIT): it starts on
// the grid's first CursorMsg or "page" DoneMsg, or FooterWait after the
// first WindowSizeMsg, whichever comes first. It runs as the task "footer",
// pqx's own work; Esc cancelling it starts it again.
package footer

import (
	"context"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Tag is the task's tag.
const Tag = "footer"

// Wait is how long the pass waits for the grid's first page at most.
const Wait = 2 * time.Second

// Result is the task's result (in a kit.DoneMsg with Tag).
type Result struct {
	Summary   []data.ChunkSummary
	RowGroups []data.RowGroup
	Err       error
}

// Reader starts the pass once. One part (Schema) passes it every message
// it gets; every part sees the result as the DoneMsg with Tag.
type Reader struct {
	env  *kit.Env
	Wait time.Duration

	timer bool // the wait's timer is set
	state int  // waiting, running, done
	gen   int  // the timer this reader set (a stale reader's timer is ignored)
}

const (
	waiting = iota
	running
	done
)

type waitMsg struct {
	r   *Reader
	gen int
}

// New makes a reader for env's dataset.
func New(env *kit.Env) *Reader { return &Reader{env: env, Wait: Wait} }

// Update looks at a broadcast message and returns the command to run, if
// any.
func (r *Reader) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		if r.state == waiting && !r.timer {
			r.timer = true
			r.gen++
			gen := r.gen
			return tea.Tick(r.Wait, func(time.Time) tea.Msg { return waitMsg{r, gen} })
		}
	case waitMsg:
		if m.r == r && m.gen == r.gen {
			return r.start()
		}
	case kit.CursorMsg:
		return r.start()
	case kit.DoneMsg:
		switch m.Tag {
		case "page":
			return r.start()
		case Tag:
			r.state = done
		}
	case kit.CancelledMsg:
		if r.state == running && slices.Contains(m.Tags, Tag) {
			r.state = waiting
			return r.start()
		}
	}
	return nil
}

// Start starts the pass now unless it has started.
func (r *Reader) Start() tea.Cmd { return r.start() }

func (r *Reader) start() tea.Cmd {
	if r.state != waiting {
		return nil
	}
	r.state = running
	ds := r.env.DS
	return r.env.Tasks.RunBackground(Tag, func(ctx context.Context) tea.Msg {
		summ, err := ds.FooterSummary(ctx)
		if err != nil {
			return Result{Err: err}
		}
		rgs, err := ds.RowGroupInfo(ctx)
		if err != nil {
			return Result{Err: err}
		}
		return Result{Summary: summ, RowGroups: rgs}
	})
}
