package data

import "sync/atomic"

var panicHook atomic.Pointer[func()]

// SetPanicHook sets the function run when one of this package's own
// goroutines panics, before the panic goes on and ends the process (nil for
// none). main sets it to term.RestoreTerminal, so a crash there leaves a
// usable terminal. Safe to call while those goroutines run.
func SetPanicHook(f func()) {
	if f == nil {
		panicHook.Store(nil)
		return
	}
	panicHook.Store(&f)
}

// repanic is deferred first in the package's goroutines.
func repanic() {
	if r := recover(); r != nil {
		if h := panicHook.Load(); h != nil {
			(*h)()
		}
		panic(r)
	}
}
