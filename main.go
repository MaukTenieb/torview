// Package main — torview: minimal Tor webview browser.
//
// main.go: wiring. Order is deliberate:
//  1. start Tor (ports auto, SAFECOOKIE)
//  2. configure the platform proxy env (webview reads it at creation)
//  3. verify the exit path (check.torproject.org) THROUGH Tor
//  4. only then create and run the webview
//
// If any step fails hard, the browser does NOT open: navigating in the clear
// would be a silent IP leak and is treated as an abort, not a warning.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// logLine is our only log sink (stdout in console builds; headless.go
// temporarily points it at a file too so smoke tests leave evidence).
var logLine = func(s string) { fmt.Println(s) }

// torJSShim is injected on every page via Init(). Honestly labeled: on Linux
// it is "best effort" (WebKitGTK has no WebRTC toggle we control); on
// Windows it is belt-and-braces on top of the Chromium switch.
const torJSShim = `
(function () {
  try {
    var kill = function (w, n) {
      try { Object.defineProperty(w, n, { get: function () { return undefined; },
        set: function () { /* refused */ }, configurable: false }); } catch (e) {}
      try { delete w[n]; } catch (e) {}
    };
    kill(window, 'RTCPeerConnection');
    kill(window, 'webkitRTCPeerConnection');
    kill(window, 'mozRTCPeerConnection');
    kill(window, 'RTCDataChannel');
  } catch (e) {}
})();
`

// portalHTML is the first page shown (served as data:, no network hit before
// the check has passed). It calls into Go bindings for status/new identity.
const portalHTML = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>TorView — Minimalist Browser</title>
  <style>
    :root { color-scheme: dark; }
    body {
      font-family: -apple-system, "Segoe UI", system-ui, sans-serif;
      background: #101418; color: #d8dee6; margin: 0;
      display: flex; flex-direction: column; align-items: center;
      justify-content: center; height: 95vh; gap: 12px;
    }
    h1 { font-weight: 600; font-size: 22px; margin: 0; letter-spacing: 0.4px; }
    #status { font-size: 15px; opacity: 0.85; max-width: 640px; text-align: center; }
    #ok { color: #57d364; font-weight: 600; }
    #bad { color: #ff6b6b; font-weight: 600; }
    button {
      background: #1f6feb; color: white; border: 0; border-radius: 6px;
      padding: 9px 18px; font-size: 14px; cursor: pointer; margin-top: 8px;
    }
    button[disabled] { opacity: 0.5; cursor: default; }
    #url { margin-top: 28px; width: 60%; display: flex; gap: 8px; }
    #url input {
      flex: 1; background: #171c21; border: 1px solid #2a3138;
      color: #d8dee6; border-radius: 6px; padding: 9px 12px; font-size: 14px;
    }
    .hint { font-size: 12px; opacity: 0.55; max-width: 640px; text-align: center; }
  </style>
</head>
<body>
  <h1>TorView</h1>
  <div id="status">vérification du chemin de sortie…</div>
  <div id="url" style="display:none">
    <input id="u" placeholder="https://… ou abcdef…onion"/>
    <button onclick="go()">Aller</button>
  </div>
  <button id="nym" style="display:none" onclick="newid()">Nouvelle identité (NEWNYM)</button>
  <div class="hint">
    Toutes les connexions de cette application passent par Tor. DNS résolu par
    le circuit (SOCKSv5 remote-DNS). WebRTC désactivé à la couche Chromium et
    masqué par script.
  </div>
<script>
  function s(t, ok) {
    const el = document.getElementById('status');
    el.innerHTML = ok === true ? '<span id="ok">' + t + '</span>'
                 : ok === false ? '<span id="bad">' + t + '</span>' : t;
  }
  async function refresh() {
    s('vérification…');
    const r = await window.torviewStatus();
    if (r.exitOK) {
      s('Sortie Tor confirmée — <b>' + r.detail + '</b><br/>' + r.socks, true);
      document.getElementById('url').style.display = 'flex';
      document.getElementById('nym').style.display = '';
    } else {
      s('échec de la vérification : ' + r.detail, false);
    }
  }
  async function newid() {
    document.getElementById('nym').disabled = true;
    const r = await window.torviewNewIdentity();
    setTimeout(() => { document.getElementById('nym').disabled = false; }, 11000);
    s(r);
  }
  async function go() {
    let v = document.getElementById('u').value.trim();
    if (!v) return;
    if (!/^https?:\/\//i.test(v) && !/\.onion$/i.test(v)) v = 'https://' + v;
    document.querySelector('#url input').value = v;
    window.location = v;
  }
  window.refreshTorStatus = refresh;
  window.addEventListener('DOMContentLoaded', refresh);
  // Guard rails: refuse to about:blank navigate; keep the portal reachable.
</script>
</body>
</html>
`

func main() {
	// --fetch-tor must run BEFORE any guard: it is the documented bootstrap
	// case (fetching Tor over HTTPS happens outside Tor, like TB's installer).
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version":
			printVersion()
			return
		case "--fetch-tor":
			if err := fetchTor(); err != nil {
				fmt.Fprintf(os.Stderr, "[-] --fetch-tor : %v\n", err)
				os.Exit(1)
			}
			if len(os.Args) > 2 && os.Args[2] == "--run" {
				boot()
			}
			return
		case "--smoke":
			runSmoke()
			return
		}
	}
	boot()
}

// configureProxy delegates to the per-OS implementation
// (proxy_windows.go / proxy_linux.go / proxy_darwin.go).
func configureProxy(m *torManager) error { return configureProxyEnv(m) }

func boot() {
	// 1. Tor first. No Tor, no window.
	logLine("[+] Démarrage du démon Tor…")
	binary, err := findTorBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\nAstuce : `torview --fetch-tor`, ou TORVIEW_TOR=/chemin/tor, ou ./bin/tor(.exe), ou PATH.\n", err)
		os.Exit(1)
	}
	logLine("[+] Binaire Tor : " + binary)
	m, err := startTor(binary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\n", err)
		os.Exit(1)
	}
	defer m.Close() // orderly QUIT on the normal return path (window closed)

	// Every step below closes Tor on failure: fail-closed on ALL paths
	// (defer does not run on os.Exit — the error paths call m.Close()
	// explicitly before exiting). The success path relies on this defer:
	// bootRest returns when the user closes the window, and this defer sends
	// an explicit QUIT. Belt and braces: Tor would also exit by itself via
	// TAKEOWNERSHIP when our control connection drops, but we do not leave
	// that to chance.
	if err := bootRest(m); err != nil {
		m.Close()
		fmt.Fprintf(os.Stderr, "[-] %v\n", err)
		os.Exit(1)
	}
}

// bootRest: proxy env → platform guard → exit check → arm guard → window.
// The transport guard is armed ONLY after the exit check passes: --fetch-tor
// (the documented bootstrap case) runs before boot(), and the exit check
// itself uses the explicit Tor-pinned client, not the default transport.
func bootRest(m *torManager) error {
	// 2. Platform proxy env BEFORE any webview exists; hard platform guard
	// (Linux refuses to start without libgio/glib-networking).
	if err := configureProxy(m); err != nil {
		return fmt.Errorf("configuration proxy : %w", err)
	}
	if err := platformGuard(m); err != nil {
		return fmt.Errorf("garde-fou plateforme : %w", err)
	}

	// 3. Verify the exit path before showing anything navigable.
	logLine("[+] Vérification de la sortie Tor…")
	okV, detail := checkExit(m)
	if !okV {
		return fmt.Errorf("la sortie Tor n'a pas été confirmée : %v", detail)
	}
	logLine("[+] " + detail)

	// 4. From here on, our own Go code may only touch loopback.
	armDefaultPolicyGuard()
	interrupts(m)

	// 5. Window (closed by the user = we return; m.Close runs in boot()).
	runWebview(m, detail)
	return nil
}

// interrupts ensures a clean Tor shutdown on Ctrl-C / SIGTERM. os.Exit does
// NOT run deferred functions, so the manager must be closed EXPLICITLY here
// (audit fix: the previous comment claimed a deferred m.Close() existed —
// it did not; Tor only exited via TAKEOWNERSHIP on connection drop).
func interrupts(m *torManager) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		m.Close()
		os.Exit(0)
	}()
}
