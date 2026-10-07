# TorView — minimal Tor webview browser

Navigateur Tor **minimaliste, indépendant et portable** en Go : moteur web
**natif** de chaque OS + démon Tor piloté et — depuis `--fetch-tor` —
**auto-téléchargé et vérifié** (Windows **et** Linux/macOS : les Expert
Bundles officiels existent pour les trois OS et sont tous gérés). Zéro cgo,
zéro module SOCKS tiers (client SOCKS5 ~120 lignes auditées), zéro serveur
local : la fenêtre s'ouvre uniquement après **preuve** que le trafic sort
par Tor — preuve renforcée en 0.6.0 d'un aller-retour **.onion réel**
(service découvert dynamiquement depuis l'en-tête officiel
`Onion-Location` de torproject.org, rien de codé en dur).

État vérifié (Windows 11, Tor 15.0.24 officiel) : smoke complet, fenêtre avec
chrome de navigation, `--proxy-server` prouvé dans la cmdline WebView2,
extinction sans orphelin. Linux/macOS : compilés, non exécutés ici (limites).

## Plateformes — ce que vous obtenez réellement

| Plateforme | État | Détail |
|---|---|---|
| **Windows 10/11 x64** | ✅ **Testé de bout en bout ici** | zip publié dans Releases ; `--smoke`, fenêtre, proxy prouvé, extinction propre |
| **Linux (desktop)** | ⚠️ Compilé (localement et en CI), **non exécuté** | WebKitGTK + garde GIO ; `--fetch-tor` gère le bundle officiel linux-x86_64/i686 |
| **macOS 14+ (Apple silicon/intel)** | ⚠️ Compilé (localement et en CI), **non exécuté** | WKWebView `proxyConfigurations` ; `--fetch-tor` gère macos-aarch64/x86_64 |
| **iOS** | ❌ Non couvert | WebKit seul moteur autorisé ; une app mobile = projet Xcode + Apple SDK (voir ci-dessous) |
| **Android** | ❌ Non couvert (pour l'instant) | `WebView.setProxyController`/ProxyController requiert autre chaîne de build (voir ci-dessous) |

**Pourquoi pas d'app mobile dans ce dépôt** : TorView est un binaire desktop
driven par un *vrai* daemon Tor. iOS exige un projet Xcode compilé avec les
SDK Apple (impossible depuis une machine Windows, signature obligatoire) ;
Android exige l'API ProxyController de WebView + embarquement du daemon Tor
(JNI/gomobile) — du travail réel, planifié, non livré. En attendant :
**OnionBrowser (iOS)** et **Orbot+TorBrowser (Android)** sont les clients
mobiles de référence (et, hélas, montrent pourquoi cette niche est difficile :
fuites WebRTC/DNS documentées dans leurs dépôts).

## Tour des dépôts — pourquoi cette architecture

| Projet | Forces | Faiblesses mesurables |
|---|---|---|
| **Tor Browser** (torproject, Firefox ESR durci) | Étalon anti-fingerprinting ; sandboxing ; patches en amont | ~500 Mo, mise à jour lourde, pas « embarquable » |
| **OnionBrowser/Orbot** (iOS/Android) | Intégration Tor/mobile éprouvée | Historique de fuites documenté (WebRTC #117/#509, DNS #112) ; plateformes mobiles |
| **opd-ai/go-tor** (client Tor en Go pur) | Pédagogique, portable | Avertissement explicite de l'auteur : *non officiel, « should NOT be considered safe »* — réimplémenter Tor est un piège |
| **Démos webview+SOCKS (Go/PyQt, divers)** | Légèreté, lisibilité | Routent par `HTTP_PROXY`/SOCKS sans preuve ; fuites DNS/WebRTC ; ports 9050 figés ; Tor externe requis |
| **Tails / Whonix** | Étanchéité système | VM/USB dédiés — l'anti-thèse du « minimaliste portable » |

**Le créneau retenu** : un *vrai* daemon Tor officiel (pas une réimplémentation)
+ un moteur natif par OS + **preuve systématique** (exit check) + garde-fous
(processus, transport, ports). C'est exactement ce que les démos GitHub
omettent et que Tor Browser fait trop lourdement.

## Architecture

```
torview (Go, un binaire)
├── Tor : spawn (ports auto appris) · SAFECOOKIE · TAKEOWNERSHIP · NEWNYM · QUIT
│         └── --fetch-tor : Expert Bundle officiel + sha256 signés → bin/tor/
├── Politique : transport Go verrouillé après vérification (sortie non-loopback refusée)
├── Preuve : check.torproject.org VIA Tor — sinon pas de fenêtre (échec dur)
├── Chrome : barre injectée (adresse, ←/→/⟳, ⭘ Circuit) sur chaque page
└── Moteurs (proxy forcé AVANT création) :
     Windows  WebView2  --proxy-server=socks5://127.0.0.1:<p> + WebRTC policy (<-loopback)
     Linux    WebKitGTK http(s)_proxy/all_proxy=socks5://… (GIO) + garde libgio
     macOS 14+ WKWebView proxyConfigurations SOCKSv5 (runtime ObjC, sans SDK récent requis)
```

Points de robustesse **prouvés en test** (pas juste affirmés) :

- **Ports auto** : choisis par nous (`bind :0`), jamais 9050/9051 ; re-attach
  éventuel vérifié par `GETINFO` (les fichiers de ports de Tor sont inégaux :
  le `tor.exe` du Tor Browser n'écrit pas de `socks-port`).
- **SAFECOOKIE** (control-spec §3.24) : HMAC-SHA256 sur `cookie‖nonceC‖nonceS`,
  labels exacts, SERVERHASH vérifié (anti-MitM). Piège Go évité : `Sum(nil)`.
- **CREATE_NO_WINDOW** : sans ça, le `tor.exe` meurt d'un événement console
  (`0xc000013a`) en plein bootstrap — le même procédé que le lanceur officiel.
- **DNS distant** : SOCKSv5 transmet le *nom* au proxy (sémantique Chromium
  documentée) ; aucun DNS ne sort par la carte réseau.
- **Fail-closed** : toute étape ratée ferme Tor ; `defer` n'exécutant rien sur
  `os.Exit`, chaque chemin d'échec ferme explicitement.
- **Extinction** : WM_CLOSE/Ctrl-C → `QUIT` → « Owning controller connection
  has closed -- exiting now » observé, zéro `tor.exe` résiduel.

## Usage

```bash
go mod tidy
go build -o torview.exe -ldflags="-H windowsgui" .   # Windows (sans gcc)
go build -o torview .                                # Linux/macOS
go build -tags nowebview -o torview-smoke .          # sans fenêtre (CI)

./torview --fetch-tor         # 1 fois : Expert Bundle officiel de VOTRE OS → bin/tor/ (somme vérifiée)
./torview --smoke             # preuve : Tor + sortie + NEWNYM + .onion réel (code 0)
./torview                     # le navigateur
```

La preuve .onion du smoke est informationnelle et non bloquante (un service
onion en panne n'est pas une fuite) ; `TORVIEW_SKIP_ONION=1` la désactive
pour les sessions hors ligne.

Tor résolu dans l'ordre : `TORVIEW_TOR` → `bin/tor/tor(.exe)` (layout
`--fetch-tor`) → `bin/tor(.exe)` → `$PATH`.

## Protocole de vérification (reproductible)

1. `--smoke` → exit 0, journal : ports appris, « trafic bien sorti par un
   relais de sortie Tor », « NEWNYM accepté », et désormais « preuve .onion :
   service onion … a répondu 200 via le circuit » (info, non bloquante).
2. Fenêtre : séquence complète dans le journal (Tor → WebView2 → vérif →
   « garde de transport armé »).
3. Preuve proxy : cmdline `msedgewebview2.exe` de torview contient
   `--proxy-server=socks5://127.0.0.1:<port-de-la-session>`,
   `--webrtc-ip-handling-policy=disable_non_proxied_udp`,
   `--proxy-bypass-list=<-loopback>`.
4. Indépendance : `--fetch-tor` (digest comparé aux `sha256sums-signed-build.txt`
   officiels) puis smoke **sans** `TORVIEW_TOR` → exit 0.
5. Extinction : fermeture → aucun processus résiduel (`tasklist`).

## Limites honnêtes

- **Pas Tor Browser** : pas d'anti-fingerprinting (canvas/WebGL/polices =
  ceux du moteur). Routé par Tor ≠ durci contre l'identification.
- **Linux** : WebKitGTK n'a pas d'interrupteur WebRTC (shim JS = mieux, pas
  garantie) ; refus de démarrer sans `libgio` (sinon DIRECT).
- **macOS 14+ requis** (`proxyConfigurations`) : sinon abandon explicite ;
  chemin macOS **non exécuté** sur cette machine.
- **Chrome injecté** : restauré sans CSP ni process séparé ; moins blindé
  qu'un chrome natif (une page hostile peut tenter de le contrarier localement).
- **Hors navigateur** : rien ne protège du reste du système ; TorView
  n'est pas une isolation OS.
- **`--fetch-tor`** : hors Tor par nature (bootstrap) ; somme vérifiée,
  signature GPG des sommes non vérifiée par le programme (référence
  manuelle torproject.org). Triple déduite de GOOS/GOARCH ; seules les
  triples réellement publiées par torproject sont acceptées. Le chemin
  windows est exécuté et vérifié ici ; linux/macos extraient le même code
  (grade compilation), la CI reconstruit le zip Windows sur tag.
- **CI** : `.github/workflows/ci.yml` — vet+tests sur les 3 OS de runners,
  cross-builds headless des autres OS, et un job de release qui refabrique
  le zip Windows (bundle Tor re-téléchargé et revérifié sur le runner) et
  l'attache au tag.-badge : remplacer `UTILISATEUR` ci-dessous.

![CI](https://github.com/UTILISATEUR/torview/actions/workflows/ci.yml/badge.svg)

## Carte des fichiers

| Fichier | Rôle |
|---|---|
| `main.go` | Câblage fail-closed, portail, shim WebRTC, `--fetch-tor`/`--smoke` |
| `control.go` | Cycle de vie Tor : ports auto, SAFECOOKIE, TAKEOWNERSHIP, bootstrap, NEWNYM |
| `control_windows.go`/`_other.go` | `CREATE_NO_WINDOW` / neutre |
| `proxy.go` | SOCKS5 audité (DNS distant), garde du transport par défaut |
| `support.go` | Client HTTP Tor-pinné, `checkExit` (la porte), armement du garde |
| `chrome.go` | Barre de navigation injectée + CSS (le « navigateur complet ») |
| `fetchtor.go` | Expert Bundle officiel → `bin/tor/`, digest vs sommes signées |
| `proxy_windows.go`/`_linux.go`/`_darwin.go` | Switch WebView2 / env GIO / runtime ObjC |
| `webview.go` | Fenêtre + bindings (statut, NEWNYM) |
| `smoke.go`/`headless.go` | Preuve en une commande ; build `-tags nowebview` |
