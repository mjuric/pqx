package data

import "testing"

func TestPanicHook(t *testing.T) {
	called := false
	SetPanicHook(func() { called = true })
	defer SetPanicHook(nil)
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
