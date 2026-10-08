// Package main — torview: minimal Tor webview browser.
//
// support.go: process-wide default transport guard + Tor-pinned HTTP client
// used by the exit check.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func init() {
	// Install the guarded dialer on the process default transport so that
	// any code we add later cannot accidentally talk in the clear: once the
	// policy is armed (armDefaultPolicyGuard), only loopback is allowed.
	// (Local Tor ports are dialed directly and explicitly, not via this
	// transport, which also lets us audit every such dial in proxy.go.)
	http.DefaultTransport.(*http.Transport).DialContext = guardedDial
	http.DefaultTransport.(*http.Transport).Proxy = nil
}

// armDefaultPolicyGuard makes the whole process refuse any outbound TCP that
// is not loopback (our own Go code, that is; the web engine is policed by the
// per-OS proxy switches). Called once Tor is up and the exit check has
// passed — after it, ANY new non-loopback dial from our Go code fails loudly
// instead of leaking. Orderings that must go to the network before the guard
// (only --fetch-tor, the documented bootstrap case) run in main() before boot().
func armDefaultPolicyGuard() {
	policy.enforce.store(true)
	logLine("[proxy] garde de transport armé : toute sortie non-loopback de notre code est refusée")
}

// torHTTPClient returns an http.Client pinned to the Tor SOCKS5 port with
// remote DNS. Used for the exit-check and the portal's live status.
func torHTTPClient(m *torManager, timeout time.Duration) *http.Client {
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Explicitly audited path: every dial here goes through Tor with
		// hostname-based CONNECT (no local DNS).
		return socks5Dial(m.SocksAddr(), addr)
	}
	tr := &http.Transport{
		DialContext:           dial,
		ForceAttemptHTTP2:     false,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
		Proxy:                 nil, // never honor ambient proxies here
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// checkExit performs the canonical leak check over Tor: check.torproject.org
// contains "Congratulations" when the stream really exits through a Tor
// exit relay. This is our gate: no pass, no browser window.
func checkExit(m *torManager) (bool, string) {
	c := torHTTPClient(m, 45*time.Second)
	req, err := http.NewRequest("GET", "https://check.torproject.org/", nil)
	if err != nil {
		return false, "URL invalide : " + err.Error()
	}
	resp, err := c.Do(req)
	if err != nil {
		return false, "échec réseau via Tor : " + err.Error()
	}
	defer resp.Body.Close()
	body := make([]byte, 0, 1<<20)
	buf := make([]byte, 32*1024)
	for len(body) < 1<<20 {
		n, rerr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	s := strings.ToLower(string(body))
	if strings.Contains(s, "congratulations") {
		return true, "trafic bien sorti par un relais de sortie Tor"
	}
	if strings.Contains(s, "sorry") || strings.Contains(s, "not configured") {
		return false, "le site n'a PAS vu une sortie Tor"
	}
	return false, "réponse inattendue du site de contrôle"
}

// checkOnionLocation fetches a clearnet URL through Tor purely to read its
// Onion-Location HTTP header — the variant the injected JS cannot see (pages
// are same-origin-blinded to response headers). Informational only: if the
// fetch fails (site down, slow circuit, not an HTML site, no header) we stay
// silent. The .onion URL is returned with the SAME path/query as the request,
// per the official spec's own recommendation.
func checkOnionLocation(m *torManager, pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	c := torHTTPClient(m, 25*time.Second)
	req, err := http.NewRequest("HEAD", pageURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:128.0) Gecko/20100101 Firefox/128.0")
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	onion := resp.Header.Get("Onion-Location")
	if onion == "" {
		return ""
	}
	o, err := url.Parse(onion)
	if err != nil || !strings.HasSuffix(o.Host, ".onion") {
		return ""
	}
	// Spec: keep the site's own path when the header advertises it; when the
	// header is origin-only, carry the current path over.
	if o.Path == "" || o.Path == "/" {
		o.Path = u.Path
		o.RawQuery = u.RawQuery
	}
	return o.String()
}

// onionProbe demonstrates a real hidden-service round trip through this
// session's Tor. The target is DISCOVERED at runtime from torproject.org's
// Onion-Location header (official, self-describing source — nothing
// hard-coded that rots), then fetched over the same SOCKS5 remote-DNS path.
// Informational only: a dead onion service must not fail the gate; the
// exit-relay check above remains the authority.
func onionProbe(m *torManager) (string, bool) {
	c := torHTTPClient(m, 60*time.Second)
	req, err := http.NewRequest("GET", "https://www.torproject.org/", nil)
	if err != nil {
		return "requête invalide : " + err.Error(), false
	}
	resp, err := c.Do(req)
	if err != nil {
		return "torproject.org injoignable via Tor : " + err.Error(), false
	}
	onion := resp.Header.Get("Onion-Location")
	resp.Body.Close()
	if onion == "" {
		return "source officielle sans Onion-Location — étape .onion ignorée", false
	}
	req2, err := http.NewRequest("GET", onion, nil)
	if err != nil {
		return "URL onion invalide (" + onion + ") : " + err.Error(), false
	}
	resp2, err := c.Do(req2)
	if err != nil {
		return "service onion injoignable : " + err.Error(), false
	}
	defer resp2.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp2.Body, 4<<10))
	host := onion
	if u, uerr := url.Parse(onion); uerr == nil {
		host = u.Host
	}
	if resp2.StatusCode == 200 {
		return "service onion " + host + " a répondu 200 via le circuit (hidden service OK)", true
	}
	return fmt.Sprintf("service onion %s a répondu HTTP %d", host, resp2.StatusCode), false
}
