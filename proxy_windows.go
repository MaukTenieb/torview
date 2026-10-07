//go:build windows

// Package main — torview: minimal Tor webview browser.
//
// proxy_windows.go: WebView2 (Chromium) proxy configuration.
//
// WebView2 reads the WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS environment
// variable when the WebView2 environment/controller is created (Microsoft
// docs, "browser flags"). Chromium's documented SOCKSv5 semantics apply:
// name resolution is ALWAYS delegated to the proxy (remote DNS), exactly
// what we need. The switch is set once, at startup, before any webview
// exists — this is the documented, reliable path (no registry, no race).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// configureProxyEnv must be called BEFORE webview.New (the loader reads the
// variable when the WebView2 environment is created).
func configureProxyEnv(m *torManager) error {
	socks := "socks5://" + m.SocksAddr()
	// "<-loopback" in the bypass list means: even loopback is NOT bypassed,
	// so every scheme of every host goes through Tor. Chromium resolves
	// SOCKSv5 targets at the proxy, so no DNS packet leaves via the NIC.
	args := strings.Join([]string{
		"--proxy-server=" + socks,
		"--proxy-bypass-list=<-loopback>",
		"--webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--disable-webrtc-hw-decoding",
		"--disable-features=WebRtcHideLocalIpsWithMdns",
		"--disable-component-update",
		"--disable-default-apps",
		"--no-first-run",
		"--disable-sync",
		"--noerrdialogs",
	}, " ")

	if prev := os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"); prev != "" {
		args = args + " " + prev
	}
	if err := os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", args); err != nil {
		return err
	}

	// Private profile directory (isolation from other WebView2 apps).
	exe, err := os.Executable()
	if err == nil {
		profile := filepath.Join(filepath.Dir(exe), "wv2_profile")
		_ = os.MkdirAll(profile, 0o700)
		_ = os.Setenv("WEBVIEW2_USER_DATA_FOLDER", profile)
	}

	logLine("[proxy] WebView2: " + socks)
	return nil
}

func platformGuard(m *torManager) error {
	if m == nil || m.SocksAddr() == "" {
		return fmt.Errorf("tor non initialisé")
	}
	return nil
}
