package data

// PanicHook, if set, runs when one of this package's own goroutines
// panics, before the panic goes on and ends the process. main sets it to
// term.RestoreTerminal, so a crash there leaves a usable terminal.
var PanicHook func()

// repanic is deferred first in the package's goroutines.
func repanic() {
	if r := recover(); r != nil {
		if h := PanicHook; h != nil {
			h()
		}
		panic(r)
	}
}
