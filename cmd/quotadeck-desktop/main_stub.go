//go:build !desktop

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "quotadeck-desktop must be built with the desktop tag; use `make desktop-build`.")
	os.Exit(1)
}
