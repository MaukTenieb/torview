//go:build nowebview

// Package main — torview: minimal Tor webview browser.
//
// headless.go: `-tags nowebview` build. No window in this mode (it exists so
// the Tor path can be exercised on machines without any GUI stack, and so
// CI can build without GUI headers). `--smoke` is the supported entry.
package main

import (
	"fmt"
	"os"
)

// runWebview is a stub for the no-window build: the GUI entry is not
// compiled in this mode (use it only via --smoke).
func runWebview(*torManager, string) {
	fmt.Fprintln(os.Stderr, "mode fenêtre indisponible dans ce build (-tags nowebview)")
	os.Exit(3)
}
