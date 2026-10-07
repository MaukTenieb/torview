//go:build darwin && !cgo

// Package main — torview: minimal Tor webview browser.
//
// proxy_darwin_nocgo.go: stub for CGO_ENABLED=0 darwin builds (for example
// a cross-compilation from another OS). There is NO safe way to drive the
// Objective-C runtime without cgo here, and "no proxy configured" would mean
// a silent DIRECT leak — so these stubs make the process REFUSE to boot
// with an actionable message. This keeps `GOOS=darwin CGO_ENABLED=0 go
// build` green in CI while making the failure loud, never silent.
package main

import "errors"

func configureProxyEnv(m *torManager) error {
	_ = m // unused: refuse before any routing decision
	return errors.New("build darwin sans cgo : le proxy WKWebView ne peut pas être configuré " +
		"(recompilez sur macOS avec cgo activé) — refus de démarrer plutôt que fuir en DIRECT")
}

func platformGuard(m *torManager) error {
	_ = m
	return errors.New("garde macOS sans cgo : compilation invalide pour un usage réel")
}
