// boba options: port + token in chrome.storage.local, and a /health check.
const $ = (id) => document.getElementById(id);
const DEFAULT_PORT = 47777;

function status(text, ok) {
  $('status').textContent = text;
  $('status').className = ok ? 'ok' : 'ng';
}

async function save() {
  const port = Number($('port').value) || DEFAULT_PORT;
  const token = $('token').value.trim();
  await chrome.storage.local.set({ port, token });
  return { port, token };
}

chrome.storage.local.get(['port', 'token']).then(({ port, token }) => {
  $('port').value = port || DEFAULT_PORT;
  $('token').value = token || '';
});

$('save').addEventListener('click', async () => {
  await save();
  status('Saved', true);
});

$('test').addEventListener('click', async () => {
  const { port, token } = await save();
  status('Connecting…', true);
  try {
    const res = await fetch(`http://127.0.0.1:${port}/health`, {
      headers: { Authorization: `Bearer ${token}` },
      signal: AbortSignal.timeout(3000),
    });
    const body = await res.json().catch(() => ({}));
    if (res.ok && body.ok) status(`Connected (127.0.0.1:${port})`, true);
    else if (res.status === 401) status('Invalid token (401)', false);
    else status(`Error: HTTP ${res.status}`, false);
  } catch (e) {
    status(`Cannot connect to bobad: ${e.message}`, false);
  }
});
