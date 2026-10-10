package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/chrome"
	"github.com/mjuric/pqx/go/internal/ui/filter"
	"github.com/mjuric/pqx/go/internal/ui/grid"
	"github.com/mjuric/pqx/go/internal/ui/kit"
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
	env := &kit.Env{DS: ds, Look: app.BasicLook{}, Tasks: kit.NewTasks()}
	env.State = newState(ds, env.Opts)
	m := app.New(env, app.Parts{Grid: grid.New(env), Filter: filter.New(env), Chrome: chrome.New(env)})
	code := runApp(panicky{m}, nil, os.Stderr)
	// the terminal is out of raw mode again: a newline is a newline
	fmt.Print("after\nexit\n")
	return code
}
