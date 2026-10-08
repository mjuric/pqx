package kit

import (
	"context"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Tasks runs background work (Python pqx's workers and tagged cursors).
// Each task has a tag ("page", "count", "cols", "detail", "locate",
// "validate", "stats", "plot", "export", "footer", …); starting a task
// cancels the running one with the same tag. User tasks are the ones the
// user asked for and waits on (a lookup, stats, a plot, an export); pqx's
// own loading (rows, columns, the count) is not. Esc closes the details pane
// unless a user task runs, and cancels everything while anything runs.
//
// Tasks is used only on the Update goroutine.
type Tasks struct {
	next    int
	running map[string]*task
}

type task struct {
	id         int
	label      string
	user       bool
	background bool
	started    time.Time
	cancel     context.CancelFunc
}

// DoneMsg carries a task's result. The root passes it on only if the task is
// still the current one for its tag (Done); a stale or cancelled task's
// result is dropped (Esc's cancellations are announced with CancelledMsg).
type DoneMsg struct {
	Tag string
	ID  int
	Msg tea.Msg // what the task's function returned
}

// NewTasks makes an empty registry.
func NewTasks() *Tasks { return &Tasks{running: map[string]*task{}} }

// Run starts fn on its own goroutine under tag, cancelling the previous task
// with that tag. label is shown in the status line while it runs ("counting
// rows", "profiling ra"). fn gets a context cancelled by Cancel, CancelAll
// or a newer task with the same tag; its result arrives as a DoneMsg.
func (t *Tasks) Run(tag, label string, user bool, fn func(ctx context.Context) tea.Msg) tea.Cmd {
	t.Cancel(tag)
	t.next++
	id := t.next
	ctx, cancel := context.WithCancel(context.Background())
	t.running[tag] = &task{id: id, label: label, user: user, started: time.Now(), cancel: cancel}
	return func() tea.Msg {
		return DoneMsg{Tag: tag, ID: id, Msg: fn(ctx)}
	}
}

// RunBackground is Run for work that is neither the user's nor shown as
// busy (the footer scan): it doesn't count for Busy, BusyUser or List, and
// Esc's CancelAll leaves it running. Cancel and Stop cancel it.
func (t *Tasks) RunBackground(tag string, fn func(ctx context.Context) tea.Msg) tea.Cmd {
	cmd := t.Run(tag, "", false, fn)
	t.running[tag].background = true
	return cmd
}

// Done is called by the root with each DoneMsg: it reports whether the
// result is current (and marks the task finished).
func (t *Tasks) Done(m DoneMsg) bool {
	r, ok := t.running[m.Tag]
	if !ok || r.id != m.ID {
		return false
	}
	r.cancel()
	delete(t.running, m.Tag)
	return true
}

// Cancel cancels the task with tag, if any; its result is dropped.
func (t *Tasks) Cancel(tag string) {
	if r, ok := t.running[tag]; ok {
		r.cancel()
		delete(t.running, tag)
	}
}

// CancelAll cancels every task (Esc) and returns the tags cancelled, so the
// parts can note what to retry (CancelledMsg is broadcast with them).
func (t *Tasks) CancelAll() []string {
	tags := make([]string, 0, len(t.running))
	for tag, r := range t.running {
		if !r.background {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	for _, tag := range tags {
		t.Cancel(tag)
	}
	return tags
}

// Stop cancels every task, background ones too (quit).
func (t *Tasks) Stop() {
	for tag := range t.running {
		t.Cancel(tag)
	}
}

// CancelledMsg is broadcast after Esc cancelled the tasks Tags.
type CancelledMsg struct{ Tags []string }

// Running reports whether a task with tag runs.
func (t *Tasks) Running(tag string) bool { _, ok := t.running[tag]; return ok }

// Busy reports whether any task runs; BusyUser whether a user task does.
func (t *Tasks) Busy() bool {
	for _, r := range t.running {
		if !r.background {
			return true
		}
	}
	return false
}
func (t *Tasks) BusyUser() bool {
	for _, r := range t.running {
		if r.user && !r.background {
			return true
		}
	}
	return false
}

// TaskInfo describes a running task for the status line.
type TaskInfo struct {
	Tag, Label string
	User       bool
	Started    time.Time
}

// List is the running tasks, oldest first.
func (t *Tasks) List() []TaskInfo {
	out := make([]TaskInfo, 0, len(t.running))
	for tag, r := range t.running {
		if r.background {
			continue
		}
		out = append(out, TaskInfo{tag, r.label, r.user, r.started})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}
