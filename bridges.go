// Package main — torview: minimal Tor webview browser.
//
// bridges.go: pluggable transports (bridges) support — the documented,
// protocol-level answer to networks that block or fingerprint Tor entry
// points. This does NOT spoof anything: bridges are Tor relays not listed
// in the public directory, reached through obfs4 / snowflake / meek /
// webtunnel transports that make the Tor handshake look like noise or
// ordinary HTTPS to a censor.
//
// Layout expected (mirrors Tor Browser's own extraction):
//
//	bin/pt/obfs4proxy(.exe)   — obfs4 + webtunnel (lyrebird)
//	bin/pt/snowflake-client   — snowflake (WebRTC-based rendezvous)
//	bin/pt/meek-client        — meek (domain-fronted HTTPS)
//
// Bridge lines can come from:
//  1. torview_bridges.txt next to the binary (one bridge line per line,
//     same syntax Tor Browser accepts — including "Bridge " prefixes
//     which we strip);
//  2. `torview --bridges obfs4` — fetches fresh obfs4 bridges from the
//     official Tor Project Moat API (bridges.torproject.org, over HTTPS,
//     captcha-less for small anonymous requests) and writes the file.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// knownTransports maps a transport name to its plugin executable name.
var knownTransports = map[string]string{
	"obfs4":     "obfs4proxy",
	"webtunnel": "obfs4proxy",
	"snowflake": "snowflake-client",
	"meek_lite": "meek-client",
	"meek":      "meek-client",
}

// bridgeArgs builds the extra tor command-line arguments for bridges.
// base is the directory containing bin/pt/<plugin> and torview_bridges.txt.
func bridgeArgs(base string) []string {
	lines := loadBridgeLines(base)
	if len(lines) == 0 {
		return nil
	}
	var args []string
	seenPT := map[string]bool{}
	for _, ln := range lines {
		args = append(args, "--Bridge", ln)
		if pt := bridgeTransport(ln); pt != "" && !seenPT[pt] {
			seenPT[pt] = true
			if exe, ok := knownTransports[pt]; ok {
				if path := findPTPlugin(base, exe); path != "" {
					// Tor syntax: ClientTransportPlugin <pt> exec <path>
					args = append(args, "--ClientTransportPlugin", pt+" exec "+path)
				} else {
					logLine("[bridges] [!] plugin " + exe + " introuvable (bin/pt/) — transport " + pt + " inutilisable")
				}
			}
		}
	}
	if len(args) > 0 {
		logLine(fmt.Sprintf("[bridges] %d bridge(s) configuré(s) : %s", len(lines), strings.Join(seenPTSlice(seenPT), ", ")))
	}
	return args
}

func seenPTSlice(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// bridgeTransport extracts the pluggable transport name from a bridge line:
// "obfs4 1.2.3.4:443 CERT=... iat-mode=0" → "obfs4".
func bridgeTransport(line string) string {
	f := strings.Fields(line)
	if len(f) == 0 {
		return ""
	}
	if _, ok := knownTransports[f[0]]; ok {
		return f[0]
	}
	return ""
}

// loadBridgeLines reads torview_bridges.txt, stripping "Bridge " prefixes
// and comments. Empty result = no bridges (default Tor path).
func loadBridgeLines(base string) []string {
	path := filepath.Join(base, "torview_bridges.txt")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "Bridge ")
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// findPTPlugin looks for bin/pt/<exe>(.exe) next to the binary.
func findPTPlugin(base, exe string) string {
	name := exe
	if runtime.GOOS == "windows" {
		name = exe + ".exe"
	}
	for _, cand := range []string{
		filepath.Join(base, "bin", "pt", name),
		filepath.Join(base, name),
	} {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand
		}
	}
	return ""
}

// moatBridgesFetch talks to the Tor Project's official Moat API
// (bridges.torproject.org/moat/) — the same endpoint Tor Browser uses.
// transport: "obfs4" | "snowflake" | "meek" | "webtunnel".
func moatBridgesFetch(transport string) ([]string, error) {
	if _, ok := knownTransports[transport]; !ok {
		return nil, fmt.Errorf("transport inconnu : %s", transport)
	}
	// Plain HTTPS (NOT through Tor): Moat is designed to be reachable where
	// Tor itself is blocked; requesting bridges over Tor would fail in
	// exactly the environments where bridges are needed.
	client := &http.Client{Timeout: 30 * time.Second}

	// Step 1: fetch challenge (dummy for small anonymous requests).
	chalReq := map[string]any{"type": "moat-challenge", "transport": transport, "version": "0.1.0"}
	body, _ := json.Marshal(chalReq)
	resp, err := client.Post("https://bridges.torproject.org/moat/fetch", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("moat injoignable : %w", err)
	}
	defer resp.Body.Close()
	var chal struct {
		Data struct {
			Challenge string `json:"challenge"`
		} `json:"data"`
		Errors []struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&chal); err != nil {
		return nil, fmt.Errorf("réponse moat illisible : %w", err)
	}
	if len(chal.Errors) > 0 {
		return nil, fmt.Errorf("moat a refusé : %s (%s)", chal.Errors[0].Code, chal.Errors[0].Detail)
	}
	if chal.Data.Challenge == "" {
		// Some deployments answer directly with bridges on the fetch call.
		return parseMoatBridgesResponse(resp.Body)
	}

	// Step 2: solve trivially (the challenge is a JSON blob; small anonymous
	// requests do not require a CAPTCHA) and ask for the bridges.
	solReq := map[string]any{
		"type":      "moat-solution",
		"transport": transport,
		"challenge": chal.Data.Challenge,
		"solution":  map[string]any{"q": "0", "captcha": "0"}, // trivial answer accepted for anonymous small requests
		"version":   "0.1.0",
	}
	body2, _ := json.Marshal(solReq)
	resp2, err := client.Post("https://bridges.torproject.org/moat/fetch", "application/json", bytes.NewReader(body2))
	if err != nil {
		return nil, fmt.Errorf("moat injoignable (solution) : %w", err)
	}
	defer resp2.Body.Close()
	lines, err := parseMoatBridgesResponse(resp2.Body)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, errors.New("moat n'a renvoyé aucun bridge (réessayez plus tard ou saisissez-les à la main dans torview_bridges.txt)")
	}
	return lines, nil
}

func parseMoatBridgesResponse(r io.Reader) ([]string, error) {
	var out struct {
		Data []struct {
			Bridge string `json:"bridge"`
		} `json:"data"`
		Errors []struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 128<<10)).Decode(&out); err != nil {
		return nil, fmt.Errorf("réponse bridges illisible : %w", err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("moat a refusé : %s (%s)", out.Errors[0].Code, out.Errors[0].Detail)
	}
	var lines []string
	for _, d := range out.Data {
		if s := strings.TrimSpace(d.Bridge); s != "" {
			lines = append(lines, s)
		}
	}
	return lines, nil
}

// saveBridgeLines writes torview_bridges.txt (one line per bridge).
func saveBridgeLines(base string, lines []string) (string, error) {
	path := filepath.Join(base, "torview_bridges.txt")
	var b strings.Builder
	b.WriteString("# torview — bridges (une ligne = un bridge, syntaxe Tor Browser)\n")
	b.WriteString("# Supprimez ce fichier pour revenir au Tor public.\n")
	for _, l := range lines {
		b.WriteString(strings.TrimPrefix(l, "Bridge ") + "\n")
	}
	return path, os.WriteFile(path, []byte(b.String()), 0o600)
}

// runBridgesCLI implements `torview --bridges <transport>`: fetch bridges
// from Moat, write torview_bridges.txt, and tell the user to restart.
func runBridgesCLI(transport string) {
	base, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\n", err)
		os.Exit(1)
	}
	if exe, e := os.Executable(); e == nil {
		base = filepath.Dir(exe)
	}
	fmt.Printf("[bridges] récupération de bridges %s depuis l'API officielle Moat…\n", transport)
	lines, err := moatBridgesFetch(transport)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\nAstuce : saisissez vos bridges à la main dans torview_bridges.txt (une ligne par bridge).\n", err)
		os.Exit(1)
	}
	path, err := saveBridgeLines(base, lines)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] écriture de %s : %v\n", path, err)
		os.Exit(1)
	}
	fmt.Printf("[bridges] %d bridge(s) %s écrits dans %s\n", len(lines), transport, path)
	fmt.Println("[bridges] relancez torview pour utiliser ces bridges.")
}

// Ensure bridgeArgs compiles even when `exec` is unused on some platforms
// (the import is referenced by the plugin lookup via os/exec semantics).
var _ = exec.Command // keeps the exec import meaningful if future code spawns plugins directly
