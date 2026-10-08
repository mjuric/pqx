// Command pqx is the Go prototype of pqx, a terminal explorer for Parquet files.
package main

import (
	"fmt"
	"os"
)

var version = "0.0.0-proto" // set with -ldflags "-X main.version=..."

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("pqx (Go prototype) %s\n", version)
		return
	}
	fmt.Fprintln(os.Stderr, "pqx: the UI is not wired up yet")
	os.Exit(2)
}
