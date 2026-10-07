# Modèle de menace — TorView

## Ce que TorView protège

1. **Adresse IP & DNS** : tout le trafic du moteur web est forcé par le
   circuit Tor (SOCKSv5, résolution des noms **côté Tor**, jamais locale) —
   préuve exigée au lancement (check.torproject.org via Tor, sinon la
   fenêtre ne s'ouvre pas).
2. **WebRTC** : politiques natives par moteur (Chromium
   `disable_non_proxied_udp` sous Windows) + shim JS (mieux que rien sous
   WebKitGTK, où aucun interrupteur n'existe).
3. **Le code de l'application lui-même** : après vérification, toute
   connexion non-loopback tentée par notre code Go est **refusée** par le
   garde de transport (échec bruyant, jamais silencieux).
4. **Le daemon Tor** : authentification SAFECOOKIE (anti-MitM), ports aléatoires
   locaux, `TAKEOWNERSHIP` — pas de Tor zombie, pas de collision avec un
   service existant.
5. **Inter-sessions (Windows)** : le profil WebView2 est **purgé à chaque
   lancement** (cookies, stockage) — les sessions ne se lient pas entre
   elles ; `TORVIEW_PERSIST=1` pour conserver l'état. Sous Linux/macOS,
   l'éphémère du profil moteur n'est pas encore garanti (chemins non
   testés) : considérez l'état comme persistant là-bas.

## Ce que TorView ne protège PAS (à lire avant de faire confiance)

- **Anti-fingerprinting absent** : canvas, WebGL, polices, horloge, taille
  d'écran trahissent votre « classe » d'appareil. TorView = routé par Tor,
  pas Tor Browser.
- **Malveillance du contenu** : un site peut toujours tenter de contrecarrer
  le chrome injecté localement ; le moteur reste le moteur de l'OS.
- **Le reste du système** : les autres applications ne passent pas par TorView.
- **macOS < 14 et Linux sans `glib-networking`** : refus de démarrer
  (fail-closed), pas de contournement.

## Bootstrap de confiance

`--fetch-tor` télécharge le Tor Expert Bundle **officiel** par HTTPS et
compare le SHA-256 aux `sha256sums-signed-build.txt` du même répertoire de
release. Ce téléchargement a lieu **hors Tor** par nature (problème du
bootstrap, comme l'installeur de Tor Browser). Pour une assurance maximale,
vérifiez la signature GPG du fichier de sommes côté torproject.org.

## Signalement

Vulnérabilité : ouvrez une issue GitHub avec le minimum de détails
nécessaires, ou contactez le mainteneur en privé. Incluez : OS, version
(`--version`), étapes de reproduction, journaux **avec les chemins
personnels masqués**.
