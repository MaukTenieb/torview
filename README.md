# TorView: a web browser in Go routed through the official Tor daemon

By Mauk Tenieb & Korhogo. Build guide: [MaukTenieb/method](https://github.com/MaukTenieb/method). English first, [French below](#français).

TorView is a web browser in Go. It drives the official Tor daemon and the web engine of each system. The `--fetch-tor` option downloads and checks the official Tor Expert Bundle for Windows, Linux, macOS. TorView uses pure Go (ObjC on macOS), a SOCKS5 client of 168 lines and one direct dependency, the webview library go-webview. On Windows, TorView wipes the WebView2 profile (cookies, engine state) at each launch; `TORVIEW_PERSIST=1` keeps it. TorView opens the window after check.torproject.org confirms the Tor exit. Since version 0.6.0, `--smoke` adds a round trip to a .onion service, found in the `Onion-Location` header of torproject.org.

Status checked on Windows 11 with the official Tor 15.0.24: full smoke, window with navigation bar, `--proxy-server` proven in the WebView2 command line. TorView closes the Tor process on exit. Linux and macOS: compiled, zero runs on the test machine so far (see Limits).

Threat model, in French: [SECURITY.md](SECURITY.md).

## Download

Windows x64 (tested): [torview-windows-x64.zip, latest Release](https://github.com/MaukTenieb/torview/releases/latest/download/torview-windows-x64.zip), with its [.sha256](https://github.com/MaukTenieb/torview/releases/latest/download/torview-windows-x64.zip.sha256) in the same Release (`certutil -hashfile <zip> SHA256`).

Home page: https://mauktenieb.github.io/torview/

## Platforms

| Platform | Status | Detail |
|---|---|---|
| Windows 10/11 x64 | Tested end to end | zip in Releases; `--smoke`, window, proxy proof, Tor closed on exit |
| Linux (desktop) | Compiled (locally and in CI); zero runs | WebKitGTK + GIO guard; `--fetch-tor` handles the official linux-x86_64/i686 bundle |
| macOS 14+ (Apple silicon/Intel) | Compiled (locally and in CI); zero runs | WKWebView `proxyConfigurations`; `--fetch-tor` handles macos-aarch64/x86_64 |
| iOS | Outside this repository | WebKit is the one engine Apple allows; a mobile app needs an Xcode project and the Apple SDK (see below) |
| Android | Outside this repository for now | `WebView.setProxyController` / ProxyController needs another build chain (see below) |

## Mobile apps

TorView is a desktop binary that drives a real Tor daemon. iOS requires an Xcode project built with the Apple SDKs, which run on macOS only, and a signature. Android requires the ProxyController API of WebView and an embedded Tor daemon (JNI/gomobile): real work, planned for later. Meanwhile, OnionBrowser (iOS) and Orbot + Tor Browser (Android) are the reference mobile clients. Their repositories document WebRTC and DNS leaks.

## Survey of the repositories, and the choice of architecture

| Project | Strengths | Measurable weaknesses |
|---|---|---|
| Tor Browser (torproject, hardened Firefox ESR) | the anti-fingerprinting reference; sandboxing; patches upstream | about 500 MB, heavy updates, hard to embed |
| OnionBrowser / Orbot (iOS/Android) | proven Tor/mobile integration | documented history of leaks (WebRTC #117/#509, DNS #112); mobile platforms |
| opd-ai/go-tor (Tor client in pure Go) | good for teaching, portable | its author labels it unofficial and unsafe; a reimplementation of Tor is a trap |
| webview + SOCKS demos (Go/PyQt, various) | light, readable | route through `HTTP_PROXY`/SOCKS with zero proof; DNS/WebRTC leaks; fixed port 9050; external Tor required |
| Tails / Whonix | sealed at system level | need a VM or a dedicated USB stick |

TorView combines the official Tor daemon, the native engine of each system, an exit check at launch, and guards on the processes, the transport and the ports.

## Architecture

```
torview (Go, one binary)
├── Tor: spawn (ports learned at start) · SAFECOOKIE · TAKEOWNERSHIP · NEWNYM · QUIT
│         └── --fetch-tor: official Expert Bundle + signed sha256 sums → bin/tor/
├── Policy: Go transport locked after the check (non-loopback exits refused)
├── Proof: check.torproject.org THROUGH Tor, else zero window (hard failure)
├── Chrome: injected bar (address, ←/→/⟳, ⭘ Circuit) on every page
└── Engines (proxy forced BEFORE creation):
     Windows  WebView2  --proxy-server=socks5://127.0.0.1:<p> + WebRTC policy (<-loopback)
     Linux    WebKitGTK http(s)_proxy/all_proxy=socks5://… (GIO) + libgio guard
     macOS 14+ WKWebView proxyConfigurations SOCKSv5 (ObjC runtime, recent SDK optional)
```

Points the tests check:

| Point | Detail |
|---|---|
| Automatic ports | TorView picks its own ports (`bind :0`), away from 9050/9051. A re-attach checks them with `GETINFO`, because Tor's port files vary: the `tor.exe` of Tor Browser skips the `socks-port` file. |
| SAFECOOKIE (control-spec §3.24) | HMAC-SHA256 over `cookie‖nonceC‖nonceS`, exact labels, SERVERHASH checked (against a man in the middle). Go pitfall avoided: `Sum(nil)`. |
| CREATE_NO_WINDOW | On Windows, TorView starts `tor.exe` with this flag, as the official launcher does. Otherwise a console event kills `tor.exe` (`0xc000013a`) in the middle of the bootstrap. |
| Remote DNS | SOCKSv5 passes the host name to the proxy (documented Chromium behaviour). Zero DNS queries leave through the network card. |
| Fail-closed | Every failed step closes Tor. Go skips `defer` on `os.Exit`, so each failure path closes Tor explicitly. |
| Shutdown | WM_CLOSE/Ctrl-C → `QUIT` → Tor logs `Owning controller connection has closed -- exiting now`; TorView closes the Tor process on exit. |

## Usage

```bash
go mod tidy
go build -o torview.exe -ldflags="-H windowsgui" .   # Windows (Go alone, gcc optional)
go build -o torview .                                # Linux/macOS
go build -tags nowebview -o torview-smoke .          # headless (CI)

./torview --fetch-tor         # once: official Expert Bundle for YOUR OS → bin/tor/ (sum checked)
./torview --smoke             # proof: Tor + exit + NEWNYM + real .onion (exit code 0)
./torview                     # the browser
./torview --version           # version, commit, Go version, OS/arch
```

The .onion proof of the smoke is informational, and the exit code stays 0 when it fails: a dead onion service is a matter of availability, and the exit-relay check is the gate. `TORVIEW_SKIP_ONION=1` skips it for offline sessions.

TorView looks for Tor in this order: `TORVIEW_TOR` → `bin/tor/tor(.exe)` (the `--fetch-tor` layout) → `bin/tor(.exe)` → `$PATH`.

## Verification protocol (reproducible)

1. `--smoke` → exit 0. The log shows the learned ports, `trafic bien sorti par un relais de sortie Tor`, `NEWNYM accepté`, and `preuve .onion : service onion … a répondu 200 via le circuit` (informational, exit code unchanged).
2. Window: the log shows the full sequence (Tor → WebView2 → check → `garde de transport armé`).
3. Proxy proof: the command line of TorView's `msedgewebview2.exe` contains `--proxy-server=socks5://127.0.0.1:<session-port>`, `--webrtc-ip-handling-policy=disable_non_proxied_udp` and `--proxy-bypass-list=<-loopback>`.
4. Independence: `--fetch-tor` (digest compared with the official `sha256sums-signed-build.txt`), then the smoke with `TORVIEW_TOR` unset → exit 0.
5. Shutdown: after closing, `tasklist` shows zero leftover processes. TorView closes Tor explicitly (QUIT) on every path. TAKEOWNERSHIP is the last resort.
6. Profile: at the next launch, TorView purges `wv2_profile/`. Cookies and engine state last one session, except with `TORVIEW_PERSIST=1`.

## Limits

- TorView routes through Tor; hardening against identification belongs to Tor Browser. Canvas, WebGL, fonts stay those of the engine, so a site can fingerprint the device.
- Linux: a JavaScript shim handles WebRTC as a best effort, because WebKitGTK offers zero WebRTC switches. TorView requires `libgio` at start-up and stops when the library stays out of reach, since WebKit then connects DIRECT.
- macOS 14 or later (`proxyConfigurations`). On an older macOS, TorView stops with an explicit error. Zero runs of the macOS path on the test machine.
- Injected chrome: TorView injects the navigation bar into each page, outside any CSP, inside the page's process. A native toolbar resists better; a hostile page can try to interfere with the bar locally.
- Outside the browser: TorView routes the traffic of its own window. The rest of the system keeps its usual connections. For isolation of the whole system, see Tails or Whonix in the survey above.
- `--fetch-tor` downloads outside Tor, by nature (bootstrap). The program checks the sum; the GPG signature of the sums stays a manual check against torproject.org. It derives the platform triple from GOOS/GOARCH and accepts only the triples torproject.org publishes. The author runs and checks the Windows path. Linux and macOS extract with the same code (compile grade), and the CI job `fetch-tor-check` runs `--fetch-tor` on Linux.
- CI: `.github/workflows/ci.yml` runs vet and tests on the three runner OS, headless cross-builds of the other OS, and a release job. That job rebuilds the Windows zip on each tag (Tor bundle downloaded and checked again on the runner) and attaches it to the tag.

![CI](https://github.com/MaukTenieb/torview/actions/workflows/ci.yml/badge.svg)

## File map

| File | Role |
|---|---|
| `main.go` | fail-closed wiring, portal, WebRTC shim, `--fetch-tor` / `--smoke` |
| `control.go` | Tor life cycle: automatic ports, SAFECOOKIE, TAKEOWNERSHIP, bootstrap, NEWNYM |
| `control_windows.go` / `_other.go` | `CREATE_NO_WINDOW` / neutral |
| `proxy.go` | audited SOCKS5 (remote DNS), guard on the default transport |
| `support.go` | HTTP client pinned to Tor, `checkExit` (the gate), arming of the guard |
| `chrome.go` | injected navigation bar + CSS (the « full browser ») |
| `fetchtor.go` | official Expert Bundle → `bin/tor/`, digest against the signed sums |
| `proxy_windows.go` / `_linux.go` / `_darwin.go` | WebView2 switch / GIO environment / ObjC runtime |
| `webview.go` | window + bindings (status, NEWNYM) |
| `smoke.go` / `headless.go` | proof in one command; `-tags nowebview` build |
| `bridges.go` | pluggable transports (bridges): `--bridges <transport> [--via-tor]` |
| `version.go` | `--version` |

---

## Français

Par Mauk Tenieb & Korhogo. Guide de fabrication : [MaukTenieb/method](https://github.com/MaukTenieb/method).

TorView est un navigateur web en Go. Il pilote le démon Tor officiel et le moteur web de chaque système. L'option `--fetch-tor` télécharge et vérifie l'Expert Bundle officiel de Tor pour Windows, Linux, macOS. TorView utilise du Go pur (ObjC sur macOS), un client SOCKS5 de 168 lignes et une seule dépendance directe, la bibliothèque webview go-webview. Sous Windows, TorView efface le profil WebView2 (cookies, état du moteur) à chaque lancement ; `TORVIEW_PERSIST=1` le conserve. TorView ouvre la fenêtre après la vérification de la sortie Tor par check.torproject.org. Depuis la version 0.6.0, `--smoke` ajoute un aller-retour vers un service .onion, trouvé dans l'en-tête `Onion-Location` de torproject.org.

État vérifié sous Windows 11 avec le Tor officiel 15.0.24 : smoke complet, fenêtre avec barre de navigation, `--proxy-server` prouvé dans la ligne de commande de WebView2. TorView ferme le processus Tor à la sortie. Linux et macOS : compilés, zéro exécution sur la machine de test à ce jour (voir Limites).

Modèle de menace : [SECURITY.md](SECURITY.md).

### Téléchargement

Windows x64 (testé) : [torview-windows-x64.zip, dernière Release](https://github.com/MaukTenieb/torview/releases/latest/download/torview-windows-x64.zip), avec son [.sha256](https://github.com/MaukTenieb/torview/releases/latest/download/torview-windows-x64.zip.sha256) dans la même Release (`certutil -hashfile <zip> SHA256`).

Page d'accueil : https://mauktenieb.github.io/torview/

### Plateformes

| Plateforme | État | Détail |
|---|---|---|
| Windows 10/11 x64 | Testé de bout en bout | zip dans Releases ; `--smoke`, fenêtre, preuve du proxy, fermeture de Tor à la sortie |
| Linux (desktop) | Compilé (en local et en CI) ; zéro exécution | WebKitGTK + garde GIO ; `--fetch-tor` gère le bundle officiel linux-x86_64/i686 |
| macOS 14+ (Apple silicon/Intel) | Compilé (en local et en CI) ; zéro exécution | WKWebView `proxyConfigurations` ; `--fetch-tor` gère macos-aarch64/x86_64 |
| iOS | Hors de ce dépôt | WebKit, seul moteur qu'Apple autorise ; une app mobile demande un projet Xcode et le SDK Apple (voir plus bas) |
| Android | Hors de ce dépôt pour l'instant | `WebView.setProxyController` / ProxyController demande une autre chaîne de build (voir plus bas) |

### Applications mobiles

TorView est un binaire de bureau qui pilote un vrai démon Tor. iOS exige un projet Xcode compilé avec les SDK Apple, qui tournent sous macOS seulement, et une signature. Android exige l'API ProxyController de WebView et un démon Tor embarqué (JNI/gomobile) : du vrai travail, prévu pour plus tard. En attendant, OnionBrowser (iOS) et Orbot + Tor Browser (Android) sont les clients mobiles de référence. Leurs dépôts documentent des fuites WebRTC et DNS.

### Tour des dépôts et choix d'architecture

| Projet | Forces | Faiblesses mesurables |
|---|---|---|
| Tor Browser (torproject, Firefox ESR durci) | étalon anti-fingerprinting ; sandboxing ; correctifs en amont | environ 500 Mo, mises à jour lourdes, difficile à embarquer |
| OnionBrowser / Orbot (iOS/Android) | intégration Tor/mobile éprouvée | historique de fuites documenté (WebRTC #117/#509, DNS #112) ; plateformes mobiles |
| opd-ai/go-tor (client Tor en Go pur) | pédagogique, portable | son auteur le déclare officieux et peu sûr ; réimplémenter Tor est un piège |
| Démos webview + SOCKS (Go/PyQt, divers) | légèreté, lisibilité | routent par `HTTP_PROXY`/SOCKS, zéro preuve ; fuites DNS/WebRTC ; port 9050 figé ; Tor externe requis |
| Tails / Whonix | étanchéité du système | demandent une VM ou une clé USB dédiée |

TorView combine le démon Tor officiel, le moteur natif de chaque système, une vérification de sortie au lancement et des garde-fous sur les processus, le transport et les ports.

### Architecture

```
torview (Go, un binaire)
├── Tor : spawn (ports auto appris) · SAFECOOKIE · TAKEOWNERSHIP · NEWNYM · QUIT
│         └── --fetch-tor : Expert Bundle officiel + sha256 signés → bin/tor/
├── Politique : transport Go verrouillé après vérification (sortie non-loopback refusée)
├── Preuve : check.torproject.org VIA Tor, sinon zéro fenêtre (échec dur)
├── Chrome : barre injectée (adresse, ←/→/⟳, ⭘ Circuit) sur chaque page
└── Moteurs (proxy forcé AVANT création) :
     Windows  WebView2  --proxy-server=socks5://127.0.0.1:<p> + WebRTC policy (<-loopback)
     Linux    WebKitGTK http(s)_proxy/all_proxy=socks5://… (GIO) + garde libgio
     macOS 14+ WKWebView proxyConfigurations SOCKSv5 (runtime ObjC, SDK récent facultatif)
```

Points vérifiés par les tests :

| Point | Détail |
|---|---|
| Ports auto | TorView choisit ses ports (`bind :0`), hors de 9050/9051. Un rattachement éventuel les vérifie par `GETINFO`, car les fichiers de ports de Tor varient : le `tor.exe` du Tor Browser saute le fichier `socks-port`. |
| SAFECOOKIE (control-spec §3.24) | HMAC-SHA256 sur `cookie‖nonceC‖nonceS`, libellés exacts, SERVERHASH vérifié (contre l'homme du milieu). Piège Go évité : `Sum(nil)`. |
| CREATE_NO_WINDOW | Sous Windows, TorView lance `tor.exe` avec ce drapeau, comme le lanceur officiel. Autrement, un événement console tue `tor.exe` (`0xc000013a`) en plein bootstrap. |
| DNS distant | SOCKSv5 transmet le nom au proxy (sémantique documentée de Chromium). Zéro requête DNS sort par la carte réseau. |
| Fail-closed | Toute étape ratée ferme Tor. Go saute les `defer` sur `os.Exit`, donc chaque chemin d'échec ferme Tor explicitement. |
| Extinction | WM_CLOSE/Ctrl-C → `QUIT` → Tor écrit `Owning controller connection has closed -- exiting now` ; TorView ferme le processus Tor à la sortie. |

### Usage

```bash
go mod tidy
go build -o torview.exe -ldflags="-H windowsgui" .   # Windows (Go seul, gcc facultatif)
go build -o torview .                                # Linux/macOS
go build -tags nowebview -o torview-smoke .          # headless (CI)

./torview --fetch-tor         # 1 fois : Expert Bundle officiel de VOTRE OS → bin/tor/ (somme vérifiée)
./torview --smoke             # preuve : Tor + sortie + NEWNYM + .onion réel (code 0)
./torview                     # le navigateur
./torview --version           # version, commit, version de Go, OS/arch
```

La preuve .onion du smoke sert d'information, et le code de sortie reste 0 quand elle échoue : un service onion en panne relève de la disponibilité, et le contrôle du relais de sortie fait office de porte. `TORVIEW_SKIP_ONION=1` la saute pour les sessions hors ligne.

TorView cherche Tor dans cet ordre : `TORVIEW_TOR` → `bin/tor/tor(.exe)` (disposition de `--fetch-tor`) → `bin/tor(.exe)` → `$PATH`.

### Protocole de vérification (reproductible)

1. `--smoke` → code 0. Le journal montre les ports appris, `trafic bien sorti par un relais de sortie Tor`, `NEWNYM accepté` et `preuve .onion : service onion … a répondu 200 via le circuit` (information, code de sortie inchangé).
2. Fenêtre : le journal montre la séquence complète (Tor → WebView2 → vérification → `garde de transport armé`).
3. Preuve du proxy : la ligne de commande du `msedgewebview2.exe` de TorView contient `--proxy-server=socks5://127.0.0.1:<port-de-la-session>`, `--webrtc-ip-handling-policy=disable_non_proxied_udp` et `--proxy-bypass-list=<-loopback>`.
4. Indépendance : `--fetch-tor` (empreinte comparée aux `sha256sums-signed-build.txt` officiels), puis le smoke avec `TORVIEW_TOR` vide → code 0.
5. Extinction : après la fermeture, `tasklist` montre zéro processus résiduel. TorView ferme Tor explicitement (QUIT) sur tous les chemins. TAKEOWNERSHIP sert de dernier recours.
6. Profil : au lancement suivant, TorView purge `wv2_profile/`. Cookies et état du moteur durent une session, sauf avec `TORVIEW_PERSIST=1`.

### Limites

- TorView passe par Tor ; le durcissement contre l'identification relève de Tor Browser. Canvas, WebGL, polices restent ceux du moteur, et un site peut donc reconnaître l'appareil.
- Linux : un shim JavaScript gère WebRTC au mieux, car WebKitGTK offre zéro interrupteur WebRTC. TorView exige `libgio` au démarrage et s'arrête quand la bibliothèque reste hors d'atteinte, car WebKit se connecte alors en DIRECT.
- macOS 14 ou ultérieur (`proxyConfigurations`). Sur un macOS plus ancien, TorView s'arrête avec une erreur explicite. Zéro exécution du chemin macOS sur la machine de test.
- Barre injectée : TorView injecte la barre de navigation dans chaque page, hors CSP, dans le processus de la page. Une barre native résiste mieux ; une page hostile peut tenter de contrarier la barre localement.
- Hors navigateur : TorView fait passer par Tor le trafic de sa propre fenêtre. Le reste du système garde ses connexions habituelles. Pour isoler tout le système, voir Tails ou Whonix dans le tour des dépôts.
- `--fetch-tor` télécharge hors Tor, par nature (bootstrap). Le programme vérifie la somme ; la signature GPG des sommes reste une vérification manuelle sur torproject.org. Il déduit le triple de plateforme de GOOS/GOARCH et accepte seulement les triples que torproject.org publie. L'auteur exécute et vérifie le chemin Windows. Linux et macOS extraient avec le même code (niveau compilation), et le job CI `fetch-tor-check` lance `--fetch-tor` sous Linux.
- CI : `.github/workflows/ci.yml` lance vet et tests sur les trois OS de runners, des builds headless croisés des autres OS, et un job de release. Ce job refabrique le zip Windows à chaque tag (bundle Tor téléchargé et vérifié de nouveau sur le runner) et l'attache au tag.

### Carte des fichiers

| Fichier | Rôle |
|---|---|
| `main.go` | câblage fail-closed, portail, shim WebRTC, `--fetch-tor` / `--smoke` |
| `control.go` | cycle de vie de Tor : ports auto, SAFECOOKIE, TAKEOWNERSHIP, bootstrap, NEWNYM |
| `control_windows.go` / `_other.go` | `CREATE_NO_WINDOW` / neutre |
| `proxy.go` | SOCKS5 audité (DNS distant), garde du transport par défaut |
| `support.go` | client HTTP épinglé sur Tor, `checkExit` (la porte), armement du garde |
| `chrome.go` | barre de navigation injectée + CSS (le « navigateur complet ») |
| `fetchtor.go` | Expert Bundle officiel → `bin/tor/`, empreinte comparée aux sommes signées |
| `proxy_windows.go` / `_linux.go` / `_darwin.go` | interrupteur WebView2 / environnement GIO / runtime ObjC |
| `webview.go` | fenêtre + liaisons (statut, NEWNYM) |
| `smoke.go` / `headless.go` | preuve en une commande ; build `-tags nowebview` |
| `bridges.go` | transports enfichables (ponts) : `--bridges <transport> [--via-tor]` |
| `version.go` | `--version` |

<!-- network:start -->
## Provenance

Made by Mauk Tenieb, alongside [Insert Koin](https://mauktenieb.github.io/insertkoin/), Mauk Tenieb's Korhogo world, games and machines in a browser.

Other tools by Mauk Tenieb: [Detour](https://github.com/MaukTenieb/detour): cuts portrait sheets into sprite strips and places the eye lights · [FaunaTor](https://github.com/MaukTenieb/faunator): reads web and .onion pages through Tor clients that run in the page · [KorhoTube](https://github.com/MaukTenieb/korhotube): lists the public videos of a YouTube channel as JSON · [Reporhogo](https://github.com/MaukTenieb/reporhogo): searches forty-one code forges for a topic and lists the repositories it finds · [Photofauna](https://github.com/MaukTenieb/Photofauna): a photo editor with 44 looks, part of Insert Koin.

Index of Mauk Tenieb's sites: https://mauktenieb.github.io/
<!-- network:end -->

Related: [mauktenieb.github.io](https://mauktenieb.github.io/) · [MaukTenieb/method](https://github.com/MaukTenieb/method)

## Rights

Code under MIT, method under CC BY 4.0, Korhogo material reserved; credit Mauk Tenieb & Korhogo. Details: [LICENSE](LICENSE).

Code sous MIT, méthode sous CC BY 4.0, matériel Korhogo réservé ; mention Mauk Tenieb & Korhogo. Détails : [LICENSE](LICENSE).
