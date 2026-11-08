// pekoe isolated-world content script: selection UI, element info, toasts,
// and relaying console batches from page.js to the service worker.
(() => {
  if (globalThis.__pekoeContent) return;
  globalThis.__pekoeContent = true;

  const STYLE_PROPS = ['margin', 'padding', 'font', 'color', 'background-color', 'width', 'height', 'border-radius'];
  const MAX_DEPTH = 3;
  const MAX_HTML = 4000;

  // ---- UI host (closed shadow root so page CSS can't touch it) ---------
  let host, root, overlay, label;
  function ensureUI() {
    if (host?.isConnected) return;
    host = document.createElement('pekoe-ui');
    host.style.cssText = 'all:initial;position:fixed;inset:0;pointer-events:none;z-index:2147483647;';
    root = host.attachShadow({ mode: 'closed' });
    root.innerHTML = `<style>
      * { box-sizing: border-box; font: 12px/1.4 ui-sans-serif, system-ui, sans-serif; }
      .ov { position: fixed; display: none; pointer-events: none; background: rgba(99,102,241,.18);
            outline: 2px solid #6366f1; border-radius: 2px; }
      .lb { position: fixed; display: none; pointer-events: none; background: #312e81; color: #fff;
            padding: 2px 6px; border-radius: 3px; white-space: nowrap; }
      .box { position: fixed; pointer-events: auto; width: 320px; height: 62px; border: 0; padding: 0;
             background: transparent; color-scheme: normal; border-radius: 6px; box-shadow: 0 6px 24px rgba(0,0,0,.35); }
      .toast { position: fixed; right: 16px; bottom: 16px; padding: 8px 12px; border-radius: 6px;
               color: #fff; box-shadow: 0 4px 16px rgba(0,0,0,.3); }
    </style><div class="ov"></div><div class="lb"></div>`;
    overlay = root.querySelector('.ov');
    label = root.querySelector('.lb');
    document.documentElement.appendChild(host);
  }

  function toast(text, ok) {
    ensureUI();
    const t = document.createElement('div');
    t.className = 'toast';
    t.style.background = ok ? '#2e7d32' : '#c62828';
    t.textContent = text;
    root.appendChild(t);
    setTimeout(() => t.remove(), 2500);
  }

  // ---- element info ----------------------------------------------------
  function segment(el) {
    const cls = [...el.classList].slice(0, 2).map((c) => '.' + c).join('');
    return el.tagName.toLowerCase() + cls;
  }

  /** `main > section > Card > button`, using component names where page.js knows them. */
  function selectorPath(el, names = []) {
    const parts = [];
    for (let n = el, i = 0; n && n !== document.body && n.nodeType === 1 && parts.length < 8; n = n.parentElement, i++) {
      parts.unshift(names[i] || segment(n));
    }
    return parts.join(' > ');
  }

  function cloneLimited(node, depth) {
    const c = node.cloneNode(false);
    if (node.nodeType !== 1) return c;
    if (depth <= 0) {
      if (node.childNodes.length) c.appendChild(document.createComment(' … '));
      return c;
    }
    for (const ch of node.childNodes) c.appendChild(cloneLimited(ch, depth - 1));
    return c;
  }

  function limitedHTML(el) {
    const html = cloneLimited(el, MAX_DEPTH).outerHTML;
    return html.length > MAX_HTML ? html.slice(0, MAX_HTML) + '<!-- truncated -->' : html;
  }

  function styles(el) {
    const cs = getComputedStyle(el);
    const out = {};
    for (const p of STYLE_PROPS) out[p] = cs.getPropertyValue(p);
    if (!out.font) out.font = `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize}/${cs.lineHeight} ${cs.fontFamily}`;
    return out;
  }

  function rectOf(el) {
    const r = el.getBoundingClientRect();
    return { x: r.x, y: r.y, width: r.width, height: r.height, dpr: window.devicePixelRatio };
  }

  /** Ask page.js (MAIN world) for component info; resolves null if it doesn't answer. */
  function inspect(el) {
    return new Promise((resolve) => {
      const nonce = crypto.randomUUID();
      const done = (v) => { window.removeEventListener('pekoe:inspect-result', onRes); resolve(v); };
      const onRes = (e) => {
        try {
          const d = JSON.parse(e.detail);
          if (d.nonce === nonce) done(d);
        } catch {}
      };
      window.addEventListener('pekoe:inspect-result', onRes);
      setTimeout(() => done(null), 300);
      el.dispatchEvent(new CustomEvent('pekoe:inspect', { detail: nonce, bubbles: true, composed: true }));
    });
  }

  // ---- selection mode --------------------------------------------------
  let active = false;
  let hovered = null;

  function highlight(el) {
    hovered = el;
    const r = el.getBoundingClientRect();
    Object.assign(overlay.style, { display: 'block', left: r.x + 'px', top: r.y + 'px', width: r.width + 'px', height: r.height + 'px' });
    label.textContent = `${segment(el)}  ${Math.round(r.width)}×${Math.round(r.height)}`;
    Object.assign(label.style, { display: 'block', left: r.x + 'px', top: Math.max(0, r.y - 22) + 'px' });
  }

  const onMove = (e) => {
    const el = e.target;
    if (el instanceof Element && el !== host && el !== hovered) highlight(el);
  };
  const block = (e) => { e.preventDefault(); e.stopPropagation(); e.stopImmediatePropagation(); };
  const onClick = (e) => {
    block(e);
    const el = hovered || e.target;
    stop();
    if (el instanceof Element) confirm(el);
  };
  const onKey = (e) => {
    if (e.key === 'Escape') { block(e); stop(); }
  };
  const BLOCKED = ['mousedown', 'mouseup', 'pointerdown', 'pointerup'];

  function start() {
    ensureUI();
    active = true;
    window.addEventListener('mousemove', onMove, true);
    window.addEventListener('click', onClick, true);
    window.addEventListener('keydown', onKey, true);
    for (const t of BLOCKED) window.addEventListener(t, block, true);
  }

  function stop() {
    active = false;
    hovered = null;
    window.removeEventListener('mousemove', onMove, true);
    window.removeEventListener('click', onClick, true);
    window.removeEventListener('keydown', onKey, true);
    for (const t of BLOCKED) window.removeEventListener(t, block, true);
    if (overlay) overlay.style.display = label.style.display = 'none';
  }

  // ---- confirm: instruction box, then send -----------------------------
  async function confirm(el) {
    const info = await inspect(el);
    const base = {
      url: location.href,
      title: document.title,
      outerHTML: limitedHTML(el),
      selector: selectorPath(el, info?.labels),
      styles: styles(el),
    };
    if (info?.component) base.component = info.component;

    ensureUI();
    highlight(el); // keep the chosen element visible while typing
    // The instruction box is an extension page in an iframe: IME composition
    // stays inline there (a text field inside the page overlay made Google
    // 日本語入力 fall back to its own window after the first character).
    const nonce = crypto.randomUUID();
    const frame = document.createElement('iframe');
    frame.className = 'box';
    const labelText = base.component?.name || segment(el);
    frame.src = chrome.runtime.getURL('prompt.html') + '#' + new URLSearchParams({ n: nonce, label: labelText });
    frame.setAttribute('allow', '');
    const r = el.getBoundingClientRect();
    const top = r.bottom + 8 + 60 < innerHeight ? r.bottom + 8 : Math.max(8, r.top - 68);
    Object.assign(frame.style, { top: top + 'px', left: Math.min(Math.max(8, r.x), innerWidth - 328) + 'px' });
    root.appendChild(frame);
    frame.addEventListener('load', () => frame.focus());
    const close = () => {
      window.removeEventListener('message', onMsg);
      frame.remove();
      overlay.style.display = label.style.display = 'none';
    };
    const onMsg = async (e) => {
      const d = e.data;
      if (e.source !== frame.contentWindow || !d || d.pekoe !== 'prompt' || d.nonce !== nonce) return;
      if (d.type === 'cancel') { close(); return; }
      if (d.type !== 'submit') return;
      close();
      // Let our UI disappear from the frame before the SW captures the tab.
      await new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(ok, 30))));
      const pick = { ...base, instruction: String(d.value || ''), rect: rectOf(el) };
      try {
        const res = await chrome.runtime.sendMessage({ type: 'pick', pick, viewportWidth: innerWidth });
        toast(res?.ok ? 'Sent to pekoe' : `Send failed: ${res?.error || 'unknown'}`, !!res?.ok);
      } catch (err) {
        toast(`Send failed: ${err.message}`, false);
      }
    };
    window.addEventListener('message', onMsg);
  }

  chrome.runtime.onMessage.addListener((msg) => {
    if (msg?.type === 'toggle') active ? stop() : start();
  });

  // ---- console relay (page.js only emits on localhost) -----------------
  window.addEventListener('pekoe:console', (e) => {
    try {
      const { url, errors } = JSON.parse(e.detail);
      if (Array.isArray(errors) && errors.length) chrome.runtime.sendMessage({ type: 'console', url, errors }).catch(() => {});
    } catch {}
  });
})();
