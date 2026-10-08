//go:build !nowebview

// Package main — torview: minimal Tor webview browser.
//
// webview.go: window + bindings. Only reached after the exit check passed
// and the transport guard is armed. Uses github.com/abemedia/go-webview
// (purego: no cgo, no gcc needed; the prebuilt native library is embedded
// via the `embedded` import).
//
// Threading note: binding callbacks run on a dedicated goroutine, NOT the
// UI thread. Any Go call that touches the view (SetHtml, Navigate, Eval)
// from a binding MUST be wrapped in w.Dispatch — that is why __torviewHome
// dispatches its SetHtml instead of calling it directly.
package main

import (
	"fmt"
	"sync"
	"time"

	webview "github.com/abemedia/go-webview"
	_ "github.com/abemedia/go-webview/embedded"
)

// nymCooldown is the minimum delay between two NEWNYM signals (Tor rate
// limits the control command; flooding it is at best useless).
const nymCooldown = 10 * time.Second

var (
	nymMu   sync.Mutex
	lastNym time.Time
)

// doNewIdentity performs NEWNYM with an anti-flood cooldown, shared by the
// chrome binding (__torviewNym) and the portal binding (torviewNewIdentity).
func doNewIdentity(m *torManager) (string, error) {
	nymMu.Lock()
	if left := nymCooldown - time.Since(lastNym); left > 0 {
		nymMu.Unlock()
		return "", fmt.Errorf("attendez %d s avant un nouveau circuit", int(left.Seconds())+1)
	}
	nymMu.Unlock()
	if err := m.NewIdentity(); err != nil {
		return "", err
	}
	nymMu.Lock()
	lastNym = time.Now()
	nymMu.Unlock()
	return "nouveau circuit Tor activé", nil
}

func runWebview(m *torManager, statusDetail string) {
	debug := false
	w := webview.New(debug)
	defer w.Destroy()

	w.SetTitle("TorView — Minimalist Tor Browser")
	w.SetSize(1024, 768, webview.HintNone)

	// Injected on EVERY page before onload: navigation chrome + WebRTC
	// kill-switch (see chrome.go and main.go's torJSShim).
	w.Init(chromeInitJS())

	// Bindings used by the injected chrome (any page).
	_ = w.Bind("__torviewNym", func() (string, error) {
		return doNewIdentity(m)
	})
	_ = w.Bind("__torviewHome", func() string {
		w.Dispatch(func() { w.SetHtml(portalHTML) })
		return ""
	})

	// Bindings of the built-in portal page.
	_ = w.Bind("torviewStatus", func() map[string]any {
		return map[string]any{
			"socks":   m.SocksAddr(),
			"exitOK":  true,
			"detail":  statusDetail,
			"torText": m.StatusText(),
		}
	})
	_ = w.Bind("torviewNewIdentity", func() (string, error) {
		return doNewIdentity(m)
	})

	w.SetHtml(portalHTML)
	w.Run()
	logLine("[+] fenêtre fermée")
}
