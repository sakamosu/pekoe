// boba service worker (ES module).
// Everything is fire-and-forget HTTP: the MV3 SW may be killed at any time,
// so no long-lived connections and no state that matters beyond one event.

const DEFAULT_PORT = 47777;
const LOCAL_MATCH = ['http://localhost/*', 'http://127.0.0.1/*'];

/** @returns {Promise<{port:number, token:string}>} */
async function getConfig() {
  const { port, token } = await chrome.storage.local.get(['port', 'token']);
  return { port: Number(port) || DEFAULT_PORT, token: token || '' };
}

/**
 * POST JSON to the daemon.
 * @param {string} path
 * @param {unknown} body
 * @returns {Promise<{ok:boolean, error?:string}>}
 */
async function post(path, body) {
  const { port, token } = await getConfig();
  try {
    const res = await fetch(`http://127.0.0.1:${port}${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(5000),
    });
    return res.ok ? { ok: true } : { ok: false, error: `HTTP ${res.status}` };
  } catch (e) {
    return { ok: false, error: `Cannot connect to bobad (${e.message})` };
  }
}

/** Show ✓ / ✗ on the action badge for a moment. */
function flashBadge(tabId, ok) {
  chrome.action.setBadgeBackgroundColor({ tabId, color: ok ? '#2e7d32' : '#c62828' });
  chrome.action.setBadgeText({ tabId, text: ok ? '✓' : '✗' });
  setTimeout(() => chrome.action.setBadgeText({ tabId, text: '' }).catch(() => {}), 2500);
}

/** Inject (idempotent) scripts and toggle selection mode on a tab. */
async function togglePick(tab) {
  if (!tab?.id) return;
  try {
    await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: ['page.js'], world: 'MAIN' });
    await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: ['content.js'] });
    await chrome.tabs.sendMessage(tab.id, { type: 'toggle' });
  } catch (e) {
    console.warn('boba: cannot inject into this tab', e);
    flashBadge(tab.id, false);
  }
}

chrome.commands.onCommand.addListener(async (command, tab) => {
  if (command !== 'toggle-pick') return;
  tab ??= (await chrome.tabs.query({ active: true, currentWindow: true }))[0];
  togglePick(tab);
});
chrome.action.onClicked.addListener(togglePick);

/** @param {ArrayBuffer} buf */
function toBase64(buf) {
  const bytes = new Uint8Array(buf);
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

/**
 * Capture the visible tab and crop it to rect (CSS px).
 * Scale is derived from the captured bitmap vs. viewport width, which also
 * handles browser zoom; falls back to dpr.
 * @returns {Promise<string>} PNG data URL
 */
async function captureCrop(windowId, rect, viewportWidth) {
  const full = await chrome.tabs.captureVisibleTab(windowId, { format: 'png' });
  const bitmap = await createImageBitmap(await (await fetch(full)).blob());
  const scale = viewportWidth ? bitmap.width / viewportWidth : rect.dpr || 1;
  const x = Math.max(0, Math.floor(rect.x * scale));
  const y = Math.max(0, Math.floor(rect.y * scale));
  const w = Math.min(bitmap.width - x, Math.ceil((rect.x + rect.width) * scale) - x);
  const h = Math.min(bitmap.height - y, Math.ceil((rect.y + rect.height) * scale) - y);
  if (w <= 0 || h <= 0) return full; // element is off-screen: send the whole viewport
  const canvas = new OffscreenCanvas(w, h);
  canvas.getContext('2d').drawImage(bitmap, x, y, w, h, 0, 0, w, h);
  const blob = await canvas.convertToBlob({ type: 'image/png' });
  return 'data:image/png;base64,' + toBase64(await blob.arrayBuffer());
}

/** @returns {Promise<{ok:boolean, error?:string}>} */
async function handlePick(msg, sender) {
  const pick = msg.pick;
  try {
    pick.screenshot = await captureCrop(sender.tab.windowId, pick.rect, msg.viewportWidth);
  } catch (e) {
    console.warn('boba: screenshot failed', e);
  }
  const res = await post('/pick', pick);
  flashBadge(sender.tab.id, res.ok);
  return res;
}

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg?.type === 'pick' && sender.tab) {
    handlePick(msg, sender).then(sendResponse);
    return true; // async response keeps the SW alive until done
  }
  if (msg?.type === 'console' && sender.tab) {
    post('/console', { url: sender.tab.url || msg.url, errors: msg.errors });
  }
  return false;
});

// --- localhost tab list -------------------------------------------------
let tabsTimer;
function scheduleTabs() {
  clearTimeout(tabsTimer);
  tabsTimer = setTimeout(async () => {
    const tabs = await chrome.tabs.query({ url: LOCAL_MATCH });
    post('/tabs', { tabs: tabs.map((t) => ({ url: t.url, title: t.title || '' })) });
  }, 1000);
}
chrome.tabs.onCreated.addListener(scheduleTabs);
chrome.tabs.onRemoved.addListener(scheduleTabs);
chrome.tabs.onUpdated.addListener((_id, info) => {
  if (info.url || info.title || info.status === 'complete') scheduleTabs();
});
chrome.runtime.onStartup.addListener(scheduleTabs);
chrome.runtime.onInstalled.addListener(scheduleTabs);
