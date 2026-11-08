// Instruction box shown in an extension-owned iframe. Living in its own
// document keeps IME composition (e.g. Google 日本語入力) inline and away from
// page scripts/CSS and the overlay host.
//
// It talks to content.js over a MessageChannel port, never by posting to
// the parent window: content.js shares that window with the page, so the
// page would read every message (the instruction the user typed). The port
// comes in one message from the parent carrying the nonce of our URL (which
// the page cannot see); the first such port is the only one used.
(() => {
  const params = new URLSearchParams(location.hash.slice(1));
  const nonce = params.get('n') || '';
  const input = document.getElementById('in');
  document.getElementById('hint').textContent = `${params.get('label') || ''} — Enter to send / Esc to cancel`;
  let port = null;
  window.addEventListener('message', (e) => {
    const d = e.data;
    if (port || e.source !== parent || !nonce || !d || d.pekoe !== 'port' || d.nonce !== nonce || !e.ports[0]) return;
    port = e.ports[0];
  });
  const post = (type, value) => port?.postMessage({ type, value });
  let composing = false;
  input.addEventListener('compositionstart', () => { composing = true; });
  input.addEventListener('compositionend', () => { setTimeout(() => { composing = false; }, 0); });
  input.addEventListener('keydown', (e) => {
    // Enter that confirms an IME conversion must not send (keyCode 229 on macOS)
    if (e.isComposing || composing || e.keyCode === 229) return;
    if (e.key === 'Escape') { e.preventDefault(); post('cancel'); }
    else if (e.key === 'Enter') { e.preventDefault(); post('submit', input.value.trim()); }
  });
  const focus = () => input.focus();
  window.addEventListener('focus', focus);
  focus();
})();
