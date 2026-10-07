// Package main — torview: minimal Tor webview browser.
//
// smoke.go: `--smoke` mode, compiled in every build. It boots Tor, verifies
// the exit path through check.torproject.org, proves a real hidden-service
// round trip (informational, non-blocking), exercises NEWNYM on the control
// channel, then shuts down cleanly. Exit 0 = the whole Tor path (spawn,
// SAFECOOKIE, bootstrap, exit, control) is verified on this machine.
package main

import (
	"fmt"
	"os"
)

func runSmoke() {
	fmt.Println("[torboot smoke] lancement de la vérification du chemin de sortie…")
	binary, err := findTorBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\n", err)
		os.Exit(1)
	}
	logLine("[torboot smoke] binaire : " + binary)
	m, err := startTor(binary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\n", err)
		os.Exit(1)
	}
	defer m.Close()
	logLine("[torboot smoke] socks=" + m.SocksAddr() + " control=" + m.ControlAddr())

	ok, detail := checkExit(m)
	if !ok {
		fmt.Fprintf(os.Stderr, "[-] vérification de sortie échouée : %v\n", detail)
		os.Exit(2)
	}
	logLine("[torboot smoke] OK : " + detail)

	if err := m.NewIdentity(); err != nil {
		fmt.Fprintf(os.Stderr, "[-] NEWNYM a échoué : %v\n", err)
		os.Exit(4)
	}
	logLine("[torboot smoke] NEWNYM accepté (nouveau circuit) — canal de contrôle OK")

	// Informational (never blocks): prove a real .onion round trip through
	// this session's Tor, target discovered at runtime from torproject.org's
	// Onion-Location header. A dead onion service is an availability issue,
	// not a configuration leak: the exit-relay check above is the gate.
	// TORVIEW_SKIP_ONION=1 skips it (offline/air-gapped runs).
	if os.Getenv("TORVIEW_SKIP_ONION") == "" {
		logLine("[torboot smoke] preuve .onion : découverte depuis l'en-tête officiel…")
		if odetail, ok2 := onionProbe(m); ok2 {
			logLine("[torboot smoke] preuve .onion : " + odetail)
		} else {
			logLine("[torboot smoke] preuve .onion (info, non bloquante) : " + odetail)
		}
	}
}
