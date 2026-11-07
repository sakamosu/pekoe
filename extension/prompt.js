// Instruction box shown in an extension-owned iframe. Living in its own
// document keeps IME composition (e.g. Google 日本語入力) inline and away from
// page scripts/CSS and the overlay host. Talks to content.js by postMessage.
(() => {
  const params = new URLSearchParams(location.hash.slice(1));
  const nonce = params.get('n') || '';
  const input = document.getElementById('in');
  document.getElementById('hint').textContent = `${params.get('label') || ''} — Enter で送信 / Esc で中止`;
  const post = (type, value) => parent.postMessage({ boba: 'prompt', nonce, type, value }, '*');
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
