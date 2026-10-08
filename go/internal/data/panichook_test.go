package data

import "testing"

func TestPanicHook(t *testing.T) {
	called := false
	PanicHook = func() { called = true }
	defer func() { PanicHook = nil }()
	func() {
		defer func() {
			if r := recover(); r != "boom" {
				t.Errorf("the panic didn't go on: %v", r)
			}
		}()
		func() {
			defer repanic()
			panic("boom")
		}()
	}()
	if !called {
		t.Error("PanicHook not called")
	}
}
