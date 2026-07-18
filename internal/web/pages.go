package web

// indexHTML is the session list page: it polls /api/sessions, lets the user
// open a terminal, create a new session, or kill one.
const indexHTML = `<!doctype html>
<html lang="it">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>ivt — sessioni</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin: 0; padding: 2rem; background: #0d1117; color: #e6edf3;
         font: 15px/1.5 system-ui, sans-serif; }
  h1 { font-size: 1.3rem; margin: 0 0 1.2rem; }
  h1 .dot { color: #3fb950; }
  table { border-collapse: collapse; width: 100%; max-width: 60rem; }
  th, td { text-align: left; padding: .55rem .8rem; border-bottom: 1px solid #21262d; }
  th { color: #8b949e; font-weight: 500; font-size: .85rem; text-transform: uppercase; }
  a.sess { color: #58a6ff; text-decoration: none; font-weight: 600; }
  a.sess:hover { text-decoration: underline; }
  .cmd { color: #8b949e; font-family: ui-monospace, monospace; font-size: .9rem; }
  .badge { background: #1f6feb33; color: #58a6ff; border-radius: 1rem;
           padding: .1rem .6rem; font-size: .8rem; }
  button { background: #21262d; color: #e6edf3; border: 1px solid #30363d;
           border-radius: 6px; padding: .35rem .8rem; cursor: pointer; }
  button:hover { background: #30363d; }
  button.kill { color: #f85149; }
  form { margin-top: 1.5rem; display: flex; gap: .6rem; max-width: 60rem; }
  input { background: #0d1117; color: #e6edf3; border: 1px solid #30363d;
          border-radius: 6px; padding: .45rem .7rem; }
  input.name { width: 11rem; }
  input.command { flex: 1; font-family: ui-monospace, monospace; }
  .empty { color: #8b949e; padding: 1.5rem 0; }
  .err { color: #f85149; margin-top: .8rem; min-height: 1.2em; }
</style>
</head>
<body>
<h1><span class="dot">●</span> ivt — sessioni attive</h1>
<table>
  <thead><tr><th>Sessione</th><th>Comando</th><th>Età</th><th>Client</th><th></th></tr></thead>
  <tbody id="rows"></tbody>
</table>
<div id="empty" class="empty" hidden>Nessuna sessione attiva. Creane una qui sotto.</div>
<form id="new">
  <input class="name" id="name" placeholder="nome (es. work)" required>
  <input class="command" id="command" placeholder="comando (vuoto = shell), es. claude">
  <button type="submit">Nuova sessione</button>
</form>
<div class="err" id="err"></div>
<script>
const esc = s => s.replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const age = t => { const d = Math.max(0, Date.now()/1000 - t);
  return d < 60 ? Math.floor(d)+'s' : d < 3600 ? Math.floor(d/60)+'m' : Math.floor(d/3600)+'h'; };

async function refresh() {
  try {
    const rs = await fetch('/api/sessions');
    if (!rs.ok) throw new Error(await rs.text());
    const list = await rs.json();
    document.getElementById('empty').hidden = list.length > 0;
    document.getElementById('rows').innerHTML = list.map(s => '<tr>' +
      '<td><a class="sess" href="/s/' + encodeURIComponent(s.name) + '">' + esc(s.name) + '</a></td>' +
      '<td class="cmd">' + esc(s.cmd.join(' ')) + '</td>' +
      '<td>' + age(s.created) + '</td>' +
      '<td>' + (s.attached > 0 ? '<span class="badge">' + s.attached + ' attaccati</span>' : '—') + '</td>' +
      '<td><button class="kill" data-name="' + esc(s.name) + '">kill</button></td>' +
      '</tr>').join('');
  } catch (e) { showErr('server non raggiungibile: ' + e.message); }
}

function showErr(m) { document.getElementById('err').textContent = m;
  setTimeout(() => document.getElementById('err').textContent = '', 5000); }

document.getElementById('rows').addEventListener('click', async e => {
  const b = e.target.closest('button.kill');
  if (!b) return;
  if (!confirm('Terminare la sessione "' + b.dataset.name + '"?')) return;
  const rs = await fetch('/api/kill', { method: 'POST',
    body: JSON.stringify({ name: b.dataset.name }) });
  if (!rs.ok && rs.status !== 204) showErr(await rs.text());
  refresh();
});

document.getElementById('new').addEventListener('submit', async e => {
  e.preventDefault();
  const name = document.getElementById('name').value.trim();
  const cmd = document.getElementById('command').value.trim();
  const rs = await fetch('/api/new', { method: 'POST',
    body: JSON.stringify({ name, cmd: cmd ? cmd.split(/\s+/) : [] }) });
  if (!rs.ok) { showErr(await rs.text()); return; }
  const { name: created } = await rs.json();
  location.href = '/s/' + encodeURIComponent(created);
});

refresh();
setInterval(refresh, 3000);
</script>
</body>
</html>`

// terminalHTML is the terminal page for one session; {{NAME}} is substituted
// (HTML-escaped) by the handler.
const terminalHTML = `<!doctype html>
<html lang="it">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>ivt — {{NAME}}</title>
<link rel="stylesheet" href="/assets/xterm.css">
<style>
  :root { color-scheme: dark; }
  html, body { height: 100%; }
  body { margin: 0; background: #0d1117; display: flex; flex-direction: column; }
  header { display: flex; align-items: center; gap: 1rem; padding: .4rem .8rem;
           background: #161b22; color: #e6edf3; font: 14px system-ui, sans-serif; }
  header a { color: #58a6ff; text-decoration: none; }
  header .name { font-weight: 600; }
  header .status { margin-left: auto; color: #8b949e; font-size: .85rem; }
  #term { flex: 1; padding: .4rem; min-height: 0; }
  .xterm { height: 100%; }
  #overlay { position: fixed; inset: 0; display: none; align-items: center;
             justify-content: center; background: #0d1117cc; }
  #overlay .box { background: #161b22; border: 1px solid #30363d; border-radius: 8px;
                  padding: 1.5rem 2rem; color: #e6edf3; font: 15px system-ui, sans-serif;
                  text-align: center; }
  #overlay a { color: #58a6ff; }
</style>
</head>
<body>
<header>
  <a href="/">&larr; sessioni</a>
  <span class="name">{{NAME}}</span>
  <span class="status" id="status">connessione…</span>
</header>
<div id="term"></div>
<div id="overlay"><div class="box" id="overlay-msg"></div></div>
<script src="/assets/xterm.js"></script>
<script src="/assets/addon-fit.js"></script>
<script>
const NAME = decodeURIComponent(location.pathname.replace(/^\/s\//, ''));
const status = m => document.getElementById('status').textContent = m;
const overlay = html => { document.getElementById('overlay-msg').innerHTML = html;
  document.getElementById('overlay').style.display = 'flex'; };

const term = new Terminal({
  fontSize: 14,
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
  scrollback: 5000,
  theme: { background: '#0d1117', foreground: '#e6edf3', cursor: '#58a6ff' },
});
const fit = new FitAddon.FitAddon();
term.loadAddon(fit);
term.open(document.getElementById('term'));
fit.fit();
term.focus();

const proto = location.protocol === 'https:' ? 'wss' : 'ws';
const ws = new WebSocket(proto + '://' + location.host + '/ws/' +
  encodeURIComponent(NAME) + '?cols=' + term.cols + '&rows=' + term.rows);
ws.binaryType = 'arraybuffer';
const enc = new TextEncoder();

ws.onopen = () => status(term.cols + '×' + term.rows);
ws.onmessage = e => {
  if (typeof e.data === 'string') {
    const ev = JSON.parse(e.data);
    if (ev.event === 'switch') { location.href = '/s/' + encodeURIComponent(ev.session); return; }
    if (ev.event === 'exit') {
      overlay('Il processo è terminato' + (ev.code ? ' (exit ' + ev.code + ')' : '') +
        (ev.error ? '<br>' + ev.error : '') + '.<br><br><a href="/">Torna alle sessioni</a>');
    } else {
      overlay('Sessione staccata.<br><br><a href="">Riattacca</a> · <a href="/">Sessioni</a>');
    }
    return;
  }
  term.write(new Uint8Array(e.data));
};
ws.onclose = () => { status('disconnesso');
  if (document.getElementById('overlay').style.display !== 'flex')
    overlay('Connessione persa.<br><br><a href="">Riconnetti</a> · <a href="/">Sessioni</a>'); };

term.onData(d => { if (ws.readyState === 1) ws.send(enc.encode(d)); });
term.onBinary(d => { if (ws.readyState === 1) {
  const b = new Uint8Array(d.length);
  for (let i = 0; i < d.length; i++) b[i] = d.charCodeAt(i) & 255;
  ws.send(b);
}});
term.onResize(({ cols, rows }) => { status(cols + '×' + rows);
  if (ws.readyState === 1) ws.send(JSON.stringify({ cols, rows })); });

new ResizeObserver(() => fit.fit()).observe(document.getElementById('term'));
</script>
</body>
</html>`
