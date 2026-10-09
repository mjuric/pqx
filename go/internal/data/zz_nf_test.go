package data

import (
	"fmt"
	"path/filepath"
	"testing"
)

var busy = 0

func TestZZNotFound(t *testing.T) {
	stop := make(chan struct{})
	for range busy {
		go func() {
			x := 0
			for {
				select {
				case <-stop:
					return
				default:
					for i := range 1 << 16 {
						x += i
					}
				}
			}
		}()
	}
	defer close(stop)
	bad := 0
	for i := range 300 {
		p := filepath.Join(t.TempDir(), fmt.Sprintf("f%d.parquet", i))
		writeInts(t, p, 1, 2, 3)
		ds, err := Open(p, Options{Threads: 2})
		if err != nil {
			t.Fatal(err)
		}
		if err := ds.SetupErr(); err != nil {
			bad++
			t.Log("setup", err)
		}
		for k := range 3 {
			if _, err := ds.Count(bg, View{Where: "a > 0"}); err != nil {
				bad++
				t.Log(k, err)
			}
		}
		ds.Close()
	}
	t.Logf("%d bad", bad)
}
