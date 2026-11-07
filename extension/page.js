// boba MAIN-world script: framework component detection + console capture.
// Talks to the isolated content script (content.js) only through CustomEvents
// carrying JSON strings; requests are correlated with a per-request nonce.
(() => {
  if (window.__bobaPage) return;
  window.__bobaPage = true;

  // ---- component detection ---------------------------------------------

  /** @param {any} type React fiber type -> component name, or null for host/other. */
  function reactName(type) {
    if (!type || typeof type === 'string') return null;
    if (typeof type === 'function') return type.displayName || type.name || null;
    if (typeof type === 'object') {
      return type.displayName || reactName(type.render) || reactName(type.type) || null; // forwardRef / memo
    }
    return null;
  }

  function fiberOf(el) {
    for (const k in el) if (k.startsWith('__reactFiber$') || k.startsWith('__reactInternalInstance$')) return el[k];
    return null;
  }

  /** Walk `key` links (e.g. _debugOwner / return) to the nearest named component fiber. */
  function nearest(fiber, key) {
    for (let f = fiber?.[key], n = 0; f && n < 200; f = f[key], n++) {
      const name = reactName(f.type) || (key === '_debugOwner' && f.name) || null; // R19 owner may be ComponentInfo
      if (name) return { fiber: f, name };
    }
    return null;
  }

  function srcOf(s) {
    return s && s.fileName ? { file: s.fileName, line: s.lineNumber } : undefined;
  }

  /** Component info for a single element, without walking up the DOM. */
  function detectHere(el) {
    const fiber = fiberOf(el);
    if (fiber) {
      let hit = nearest(fiber, '_debugOwner');
      let via = 'fiber _debugOwner';
      if (!hit) { hit = nearest(fiber, 'return'); via = 'fiber return'; }
      if (hit) {
        // _debugSource on the host fiber is the JSX site inside the owner (React <= 18).
        const source = srcOf(fiber._debugSource) || srcOf(hit.fiber._debugSource);
        return { name: hit.name, framework: 'react', source, via };
      }
    }
    const vue = el.__vueParentComponent;
    if (vue) {
      const t = vue.type || {};
      const file = t.__file;
      const name = t.name || t.__name || (file && file.split('/').pop().replace(/\.vue$/, '')) || 'Anonymous';
      return { name, framework: 'vue', source: file ? { file } : undefined, via: '__vueParentComponent' };
    }
    const loc = el.__svelte_meta?.loc;
    if (loc?.file) {
      // Svelte 4 lines are 0-based (and carry `char`); Svelte 5 lines are 1-based.
      const line = typeof loc.line === 'number' ? loc.line + (loc.char !== undefined ? 1 : 0) : undefined;
      const name = loc.file.split('/').pop().replace(/\.svelte$/, '');
      return { name, framework: 'svelte', source: { file: loc.file, line }, via: '__svelte_meta' };
    }
    return null;
  }

  /** Nearest component for el, walking up the DOM (Vue/Svelte tag only some nodes). */
  function detect(el) {
    for (let n = el; n && n.nodeType === 1; n = n.parentElement) {
      const c = detectHere(n);
      if (c) return c;
    }
    return null;
  }

  /**
   * Label for each element from el up to (excluding) body: the component name
   * when the element is the root DOM node of that component, else null.
   */
  function labels(el) {
    const out = [];
    for (let n = el; n && n !== document.body && n.nodeType === 1; n = n.parentElement) {
      const mine = detect(n)?.name;
      const parent = n.parentElement ? detect(n.parentElement)?.name : undefined;
      out.push(mine && mine !== parent ? mine : null);
    }
    return out;
  }

  window.addEventListener('boba:inspect', (e) => {
    const nonce = e.detail;
    const el = e.target;
    if (typeof nonce !== 'string' || !(el instanceof Element)) return;
    let result;
    try {
      result = { nonce, component: detect(el), labels: labels(el) };
    } catch (err) {
      result = { nonce, component: null, labels: [] };
    }
    window.dispatchEvent(new CustomEvent('boba:inspect-result', { detail: JSON.stringify(result) }));
  }, true);

  // ---- console capture (localhost only) ---------------------------------

  if (!/^(localhost|127\.0\.0\.1)$/.test(location.hostname)) return;

  /** @type {Array<{message:string, source?:string, line?:number, stack?:string, at:string}>} */
  let queue = [];
  let timer;
  function push(err) {
    if (queue.length >= 50) return;
    queue.push({ ...err, at: new Date().toISOString() });
    clearTimeout(timer);
    timer = setTimeout(() => {
      const errors = queue;
      queue = [];
      window.dispatchEvent(new CustomEvent('boba:console', { detail: JSON.stringify({ url: location.href, errors }) }));
    }, 1000);
  }

  function str(v) {
    if (v instanceof Error) return `${v.name}: ${v.message}`;
    if (typeof v === 'string') return v;
    try { return JSON.stringify(v); } catch { return String(v); }
  }

  window.addEventListener('error', (e) => {
    if (!(e instanceof ErrorEvent)) return; // resource load errors
    push({ message: e.message, source: e.filename || undefined, line: e.lineno || undefined, stack: e.error?.stack });
  }, true);

  window.addEventListener('unhandledrejection', (e) => {
    const r = e.reason;
    push({ message: 'Unhandled rejection: ' + str(r), stack: r?.stack });
  });

  const origError = console.error;
  console.error = function (...args) {
    try {
      push({ message: args.map(str).join(' ').slice(0, 2000), stack: args.find((a) => a instanceof Error)?.stack });
    } catch {}
    return origError.apply(this, args);
  };
})();
