// Package main — torview: minimal Tor webview browser.
//
// control.go: lifecycle of the embedded (or system) Tor daemon.
//
// Design decisions (all deliberate, none cosmetic):
//   - We pick two FREE ports ourselves (bind :0, read, close) and hand them
//     to Tor as `127.0.0.1:PORT`. We do NOT rely on Tor port files: several
//     builds (including Tor Browser's tor.exe) do not write a socks-port
//     file, and the controller spec itself documents GETINFO
//     net/listeners/* as the discovery mechanism.
//   - After a successful bootstrap we persist the ports in OUR state file
//     (tor_data/torview_state). A later start tries to re-attach: dial the
//     recorded ControlPort, SAFECOOKIE-auth, and GETINFO
//     net/listeners/socks to confirm the recorded SOCKS port — a controller
//     must not trust a stale checkpoint on faith.
//   - ControlPort auth is SAFECOOKIE (control-spec.txt §3.24). No password,
//     no AUTHENTICATE "" (which fails on any default-configured Tor).
//   - TAKEOWNERSHIP + __OwningControllerProcess=ourpid so a spawned Tor
//     exits when we exit, even after our crash.
//   - Bootstrap completion is gated on Tor's log ("Bootstrapped 100%"),
//     not on control-event parsing: log lines are append-only, a single
//     source of truth, and immune to control-stream interleaving races.
package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Control connection constants (tor control-spec.txt).
const (
	cmdAuthenticate  = "AUTHENTICATE"
	cmdAuthChallenge = "AUTHCHALLENGE"
	cmdTakeOwnership = "TAKEOWNERSHIP"
	cmdSignal        = "SIGNAL"
	cmdSetConf       = "SETCONF"
	cmdGetInfo       = "GETINFO"
	cmdQuit          = "QUIT"

	// SAFECOOKIE HMAC key labels, exactly as specified by the spec.
	safeCookieServerLabel = "Tor safe cookie authentication server-to-controller hash"
	safeCookieClientLabel = "Tor safe cookie authentication controller-to-server hash"
)

// torManager owns the Tor child process and one control connection.
type torManager struct {
	mu           sync.Mutex
	cmd          *exec.Cmd
	dataDir      string
	socksPort    int
	controlPort  int
	conn         net.Conn
	reader       *bufio.Reader
	bootstrapped bool
	bootNotify   chan string // sent by the log drain on each progress line
}

func (t *torManager) SocksAddr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(t.socksPort))
}

func (t *torManager) ControlAddr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(t.controlPort))
}

// findTorBinary locates a usable tor executable. Environment override first,
// then a bundled ./bin/tor(.exe), then $PATH.
func findTorBinary() (string, error) {
	if p := os.Getenv("TORVIEW_TOR"); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("TORVIEW_TOR=%s introuvable", p)
	}
	name := "tor"
	if runtime.GOOS == "windows" {
		name = "tor.exe"
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "bin", name)
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	if wd, err := os.Getwd(); err == nil {
		// Layout produced by --fetch-tor: bin/tor/tor(.exe), plus a flat
		// bin/tor(.exe) for bundles prepared by hand.
		for _, cand := range []string{
			filepath.Join(wd, "bin", "tor", name),
			filepath.Join(wd, "bin", name),
		} {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand, nil
			}
		}
	}
	return exec.LookPath(name)
}

// pickFreePort asks the OS for a free TCP port on loopback and releases it.
// The only race is between our close and Tor's bind; the spawn logic retries
// with fresh ports if Tor dies from a bind failure.
func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

func (t *torManager) stateFile() string { return filepath.Join(t.dataDir, "torview_state") }

func (t *torManager) writeStateFile() error {
	body := fmt.Sprintf("socks=%d\ncontrol=%d\n", t.socksPort, t.controlPort)
	return os.WriteFile(t.stateFile(), []byte(body), 0o600)
}

func (t *torManager) readStateFile() bool {
	b, err := os.ReadFile(t.stateFile())
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "socks="); ok {
			if n, err := strconv.Atoi(v); err == nil {
				t.socksPort = n
			}
		}
		if v, ok := strings.CutPrefix(line, "control="); ok {
			if n, err := strconv.Atoi(v); err == nil {
				t.controlPort = n
			}
		}
	}
	return t.socksPort > 0 && t.controlPort > 0
}

// Start attaches to a verified, bootstrapped Tor from a previous run, or
// spawns a new one. Never touches the well-known 9050/9051.
func startTor(binary string) (*torManager, error) {
	base, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if exe, err := os.Executable(); err == nil {
		base = filepath.Dir(exe)
	}
	m := &torManager{
		dataDir:    filepath.Join(base, "tor_data"),
		bootNotify: make(chan string, 64),
	}
	if err := os.MkdirAll(m.dataDir, 0o700); err != nil {
		return nil, err
	}

	// 1) Try to re-attach to our own previous daemon.
	if m.readStateFile() {
		if err := m.connectAndAuth(5 * time.Second); err == nil {
			phase, err := m.getInfo("status/bootstrap-phase")
			if err == nil && strings.Contains(phase, "progress=100") {
				m.bootstrapped = true
				logLine("[tor] ré-attaché au démon existant (ControlPort vérifié par GETINFO)")
				return m, nil
			}
			m.closeConn()
		}
		m.socksPort, m.controlPort = 0, 0
	}

	// 2) Spawn. Two attempts cover the pickFreePort/bind race.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		var m2 *torManager
		if attempt == 0 {
			m2 = m
		} else {
			m2 = &torManager{
				dataDir:    m.dataDir,
				bootNotify: make(chan string, 64),
			}
		}
		err := m2.spawn(binary, base)
		lastErr = err
		if err == nil {
			return m2, nil
		}
		if !errors.Is(err, errBindRace) {
			break
		}
	}
	return nil, lastErr
}

// errBindRace marks a retryable Tor death (ports lost the race).
var errBindRace = errors.New("bind")

func (t *torManager) spawn(binary, base string) error {
	socks, err := pickFreePort()
	if err != nil {
		return err
	}
	ctl, err := pickFreePort()
	if err != nil {
		return err
	}
	t.socksPort, t.controlPort = socks, ctl

	args := []string{
		"--SocksPort", fmt.Sprintf("127.0.0.1:%d IsolateDestAddr IsolateDestPort", socks),
		"--ControlPort", fmt.Sprintf("127.0.0.1:%d", ctl),
		"--CookieAuthentication", "1",
		"--DataDirectory", t.dataDir,
		"--TruncateLogFile", "1",
		"--Log", "NOTICE",
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = base
	cmd.SysProcAttr = platformProcAttr() // Windows: detached hidden console (see control_windows.go).
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	t.cmd = cmd
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("impossible de lancer Tor (%s) : %w", binary, err)
	}
	go drainLog(stdout, t.bootNotify)

	// Auth as soon as the control port answers (dial loop inside).
	if err := t.connectAndAuth(30 * time.Second); err != nil {
		_ = t.cmd.Process.Kill()
		return err
	}
	if err := t.sendSimple(cmdTakeOwnership); err != nil {
		return fmt.Errorf("TAKEOWNERSHIP : %w", err)
	}
	owning := fmt.Sprintf("%s __OwningControllerProcess=%d", cmdSetConf, os.Getpid())
	if err := t.sendSimple(owning); err != nil {
		return fmt.Errorf("SETCONF __OwningControllerProcess : %w", err)
	}
	if err := t.waitBootstrap(240 * time.Second); err != nil {
		return err
	}
	if err := t.writeStateFile(); err != nil {
		logLine("[tor] état non persisté : " + err.Error())
	}
	return nil
}

// waitBootstrap gates completion on the daemon's own log feed.
func (t *torManager) waitBootstrap(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case line := <-t.bootNotify:
			if strings.Contains(line, "100%") {
				t.bootstrapped = true
				return nil
			}
		case <-time.After(deadline.Sub(time.Now())):
			if exited(t.cmd) {
				return fmt.Errorf("tor s'est arrêté avant la fin du bootstrap (%v)", exitErr(t.cmd))
			}
			return errors.New("délai dépassé en attendant le bootstrap Tor (la sortie est-elle bloquée ?)")
		}
	}
}

// connectAndAuth dials the control port until it answers, then performs
// SAFECOOKIE authentication.
func (t *torManager) connectAndAuth(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", t.ControlAddr(), 2*time.Second)
		if err == nil {
			t.conn = conn
			t.reader = bufio.NewReader(conn)
			if aerr := t.safeCookieAuth(); aerr != nil {
				lastErr = aerr
				t.closeConn()
			} else {
				return nil
			}
		} else {
			lastErr = err
		}
		if t.cmd != nil && exited(t.cmd) {
			if isBindDeath(lastErr) {
				return fmt.Errorf("%w : %v", errBindRace, lastErr)
			}
			return fmt.Errorf("tor s'est arrêté pendant la connexion : %w", lastErr)
		}
		if t.cmd == nil && lastErr != nil {
			return fmt.Errorf("tor injoignable au ControlPort : %w", lastErr) // attach mode
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("authentification ControlPort impossible : %w", lastErr)
}

func isBindDeath(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "bind") || strings.Contains(s, "address already in use") ||
		strings.Contains(s, "refused") && strings.Contains(s, "127.0.0.1")
}

// safeCookieAuth implements control-spec §3.24:
//
//	C -> S: AUTHCHALLENGE SAFECOOKIE <client nonce hex>
//	S -> C: 250 AUTHCHALLENGE SERVERHASH=<hex> SERVERNONCE=<hex>
//	C -> S: AUTHENTICATE <client hash hex>
//
// with H = HMAC-SHA256 and message M = cookie || clientNonce || serverNonce:
//
//	server hash = H("Tor safe cookie authentication server-to-controller hash", M)
//	client hash = H("Tor safe cookie authentication controller-to-server hash", M)
func (t *torManager) safeCookieAuth() error {
	cookiePath := filepath.Join(t.dataDir, "control_auth_cookie")
	cookie, err := os.ReadFile(cookiePath)
	if err != nil {
		return fmt.Errorf("lecture du cookie d'authentification (%s) : %w", cookiePath, err)
	}
	if len(cookie) == 0 {
		return errors.New("cookie d'authentification vide")
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}

	t.reader.Reset(t.conn)
	fmt.Fprintf(t.conn, "%s SAFECOOKIE %s\r\n", cmdAuthChallenge, hex.EncodeToString(nonce))
	line, err := t.readReplyJoined()
	if err != nil {
		return err
	}
	if !strings.Contains(line, "AUTHCHALLENGE") {
		return fmt.Errorf("AUTHCHALLENGE refusé : %s", line)
	}
	var serverHashHex, serverNonceHex string
	for _, f := range strings.Fields(line) {
		if v, ok := strings.CutPrefix(f, "SERVERHASH="); ok {
			serverHashHex = v
		}
		if v, ok := strings.CutPrefix(f, "SERVERNONCE="); ok {
			serverNonceHex = v
		}
	}
	if serverHashHex == "" || serverNonceHex == "" {
		return errors.New("réponse AUTHCHALLENGE incomplète")
	}
	serverNonce, err := hex.DecodeString(serverNonceHex)
	if err != nil {
		return err
	}
	wantServerHash, err := hex.DecodeString(serverHashHex)
	if err != nil {
		return err
	}

	msg := make([]byte, 0, len(cookie)+len(nonce)+len(serverNonce))
	msg = append(msg, cookie...)
	msg = append(msg, nonce...)
	msg = append(msg, serverNonce...)

	// NOTE: hash.Hash.Sum(b) APPENDS the digest to b; Sum(nil) is the MAC.
	// And the message must be written into the HMAC before Sum.
	macServer := hmac.New(sha256.New, []byte(safeCookieServerLabel))
	_, _ = macServer.Write(msg)
	if !hmac.Equal(wantServerHash, macServer.Sum(nil)) {
		// Wrong cookie / wrong daemon / MitM: refuse to continue.
		return errors.New("SERVERHASH invalide (cookie ou démon erroné)")
	}
	macClient := hmac.New(sha256.New, []byte(safeCookieClientLabel))
	_, _ = macClient.Write(msg)
	clientMAC := macClient.Sum(nil)

	fmt.Fprintf(t.conn, "%s %s\r\n", cmdAuthenticate, hex.EncodeToString(clientMAC))
	line, err = t.readReplyJoined()
	if err != nil {
		return err
	}
	if strings.HasPrefix(line, "OK") || strings.Contains(line, "OK") {
		return nil
	}
	return fmt.Errorf("AUTHENTICATE refusé : %s", line)
}

// readReplyJoined reads one full reply (all of its lines) and joins the text
// parts, which keeps GETINFO multi-line replies intact.
func (t *torManager) readReplyJoined() (string, error) {
	var texts []string
	for {
		line, err := t.reader.ReadString('\n')
		if err != nil {
			return strings.Join(texts, " "), err
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 4 {
			continue
		}
		switch line[3] {
		case ' ': // final line
			texts = append(texts, strings.TrimPrefix(line, line[:4]))
			return strings.Join(texts, " "), nil
		case '-': // continuation
			texts = append(texts, strings.TrimPrefix(line, line[:4]))
			continue
		default:
			texts = append(texts, line)
		}
	}
}

// sendCmd runs one control command under the manager lock and returns the
// joined reply text. 5xx answers surface as errors.
func (t *torManager) sendCmd(cmdline string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == nil {
		return "", errors.New("connexion de contrôle fermée")
	}
	t.reader.Reset(t.conn)
	if _, err := fmt.Fprintf(t.conn, "%s\r\n", cmdline); err != nil {
		return "", err
	}
	resp, err := t.readReplyJoined()
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(resp, "OK") {
		return resp, nil
	}
	// GETINFO replies embed the key; values pass through, errors do not.
	fields := strings.Fields(cmdline)
	key := fields[len(fields)-1]
	if strings.Contains(resp, key+"=") {
		return resp, nil
	}
	return "", fmt.Errorf("%s refusé : %s", fields[0], resp)
}

func (t *torManager) sendSimple(cmdline string) error {
	_, err := t.sendCmd(cmdline)
	return err
}

func (t *torManager) getInfo(key string) (string, error) {
	resp, err := t.sendCmd(cmdGetInfo + " " + key)
	if err != nil {
		return "", err
	}
	v := strings.TrimPrefix(resp, "OK ")
	// The reply looks like "net/listeners/socks=127.0.0.1:9150 250 OK" or
	// "status/bootstrap-phase=..." — extract after the first '='.
	if i := strings.Index(v, "="); i >= 0 {
		v = v[i+1:]
	}
	return v, nil
}

// NewIdentity sends SIGNAL NEWNYM (rate-limited by Tor itself to 1/10s).
func (t *torManager) NewIdentity() error {
	return t.sendSimple(cmdSignal + " NEWNYM")
}

// StatusText asks the daemon for its bootstrap phase (informational).
func (t *torManager) StatusText() string {
	phase, err := t.getInfo("status/bootstrap-phase")
	if err != nil || phase == "" {
		return "connecté"
	}
	return phase
}

// Close shuts the daemon down cleanly (QUIT for spawned Tor under
// TAKEOWNERSHIP), with a hard kill fallback on timeout.
func (t *torManager) Close() {
	t.mu.Lock()
	conn := t.conn
	t.conn, t.reader = nil, nil
	t.mu.Unlock()

	if conn != nil {
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprintf(conn, "%s\r\n", cmdQuit)
		_ = conn.Close()
	}
	if t.cmd != nil && t.cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			// Complements the one-shot waiter registered in exited().
			select {
			case <-doneFromProcess(t.cmd):
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = t.cmd.Process.Kill()
		}
	}
}

func (t *torManager) closeConn() {
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn, t.reader = nil, nil
	}
}

func exited(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return true
	}
	select {
	case <-doneFromProcess(cmd):
		return true
	default:
		return false
	}
}

// exitErr returns the recorded Wait() error for a finished command.
func exitErr(cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("aucune commande")
	}
	waiters.mu.Lock()
	defer waiters.mu.Unlock()
	slot := waiters.m[cmd]
	if slot == nil {
		doneFromProcess(cmd) // force registration; then re-lock read
		waiters.mu.Lock()
		slot = waiters.m[cmd]
		waiters.mu.Unlock()
	}
	if slot == nil {
		return errors.New("état inconnu")
	}
	<-slot.done
	slot.mu.Lock()
	defer slot.mu.Unlock()
	return slot.err
}

// doneFromProcess wraps cmd.Wait in a one-shot signal so exit can be polled.
type waitSlot struct {
	done chan struct{}
	mu   sync.Mutex
	err  error
}

var waiters = &waitRegistry{m: map[*exec.Cmd]*waitSlot{}}

type waitRegistry struct {
	mu sync.Mutex
	m  map[*exec.Cmd]*waitSlot
}

func doneFromProcess(cmd *exec.Cmd) <-chan struct{} {
	waiters.mu.Lock()
	defer waiters.mu.Unlock()
	if slot, ok := waiters.m[cmd]; ok {
		return slot.done
	}
	slot := &waitSlot{done: make(chan struct{})}
	waiters.m[cmd] = slot
	go func() {
		slot.err = cmd.Wait()
		slot.mu.Lock()
		if slot.err == nil {
			slot.err = errors.New("sortie sans erreur rapportée")
		}
		slot.mu.Unlock()
		close(slot.done)
	}()
	return slot.done
}

// drainLog relays Tor's stdout to our log and to the bootstrap gate.
func drainLog(r interface{ Read([]byte) (int, error) }, boot chan<- string) {
	verbose := os.Getenv("TORVIEW_VERBOSE") != ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		s := sc.Text()
		if strings.Contains(s, "Bootstrapped") {
			select {
			case boot <- s:
			default:
			}
			if verbose || strings.Contains(s, "Problem bootstrapping") {
				logLine("[tor] " + s)
			}
			continue
		}
		if verbose || strings.Contains(s, "Problem") || strings.Contains(s, "Failed") || strings.Contains(s, "[err]") {
			logLine("[tor] " + s)
		}
	}
}
