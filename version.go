// Package main — torview: minimal Tor webview browser.
//
// version.go: `--version` banner. Packagers may override appVersion and
// buildCommit with -ldflags "-X main.appVersion=... -X main.buildCommit=...".
package main

import (
	"fmt"
	"runtime"
)

var (
	appVersion  = "0.5.0"
	buildCommit = "unset"
)

func printVersion() {
	fmt.Printf("TorView %s (commit %s, %s %s/%s)\n",
		appVersion, buildCommit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
