// Package main — torview: minimal Tor webview browser.
//
// chrome.go: the "complete browser" part. A navigation bar (back, forward,
// reload, address field, NEWNYM button) is injected into EVERY page via
// webview Init(), before window.onload, on top of everything (z-index max).
// It is CSS/JS only: no extra process, no dev server, no CDP.
//
// Honesty note: an injected bar cannot be as robust as a native one (a page
// fighting it could win locally), but navigation basics are restored without
// touching CSP or the engine.
package main

import "strings"

const chromeCSS = `
  .tv-chrome {
    position: fixed; top: 0; left: 0; right: 0; z-index: 2147483647;
    display: flex; gap: 6px; align-items: center;
    padding: 8px; background: rgba(12,14,18,0.92);
    border-bottom: 1px solid #2a3138;
    font: 13px/1.4 -apple-system, "Segoe UI", system-ui, sans-serif;
    color: #d8dee6;
  }
  .tv-chrome button {
    background: #1f6feb; border: 0; border-radius: 5px; color: #fff;
    cursor: pointer; padding: 6px 10px; font-size: 13px;
  }
  .tv-chrome button:disabled { opacity: 0.45; cursor: default; }
  .tv-chrome .tv-url {
    flex: 1; background: #171c21; border: 1px solid #2a3138; border-radius: 5px;
    color: #d8dee6; padding: 6px 10px; font-size: 13px;
  }
  .tv-chrome .tv-nym { background: #6d28d9; }
  body.tv-chrome-padding { padding-top: 46px !important; }
`

const chromeJSTemplate = `(function () {
  if (window.__torviewChrome) return;
  window.__torviewChrome = true;
  try {
    var css = document.createElement('style');
    css.textContent = "__CSS__";
    (document.head || document.documentElement).appendChild(css);

    var bar = document.createElement('div');
    bar.className = 'tv-chrome';

    function mkButton(txt, title, fn) {
      var b = document.createElement('button');
      b.textContent = txt; b.title = title;
      b.addEventListener('click', fn);
      return b;
    }
    bar.appendChild(mkButton('←', 'Précédent', function () { history.back(); }));
    bar.appendChild(mkButton('→', 'Suivant', function () { history.forward(); }));
    bar.appendChild(mkButton('⟳', 'Recharger', function () { location.reload(); }));

    var url = document.createElement('input');
    url.className = 'tv-url';
    url.placeholder = 'https://… ou abcdef….onion';
    url.spellcheck = false;
    url.addEventListener('keydown', function (ev) {
      if (ev.key !== 'Enter') return;
      var v = (url.value || '').trim();
      if (!v) return;
      if (!/^https?:\/\//i.test(v) && !/\.onion$/i.test(v)) v = 'https://' + v;
      location.href = v;
    });
    bar.appendChild(url);

    var nym = mkButton('⭘ Circuit', 'Nouveau circuit Tor (NEWNYM)', function () {
      if (window.__torviewNym) { window.__torviewNym(); }
    });
    nym.className = 'tv-nym';
    bar.appendChild(nym);

    function mount() {
      (document.body || document.documentElement).appendChild(bar);
      if (document.body) document.body.classList.add('tv-chrome-padding');
    }
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', mount);
    } else {
      mount();
    }
    var paint = function () { try { url.value = location.href; } catch (e) {} };
    if (document.readyState === 'complete') { paint(); }
    window.addEventListener('load', paint);
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
