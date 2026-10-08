package main

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// ---- SOCKS5 framing (proxy.go) -----------------------------------------------

func TestSOCKS5HandshakeRejectsWrongVersion(t *testing.T) {
	srv, client := net.Pipe()
	defer srv.Close()
	defer client.Close()

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		// Server speaks a bogus version: the client must bail out.
		var greeting [3]byte
		_ = binary.Read(srv, binary.BigEndian, &greeting)
		srv.Write([]byte{0x04, 0x00}) // VER=4 instead of 5
		// Drain the CONNECT attempt so the write side does not block.
		buf := make([]byte, 512)
		_ = srv.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = srv.Read(buf)
	}()
	if _, err := socks5DialOver(client); err == nil || !strings.Contains(err.Error(), "version de réponse") {
		t.Fatalf("attendu une erreur de version SOCKS, obtenu : %v", err)
	}
	<-handlerDone
}

func TestSOCKS5HandshakeRejectsNonAuth(t *testing.T) {
	srv, client := net.Pipe()
	defer srv.Close()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var greeting [3]byte
		_ = binary.Read(srv, binary.BigEndian, &greeting)
		srv.Write([]byte{0x05, 0xFF}) // no acceptable auth method
	}()
	if _, err := socks5DialOver(client); err == nil || !strings.Contains(err.Error(), "méthode refusée") {
		t.Fatalf("attendu un refus de méthode, obtenu : %v", err)
	}
	<-done
}

func TestSOCKS5ConnectSuccessCarriesHostname(t *testing.T) {
	srv, client := net.Pipe()
	defer srv.Close()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var greeting [3]byte
		_ = binary.Read(srv, binary.BigEndian, &greeting)
		if greeting[0] != 0x05 || greeting[1] != 1 || greeting[2] != 0x00 {
			t.Error("greeting invalide")
			return
		}
		srv.Write([]byte{0x05, 0x00})
		// Read the CONNECT request: VER CMD RSV ATYP LEN HOST PORT
		head := make([]byte, 5)
		if _, err := ioReadFull(srv, head); err != nil {
			t.Error(err)
			return
		}
		if head[3] != 0x03 {
			t.Errorf("attendu ATYP=domainname (DNS distant), obtenu %d", head[3])
			return
		}
		host := make([]byte, head[4])
		if _, err := ioReadFull(srv, host); err != nil {
			t.Error(err)
			return
		}
		if string(host) != "check.torproject.org" {
			t.Errorf("hôte attendu check.torproject.org, obtenu %q", string(host))
			return
		}
		var port [2]byte
		_, _ = ioReadFull(srv, port[:])
		want := binary.BigEndian.Uint16(port[:])
		if want != 443 {
			t.Errorf("port attendu 443, obtenu %d", want)
			return
		}
		// Success reply with a 4-byte bounded BND.ADDR.
		srv.Write([]byte{0x05, 0x00, 0x00, 0x01, 10, 0, 0, 1, 0x01, 0xBB})
	}()
	if _, err := socks5DialOver(client); err != nil {
		t.Fatalf("CONNECT valide rejeté : %v", err)
	}
	<-done
}

// socks5DialOver runs the handshake seam over a pipe.
func socks5DialOver(c net.Conn) (net.Conn, error) {
	return socks5DialConn(c, "check.torproject.org:443")
}

func ioReadFull(c net.Conn, b []byte) (int, error) {
	return io.ReadFull(c, b)
}

// ---- version ordering (fetchtor.go) ------------------------------------------

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"15.0.24", "", true},
		{"15.0.24", "15.0.20", true},
		{"15.0.20", "15.0.24", false},
		{"15.1.0", "15.0.24", true},
		{"9.5", "15.0.24", false},
		{"15.0.24", "15.0.24", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, attendu %v", c.a, c.b, got, c.want)
		}
	}
}

// ---- Expert Bundle triples (fetchtor.go) --------------------------------------
// The expected values were verified against the real torbrowser index
// (dist.torproject.org/torbrowser/15.0.24/), which publishes exactly:
// windows-x86_64, windows-i686, linux-x86_64, linux-i686, macos-x86_64,
// macos-aarch64, and four android-* bundles (out of scope).
func TestBundleTriples(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
	}{
		{"windows", "amd64", "windows-x86_64"},
		{"windows", "386", "windows-i686"},
		{"linux", "amd64", "linux-x86_64"},
		{"linux", "386", "linux-i686"},
		{"darwin", "amd64", "macos-x86_64"},
		{"darwin", "arm64", "macos-aarch64"},
	}
	for _, c := range cases {
		got, err := bundleTriple(c.goos, c.goarch)
		if err != nil || got != c.want {
			t.Errorf("bundleTriple(%s/%s) = (%q, %v), attendu %q", c.goos, c.goarch, got, err, c.want)
		}
	}
	// Only triples actually published upstream are accepted: no invented
	// entries, and the android bundles stay out of scope on purpose.
	for _, bad := range [][2]string{{"linux", "arm64"}, {"android", "arm64"}, {"plan9", "amd64"}} {
		if got, err := bundleTriple(bad[0], bad[1]); err == nil {
			t.Errorf("bundleTriple(%s/%s) = %q : aurait dû être refusé", bad[0], bad[1], got)
		}
	}
}

// ---- SAFECOOKIE constants (control-spec §3.24 exact labels) -------------------

func TestSafeCookieLabelsExact(t *testing.T) {
	if safeCookieServerLabel != "Tor safe cookie authentication server-to-controller hash" {
		t.Errorf("label serveur erroné : %q", safeCookieServerLabel)
	}
	if safeCookieClientLabel != "Tor safe cookie authentication controller-to-server hash" {
		t.Errorf("label client erroné : %q", safeCookieClientLabel)
	}
}

// ---- chrome payload integrity (chrome.go) -------------------------------------

func TestChromeInitJSShape(t *testing.T) {
	js := chromeInitJS()
	for _, want := range []string{"tv-chrome", "history.back", "history.forward",
		"location.reload", "RTCPeerConnection", "__CSS__"} {
		// __CSS__ must have been *replaced*, not left in place.
		if want == "__CSS__" {
			if strings.Contains(js, "__CSS__") {
				t.Fatal("placeholder __CSS__ non substitué")
			}
			continue
		}
		if !strings.Contains(js, want) {
			t.Fatalf("payload chrome sans %q", want)
		}
	}
	if strings.Count(js, "tv-chrome") < 2 {
		t.Fatal("CSS de chrome trop court")
	}
}

// TestChromeUXv2 pins the UX redo (v2) contract: loading state, security
// badge, home binding, keyboard shortcuts and the NEWNYM countdown.
func TestChromeUXv2(t *testing.T) {
	js := chromeInitJS()
	for _, want := range []string{
		"tv-prog",                // loading progress strip
		"tv-sec",                 // security badge
		"tv-sec-onion",           // 🧅 onion badge class
		"tv-sec-http",            // plain-HTTP warning class
		"tv-toast",               // feedback toast
		"__torviewHome",          // home button → Go binding
		"__torviewNym",           // NEWNYM binding call
		"ArrowLeft",              // Alt+← shortcut
		"ArrowRight",             // Alt+→ shortcut
		"'l'",                    // Ctrl+L focus address field
		"'F5'",                   // reload shortcut
		"Nouvelle identité",      // NEWNYM button label
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("chrome UX v2 sans %q", want)
		}
	}
	// The placeholder must be replaced by the real CSS, which carries the
	// new classes too.
	for _, want := range []string{"tv-prog-on", "tv-spin", "tv-toast-on", "tv-sec-home"} {
		if !strings.Contains(js, want) {
			t.Fatalf("CSS chrome UX v2 sans %q", want)
		}
	}
}

// TestChromeCSSQuoteSafe: chromeCSS is spliced into a single-quoted JS
// string literal — a single quote would terminate it early and silently
// break the whole injected chrome.
func TestChromeCSSQuoteSafe(t *testing.T) {
	if strings.Contains(chromeCSS, "'") {
		t.Fatal("chromeCSS contient une apostrophe : casserait le littéral JS '__CSS__'")
	}
}
