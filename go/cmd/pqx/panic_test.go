package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui"
)

// panicky is the app with a key that panics, for the crash test.
type panicky struct{ tea.Model }

func (p panicky) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "P" {
		panic("test panic")
	}
	m, cmd := p.Model.Update(msg)
	return panicky{m}, cmd
}

// panicMain is pqx with panicky (PQX_TEST_MAIN=panic).
func panicMain(path string) int {
	ds, err := data.Open(path, data.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	defer ds.Close()
	code := runApp(panicky{ui.New(ds)}, os.Stderr)
	// the terminal is out of raw mode again: a newline is a newline
	fmt.Print("after\nexit\n")
	return code
}
