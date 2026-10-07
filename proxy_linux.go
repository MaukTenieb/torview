//go:build linux

// Package main — torview: minimal Tor webview browser.
//
// proxy_linux.go: WebKitGTK proxy configuration.
//
// WebKitGTK resolves proxies through GIO's GProxyResolver, which reads the
// proxy environment variables when the network process initialises. The
// lowercase forms are the documented ones (case matters); we set both cases
// plus all_proxy defensively. GIO ships a SOCKS5 GProxy implementation
// (glib-networking, gio/gsocks5proxy.c) on all modern distros; it supports
// remote name resolution when the client asks for it.
//
// NOTE (honesty box): WebKitGTK does NOT let us toggle WebRTC from here.
// In distro builds WebRTC is typically compiled out; we still ship a JS shim
// in main.go that makes RTCPeerConnection undefined, documented as best-effort.
package main

import (
	"errors"
	"os"
)

// configureProxyEnv must be called before webview.New / gtk init.
func configureProxyEnv(m *torManager) error {
	socks := "socks5://" + m.SocksAddr()
	set := map[string]string{
		// Lowercase first (the documented form), uppercase as fallback.
		"http_proxy":  socks,
		"https_proxy": socks,
		"all_proxy":   socks,
		"HTTP_PROXY":  socks,
		"HTTPS_PROXY": socks,
		"ALL_PROXY":   socks,
		// GIO/libcurl-style explicit SOCKS resolvers.
		"SOCKS_SERVER": socks, // historical libsocks resolver
		"SOCKS5SRV":    socks, // tsocks-family resolvers
		// Exclusions: nothing in the webview may go DIRECT.
		"no_proxy": "",
		"NO_PROXY": "",
	}
	for k, v := range set {
		_ = os.Setenv(k, v)
	}
	logLine("[proxy] WebKitGTK: " + socks)
	return nil
}

func platformGuard(*torManager) error {
	// Fail fast if libgio is missing (then WebKit would try DIRECT, which
	// is exactly the failure mode we must refuse).
	paths := []string{
		"/usr/lib/x86_64-linux-gnu/libgio-2.0.so.0",
		"/usr/lib/aarch64-linux-gnu/libgio-2.0.so.0",
		"/usr/lib/libgio-2.0.so.0",
		"/usr/lib64/libgio-2.0.so.0",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return nil
		}
	}
	return errors.New("libgio-2.0 introuvable : glib-networking requis pour le proxy SOCKS5 de GIO")
}
