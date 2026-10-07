// Package main — torview: minimal Tor webview browser.
//
// fetchtor.go: `--fetch-tor` makes the app INDEPENDENT from a preinstalled
// Tor. It downloads the official Tor Expert Bundle
// (tor-expert-bundle-<triple>-<version>.tar.gz) from dist.torproject.org
// over HTTPS, checks the archive digest against the official
// sha256sums-signed-build.txt of the same release directory, and extracts
// the tor/ folder into ./bin/.
//
// Supported triples (verified against the 15.0.x index layout):
//   windows-x86_64, windows-i686, linux-x86_64, linux-i686,
//   macos-x86_64, macos-aarch64. (android-* bundles exist but need the
//   dedicated Android port; there is no linux-aarch64 bundle upstream.)
//
// Honest notes:
//   - Fetching Tor necessarily happens OUTSIDE Tor (bootstrap problem, same
//     as Tor Browser's installer). That is why --fetch-tor runs before the
//     transport guard is armed.
//   - We verify the digest against the sha256sums file but we do NOT run GPG
//     on its signature; compare the printed version against torproject.org
//     for full assurance.
//   - Only the windows path is executed in CI so far; linux/macos extraction
//     is compile-grade (same code path, different triple).
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	distIndex = "https://dist.torproject.org/torbrowser/"
	sumsFile  = "sha256sums-signed-build.txt"
)

var reVersionDir = regexp.MustCompile(`href="(\d+\.\d+\.\d+)/"`) // stable dirs only (15.0.24); alphas like 16.0a13 don't match

// bundleTriple maps a GOOS/GOARCH pair to the Tor Expert Bundle triple,
// exactly the set published by torproject (no invented entries).
func bundleTriple(goos, goarch string) (string, error) {
	switch goos {
	case "windows":
		switch goarch {
		case "amd64":
			return "windows-x86_64", nil
		case "386":
			return "windows-i686", nil
		}
	case "linux":
		switch goarch {
		case "amd64":
			return "linux-x86_64", nil
		case "386":
			return "linux-i686", nil
		}
	case "darwin":
		switch goarch {
		case "arm64":
			return "macos-aarch64", nil
		case "amd64":
			return "macos-x86_64", nil
		}
	}
	return "", fmt.Errorf("pas de Tor Expert Bundle publié pour %s/%s", goos, goarch)
}

// fetchTor downloads and installs the Tor daemon into ./bin/tor/.
func fetchTor() error {
	triple, err := bundleTriple(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fmt.Errorf("--fetch-tor : %w", err)
	}
	reExpertGz := regexp.MustCompile(`href="(tor-expert-bundle-` + triple + `-(\d+\.\d+\.\d+)\.tar\.gz)"`)

	// 1) Newest stable version directory from the official index.
	idx, err := httpGet(distIndex)
	if err != nil {
		return fmt.Errorf("index dist inaccessible : %w", err)
	}
	newest := ""
	for _, m := range reVersionDir.FindAllStringSubmatch(idx, -1) {
		if newerVersion(m[1], newest) {
			newest = m[1]
		}
	}
	if newest == "" {
		return errors.New("aucun répertoire de version stable trouvé (format de l'index changé ?)")
	}
	dirURL := distIndex + newest + "/"
	fmt.Println("[+] Version retenue : " + newest)

	// 2) Locate the Expert Bundle archive in that directory.
	dirIdx, err := httpGet(dirURL)
	if err != nil {
		return fmt.Errorf("répertoire %s inaccessible : %w", dirURL, err)
	}
	asset := reExpertGz.FindStringSubmatch(dirIdx)
	if asset == nil {
		return fmt.Errorf("tor-expert-bundle-%s-*.tar.gz introuvable dans %s", triple, dirURL)
	}
	gzURL := dirURL + asset[1]

	// 3) Download, digest, verify against the official sums file.
	fmt.Println("[+] Téléchargement de " + gzURL)
	raw, digest, err := httpGetBytes(gzURL, 240*time.Second)
	if err != nil {
		return err
	}
	fmt.Println("[+] SHA-256 : " + digest)
	if err := verifyAgainstOfficialSums(dirURL+sumsFile, asset[1], digest); err != nil {
		return err
	}

	// 4) Extract the tor/ folder into ./bin/ (sanitized paths).
	base, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := extractTorFolder(raw, filepath.Join(base, "bin")); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(base, "bin", "tor.version"), []byte(newest+"\n"), 0o644)
	fmt.Println("[+] Tor " + newest + " extrait dans bin/tor/ — relancez torview sans TORVIEW_TOR.")
	return nil
}

// verifyAgainstOfficialSums compares our digest with the entry of the
// release's sha256sums-signed-build.txt (warns when the file is unavailable).
func verifyAgainstOfficialSums(url, filename, digest string) error {
	sums, err := httpGet(url)
	if err != nil {
		fmt.Println("[!] Impossible de lire " + url + " : comparez manuellement l'empreinte ci-dessus avec les sommes signées de torproject.org.")
		return nil
	}
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 2 && strings.EqualFold(f[0], digest) &&
			(strings.HasSuffix(f[len(f)-1], filename) || strings.Contains(line, filename)) {
			fmt.Println("[+] Somme vérifiée contre " + url)
			return nil
		}
	}
	return fmt.Errorf("somme absente ou divergente dans %s — abandon (archive=%s)", url, filename)
}

// extractTorFolder unpacks <archive root>/tor/* into <binDir>/tor/*,
// refusing absolute paths and traversal attempts.
func extractTorFolder(gz []byte, binDir string) error {
	zr, err := gzip.NewReader(strings.NewReader(string(gz)))
	if err != nil {
		return fmt.Errorf("archive gzip illisible : %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)

	n := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("archive tar corrompue : %w", err)
		}
		name := hdr.Name
		if hdr.Typeflag != tar.TypeReg || !strings.HasPrefix(name, "tor/") {
			continue
		}
		clean := filepath.Clean(strings.TrimPrefix(name, "tor/"))
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("chemin dangereux dans l'archive : %s", name)
		}
		out := filepath.Join(binDir, "tor", clean)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil { //nolint:gosec — archive digest vérifiée, tailles bornées par disque
			f.Close()
			return err
		}
		f.Close()
		n++
	}
	if n == 0 {
		return errors.New("aucun fichier tor/ extrait (layout de l'archive changé ?)")
	}
	return nil
}

func newerVersion(a, b string) bool {
	if b == "" {
		return true
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, y := atoiSafe(as[i]), atoiSafe(bs[i])
		if x != y {
			return x > y
		}
	}
	return len(as) > len(bs)
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func httpGet(url string) (string, error) {
	b, _, err := httpGetBytes(url, 30*time.Second)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func httpGetBytes(url string, timeout time.Duration) ([]byte, string, error) {
	c := &http.Client{Timeout: timeout}
	resp, err := c.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("HTTP %d sur %s", resp.StatusCode, url)
	}
	h := sha256.New()
	body, err := io.ReadAll(io.TeeReader(io.LimitReader(resp.Body, 1<<30), h))
	if err != nil {
		return nil, "", err
	}
	return body, hex.EncodeToString(h.Sum(nil)), nil
}
