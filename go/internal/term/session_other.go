//go:build !(linux || darwin || dragonfly || freebsd || netbsd || openbsd)

package term

import (
	"os"
	"sync/atomic"
)

var active atomic.Pointer[Session]

// open on Windows (and other systems) leaves the input to Bubble Tea, which
// reads the console's input records itself (the mouse problems the filter solves are those of
// GNU screen and old Unix terminals), and only wraps the output.
func open(_, stdout *os.File) (*Session, error) {
	return &Session{out: NewOutput(stdout)}, nil
}
