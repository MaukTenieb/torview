// Package main — download-server: minimal local 1-click delivery of a
// release artifact. Serves a small landing page and the file itself with
// Content-Disposition: attachment (a click = a download, never a preview).
//
// Usage: download-server <zipPath> [shaLine] [port]
// Loopback only: 127.0.0.1, never exposed to the network.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const page = `<!doctype html>
<html lang="fr">
<head><meta charset="utf-8"><title>Télécharger TorView</title>
<style>
 body { font-family: system-ui, sans-serif; background:#101418; color:#d8dee6;
        display:flex; flex-direction:column; align-items:center; gap:16px; margin-top:8vh; }
 h1 { font-weight:600; font-size:22px; }
 code { background:#171c21; padding:2px 8px; border-radius:6px; font-size:13px; }
 a.btn { display:inline-block; background:#1f6feb; color:#fff; text-decoration:none;
         padding:14px 28px; border-radius:10px; font-size:17px; font-weight:600; }
 .sha { max-width:640px; font-size:12px; opacity:.7; word-break:break-all; text-align:center; }
</style></head>
<body>
 <h1>TorView 0.6.0 — Windows x64</h1>
 <a class="btn" href="/download" download>⬇ Télécharger le .zip (20,8 Mo)</a>
 <div class="sha">SHA-256 : <code>%s</code></div>
 <div>Après téléchargement : dézipper → <code>torview.exe --smoke</code> (preuve) → <code>torview.exe</code></div>
</body></html>`

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: download-server <zipPath> [shaLine] [port]")
	}
	zipPath := os.Args[1]
	sha := "voir le fichier .sha256"
	if len(os.Args) > 2 {
		sha = strings.TrimSpace(os.Args[2])
	}
	port := "8899"
	if len(os.Args) > 3 {
		port = strings.TrimPrefix(os.Args[3], ":")
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, page, sha)
	})
	http.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(zipPath)+"\"")
		http.ServeFile(w, r, zipPath)
	})
	log.Printf("[dl] écoute sur http://127.0.0.1:%s — fichier: %s", port, zipPath)
	log.Fatal(http.ListenAndServe("127.0.0.1:"+port, nil))
}
