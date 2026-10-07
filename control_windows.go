//go:build windows

// Package main — torview: minimal Tor webview browser.
//
// control_windows.go: give the Tor child its own hidden console
// (CREATE_NO_WINDOW). This detaches it from our console (and from the
// ephemeral console a -H windowsgui binary otherwise induces), so stray
// console ctrl events cannot kill the daemon mid-bootstrap — the same trick
// the official Tor Browser launcher uses. Our own SIGINT handler + QUIT
// remain the single shutdown path.
package main

import "syscall"

const createNoWindow = 0x08000000

func platformProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}
