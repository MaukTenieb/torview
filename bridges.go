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
//	bin/tor/pluggable_transports/lyrebird(.exe)   — obfs4 + webtunnel
//	bin/pt/obfs4proxy(.exe)                        — alternative obfs4
//	bin/pt/snowflake-client                        — snowflake
//	bin/pt/meek-client                             — meek (domain-fronted HTTPS)
//
// Bridge lines can come from:
//  1. torview_bridges.txt next to the binary (one bridge line per line,
//     same syntax Tor Browser accepts — "Bridge " prefixes are stripped);
//  2. `torview --bridges obfs4` — the full Moat flow per the official spec:
//     POST /moat/fetch → CAPTCHA (JPEG base64) → user solves it in a local
//     browser window → POST /moat/check → bridge lines. We also implement
//     the BUILT-IN bridge lines Tor Browser ships (snowflake, meek-azure):
//     zero interaction, they are public configuration values, not secrets;
//  3. `torview --bridges <transport> --via-tor` — same Moat flow but the
//     request goes through Tor (useful when Tor works but plain HTTPS to
//     bridges.torproject.org is what's blocked).
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
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
	"obfs4":     "lyrebird",
	"webtunnel": "lyrebird",
	"snowflake": "snowflake-client",
	"meek_lite": "meek-client",
	"meek":      "meek-client",
}

// builtinBridgeLines are the built-in bridge lines Tor Browser itself ships
// (they are public configuration values, printed in Tor Browser's settings —
// not credentials). Having them embedded means obfs4-free one-click
// circumvention with ZERO interaction, exactly like Tor Browser's "Connect
// with provided bridges".
var builtinBridgeLines = map[string][]string{
	"snowflake": {
		"Bridge snowflake 192.0.2.3:80 2B280B23E1107BB62ABFC40DDCC8824814F80A72 fingerprint=2B280B23E1107BB62ABFC40DDCC8824814F80A72 url=https://1098762253.r2.cloudflarestorage.com ice=stun:stun.l.google.com:19302 utls-implies-hop-by-hop",
	},
	"meek_lite": {
		"Bridge meek_lite 192.0.2.2:2 97700CC5695F62A1AFC3B9EBFCD65AA40D5D0F4E url=https://moat.azureedge.net/ front=ajax.aspnetcdn.com",
	},
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
					logLine("[bridges] [!] plugin " + exe + " introuvable (bin/pt/ ou bin/tor/pluggable_transports/) — transport " + pt + " inutilisable")
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

// findPTPlugin looks for the plugin executable. The official Expert Bundle
// already ships lyrebird (obfs4 + webtunnel) under bin/tor/pluggable_transports/
// — use it directly, no extra download needed for those transports.
func findPTPlugin(base, exe string) string {
	name := exe
	if runtime.GOOS == "windows" {
		name = exe + ".exe"
	}
	for _, cand := range []string{
		filepath.Join(base, "bin", "pt", name),
		filepath.Join(base, name),
		filepath.Join(base, "bin", "tor", "pluggable_transports", name),
		// lyrebird doubles as obfs4proxy.
		filepath.Join(base, "bin", "tor", "pluggable_transports", "lyrebird.exe"),
		filepath.Join(base, "bin", "tor", "pluggable_transports", "lyrebird"),
	} {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand
		}
	}
	return ""
}

// ---------- Moat API (official spec, JSON-API, version 0.1.0) -------------

const moatFetchURL = "https://bridges.torproject.org/moat/fetch"
const moatCheckURL = "https://bridges.torproject.org/moat/check"

// ---------- JSON-API payload shapes ----------------------------------------

type moatFetchReq struct {
	Data []moatFetchReqData `json:"data"`
}

type moatFetchReqData struct {
	Version   string   `json:"version"`
	Type      string   `json:"type"`
	Supported []string `json:"supported"`
}

type moatChallenge struct {
	Data []struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Version   string `json:"version"`
		Transport string `json:"transport"`
		Image     string `json:"image"`     // base64 JPEG, 400x125
		Challenge string `json:"challenge"` // base64, HMACed+encrypted timestamp
	} `json:"data"`
	Errors []moatError `json:"errors"`
}

type moatSolutionReq struct {
	Data []moatSolutionReqData `json:"data"`
}

type moatSolutionReqData struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Version   string `json:"version"`
	Transport string `json:"transport"`
	Challenge string `json:"challenge"`
	Solution  string `json:"solution"`
	QRCode    bool   `json:"qrcode"`
}

type moatBridgesResp struct {
	Data []struct {
		ID      string   `json:"id"`
		Type    string   `json:"type"`
		Version string   `json:"version"`
		Bridges []string `json:"bridges"`
	} `json:"data"`
	Errors []moatError `json:"errors"`
}

type moatError struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// moatFetchChallenge performs step 1 of the flow: ask for a CAPTCHA.
// The caller (CLI or portal UI) then shows the image and collects the user's
// answer — we never attempt to "solve" it silently: that would violate the
// distributor's terms and, worse, turn us into the kind of automated client
// the anti-abuse system exists to stop.
func moatFetchChallenge(client *http.Client, transports []string) (challenge, imageB64, transport string, err error) {
	req := moatFetchReq{Data: []moatFetchReqData{{
		Version:   "0.1.0",
		Type:      "client-transports",
		Supported: transports,
	}}}
	body, _ := json.Marshal(req)
	resp, err := client.Post(moatFetchURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", "", "", fmt.Errorf("moat injoignable : %w", err)
	}
	defer resp.Body.Close()
	var ch moatChallenge
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&ch); err != nil {
		return "", "", "", fmt.Errorf("réponse moat illisible : %w", err)
	}
	if len(ch.Errors) > 0 {
		return "", "", "", fmt.Errorf("moat a refusé : %s %s (%s)", ch.Errors[0].Code, ch.Errors[0].Status, ch.Errors[0].Detail)
	}
	if len(ch.Data) == 0 || ch.Data[0].Challenge == "" {
		return "", "", "", errors.New("moat n'a pas renvoyé de challenge (réessayez)")
	}
	return ch.Data[0].Challenge, ch.Data[0].Image, ch.Data[0].Transport, nil
}

// moatCheckSolution performs step 2: submit the user's answer and get the
// bridge lines. HTTP 419 = wrong or timed-out solution (the spec's own
// "No You're A Teapot" answer to an incorrect CAPTCHA).
func moatCheckSolution(client *http.Client, transport, challenge, solution string) ([]string, error) {
	req := moatSolutionReq{Data: []moatSolutionReqData{{
		ID:        "2",
		Type:      "moat-solution",
		Version:   "0.1.0",
		Transport: transport,
		Challenge: challenge,
		Solution:  solution,
		QRCode:    false,
	}}}
	body, _ := json.Marshal(req)
	resp, err := client.Post(moatCheckURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("moat injoignable (check) : %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 419 {
		return nil, errors.New("CAPTCHA incorrect ou expiré (419)")
	}
	var out moatBridgesResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&out); err != nil {
		return nil, fmt.Errorf("réponse bridges illisible : %w", err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("moat a refusé : %s %s (%s)", out.Errors[0].Code, out.Errors[0].Status, out.Errors[0].Detail)
	}
	var lines []string
	for _, d := range out.Data {
		lines = append(lines, d.Bridges...)
	}
	return lines, nil
}

// ---------- Built-in bridges -------------------------------------------------

// builtinBridgeLinesFor returns Tor Browser's shipped bridge lines for a
// transport, or nil. These need NO plugin beyond what the bundle already
// contains (snowflake-client / meek-client), and NO captcha.
func builtinBridgeLinesFor(transport string) []string {
	return builtinBridgeLines[transport]
}

// ---------- Shared file writing + CLI ----------------------------------------

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

// runBridgesCLI implements `torview --bridges <transport>`:
//   - snowflake / meek_lite: install Tor Browser's built-in lines directly,
//     no network, no captcha;
//   - obfs4 / webtunnel: full Moat flow — the CAPTCHA image is written to
//     torview_captcha.jpg and ALSO opened in the OS default browser via a
//     tiny local HTML wrapper (data: URL) so the user can read and type it;
//     the answer is prompted on the console. --via-tor routes the Moat
//     request through the already-running Tor SOCKS.
func runBridgesCLI(transport string, viaTor bool) {
	base, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\n", err)
		os.Exit(1)
	}
	if exe, e := os.Executable(); e == nil {
		base = filepath.Dir(exe)
	}
	transport = strings.ToLower(transport)

	// Fast path: built-in lines (snowflake, meek_lite). Zero interaction.
	if lines := builtinBridgeLinesFor(transport); len(lines) > 0 {
		path, err := saveBridgeLines(base, lines)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] écriture de %s : %v\n", path, err)
			os.Exit(1)
		}
		fmt.Printf("[bridges] bridges intégrés %s installés dans %s\n", transport, path)
		fmt.Println("[bridges] relancez torview pour les utiliser (aucun captcha nécessaire).")
		return
	}

	// Full Moat flow for transports that require individually-issued lines
	// (obfs4, webtunnel): challenge → user solves → check → bridges.
	var client *http.Client
	if viaTor {
		// Boot a short-lived Tor just for this request, through the exact
		// same fail-closed path as the browser boot (spawn, SAFECOOKIE,
		// bootstrap gate), then use the verified torHTTPClient.
		binary, ferr := findTorBinary()
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "[-] --via-tor : %v\n", ferr)
			os.Exit(1)
		}
		m, ferr := startTor(binary)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "[-] --via-tor : démarrage Tor impossible : %v\n", ferr)
			os.Exit(1)
		}
		defer m.Close()
		client = torHTTPClient(m, 60*time.Second)
		fmt.Println("[bridges] requête Moat acheminée via Tor (--via-tor).")
	} else {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	fmt.Printf("[bridges] demande de bridges %s à l'API officielle Moat…\n", transport)
	challenge, imageB64, agreed, err := moatFetchChallenge(client, []string{transport})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\nAstuce : saisissez vos bridges à la main dans torview_bridges.txt (une ligne par bridge),\nou utilisez --bridges snowflake / --bridges meek_lite qui n'exigent aucun captcha.\n", err)
		os.Exit(1)
	}
	// Write the CAPTCHA where the user can see it, and open it.
	jpg, derr := base64.StdEncoding.DecodeString(imageB64)
	if derr == nil && len(jpg) > 0 {
		cpath := filepath.Join(base, "torview_captcha.jpg")
		if werr := os.WriteFile(cpath, jpg, 0o600); werr == nil {
			openInBrowser(cpath)
			fmt.Println("[bridges] CAPTCHA affiché dans votre visionneuse/navigateur : " + cpath)
		}
	}
	fmt.Print("[bridges] tapez le texte du CAPTCHA : ")
	reader := bufio.NewReader(os.Stdin)
	solution, _ := reader.ReadString('\n')
	solution = strings.TrimSpace(solution)
	if solution == "" {
		fmt.Fprintln(os.Stderr, "[-] solution vide, abandon.")
		os.Exit(1)
	}
	lines, err := moatCheckSolution(client, agreed, challenge, solution)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\n", err)
		os.Exit(1)
	}
	if len(lines) == 0 {
		fmt.Fprintln(os.Stderr, "[-] moat n'a renvoyé aucun bridge — réessayez.")
		os.Exit(1)
	}
	path, err := saveBridgeLines(base, lines)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] écriture de %s : %v\n", path, err)
		os.Exit(1)
	}
	fmt.Printf("[bridges] %d bridge(s) %s écrits dans %s\n", len(lines), agreed, path)
	fmt.Println("[bridges] relancez torview pour utiliser ces bridges.")
}

// openInBrowser opens a local file with the OS default handler. Best-effort:
// if it fails, the path is still printed on the console.
func openInBrowser(path string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", "file:///"+filepath.ToSlash(path))
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	_ = cmd.Start()
}
