// BombeCam: "Use with Frigate / Home Assistant" page.
// Reads /api/v1/integrations/settings, /frigate and /homeassistant, and saves
// changes with the same session + CSRF token as the main page.
(function () {
  'use strict';

  const page = { csrf: '', settings: null, frigate: null, ha: null, where: 'other', busy: false };

  function $(id) { return document.getElementById(id); }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }

  function toast(msg, type) {
    const box = $('toast-container');
    if (!box) return;
    const t = document.createElement('div');
    t.className = 'toast toast-' + (type || 'info');
    t.textContent = msg;
    box.appendChild(t);
    setTimeout(() => t.remove(), 4000);
  }

  async function fetchCSRF() {
    try {
      const r = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin' });
      const d = await r.json();
      page.csrf = (d && d.csrf_token) || '';
    } catch (e) { page.csrf = ''; }
  }

  async function api(path, method, body) {
    method = method || 'GET';
    const opts = { method, credentials: 'same-origin', headers: { 'Accept': 'application/json' } };
    if (method !== 'GET') {
      if (!page.csrf) await fetchCSRF();
      opts.headers['X-CSRF-Token'] = page.csrf;
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body || {});
    }
    let res = await fetch(path, opts);
    if (method !== 'GET' && (res.status === 401 || res.status === 403)) {
      await fetchCSRF();
      opts.headers['X-CSRF-Token'] = page.csrf;
      res = await fetch(path, opts);
    }
    let data = {};
    try { data = await res.json(); } catch (e) { data = {}; }
    // Signed out: the main page has the sign-in.
    if (res.status === 401 && data.error === 'unauthorized') location.href = '/';
    return { ok: res.ok, status: res.status, data };
  }

  async function copyText(text, btn) {
    if (!text) return;
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(text);
      } else {
        const ta = document.createElement('textarea');
        ta.value = text;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        ta.remove();
      }
      if (btn) {
        const old = btn.textContent;
        btn.textContent = '✓ Copied';
        setTimeout(() => { btn.textContent = old; }, 1500);
      }
    } catch (e) {
      toast('Could not copy. Select the text and copy it by hand.', 'warning');
    }
  }

  // ---- where Frigate / Home Assistant run: swap the host in addresses ----
  function hostFor(where) {
    if (where === 'docker') return 'host.docker.internal';
    if (where === 'same') return '127.0.0.1';
    return null;
  }

  function swapHost(text) {
    const s = page.settings;
    const to = hostFor(page.where);
    if (!s || !to || !text || s.override) return text || '';
    const from = s.advertised_address;
    const re = new RegExp('((?:rtsp|http)://(?:[^/@\\s"]*@)?)' + from.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + ':', 'g');
    return text.replace(re, '$1' + to + ':');
  }

  function copyRow(label, value, id) {
    return `<div class="integ-copyrow"><span class="integ-copyrow-label">${esc(label)}</span>` +
      `<code class="integ-copyrow-value" id="${esc(id)}">${esc(value)}</code>` +
      `<button type="button" class="btn btn-secondary btn-sm btn-copy" data-copy-from="${esc(id)}">📋 Copy</button></div>`;
  }

  // ---- rendering ----
  function renderWarnings() {
    const s = page.settings;
    const box = $('integ-warnings');
    const items = [];
    if (s && !s.has_profile) items.push('Add your cameras first (Back to BombeCam, then Add cameras).');
    if (s && !s.media_server_ok && s.media_server_detail) items.push('Video server: ' + s.media_server_detail);
    if (s && s.stream_names_error) items.push('Stream names: ' + s.stream_names_error);
    (s && s.warnings || []).forEach(w => items.push(w));
    if (!items.length) { box.hidden = true; box.innerHTML = ''; return; }
    box.hidden = false;
    box.innerHTML = items.map(t => `<div class="integ-warning">⚠ ${esc(t)}</div>`).join('');
  }

  function renderAddresses() {
    const s = page.settings;
    const box = $('integ-addresses');
    const note = $('integ-address-note');
    if (s.override) {
      box.innerHTML = `<p class="integ-note">Set when BombeCam was started: <code>${esc(s.override)}</code> (BOMBECAM_RTSP_CONSUMER_BASE).</p>`;
      note.textContent = '';
      return;
    }
    const auto = (s.addresses || []).find(a => a.default);
    const rows = [];
    const autoLabel = auto ? `${auto.ip}${auto.interface ? ' · ' + auto.interface : ''}` : (s.address_source === 'loopback' ? 'none found' : s.advertised_address);
    rows.push({ value: '', label: `Automatic (recommended): ${autoLabel}`, hint: "the network Windows uses for the internet" });
    (s.addresses || []).forEach(a => rows.push({ value: a.ip, label: `${a.ip}${a.interface ? ' · ' + a.interface : ''}`, hint: a.default ? 'the same as Automatic' : '' }));
    const chosen = s.chosen_missing ? '' : (s.nvr_address || '');
    box.innerHTML = rows.map((r, i) =>
      `<label class="integ-addr"><input type="radio" name="integ-addr" value="${esc(r.value)}" ${r.value === chosen ? 'checked' : ''}>` +
      `<span>${esc(r.label)}${r.hint ? ` <span class="text-muted">(${esc(r.hint)})</span>` : ''}</span></label>`).join('');
    box.querySelectorAll('input[name="integ-addr"]').forEach(inp => inp.addEventListener('change', () => saveSettings({ nvr_address: inp.value }, 'Address saved. Copy the addresses again into Frigate or Home Assistant.')));
    if (s.address_source === 'page') {
      note.innerHTML = `This page was opened as <code>${esc(s.advertised_address)}</code>, so that address is used.`;
    } else {
      note.textContent = '';
    }
  }

  function renderCams() {
    const s = page.settings;
    const box = $('integ-cams');
    const cams = s.cameras || [];
    if (!cams.length) { box.innerHTML = '<p class="text-muted">No cameras yet.</p>'; return; }
    box.innerHTML = cams.map((c, i) => {
      const size = c.width ? ` <span class="text-muted">· ${c.width}×${c.height}</span>` : '';
      const live = c.streaming ? '<span class="integ-live">live</span>' : '<span class="integ-idle">not streaming</span>';
      let html = `<div class="integ-cam"><div class="integ-cam-name">${esc(c.name)} ${live}${size}</div>`;
      html += copyRow('Stream (RTSP)', swapHost(c.rtsp_url), 'cam-rtsp-' + i);
      if (c.snapshot_url) html += copyRow('Snapshot', swapHost(c.snapshot_url), 'cam-snap-' + i);
      return html + '</div>';
    }).join('');
    const first = cams[0];
    if (first) $('integ-ffprobe').textContent = 'ffprobe -rtsp_transport tcp ' + swapHost(first.rtsp_url);
  }

  function renderWhere() {
    const note = $('integ-where-note');
    const msgs = {
      other: 'They use this PC\'s network address, shown above. Windows has to let them in: see <a href="#firewall">If other devices can\'t connect</a>.',
      docker: 'Containers in Docker Desktop reach this PC as <code>host.docker.internal</code>: the addresses on this page now use it.',
      same: 'Programs on this PC reach it as <code>127.0.0.1</code>: the addresses on this page now use it.'
    };
    note.innerHTML = msgs[page.where] || msgs.other;
  }

  function renderFrigate() {
    const f = page.frigate || {};
    $('integ-frigate').textContent = swapHost(f.config_yaml || '# Could not load the Frigate configuration.');
    $('integ-frigate-new').textContent = swapHost(f.config_yaml_new || '');
  }

  function renderHA() {
    const box = $('integ-ha-cams');
    const cams = (page.ha && page.ha.cameras) || [];
    if (!cams.length) { box.innerHTML = '<p class="text-muted">No cameras yet.</p>'; return; }
    box.innerHTML = cams.map((c, i) => {
      const g = c.generic_camera || {};
      let html = `<div class="integ-ha-cam"><h3>${esc(c.name)}</h3><ol class="integ-steps">` +
        '<li>In Home Assistant, open <strong>Settings &rarr; Devices &amp; services</strong>.</li>' +
        '<li>Click <strong>Add integration</strong>, search for <strong>Generic Camera</strong> and select it.</li>' +
        '<li>Fill in:</li></ol>';
      html += copyRow('Stream source URL', swapHost(g.stream_source), 'ha-stream-' + i);
      if (g.still_image_url) {
        html += copyRow('Still image URL', swapHost(g.still_image_url), 'ha-still-' + i);
      } else {
        html += '<div class="integ-copyrow"><span class="integ-copyrow-label">Still image URL</span><span class="text-muted">leave empty (or turn on Snapshots under Options)</span></div>';
      }
      html += '<div class="integ-copyrow"><span class="integ-copyrow-label">Username, Password</span><span class="text-muted">leave empty' +
        (String(g.stream_source || '').includes('@') ? ' (the addresses above already carry them)' : '') + '</span></div>';
      html += '<div class="integ-copyrow"><span class="integ-copyrow-label">More options</span><span class="text-muted">keep the defaults (<strong>RTSP transport protocol</strong> is already TCP)</span></div>';
      html += '<ol class="integ-steps" start="4"><li>Submit. When the preview shows the picture, tick <strong>Everything looks good.</strong> and submit again.</li></ol></div>';
      return html;
    }).join('');
  }

  function mqttStatusText(m) {
    switch (m.status) {
      case 'connected': return '● Connected: cameras are in Home Assistant.';
      case 'connecting': return '◌ Connecting to ' + (m.host || 'the broker') + '...' + (m.detail ? ' (' + m.detail + ')' : '');
      case 'error': return '✕ Not connected: ' + (m.detail || 'unknown error');
      default: return 'Off.';
    }
  }

  function renderStatuses() {
    const s = page.settings;
    const m = s.mqtt || {};
    const st = $('mqtt-status');
    st.textContent = mqttStatusText(m);
    st.className = 'integ-status integ-status-' + (m.status || 'off');
    $('opt-snapshot-status').textContent = s.snapshots
      ? (s.ffmpeg ? 'Snapshots: ' + (s.snapshot_status || '') : 'Snapshots need FFmpeg on this PC (winget install Gyan.FFmpeg), then restart BombeCam.')
      : '';
  }

  function renderForms(full) {
    const s = page.settings;
    const m = s.mqtt || {};
    if (full) {
      $('mqtt-enabled').checked = !!m.enabled;
      $('mqtt-host').value = m.host || '';
      $('mqtt-port').value = m.port || 1883;
      $('mqtt-user').value = m.username || '';
      $('mqtt-pass').value = '';
      $('mqtt-pass').placeholder = m.has_password ? '(saved; type to change)' : '';
      $('mqtt-prefix').value = m.discovery_prefix || 'homeassistant';
      const p = s.ports || {};
      $('port-rtsp').value = p.rtsp; $('port-hls').value = p.hls; $('port-webrtc').value = p.webrtc;
      $('port-webrtc-udp').value = p.webrtc_udp; $('port-snapshot').value = p.snapshot;
    }
    $('opt-stream-auth').checked = !!s.requested_stream_auth;
    $('opt-stream-auth').disabled = !s.stream_auth_editable;
    $('opt-stream-auth').title = s.stream_auth_detail || '';
    $('opt-stream-regen').disabled = !s.stream_auth_editable;
    $('opt-stream-status').textContent = s.stream_auth_verified ? (s.stream_auth ? 'Password protection verified on the running video server.' : 'Public stream access verified on the running video server.') : ('Stream access policy unverified. ' + (s.stream_auth_detail || ''));
    $('opt-stream-creds').hidden = !s.requested_stream_auth;
    $('opt-stream-user').textContent = s.stream_user || '';
    $('opt-stream-pass').textContent = s.stream_password || '';
    $('opt-snapshots').checked = !!s.snapshots;
    $('opt-snapshot-port').textContent = (s.ports && s.ports.snapshot) || 8655;
    const editable = !!s.ports_editable;
    ['port-rtsp', 'port-hls', 'port-webrtc', 'port-webrtc-udp'].forEach(id => { $(id).disabled = !editable; });
    $('ports-save').disabled = false;
    $('ports-default').disabled = !editable;
    if (!editable && s.ports_note) $('opt-ports-note').textContent = s.ports_note;
    const noProfile = !s.has_profile;
    document.querySelectorAll('#integ-mqtt-form input, #integ-mqtt-form button, #options input, #options button').forEach(el => {
      if (noProfile) el.disabled = true;
    });
  }

  function renderAll(full) {
    if (!page.settings) return;
    renderWarnings();
    renderAddresses();
    renderCams();
    renderWhere();
    renderFrigate();
    renderHA();
    renderForms(full);
    renderStatuses();
  }

  async function load(full) {
    const [s, f, h] = await Promise.all([
      api('/api/v1/integrations/settings'),
      api('/api/v1/integrations/frigate'),
      api('/api/v1/integrations/homeassistant')
    ]);
    if (s.ok) page.settings = s.data;
    if (f.ok) page.frigate = f.data;
    if (h.ok) page.ha = h.data;
    renderAll(full);
  }

  async function saveSettings(body, okMsg) {
    if (page.busy) return false;
    page.busy = true;
    try {
      const r = await api('/api/v1/integrations/settings', 'POST', body);
      if (!r.ok) {
        toast((r.data && (r.data.message || r.data.error)) || ('Could not save (HTTP ' + r.status + ').'), 'error');
        await load(false);
        return false;
      }
      page.settings = r.data.settings || page.settings;
      await load(false);
      const warn = (r.data.settings && r.data.settings.warnings) || [];
      if (warn.length && warn[0].indexOf('could not be restarted') >= 0) toast(warn[0], 'warning');
      else if (okMsg) toast(okMsg, 'success');
      return true;
    } finally {
      page.busy = false;
    }
  }

  function bind() {
    document.addEventListener('click', e => {
      const btn = e.target.closest('[data-copy-from]');
      if (btn) {
        const src = $(btn.getAttribute('data-copy-from'));
        copyText(src ? src.textContent : '', btn);
      }
    });
    document.querySelectorAll('input[name="integ-where"]').forEach(r => r.addEventListener('change', () => {
      page.where = r.value;
      try { localStorage.setItem('bombecam-integ-where', page.where); } catch (e) {}
      renderAll(false);
    }));
    try {
      const w = localStorage.getItem('bombecam-integ-where');
      if (w) {
        page.where = w;
        const r = document.querySelector(`input[name="integ-where"][value="${w}"]`);
        if (r) r.checked = true;
      }
    } catch (e) {}

    $('integ-mqtt-form').addEventListener('submit', async e => {
      e.preventDefault();
      const body = {
        enabled: $('mqtt-enabled').checked,
        host: $('mqtt-host').value.trim(),
        port: parseInt($('mqtt-port').value, 10) || 1883,
        username: $('mqtt-user').value.trim(),
        discovery_prefix: $('mqtt-prefix').value.trim()
      };
      const pw = $('mqtt-pass').value;
      if (pw) body.password = pw;
      if (await saveSettings({ mqtt: body }, body.enabled ? 'Saved. Connecting to the broker...' : 'Saved.')) {
        $('mqtt-pass').value = '';
        renderForms(true);
      }
    });
    $('opt-stream-auth').addEventListener('change', e => {
      const on = e.target.checked;
      saveSettings({ stream_auth: on }, on ? 'Stream password on. Copy the new addresses into Frigate and Home Assistant.' : 'Stream password off.');
    });
    $('opt-stream-regen').addEventListener('click', () => {
      if (confirm('Make a new stream password? Frigate and Home Assistant need the new addresses afterwards.')) {
        saveSettings({ regenerate_password: true }, 'New password made. Copy the new addresses into Frigate and Home Assistant.');
      }
    });
    $('opt-snapshots').addEventListener('change', e => {
      saveSettings({ snapshots: e.target.checked }, e.target.checked ? 'Snapshots on.' : 'Snapshots off.');
    });
    const portsBody = () => ({
      rtsp: parseInt($('port-rtsp').value, 10) || 0,
      hls: parseInt($('port-hls').value, 10) || 0,
      webrtc: parseInt($('port-webrtc').value, 10) || 0,
      webrtc_udp: parseInt($('port-webrtc-udp').value, 10) || 0,
      snapshot: parseInt($('port-snapshot').value, 10) || 0
    });
    $('ports-save').addEventListener('click', async () => {
      if (await saveSettings({ ports: portsBody() }, 'Ports saved. The video server restarted; the cameras reconnect in a few seconds.')) renderForms(true);
    });
    $('ports-default').addEventListener('click', async () => {
      if (await saveSettings({ ports: { rtsp: 0, hls: 0, webrtc: 0, webrtc_udp: 0, snapshot: 0 } }, 'Default ports restored.')) renderForms(true);
    });
  }

  async function init() {
    bind();
    await fetchCSRF();
    await load(true);
    // keep the status lines (MQTT, snapshots, warnings) current
    setInterval(async () => {
      if (page.busy) return;
      const s = await api('/api/v1/integrations/settings');
      if (s.ok) {
        const camsChanged = JSON.stringify(s.data.cameras) !== JSON.stringify(page.settings && page.settings.cameras);
        page.settings = s.data;
        renderWarnings();
        renderStatuses();
        if (camsChanged) await load(false);
      }
    }, 5000);
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
