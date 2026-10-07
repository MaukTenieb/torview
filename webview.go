//go:build !nowebview

// Package main — torview: minimal Tor webview browser.
//
// webview.go: window + bindings. Only reached after the exit check passed
// and the transport guard is armed. Uses github.com/abemedia/go-webview
// (purego: no cgo, no gcc needed; the prebuilt native library is embedded
// via the `embedded` import).
package main

import (
	webview "github.com/abemedia/go-webview"
	_ "github.com/abemedia/go-webview/embedded"
)

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
	_ = w.Bind("__torviewNym", func() string {
		if err := m.NewIdentity(); err != nil {
			return "échec NEWNYM : " + err.Error()
		}
		return "nouveau circuit Tor activé"
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
	_ = w.Bind("torviewNewIdentity", func() string {
		if err := m.NewIdentity(); err != nil {
			return "échec NEWNYM : " + err.Error()
		}
		return "nouveau circuit Tor activé"
	})

	w.SetHtml(portalHTML)
	w.Run()
	logLine("[+] fenêtre fermée")
}
