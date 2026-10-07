//go:build !windows

// Package main — torview: minimal Tor webview browser.
//
// control_other.go: no special process attributes for the Tor child on
// Unix; the daemon inherits our process group and dies with us via
// TAKEOWNERSHIP/QUIT (and prctl PDEATHSIG would be redundant here).
package main

import "syscall"

func platformProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: false} }
