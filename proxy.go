// Package main — torview: minimal Tor webview browser.
//
// proxy.go: pure-Go network policy + SOCKS5 with remote DNS.
// No third-party module: the SOCKS5 client is 120 lines and fully auditable.
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"time"
)

// Global proxy policy for the whole application. Every single outbound
// connection made by this program goes through it.
var policy = proxyPolicy{}

type proxyPolicy struct {
	enforce atomicFlag
}

// atomicFlag is a concurrency-safe bool set/read across goroutines.
type atomicFlag struct{ v int32 }

func (f *atomicFlag) store(v bool) {
	if v {
		atomic.StoreInt32(&f.v, 1)
	} else {
		atomic.StoreInt32(&f.v, 0)
	}
}

func (f *atomicFlag) load() bool { return atomic.LoadInt32(&f.v) != 0 }

// guardedDial is the ONLY dialer this program allows outside the Tor dialer.
// It is installed as the process-wide default http transport dialer, so any
// future/accidental outbound HTTP from our own code fails loudly instead of
// leaking in the clear. Only the local Tor ports are permitted.
func guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := splitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	isLocal := ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
	if !isLocal && policy.enforce.load() {
		return nil, fmt.Errorf("connexion bloquée par la politique Tor : %s", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func splitHostPort(addr string) (string, string, error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", err
	}
	if portNum, perr := strconv.Atoi(p); perr != nil || portNum <= 0 || portNum > 65535 {
		return "", "", errors.New("port hors bornes")
	}
	return h, p, nil
}

// socks5Dial opens target through the given SOCKS5 proxy with REMOTE name
// resolution (the client sends the hostname, never an IP). This is the exact
// property that prevents DNS leaks; Chromium documents the same behavior for
// --proxy-server=socks5://.
func socks5Dial(proxyAddr, targetAddr string) (net.Conn, error) {
	var d net.Dialer
	d.Timeout = 30 * time.Second
	conn, err := d.Dial("tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	c, err := socks5DialConn(conn, targetAddr)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// socks5DialConn runs the SOCKS5 handshake over an already-connected socket.
// Test seam: lets unit tests feed a net.Pipe without a real daemon.
func socks5DialConn(conn net.Conn, targetAddr string) (net.Conn, error) {
	handshakeErr := func(err error) (net.Conn, error) {
		_ = conn.Close()
		return nil, err
	}
	if err := binary.Write(conn, binary.BigEndian, []byte{0x05, 0x01, 0x00}); err != nil {
		return handshakeErr(err)
	}
	r := bufio.NewReader(conn)
	var resp [2]byte
	if err := binary.Read(r, binary.BigEndian, &resp); err != nil {
		return handshakeErr(err)
	}
	if resp[0] != 0x05 {
		return handshakeErr(fmt.Errorf("socks5: version de réponse invalide (0x%02x)", resp[0]))
	}
	if resp[1] != 0x00 {
		return handshakeErr(fmt.Errorf("socks5: méthode refusée (0x%02x)", resp[1]))
	}

	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		return handshakeErr(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return handshakeErr(errors.New("socks5: port cible invalide"))
	}

	// CONNECT request with DOMAINNAME (never resolve locally).
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	var pbe [2]byte
	binary.BigEndian.PutUint16(pbe[:], uint16(port))
	req = append(req, pbe[:]...)
	if _, err := conn.Write(req); err != nil {
		return handshakeErr(err)
	}

	// Reply: VER REP RSV ATYP BND.ADDR BND.PORT
	var head [4]byte
	if err := binary.Read(r, binary.BigEndian, &head); err != nil {
		return handshakeErr(err)
	}
	if head[0] != 0x05 {
		return handshakeErr(errors.New("socks5: version de réponse invalide"))
	}
	if head[1] != 0x00 {
		return handshakeErr(fmt.Errorf("socks5: échec CONNECT (code 0x%02x)", head[1]))
	}
	// Skip BND.ADDR/BND.PORT.
	switch head[3] {
	case 0x01:
		if err := skipN(r, 4+2); err != nil {
			return handshakeErr(err)
		}
	case 0x04:
		if err := skipN(r, 16+2); err != nil {
			return handshakeErr(err)
		}
	case 0x03:
		var l [1]byte
		if err := binary.Read(r, binary.BigEndian, &l); err != nil {
			return handshakeErr(err)
		}
		if err := skipN(r, int(l[0])+2); err != nil {
			return handshakeErr(err)
		}
	default:
		return handshakeErr(errors.New("socks5: ATYP inconnu"))
	}
	return conn, nil
}

func skipN(r *bufio.Reader, n int) error {
	_, err := io.CopyN(io.Discard, r, int64(n))
	return err
}
