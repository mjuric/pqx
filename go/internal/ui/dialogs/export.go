package dialogs

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/chrome"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// exportDialog asks where and how to write the current view (Python's
// ExportScreen and action_export), then writes it as the user task
// "export" and reports with a notice.
type exportDialog struct {
	env     *kit.Env
	summary string
	view    data.View
	visible []string // the grid's visible columns, in order
	path    *field
	format  data.ExportFormat
	onlyVis bool
	over    bool
	focus   int // exPath … exCancel

	rows    map[int]int // dialog row → the control drawn there
	buttonY int
	buttons [][2]int
	pathY   int
}

const (
	exPath = iota
	exFormat
	exVisible
	exOverwrite
	exExport
	exCancel
	exControls
)

var formatNames = []string{"Parquet (zstd)", "CSV", "JSON (newline-delimited)"}
var formatExts = []string{".parquet", ".csv", ".json"}

func newExport(env *kit.Env) *exportDialog {
	st, ds := env.State, env.DS
	base := baseName(ds.Path())
	stem := fmtx.Sanitize(strings.TrimSuffix(base, splitExt(base)), false) // (no control characters in the box)
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	v := st.View
	desc := ""
	switch {
	case v.Plain():
		desc = "all rows"
	case v.IsSQL():
		desc = "SQL result"
	case strings.TrimSpace(v.Where) != "":
		desc = "where " + fmtx.Sanitize(v.Where, false)
	}
	if !v.IsSQL() && len(v.OrderBy) > 0 {
		desc += " · sorted by " + fmtx.Sanitize(v.OrderBy[0].Column, false)
	}
	n := "row count pending"
	if st.Total >= 0 {
		n = chrome.Commas(st.Total) + " rows"
	}
	var vis []string
	for _, c := range st.Columns {
		if !st.Hidden[c.Name] {
			vis = append(vis, c.Name)
		}
	}
	d := &exportDialog{env: env, view: v, visible: vis, onlyVis: true,
		summary: desc + " · " + n + " · " + strconv.Itoa(len(vis)) + " visible columns",
		path:    newField(filepath.Join(cwd, stem+".subset.parquet"), "")}
	d.path.focus()
	return d
}

// splitExt is Python's os.path.splitext's extension: from the last dot of
// the name, not counting leading dots.
func splitExt(name string) string {
	trimmed := strings.TrimLeft(name, ".")
	i := strings.LastIndexByte(trimmed, '.')
	if i < 0 {
		return ""
	}
	return trimmed[i:]
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (d *exportDialog) summaryLines(iw int) []string {
	var out []string
	for _, l := range wrapText(styled.New(fmtx.Sanitize(d.summary, false), d.env.Look.Style("dim")), iw) {
		out = append(out, d.env.Look.Render(l))
	}
	return out
}

func (d *exportDialog) Size(w, h int) (int, int) {
	dw := min(dialogW, w*95/100)
	// title, summary, path (3), blank + 3 formats, blank + check box twice,
	// blank + buttons
	return dialogSize(dialogW, 1+len(d.summaryLines(innerW(dw)))+3+4+2+2+2, w, h)
}

func (d *exportDialog) Keys() []kit.KeyHint { return nil }

func (d *exportDialog) View(w, h int) string {
	look := d.env.Look
	iw := innerW(w)
	d.rows = map[int]int{}
	content := []string{text(look, "Export current view", styled.Style{Bold: true})}
	content = append(content, d.summaryLines(iw)...)
	d.pathY = 1 + padY + len(content)
	for i := 0; i < 3; i++ {
		d.rows[d.pathY+i] = exPath
	}
	content = append(content, d.path.lines(look, iw)...)
	add := func(control int, line string) {
		d.rows[1+padY+len(content)] = control
		content = append(content, line)
	}
	content = append(content, "")
	for i, name := range formatNames {
		add(exFormat*100+i, toggle(look, "●", name, data.ExportFormat(i) == d.format,
			d.focus == exFormat && data.ExportFormat(i) == d.format))
	}
	content = append(content, "")
	add(exVisible, toggle(look, "X", "Only the visible columns", d.onlyVis, d.focus == exVisible))
	content = append(content, "")
	add(exOverwrite, toggle(look, "X", "Overwrite if the file exists", d.over, d.focus == exOverwrite))
	content = append(content, "")
	row, spans := buttonsRow(look, iw, []string{"Export", "Cancel"}, 0, d.focus-exExport)
	content = append(content, row)
	d.buttonY = 1 + padY + len(content) - 1
	d.buttons = d.buttons[:0]
	for _, s := range spans {
		d.buttons = append(d.buttons, [2]int{s[0] + 1 + padX, s[1] + 1 + padX})
	}
	return box(look, content, w, h)
}

func (d *exportDialog) Cursor() *tea.Cursor { return d.path.cursor(1+padX, d.pathY) }

func (d *exportDialog) setFocus(f int) {
	d.focus = (f + exControls) % exControls
	if d.focus == exPath {
		d.path.focus()
	} else {
		d.path.blur()
	}
}

// setFormat picks a format and gives the path its extension, if it has one
// of the formats' (Python's fmt_changed).
func (d *exportDialog) setFormat(f data.ExportFormat) {
	d.format = f
	p := d.path.Value()
	ext := splitExt(baseName(p))
	switch strings.ToLower(ext) {
	case ".parquet", ".csv", ".json", ".ndjson", ".pq":
		d.path.SetValue(p[:len(p)-len(ext)] + formatExts[f])
	}
}

func (d *exportDialog) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		return d.key(m)
	case tea.PasteMsg:
		if d.focus == exPath {
			return d.path.update(m)
		}
	case tea.MouseClickMsg:
		return d.click(m)
	}
	return nil
}

func (d *exportDialog) key(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	switch s {
	case "esc":
		return closeWith()
	case "tab":
		d.setFocus(d.focus + 1)
		return nil
	case "shift+tab":
		d.setFocus(d.focus - 1)
		return nil
	}
	activate := s == "enter" || s == "space"
	switch d.focus {
	case exPath:
		if s == "enter" {
			return d.submit()
		}
		return d.path.update(k)
	case exFormat:
		switch s {
		case "up", "left":
			d.setFormat((d.format + 2) % 3)
		case "down", "right":
			d.setFormat((d.format + 1) % 3)
		}
	case exVisible:
		if activate {
			d.onlyVis = !d.onlyVis
		}
	case exOverwrite:
		if activate {
			d.over = !d.over
		}
	case exExport:
		if activate {
			return d.submit()
		}
	case exCancel:
		if activate {
			return closeWith()
		}
	}
	return nil
}

func (d *exportDialog) click(m tea.MouseClickMsg) tea.Cmd {
	x, y, ok := clicked(m)
	if !ok {
		return nil
	}
	if y == d.buttonY {
		for i, b := range d.buttons {
			if x >= b[0] && x < b[1] {
				d.setFocus(exExport + i)
				if i == 0 {
					return d.submit()
				}
				return closeWith()
			}
		}
		return nil
	}
	c, ok := d.rows[y]
	if !ok {
		return nil
	}
	switch {
	case c >= exFormat*100:
		d.setFocus(exFormat)
		d.setFormat(data.ExportFormat(c - exFormat*100))
	case c == exVisible:
		d.setFocus(c)
		d.onlyVis = !d.onlyVis
	case c == exOverwrite:
		d.setFocus(c)
		d.over = !d.over
	default:
		d.setFocus(c)
	}
	return nil
}

// expandUser is Python's os.path.expanduser for "~" and "~/…".
func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

// submit checks the path and starts the export (Python's do_export and
// export_worker).
func (d *exportDialog) submit() tea.Cmd {
	path := strings.TrimSpace(d.path.Value())
	if path == "" {
		return kit.Send(kit.NotifyMsg{Severity: kit.Warning, Text: "Enter a file name"})
	}
	full, err := filepath.Abs(expandUser(path))
	if err != nil {
		full = path
	}
	if _, err := os.Stat(full); err == nil && !d.over {
		return kit.Send(kit.NotifyMsg{Severity: kit.Warning,
			Text: fmtx.Sanitize(path, false) + " exists — tick 'Overwrite' to replace it"})
	}
	var cols []string
	if d.onlyVis {
		cols = append(cols, d.visible...)
	}
	ds, view, format := d.env.DS, d.view, d.format
	run := d.env.Tasks.Run("export", "exporting", true, func(ctx context.Context) tea.Msg {
		return exportFile(ctx, ds, view, full, format, cols)
	})
	return tea.Sequence(kit.Send(kit.CloseDialogMsg{}), run)
}

// exportFile writes the view and returns the notice to show (handed to the
// chrome in the task's DoneMsg).
func exportFile(ctx context.Context, ds data.Dataset, v data.View, path string, f data.ExportFormat, cols []string) tea.Msg {
	t0 := time.Now()
	n, err := ds.Export(ctx, v, path, f, cols)
	if err != nil {
		msg := fmtx.Sanitize(strings.TrimSpace(err.Error()), true)
		if r := []rune(msg); len(r) > 600 {
			msg = string(r[:600])
		}
		return kit.NotifyMsg{Severity: kit.Error, Title: "✗ Query failed", Text: msg, Timeout: 8 * time.Second}
	}
	size := "?"
	if fi, err := os.Stat(path); err == nil {
		size = fmtx.HumanBytes(float64(fi.Size()))
	}
	return kit.NotifyMsg{Severity: kit.Info, Timeout: 8 * time.Second,
		Text: "✓ Wrote " + fmtx.HumanCount(float64(n)) + " rows · " + size + " · " +
			strconv.FormatFloat(time.Since(t0).Seconds(), 'f', 1, 64) + " s\n→ " + fmtx.Sanitize(path, false)}
}
