// Package main — torview: minimal Tor webview browser.
//
// chrome.go: the "complete browser" part. A navigation bar (back, forward,
// reload, home, address field with security badge, NEWNYM button) is injected
// into EVERY page via webview Init(), before window.onload, on top of
// everything (z-index max). It is CSS/JS only: no extra process, no dev
// server, no CDP.
//
// UX contract (v2):
//   - one single bar everywhere, including the portal page (no double bar);
//   - loading state: progress strip + spinning reload button while the
//     document parses (Init JS runs at document start, so we see it);
//   - security badge inside the address field: 🧅 onion (end-to-end), 🔒
//     https, ⚠ plain http (explicit warning), ⌂ local portal;
//   - home button returns to the built-in portal via the __torviewHome
//     binding (Go-side SetHtml — bindings run off the UI thread, so the
//     Go handler must Dispatch);
//   - keyboard shortcuts: Ctrl+L focus address, Alt+←/→ history, F5/Ctrl+R
//     reload, Escape leaves the address field;
//   - NEWNYM: 10 s cooldown enforced in Go (anti-flood) AND echoed by the
//     button (countdown), with a toast for the result.
//
// Honesty note: an injected bar cannot be as robust as a native one (a page
// fighting it could win locally), but navigation basics are restored without
// touching CSP or the engine.
package main

import "strings"

// chromeCSS must NOT contain single quotes (it is spliced into a
// single-quoted JS string literal at the __CSS__ placeholder).
const chromeCSS = `
  .tv-chrome {
    position: fixed; top: 0; left: 0; right: 0; z-index: 2147483647;
    display: flex; gap: 6px; align-items: center;
    padding: 7px 8px; background: rgba(13,16,20,0.96);
    border-bottom: 1px solid #2a3138;
    box-shadow: 0 1px 6px rgba(0,0,0,0.35);
    font: 13px/1.4 -apple-system, "Segoe UI", system-ui, sans-serif;
    color: #d8dee6;
  }
  .tv-chrome button {
    background: transparent; border: 0; border-radius: 6px; color: #d8dee6;
    cursor: pointer; padding: 6px 9px; font-size: 14px; line-height: 1;
    transition: background 0.12s ease;
  }
  .tv-chrome button:hover { background: #232a32; }
  .tv-chrome button:active { background: #2c3540; }
  .tv-chrome button:disabled { opacity: 0.4; cursor: default; background: transparent; }
  .tv-box {
    flex: 1; display: flex; align-items: center; gap: 6px;
    background: #171c21; border: 1px solid #2a3138; border-radius: 7px;
    padding: 0 10px; min-width: 0;
  }
  .tv-box:focus-within { border-color: #1f6feb; }
  .tv-sec { font-size: 13px; flex: none; user-select: none; }
  .tv-sec-onion { color: #7ee787; }
  .tv-sec-https { color: #9aa7b4; }
  .tv-sec-http { color: #ffb454; font-weight: 700; }
  .tv-sec-home { color: #58a6ff; }
  .tv-url {
    flex: 1; background: transparent; border: 0; outline: 0; min-width: 0;
    color: #d8dee6; padding: 7px 0; font-size: 13px;
  }
  .tv-nym { background: #2d1f52 !important; color: #c9b3ff !important; }
  .tv-nym:hover { background: #3a2a68 !important; }
  .tv-onion { background: #12351f !important; color: #7ee787 !important; font-size: 12px; }
  .tv-onion:hover { background: #1a4a2c !important; }
  .tv-prog {
    position: fixed; top: 42px; left: 0; right: 0; height: 2px;
    z-index: 2147483647; background: transparent; pointer-events: none;
  }
  .tv-prog-on {
    background: linear-gradient(90deg, #1f6feb 0%, #7ee787 50%, #1f6feb 100%);
    background-size: 200% 100%;
    animation: tv-slide 0.9s linear infinite;
  }
  @keyframes tv-slide { from { background-position: 0% 0; } to { background-position: -200% 0; } }
  .tv-spin { animation: tv-rot 0.8s linear infinite; display: inline-block; }
  @keyframes tv-rot { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
  .tv-toast {
    position: fixed; right: 14px; bottom: 14px; z-index: 2147483647;
    background: #161b22; color: #d8dee6; border: 1px solid #2a3138;
    border-left: 3px solid #7ee787; border-radius: 7px;
    padding: 9px 13px; font: 13px/1.4 -apple-system, "Segoe UI", system-ui, sans-serif;
    max-width: 340px; box-shadow: 0 4px 14px rgba(0,0,0,0.45);
    opacity: 0; transform: translateY(6px);
    transition: opacity 0.18s ease, transform 0.18s ease; pointer-events: none;
  }
  .tv-toast-on { opacity: 1; transform: translateY(0); }
  .tv-toast-bad { border-left-color: #ff6b6b; }
  body.tv-chrome-padding { padding-top: 46px !important; }
`

// chromeJSTemplate: navigation chrome injected at document start on every
// page. Single-quote strings only inside the template body where nesting
// matters; __CSS__ is replaced by chromeCSS (no single quotes — see above).
const chromeJSTemplate = `(function () {
  if (window.__torviewChrome) return;
  window.__torviewChrome = true;
  try {
    var css = document.createElement('style');
    css.textContent = '__CSS__';
    (document.head || document.documentElement).appendChild(css);

    var bar = document.createElement('div');
    bar.className = 'tv-chrome';

    function mkButton(txt, title, fn) {
      var b = document.createElement('button');
      b.textContent = txt; b.title = title;
      b.addEventListener('click', fn);
      return b;
    }

    var backBtn = mkButton('←', 'Page précédente (Alt+←)', function () { history.back(); });
    var fwdBtn = mkButton('→', 'Page suivante (Alt+→)', function () { history.forward(); });
    var rlBtn = mkButton('⟳', 'Recharger (F5)', function () { location.reload(); });
    var homeBtn = mkButton('⌂', 'Portail TorView', function () {
      if (window.__torviewHome) { window.__torviewHome(); }
    });
    bar.appendChild(backBtn);
    bar.appendChild(fwdBtn);
    bar.appendChild(rlBtn);
    bar.appendChild(homeBtn);

    var box = document.createElement('div');
    box.className = 'tv-box';
    var sec = document.createElement('span');
    sec.className = 'tv-sec';
    var url = document.createElement('input');
    url.className = 'tv-url';
    url.placeholder = 'https://… ou abcdef….onion';
    url.spellcheck = false;
    url.addEventListener('keydown', function (ev) {
      if (ev.key !== 'Enter') return;
      var v = (url.value || '').trim();
      if (!v) return;
      if (!/^https?:\\/\\//i.test(v) && !/\\.onion$/i.test(v)) v = 'https://' + v;
      url.blur();
      location.href = v;
    });
    url.addEventListener('focus', function () { url.select(); });
    box.appendChild(sec);
    box.appendChild(url);
    bar.appendChild(box);

    var nym = mkButton('⭘ Nouvelle identité', 'Nouveau circuit Tor (NEWNYM)', requestNym);
    nym.className = 'tv-nym';
    bar.appendChild(nym);

    // Onion-Location pill: when a clearnet page advertises an .onion
    // equivalent, offer the switch. Two detection paths, per the official
    // spec (header AND <meta http-equiv> equivalent):
    //   1. <meta http-equiv="onion-location"> in the DOM — synchronous;
    //   2. the real HTTP response header — invisible to same-origin JS, so
    //      the Go side fetches it via a throttled HEAD through Tor
    //      (__torviewProbeOnion). This is what Tor Browser does natively.
    // Reaching the .onion service bypasses the exit relay entirely: no
    // shared exit IP, no exit-IP-based blocks or captchas on that site.
    var onionBtn = null;
    var onionTarget = '';
    function setOnion(t) {
      var here = '';
      try { here = location.href; } catch (e) {}
      if (t && here && t.split('#')[0] === here.split('#')[0]) t = '';
      if (t) {
        if (!onionBtn) {
          onionBtn = mkButton('🧅 .onion', 'Ce site propose une version .onion — basculer (passe par un service onion, pas par une sortie Tor)…', function () { location.href = onionTarget; });
          onionBtn.className = 'tv-onion';
          bar.insertBefore(onionBtn, nym);
        }
        onionTarget = t;
        onionBtn.style.display = '';
      } else if (onionBtn) {
        onionBtn.style.display = 'none';
      }
    }
    var onionProbedHost = '';
    function checkOnion() {
      if (!document.head) return;
      var meta = null;
      try { meta = document.querySelector('meta[http-equiv="onion-location" i]'); } catch (e) {}
      var t = meta && meta.content ? String(meta.content).trim() : '';
      if (!/^https?:\/\//i.test(t) || !/\.onion(:|\/|$)/i.test(t)) t = '';
      if (t) { setOnion(t); return; }
      // Header path: ask Go exactly once per page host.
      var host = '';
      try { host = location.host; } catch (e) {}
      if (!host || !window.__torviewProbeOnion) return;
      if (host === onionProbedHost) return;
      onionProbedHost = host;
      try {
        window.__torviewProbeOnion(location.href).then(function (h) {
          if (h && /^https?:\/\//i.test(h) && /\.onion(:|\/|$)/i.test(h)) setOnion(String(h));
        }).catch(function () {});
      } catch (e) {}
    }

    var toast = document.createElement('div');
    toast.className = 'tv-toast';
    var toastTimer = null;
    function show(msg, ok) {
      toast.textContent = msg;
      toast.className = 'tv-toast tv-toast-on' + (ok ? '' : ' tv-toast-bad');
      if (toastTimer) clearTimeout(toastTimer);
      toastTimer = setTimeout(function () { toast.className = 'tv-toast'; }, 4200);
    }

    var prog = document.createElement('div');
    prog.className = 'tv-prog';

    function setLoading(on) {
      prog.className = on ? 'tv-prog tv-prog-on' : 'tv-prog';
      rlBtn.className = on ? 'tv-spin' : '';
    }
    setLoading(document.readyState === 'loading');

    function secInfo() {
      var h = '';
      try { h = location.href; } catch (e) {}
      if (h.indexOf('data:') === 0 || h === 'about:blank') {
        return { t: '⌂', c: 'tv-sec-home', title: 'portail local TorView (aucun réseau)' };
      }
      if (/^https:\\/\\/[^/]*\\.onion/i.test(h)) {
        return { t: '🧅', c: 'tv-sec-onion', title: 'service onion — chiffré de bout en bout via Tor' };
      }
      if (/^https:/i.test(h)) {
        return { t: '🔒', c: 'tv-sec-https', title: 'HTTPS via circuit Tor' };
      }
      if (/^http:/i.test(h)) {
        return { t: '⚠', c: 'tv-sec-http', title: 'HTTP NON chiffré — le contenu transite en clair vers la sortie' };
      }
      return { t: '·', c: '', title: '' };
    }

    function paint() {
      var h = '';
      try { h = location.href; } catch (e) {}
      url.value = (h.indexOf('data:') === 0 || h === 'about:blank') ? '' : h;
      var s = secInfo();
      sec.textContent = s.t;
      sec.className = 'tv-sec ' + s.c;
      sec.title = s.title;
      setLoading(document.readyState === 'loading');
    }

    var nymBusy = false;
    function requestNym() {
      if (nymBusy) return;
      if (!window.__torviewNym) { show('indisponible sur cette page', false); return; }
      nymBusy = true;
      nym.disabled = true;
      var left = 10;
      nym.textContent = '⭘ ' + left + ' s…';
      var tick = setInterval(function () {
        left--;
        if (left > 0) { nym.textContent = '⭘ ' + left + ' s…'; }
      }, 1000);
      window.__torviewNym().then(function (r) {
        show(typeof r === 'string' && r ? r : 'nouveau circuit Tor activé', true);
      }).catch(function (e) {
        show('échec NEWNYM : ' + (e && e.message ? e.message : e), false);
      }).then(function () {
        setTimeout(function () {
          clearInterval(tick);
          nym.disabled = false;
          nym.textContent = '⭘ Nouvelle identité';
          nymBusy = false;
        }, left > 0 ? left * 1000 : 0);
      });
    }

    function mount() {
      (document.body || document.documentElement).appendChild(prog);
      (document.body || document.documentElement).appendChild(bar);
      (document.body || document.documentElement).appendChild(toast);
      if (document.body) document.body.classList.add('tv-chrome-padding');
    }
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', mount);
    } else {
      mount();
    }

    document.addEventListener('keydown', function (ev) {
      if (ev.key === 'F5' || (ev.ctrlKey && !ev.altKey && !ev.shiftKey && (ev.key === 'r' || ev.key === 'R'))) {
        ev.preventDefault(); location.reload(); return;
      }
      if (ev.altKey && ev.key === 'ArrowLeft') { ev.preventDefault(); history.back(); return; }
      if (ev.altKey && ev.key === 'ArrowRight') { ev.preventDefault(); history.forward(); return; }
      if ((ev.ctrlKey || ev.metaKey) && !ev.altKey && !ev.shiftKey && (ev.key === 'l' || ev.key === 'L')) {
        ev.preventDefault(); url.focus(); return;
      }
      if (ev.key === 'Escape' && document.activeElement === url) {
        url.blur(); paint();
      }
    }, true);

    paint();
    if (document.readyState !== 'loading') {
      setLoading(document.readyState === 'loading');
    }
    window.addEventListener('load', function () { setLoading(false); paint(); checkOnion(); });
    window.addEventListener('pageshow', paint);
    document.addEventListener('readystatechange', paint);
    setInterval(function () { paint(); checkOnion(); }, 700);
    window.__torviewPaintUrl = paint;
  } catch (e) { /* never break the host page */ }
})();
`

// chromeInitJS is the single Init() payload: navigation chrome + WebRTC
// kill-switch. torJSShim lives in main.go (shared with the headless build).
func chromeInitJS() string {
	js := strings.Replace(chromeJSTemplate, "__CSS__", chromeCSS, 1)
	return js + "\n" + torJSShim
}
