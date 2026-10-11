package term

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// keysModel records the messages it gets.
type keysModel struct{ got *[]tea.Msg }

func (m keysModel) Init() tea.Cmd { return nil }
func (m keysModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	*m.got = append(*m.got, msg)
	return m, nil
}
func (m keysModel) View() tea.View { return tea.NewView("") }

// Ctrl+Z asks for a suspend and never reaches the app; other keys do.
func TestCtrlZRequestsSuspend(t *testing.T) {
	var got []tea.Msg
	req := make(chan struct{}, 1)
	var m tea.Model = ctrlZ{keysModel{&got}, req}
	ctrlz := tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl}
	if ctrlz.String() != "ctrl+z" {
		t.Fatalf("key %q", ctrlz.String())
	}
	m, cmd := m.Update(ctrlz)
	if cmd != nil || len(got) != 0 {
		t.Fatalf("Ctrl+Z reached the app: %v, cmd %v", got, cmd != nil)
	}
	select {
	case <-req:
	default:
		t.Fatal("no suspend request")
	}
	// a second one while one is pending doesn't block
	m, _ = m.Update(ctrlz)
	m, _ = m.Update(ctrlz)
	if len(req) != 1 {
		t.Fatalf("%d requests pending", len(req))
	}
	<-req
	z := tea.KeyPressMsg{Code: 'z', Text: "z"}
	m, _ = m.Update(z)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if _, ok := m.(ctrlZ); !ok {
		t.Fatalf("the wrapper was dropped: %T", m)
	}
	if len(got) != 2 || got[0] != tea.Msg(z) || len(req) != 0 {
		t.Fatalf("other keys: %v, %d requests", got, len(req))
	}
}
