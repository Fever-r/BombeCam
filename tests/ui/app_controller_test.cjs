// Exercise the shipped controller with delayed browser/network operations.
const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const path = require('node:path');
const app = fs.readFileSync(path.join(__dirname, '../../web/app.js'), 'utf8');
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
const flush = () => new Promise(r => setImmediate(r));

function fixture(callbacks = false) {
  let now = 10000;
  let nextCallback;
  const player = {
    src: 'synthetic-stream', srcObject: null, paused: false, ended: false,
    videoWidth: 640, videoHeight: 480, webkitDecodedFrameCount: 5,
    pause() { this.paused = true; }, removeAttribute() { this.src = ''; }, load() {},
  };
  if (callbacks) {
    player.requestVideoFrameCallback = cb => { nextCallback = cb; return 1; };
    player.cancelVideoFrameCallback = () => {};
  }
  const ctx = vm.createContext({ console, performance: { now: () => now },
    document: { addEventListener() {}, getElementById: id => id === 'preview-player' ? player : null },
    window: {}, navigator: {}, setTimeout, clearTimeout, setInterval, clearInterval,
    Blob, AbortController, ArrayBuffer, Uint8Array, Int16Array,
  });
  vm.runInContext(app, ctx);
  const run = code => vm.runInContext(code, ctx);
  run(`updateDecodedFrameBadge = s => window.badge = s; setTalkUI = () => {};
    showToast = () => {}; viewerTick = async () => {}; pollShadowTelemetry = async () => {};`);
  return { ctx, player, run, clock: n => { now = n; }, frame: n => nextCallback(0, { presentedFrames: n }), callback: () => nextCallback };
}

function microphone() {
  const track = { stops: 0, stop() { this.stops++; } };
  return { track, stream: { getTracks: () => [track] } };
}

function audioContext(f, resume) {
  const stats = { connections: 0, closes: 0 };
  const node = () => ({ gain: {}, connect() { stats.connections++; }, disconnect() {} });
  f.ctx.window.AudioContext = class {
    constructor() { this.state = resume ? 'suspended' : 'running'; this.sampleRate = 48000; this.destination = {}; }
    resume() { return resume.promise.then(() => { this.state = 'running'; }); }
    close() { stats.closes++; return Promise.resolve(); }
    createMediaStreamSource() { return node(); }
    createScriptProcessor() { return node(); }
    createGain() { return node(); }
  };
  return stats;
}

async function test(name, fn) { await fn(); console.log('PASS:', name); }

(async () => {
  await test('camera models map to their controls, ignoring Osaio hardware revisions', async () => {
    const f = fixture();
    const caps = m => { const c = f.run(`getCameraCapabilities(${JSON.stringify(m)})`); return [c.ptz, c.spotlight]; };
    assert.deepEqual(caps('WS03'), [true, false]);
    assert.deepEqual(caps('WS01'), [false, false]);
    assert.deepEqual(caps('GC3_A3S11A3'), [false, false]);
    assert.deepEqual(caps('K1PRO'), [true, true]);
    assert.deepEqual(caps('GW30'), [true, true]);
    assert.deepEqual(caps('GT1PRO'), [false, false]);
    assert.deepEqual(caps('unknown'), [true, false]);
    assert.equal(f.run(`cameraModelKey('GP5')`), 'P5');
    assert.equal(f.run(`cameraModelKey('P10')`), 'P10');
    assert.equal(f.run(`cameraModelKey('C1_P1')`), 'C1');
    assert.equal(f.run(`cameraModelKey('XP1C1')`), 'C1');
  });
  await test('a successfully sent Talk start needs no acknowledgement warning', async () => {
    const f = fixture(); const mic = microphone(); const notices = [];
    audioContext(f);
    f.ctx.navigator.mediaDevices = { getUserMedia: async () => mic.stream };
    f.ctx.notice = (text, kind) => notices.push([text, kind]);
    f.run(`showToast = notice; state.selectedCameraId = 'A';
      apiRequest = async () => ({ok:true,json:async()=>({camera_answered:false})});`);
    await f.run('startTalkback()'); assert.equal(f.run('state.isTalkActive'), true);
    assert.deepEqual(notices, []); await f.run('stopTalkback()'); assert.equal(mic.track.stops, 1);
  });
  await test('auxiliary commands finish without telemetry polling or missing-response warnings', async () => {
    const f = fixture(); const calls = []; const notices = [];
    f.ctx.notice = (text, kind) => notices.push([text, kind]);
    f.ctx.request = async endpoint => { calls.push(endpoint); return { ok: true }; };
    f.run(`apiRequest = request; showToast = notice; state.selectedCameraId = 'A';
      pollShadowTelemetry = () => { throw new Error('confirmation poll must not run'); };`);
    await f.run(`sendAuxControl('led', 'off')`);
    assert.deepEqual(calls, ['/api/v1/cameras/A/control']);
    assert.deepEqual(notices, [['Status light: command sent.', 'success']]);
    assert.equal(f.run('state.updatingControls.size'), 0);
  });
  await test('genuine auxiliary send failures remain visible and release buttons', async () => {
    const f = fixture(); const notices = [];
    f.ctx.notice = (text, kind) => notices.push([text, kind]);
    f.run(`showToast = notice; state.selectedCameraId = 'A';
      apiRequest = async () => ({ ok: false, json: async () => ({error:'send failed'}) });`);
    await f.run(`sendAuxControl('led', 'off')`);
    assert.deepEqual(notices, [['send failed', 'error']]);
    assert.equal(f.run('state.updatingControls.size'), 0);
  });
  await test('audio-context resume failure stops microphone and the remote Talk session', async () => {
    const f = fixture(); const mic = microphone(); const resume = deferred(); const calls = [];
    const stats = audioContext(f, resume);
    f.ctx.navigator.mediaDevices = { getUserMedia: async () => mic.stream };
    f.ctx.request = async endpoint => { calls.push(endpoint); return {ok:true,json:async()=>({})}; };
    f.run(`apiRequest = request; state.selectedCameraId = 'A';`);
    const starting = f.run('startTalkback()'); await flush();
    // Reject after the start handler is awaiting resume, avoiding an unhandled rejection.
    // deferred exposes its resolver only; resolve with a rejected thenable.
    resume.resolve(Promise.reject(new Error('audio device unavailable'))); await starting;
    assert.equal(f.run('state.isTalkActive || state.talkStarting'), false);
    assert.equal(mic.track.stops, 1); assert.equal(stats.closes, 1); assert.equal(stats.connections, 0);
    assert.deepEqual(calls, ['/api/v1/cameras/A/talk/start','/api/v1/cameras/A/talk/stop']);
  });
  await test('country inference requires a region and preserves saved account-specific codes', () => {
    const f = fixture();
    for (const [locale, expected] of [['en', ''], ['fr', ''], ['zz-ZZ', ''], ['en-US', '1'], ['fr-CA', '1'], ['zh-Hant-CN', '86'], ['en-US-u-ca-gregory', '1']]) {
      f.ctx.navigator.languages = [locale]; assert.equal(f.run('inferCountryCode()'), expected, locale);
    }
    f.run(`state.profile = { accounts: [{ account_email: 'a@example.test', country: '44' }, { account_email: 'b@example.test', country: '39' }] };`);
    assert.equal(f.run(`suggestedAccountCountry('a@example.test')`), '44');
    assert.equal(f.run(`suggestedAccountCountry('b@example.test')`), '39');
    assert.equal(f.run(`normalizedCountryInput('+64')`), '64');
    assert.equal(f.run(`normalizedCountryInput('US')`), '');
  });
  await test('unknown country prevents login; explicit correction sends one setup request', async () => {
    const f = fixture(); const calls = [];
    const inputs = { 'input-email': { value: 'a@example.test' }, 'input-password': { value: 'synthetic' }, 'input-country': { value: '' }, 'btn-login-go': {} };
    f.ctx.document.getElementById = id => inputs[id] || null;
    f.ctx.navigator.languages = ['en'];
    f.ctx.request = async (endpoint, method, payload) => { calls.push({ endpoint, method, payload }); return { ok: true, json: async () => ({}) }; };
    f.run(`apiRequest = request; checkOnboardingStatus = async () => {}; loadDiscoveredCameras = async () => {};`);
    await f.run('handleLoginGo()'); assert.equal(calls.length, 0);
    inputs['input-country'].value = '+44'; await f.run('handleLoginGo()');
    const setups = calls.filter(c => c.endpoint === '/api/v1/onboarding/setup' && c.method === 'POST');
    assert.equal(setups.length, 1); assert.equal(setups[0].payload.country, '44');
    // the quick start lists the cameras of the login it just added
    assert.equal(calls.filter(c => c.endpoint === '/api/v1/accounts/cameras' && c.payload.email === 'a@example.test').length, 1);
  });
  await test('a missing server key shows its field; sign-in saves the key before the setup request', async () => {
    const f = fixture(); const calls = [];
    const inputs = {
      'server-key-panel': { style: { display: 'none' } }, 'input-server-key': { value: '', focus() {} },
      'server-key-problem': {}, 'btn-save-server-key': {},
      'input-email': { value: 'a@example.test' }, 'input-password': { value: 'synthetic' }, 'input-country': { value: '+44' }, 'btn-login-go': {},
    };
    f.ctx.document.getElementById = id => inputs[id] || null;
    f.ctx.request = async (endpoint, method, payload) => {
      calls.push({ endpoint, method, payload });
      const body = endpoint === '/api/v1/server-key' ? { ok: true, server_key: { configured: true, source: 'key_file', editable: true } } : {};
      return { ok: true, json: async () => body };
    };
    f.run(`apiRequest = request; checkOnboardingStatus = async () => {}; loadDiscoveredCameras = async () => {};`);
    f.run(`renderServerKeyPanel({ configured: false, source: '', editable: true })`);
    assert.equal(inputs['server-key-panel'].style.display, 'block');
    await f.run('handleLoginGo()'); assert.equal(calls.length, 0); // no key typed: nothing sent
    inputs['input-server-key'].value = 'synthetic-key';
    await f.run('handleLoginGo()');
    assert.equal(calls[0].endpoint, '/api/v1/server-key'); assert.equal(calls[0].payload.key, 'synthetic-key');
    assert.equal(calls[1].endpoint, '/api/v1/onboarding/setup');
    assert.equal(JSON.stringify(calls[1].payload).includes('synthetic-key'), false);
    assert.equal(inputs['server-key-panel'].style.display, 'none');
  });
  await test('Settings > Server key never shows the key, says where it comes from, and offers the built-in key back', async () => {
    const f = fixture();
    const els = {
      'modal-server-key-input': { value: '', readOnly: false }, 'modal-server-key-status': {},
      'btn-reset-server-key': { style: { display: 'none' } }, 'btn-submit-server-key': {},
    };
    f.ctx.document.getElementById = id => els[id] || null;
    f.ctx.request = async () => ({ ok: true, json: async () => ({ configured: true, source: 'key_file', editable: true, has_built_in: true, key: 'synthetic-saved-key' }) });
    f.run(`apiRequest = request;`);
    await f.run('openServerKeyModal()');
    assert.equal(els['modal-server-key-input'].value, '', 'the key must not be shown, even if a server sent it');
    assert.equal(els['modal-server-key-input'].readOnly, false);
    assert.equal(els['btn-reset-server-key'].style.display, '');
    assert.match(els['modal-server-key-status'].textContent, /you saved/);
  });
  await test('a missing app ID shows its field too; sign-in saves the key and the app ID before the setup request', async () => {
    const f = fixture(); const calls = [];
    const inputs = {
      'server-key-panel': { style: { display: 'none' } }, 'input-server-key': { value: '', focus() {} },
      'server-key-problem': {}, 'btn-save-server-key': {},
      'app-id-panel': { style: { display: 'none' } }, 'input-app-id': { value: '', focus() {} },
      'app-id-problem': {}, 'btn-save-app-id': {},
      'input-email': { value: 'a@example.test' }, 'input-password': { value: 'synthetic' }, 'input-country': { value: '+44' }, 'btn-login-go': {},
    };
    f.ctx.document.getElementById = id => inputs[id] || null;
    f.ctx.request = async (endpoint, method, payload) => {
      calls.push({ endpoint, method, payload });
      const saved = { configured: true, source: 'key_file', editable: true };
      const body = endpoint === '/api/v1/server-key' ? { ok: true, server_key: saved } : endpoint === '/api/v1/app-id' ? { ok: true, app_id: saved } : {};
      return { ok: true, json: async () => body };
    };
    f.run(`apiRequest = request; checkOnboardingStatus = async () => {}; loadDiscoveredCameras = async () => {};`);
    f.run(`renderServerKeyPanel({ configured: false, source: '', editable: true })`);
    f.run(`renderAppIdPanel({ configured: false, source: '', editable: true })`);
    assert.equal(inputs['app-id-panel'].style.display, 'block');
    inputs['input-server-key'].value = 'synthetic-key';
    await f.run('handleLoginGo()');
    // the key is saved, but without an app ID typed nothing else is sent
    assert.deepEqual(calls.map(c => c.endpoint), ['/api/v1/server-key']);
    inputs['input-app-id'].value = 'synthetic-app-id';
    await f.run('handleLoginGo()');
    const order = calls.map(c => c.endpoint);
    const saveAt = order.indexOf('/api/v1/app-id'), setupAt = order.indexOf('/api/v1/onboarding/setup');
    assert.ok(saveAt > 0 && setupAt > saveAt, `app ID saved before the setup request: ${order}`);
    assert.equal(calls[saveAt].payload.app_id, 'synthetic-app-id');
    assert.equal(JSON.stringify(calls[setupAt].payload).includes('synthetic-app-id'), false);
    assert.equal(inputs['app-id-panel'].style.display, 'none');
    assert.equal(inputs['input-server-key'].value, ''); assert.equal(inputs['input-app-id'].value, '');
  });
  await test('Settings > App ID never shows the app ID, says where it comes from, and offers the built-in one back', async () => {
    const f = fixture();
    const els = {
      'modal-app-id-input': { value: '', readOnly: false }, 'modal-app-id-status': {},
      'btn-reset-app-id': { style: { display: 'none' } }, 'btn-submit-app-id': {},
    };
    f.ctx.document.getElementById = id => els[id] || null;
    const asked = [];
    f.ctx.request = async (endpoint) => { asked.push(endpoint); return { ok: true, json: async () => ({ configured: true, source: 'key_file', editable: true, has_built_in: true, app_id: 'synthetic-saved-app-id' }) }; };
    f.run(`apiRequest = request;`);
    await f.run('openAppIdModal()');
    assert.deepEqual(asked, ['/api/v1/app-id']);
    assert.equal(els['modal-app-id-input'].value, '', 'the app ID must not be shown, even if a server sent it');
    assert.equal(els['btn-reset-app-id'].style.display, '');
    assert.match(els['modal-app-id-status'].textContent, /the app ID you saved/);
  });
  await test('the key and app ID dialogs drop a typed value after saving and before showing again', async () => {
    for (const [id, open, submit, field] of [['server-key', 'openServerKeyModal', 'handleSubmitServerKeyModal', 'key'], ['app-id', 'openAppIdModal', 'handleSubmitAppIdModal', 'app_id']]) {
      const f = fixture(); const sent = [];
      const els = {
        [`modal-${id}-input`]: { value: '', readOnly: false }, [`modal-${id}-status`]: {},
        [`btn-reset-${id}`]: { style: { display: 'none' } }, [`btn-submit-${id}`]: {},
      };
      f.ctx.document.getElementById = name => els[name] || null;
      f.ctx.request = async (endpoint, method, payload) => {
        if (method === 'POST') { sent.push(payload); return { ok: true, json: async () => ({ ok: true }) }; }
        throw new Error('BombeCam is restarting');
      };
      f.run(`apiRequest = request; refreshSessionAndProfile = async () => {};`);
      els[`modal-${id}-input`].value = 'synthetic-typed-value';
      await f.run(`${submit}()`);
      assert.equal(sent[0][field], 'synthetic-typed-value');
      assert.equal(els[`modal-${id}-input`].value, '', `${id}: cleared after saving`);
      els[`modal-${id}-input`].value = 'synthetic-left-behind';
      await f.run(`${open}()`); // the status request fails
      assert.equal(els[`modal-${id}-input`].value, '', `${id}: cleared on open even when the status can't be read`);
      assert.match(els[`modal-${id}-status`].textContent, /Could not read/);
    }
  });
  await test('a sign-in refused for a missing app ID opens its field', async () => {
    const f = fixture();
    const els = { 'app-id-panel': { style: { display: 'none' } } };
    f.ctx.document.getElementById = id => els[id] || null;
    f.ctx.request = async () => ({ ok: false, status: 428, json: async () => ({ error: 'app_id_missing', message: 'BombeCam needs the Osaio app ID before it can sign in.' }) });
    f.run(`apiRequest = request;`);
    const r = await f.run(`signInLogin('a@example.test', 'synthetic', '+44')`);
    assert.equal(r.ok, false); assert.match(r.message, /app ID/);
    assert.equal(els['app-id-panel'].style.display, 'block');
  });
  await test('cached frames and clock events cannot claim Live; counter progress expires', () => {
    const f = fixture();
    f.run(`trackFrames(document.getElementById('preview-player'), viewer.generation); decodedFrameWatchdog();`);
    assert.equal(f.ctx.window.badge, 'buffering');
    assert.equal(f.player.ontimeupdate, null);
    f.player.webkitDecodedFrameCount++;
    f.run('decodedFrameWatchdog()');
    assert.equal(f.ctx.window.badge, 'active');
    f.clock(11501); f.run('decodedFrameWatchdog()');
    assert.equal(f.ctx.window.badge, 'buffering');
    f.player.webkitDecodedFrameCount = 0; f.run('decodedFrameWatchdog()');
    assert.equal(f.run('state.lastDecodedFrameAdvance'), null);
    f.player.webkitDecodedFrameCount = 1; f.run('decodedFrameWatchdog()');
    assert.equal(f.ctx.window.badge, 'active');
    f.player.src = 'different-source'; f.run('decodedFrameWatchdog()');
    assert.equal(f.ctx.window.badge, 'buffering');
  });
  await test('unavailable counters remain unverified; a replacement player cannot reuse proof', () => {
    const f = fixture(); delete f.player.webkitDecodedFrameCount;
    f.run(`trackFrames(document.getElementById('preview-player'), viewer.generation); decodedFrameWatchdog();`);
    assert.equal(f.ctx.window.badge, 'buffering');
    f.player.webkitDecodedFrameCount = 9; f.run('decodedFrameWatchdog()');
    assert.equal(f.ctx.window.badge, 'buffering'); // first metric is only a baseline
    f.player.webkitDecodedFrameCount = 10; f.run('decodedFrameWatchdog()');
    assert.equal(f.ctx.window.badge, 'active');
    f.ctx.document.getElementById = () => ({ ...f.player });
    f.run('decodedFrameWatchdog()'); assert.equal(f.ctx.window.badge, 'buffering');
  });
  await test('frame callbacks require fresh progress and ignore stale generations', () => {
    const f = fixture(true);
    f.run(`trackFrames(document.getElementById('preview-player'), viewer.generation);`);
    f.frame(7); f.run('decodedFrameWatchdog()'); assert.equal(f.ctx.window.badge, 'active');
    f.clock(12000); f.frame(7); f.run('decodedFrameWatchdog()'); assert.equal(f.ctx.window.badge, 'buffering');
    const stale = f.callback(); f.run('teardownVideo()'); stale(0, { presentedFrames: 8 });
    assert.equal(f.run('state.lastDecodedFrameAdvance'), null);
    assert.equal(f.run('state.decodedFrameCount'), 0);
  });
  await test('attachment tracks the established source rather than the cleared previous source', async () => {
    const f = fixture();
    f.run(`startWHEP = async (_, player) => { player.srcObject = {}; player.paused = false; };
      applySoundState = () => {}; ensureSoundPath = () => {};`);
    await f.run(`attachVideo('A', {})`);
    f.player.webkitDecodedFrameCount++; f.run('decodedFrameWatchdog()');
    assert.equal(f.ctx.window.badge, 'active');
  });
  await test('switching an active Talk session stops its original camera and microphone immediately', async () => {
    const f = fixture(); const mic = microphone(); const calls = []; const stop = deferred();
    f.ctx.mic = mic.stream;
    f.ctx.request = endpoint => { calls.push(endpoint); return stop.promise; };
    f.run(`apiRequest = request; state.selectedCameraId = 'A'; viewer.camId = 'A';
      state.isTalkActive = true; state.talkCameraId = 'A'; state.talkStream = mic;
      state.talkAbortController = new AbortController(); window.oldSignal = state.talkAbortController.signal;`);
    const switching = f.run(`selectActiveCamera('B')`);
    assert.equal(f.run('state.selectedCameraId'), 'B'); assert.equal(mic.track.stops, 1);
    assert.equal(f.ctx.window.oldSignal.aborted, true); assert.equal(f.run('state.isTalkActive'), false);
    assert.deepEqual(calls, ['/api/v1/cameras/A/talk/stop']);
    stop.resolve({ ok: true }); await switching;
  });
  await test('a microphone permission result arriving after a switch is closed without starting Talk', async () => {
    const f = fixture(); const mic = microphone(); const permission = deferred(); const calls = [];
    f.ctx.navigator.mediaDevices = { getUserMedia: () => permission.promise };
    f.ctx.request = async endpoint => { calls.push(endpoint); return { ok: true }; };
    f.run(`apiRequest = request; state.selectedCameraId = 'A'; viewer.camId = 'A';`);
    const starting = f.run('startTalkback()'); await f.run(`selectActiveCamera('B')`);
    permission.resolve(mic.stream); await starting;
    assert.equal(mic.track.stops, 1); assert.deepEqual(calls, []);
    assert.equal(f.run('state.talkStarting || state.isTalkActive'), false);
  });
  await test('an in-flight start is followed by stop on A before a new session can start on B', async () => {
    const f = fixture(); const mic = microphone(); const start = deferred(); const calls = [];
    f.ctx.navigator.mediaDevices = { getUserMedia: async () => mic.stream }; audioContext(f);
    f.ctx.request = endpoint => {
      calls.push(endpoint);
      return endpoint === '/api/v1/cameras/A/talk/start' ? start.promise : Promise.resolve({ ok: true, json: async () => ({}) });
    };
    f.run(`apiRequest = request; state.selectedCameraId = 'A'; viewer.camId = 'A';`);
    const starting = f.run('startTalkback()'); await flush();
    const switching = f.run(`selectActiveCamera('B')`); await f.run('startTalkback()');
    assert.equal(mic.track.stops, 1); assert.deepEqual(calls, ['/api/v1/cameras/A/talk/start']);
    start.resolve({ ok: true, json: async () => ({}) }); await starting; await switching;
    assert.deepEqual(calls, ['/api/v1/cameras/A/talk/start', '/api/v1/cameras/A/talk/stop']);
    await f.run('startTalkback()'); assert.equal(f.run('state.isTalkActive'), true);
    assert.equal(calls[2], '/api/v1/cameras/B/talk/start'); await f.run('stopTalkback()');
  });
  await test('a delayed audio-context resume cannot revive a cancelled session', async () => {
    const f = fixture(); const mic = microphone(); const resume = deferred(); const stats = audioContext(f, resume);
    f.ctx.navigator.mediaDevices = { getUserMedia: async () => mic.stream };
    f.run(`apiRequest = async () => ({ok: true, json: async () => ({})}); state.selectedCameraId = 'A'; viewer.camId = 'A';`);
    const starting = f.run('startTalkback()'); await flush(); await f.run(`selectActiveCamera('B')`);
    resume.resolve(); await starting;
    assert.equal(mic.track.stops, 1); assert.equal(stats.closes, 1); assert.equal(stats.connections, 0);
    assert.equal(f.run('state.isTalkActive'), false);
  });
  await test('a late upload error body cannot stop a newer Talk session', async () => {
    const f = fixture(); const body = deferred(); let stops = 0;
    f.ctx.response = { ok: false, status: 503, json: () => body.promise };
    f.ctx.stopped = () => { stops++; };
    f.run(`apiRequest = async () => response; stopTalkback = stopped;
      state.isTalkActive = true; state.talkCameraId = 'A'; state.talkSeq = 1;`);
    const upload = f.run(`deliverTalkbackChunk('A', new Uint8Array([1]), 'audio/l16', 1)`);
    await flush(); f.run(`state.talkCameraId = 'B'; state.talkSeq = 2;`);
    body.resolve({ error: 'old rejection' }); await upload; assert.equal(stops, 0);
  });
  await test('queued chunks discard old sessions and retain current upload order', async () => {
    const f = fixture(); const first = deferred(); const sent = [];
    f.ctx.send = async (cam, bytes) => { sent.push([cam, Array.from(bytes)]); if (sent.length === 1) await first.promise; };
    f.run(`deliverTalkbackChunk = send; state.isTalkActive = true; state.talkSeq = 2; state.talkCameraId = 'B';
      talkQueue.items = [{ camId: 'A', bytes: new Uint8Array([9]), mimeType: 'audio/l16', seq: 1 }];
      enqueueTalkChunk('B', new Uint8Array([1]), 'audio/l16');
      enqueueTalkChunk('B', new Uint8Array([2]), 'audio/l16');
      enqueueTalkChunk('B', new Uint8Array([3]), 'audio/l16');`);
    assert.deepEqual(sent, [['B', [1]]]); first.resolve(); await flush();
    assert.deepEqual(sent, [['B', [1]], ['B', [2, 3]]]);
  });
  // Every action that turns Block cloud video on asks before the router
  // clears the camera's open connections; turning it off does not ask.
  function privacyFixture(answer) {
    const f = fixture(); const asked = []; const sent = [];
    const fields = { 'privacy-router-address': { value: '192.168.8.1' }, 'privacy-router-user': { value: 'root' },
      'privacy-router-password': { value: 'pw', select() {} } };
    f.ctx.document.getElementById = id => fields[id] || null;
    f.ctx.confirm = q => { asked.push(q); return answer; };
    f.ctx.sent = sent;
    f.run(`apiRequest = async (path, method, body) => { sent.push(path); return { ok: true, status: 200, json: async () => ({}) }; };
      renderPrivacy = () => {}; showPrivacyResult = () => {}; closeModal = () => {}; refreshPrivacyStatus = async () => {};
      state.privacy = { cameras: [
        { id: 'c1', name: 'Porch', mac: 'aa:bb:cc:dd:ee:01', blocked: true },
        { id: 'c2', name: 'Yard', mac: '', blocked: true },
        { id: 'c3', name: 'Door', mac: 'aa:bb:cc:dd:ee:03', blocked: false } ], router: {} };`);
    return { f, asked, sent };
  }
  await test('turning a camera switch on asks first; cancel sends nothing', async () => {
    const { f, asked, sent } = privacyFixture(false);
    await f.run(`setCameraBlocked('c3', true)`);
    assert.deepEqual(asked, ["Block cloud video? This will temporarily clear Door's open connections on the router."]);
    assert.deepEqual(sent, []);
  });
  await test('turning a switch off or confirming Block all behaves as chosen', async () => {
    const { f, asked, sent } = privacyFixture(true);
    await f.run(`setCameraBlocked('c1', false)`);
    assert.equal(asked.length, 0);
    await f.run(`setAllBlocked(true)`);
    assert.deepEqual(asked, ["Block cloud video? This will temporarily clear every camera's open connections on the router."]);
    assert.deepEqual(sent, ['/api/v1/privacy/camera', '/api/v1/privacy/block-all']);
  });
  await test('Connect/Update router asks when it will apply switches that are on', async () => {
    const { f, asked, sent } = privacyFixture(false);
    await f.run(`handleRouterConnect()`);
    assert.deepEqual(asked, ["Block cloud video? This will temporarily clear Porch's open connections on the router."]);
    assert.deepEqual(sent, []);
    const ok = privacyFixture(true);
    ok.f.run(`state.privacy.cameras[0].blocked = false`);
    await ok.f.run(`handleRouterConnect()`);
    assert.equal(ok.asked.length, 0, 'nothing to block: no question');
    assert.deepEqual(ok.sent, ['/api/v1/privacy/router/connect']);
  });
})().catch(err => { console.error(err); process.exitCode = 1; });
