import test from 'node:test';
import assert from 'node:assert/strict';
import { installIdentity, stopSDKTasks } from './identity.mjs';

// Requirement 2.5: only a fresh, exact one-use tuple may reach SDK input.
function fixture(account = { apiProvider: 'firstParty', apiKeySource: 'ANTHROPIC_API_KEY', tokenSource: 'none' }, source = 'api_key', timeout = 100, install = true) {
  const input = { done: false, queue: [], push(v) { this.queue.push(v); } };
  const session = { input, query: { async reinitialize() { return { account }; }, async applyFlagSettings() {} }, queryClosed: false, cancelled: false, lastSessionState: 'idle', liveBackgroundTasks: new Map() };
  const agent = {
    sessions: { s: session },
    async prompt(p) { if (this.beforeAppend) await this.beforeAppend(); this.respawnSignedOutSession(p.sessionId, session); session.input.push({ type: 'user' }); return { stopReason: 'end_turn' }; },
    respawnSignedOutSession() {}, async awaitSignOutRespawn() {}, async recreateSignedOutQuery() {},
    async newSession() { return { sessionId: 's' }; }, async setSessionConfigOption() { return {}; },
    async cancel() { session.cancelled = true; }, async closeSession() { session.queryClosed = true; }, async dispose() {},
    async steer() { input.push({ type: 'user' }); }, async createSession() {},
    closeQueryStream(s) { s.queryClosed = true; s.query.close?.(); },
  };
  if (source === 'gateway') {
    agent.authenticate = async params => { agent.authenticated = params; return {}; };
    agent.gateway = { baseUrl: 'https://gw.example.com', snapshot: null };
    agent.sessions = {}; // authentication is admitted only pre-session
  }
  const observe = install ? installIdentity(agent, source, { timeout, gateway: agent.gateway }) : undefined;
  return { agent, session, input, observe, ensureSession: () => { agent.sessions['s'] = session; }, prompt: tuple => agent.prompt({ sessionId: 's', _meta: { aipIdentity: tuple } }) };
}
const checkpoint = 'a'.repeat(64);

test('SDK stop outcomes distinguish rejection and timeout from acknowledgement', async () => {
  const session = { liveBackgroundTasks: new Map([['reject', {}], ['stall', {}], ['early', {}]]), query: { stopTask(id) {
    if (id === 'reject') throw new Error('synthetic failure');
    if (id === 'stall') return new Promise(() => {});
    return Promise.resolve();
  } } };
  assert.deepEqual(await stopSDKTasks(session, 10), [
    { id: 'reject', outcome: 'rejected' }, { id: 'stall', outcome: 'timed_out' }, { id: 'early', outcome: 'acknowledged' },
  ]);
});

test('foreground completion cannot release a live background runtime into a second turn', async () => {
  for (const live of [true, false]) {
    const f = fixture(undefined, 'api_key', 100, false);
    let foreground;
    const finished = new Promise(resolve => { foreground = resolve; });
    const original = f.agent.prompt.bind(f.agent);
    f.agent.prompt = async p => { const result = await original(p); foreground(); return result; };
    const observe = installIdentity(f.agent, 'api_key');
    if (live) f.session.liveBackgroundTasks.set('detached', { isSubagent: false });
    f.session.lastSessionState = live ? 'idle' : 'running';
    const first = await observe({ sessionId: 's', checkpointId: checkpoint });
    let released = false;
    const pending = f.prompt(first).then(() => { released = true; });
    await finished;
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(released, false, 'foreground result released an unsettled runtime');
    f.session.liveBackgroundTasks.clear();
    f.session.lastSessionState = 'idle';
    await pending;
    const second = await observe({ sessionId: 's', checkpointId: 'b'.repeat(64) });
    await f.prompt(second);
    assert.equal(f.input.queue.length, 2);
  }
});

test('background drain timeout or cancellation withdraws reuse rather than returning success', { timeout: 3000 }, async () => {
  for (const mode of ['timeout', 'cancel', 'wedged-stop']) {
    const f = fixture(undefined, 'api_key', 100, false);
    let closed = false;
    const stopped = [];
    f.session.query.close = () => { closed = true; };
    f.session.query.stopTask = async id => { stopped.push(id); if (mode === 'wedged-stop') await new Promise(() => {}); };
    const observe = installIdentity(f.agent, 'api_key', { drainTimeout: 20 });
    f.session.liveBackgroundTasks.set('detached', {});
    const tuple = await observe({ sessionId: 's', checkpointId: checkpoint });
    const pending = f.prompt(tuple);
    if (mode === 'cancel') await f.agent.cancel({ sessionId: 's' });
    await assert.rejects(pending);
    assert.equal(closed, true, 'unsettled SDK query was not terminated');
    assert.deepEqual(stopped, ['detached']);
    await assert.rejects(observe({ sessionId: 's', checkpointId: 'b'.repeat(64) }));
  }
});

test('connection disposal stops SDK tasks before closing their query owner', async () => {
  const f = fixture(undefined, 'api_key', 100, false);
  const calls = [];
  f.session.liveBackgroundTasks.set('detached', {});
  f.session.query.stopTask = async id => { calls.push(id); };
  f.agent.dispose = async () => { calls.push('dispose'); };
  installIdentity(f.agent, 'api_key');
  await f.agent.dispose();
  assert.deepEqual(calls, ['detached', 'dispose']);
});

test('connector bypass mode reaches upstream session creation; deny stays untouched', async () => {
  for (const bypass of [false, true]) {
    const f = fixture(undefined, 'api_key', 100, false);
    f.agent.sessions = {};
    f.agent.createSession = async (_params, options) => options;
    installIdentity(f.agent, 'api_key');
    const options = { resume: 'existing' };
    const result = await f.agent.createSession({ _meta: { claudeCode: { options: { permissionMode: bypass ? 'bypassPermissions' : 'default', allowDangerouslySkipPermissions: bypass } } } }, options);
    assert.deepEqual(result, bypass ? { resume: 'existing', permissionMode: 'bypassPermissions' } : options);
    assert.deepEqual(options, { resume: 'existing' });
  }
});

// 4.1/4.2: only picker rows authorize exact IDs; capability differences cannot
// be hidden by alias collapse. Deleting the selectable/denied/dedup checks
// would admit the forbidden rows below.
test('exact model extraction preserves capabilities and rejects unsafe inventories', async () => {
  const { exactModels } = await import('./models.mjs');
  const row = { value: 'future', resolvedModel: 'claude-nebula-17-3-20261013[1m]', displayName: 'Nebula', description: '', supportsEffort: true, supportedEffortLevels: ['low', 'high'], futureCapability: { enabled: true } };
  const session = { modelInfos: [row, { ...row, value: 'current-only', resolvedModel: 'excluded-1-2' }], models: { currentModelId: 'future', availableModels: [{ modelId: 'future' }] }, settingsManager: { getManagedDeniedModels: () => [] } };
  const result = exactModels(session, () => false);
  assert.equal(result.currentModelId, row.resolvedModel);
  assert.deepEqual(result.rows, [{ ...row, value: row.resolvedModel }]);
  result.rows[0].futureCapability.enabled = false;
  assert.equal(row.futureCapability.enabled, true);
  session.modelInfos.push({ ...row, value: 'other' });
  session.models.availableModels.push({ modelId: 'other' });
  assert.equal(exactModels(session, () => false).rows.length, 1);
  session.modelInfos[2].supportsEffort = false;
  assert.throws(() => exactModels(session, () => false));
  for (const bad of [{ ...row, resolvedModel: undefined }, { ...row, resolvedModel: 'moving' }, { ...row, resolvedModel: '' }, { ...row, displayName: null }, { ...row, supportsEffort: 'true' }, { ...row, supportedEffortLevels: ['high', 'high'] }]) {
    session.modelInfos = [bad]; session.models.availableModels = [{ modelId: 'future' }];
    assert.throws(() => exactModels(session, () => false));
  }
  session.modelInfos = [row];
  assert.throws(() => exactModels(session, id => id === row.resolvedModel));
  session.models.availableModels = [];
  assert.throws(() => exactModels(session, () => false));
  session.modelInfos = [{ ...row, value: 'claude-nebula-17-3', resolvedModel: undefined }];
  session.models = { currentModelId: 'claude-nebula-17-3', availableModels: [{ modelId: 'claude-nebula-17-3' }] };
  assert.equal(exactModels(session, () => false).rows[0].value, 'claude-nebula-17-3');
  session.modelInfos.push({ ...row, value: 'other', resolvedModel: 'claude-nebula-17.3' });
  session.models.availableModels.push({ modelId: 'other' });
  assert.throws(() => exactModels(session, () => false));
});

test('model guards share identity admission and refuse watcher/fuzzy changes before input', async () => {
  const { installModels } = await import('./models.mjs');
  for (const mutation of ['none', 'alias', 'watcher', 'notify', 'advertisement', 'late', 'denied']) {
    const f = fixture(undefined, 'api_key', 100, false);
    const row = { value: 'future', resolvedModel: 'claude-nebula-17-3', displayName: 'Nebula', description: '', supportsEffort: true };
    const s = f.session;
    s.modelInfos = [row]; s.models = { currentModelId: 'future', availableModels: [{ modelId: 'future' }] };
    s.settingsManager = { getManagedDeniedModels: () => [] };
    s.configOptions = [{ id: 'effort', currentValue: 'low' }];
    const selected = [];
    s.query.setModel = async value => { selected.push(value); };
    f.agent.updateConfigOption = async (_id, _config, value) => { s.models.currentModelId = value; };
    // The boundary fake retains upstream option rebuild and raw SDK selection.
    f.agent.setSessionConfigOption = async p => { if (p.configId === 'model') { await s.query.setModel(p.value); s.models.currentModelId = p.value; } return { configOptions: s.configOptions }; };
    const build = (_modes, models) => [{ id: 'model', currentValue: models.currentModelId, options: models.availableModels.map(m => ({ value: m.modelId })) }, { id: 'effort', currentValue: 'low', options: [{ value: 'low' }] }];
    const hooks = installModels(f.agent, build, (id, denied) => denied.includes(id));
    hooks.prepare(s);
    // Install identity with model hooks on a fresh agent, not a second guard.
    const input = { done: false, queue: [], push(v) { this.queue.push(v); } };
    s.input = input;
    const a = { ...f.agent, prompt: async () => { if (a.beforeAppend) await a.beforeAppend(); s.input.push({ type: 'user' }); return {}; }, dispose: async () => {}, newSession: async () => ({ sessionId: 's' }), createSession: async () => {} };
    const observe = installIdentity(a, 'api_key', { models: hooks });
    if (mutation === 'alias') { await assert.rejects(a.setSessionConfigOption({ sessionId: 's', configId: 'model', value: 'future' })); assert.deepEqual(selected, []); continue; }
    await a.setSessionConfigOption({ sessionId: 's', configId: 'model', value: row.resolvedModel });
    assert.deepEqual(selected, [row.resolvedModel]);
    const tuple = await observe({ sessionId: 's', checkpointId: checkpoint });
    if (mutation === 'watcher') await assert.rejects(s.query.setModel('future'));
    if (mutation === 'notify') await assert.rejects(a.updateConfigOption('s', 'model', row.resolvedModel));
    if (mutation === 'advertisement') s.models.availableModels.push({ modelId: 'future' });
    if (mutation === 'denied') s.settingsManager.getManagedDeniedModels = () => [row.resolvedModel];
    if (mutation === 'late') a.beforeAppend = async () => { s.models.currentModelId = 'future'; };
    const pending = a.prompt({ sessionId: 's', _meta: { aipIdentity: tuple } });
    if (mutation === 'none') { await pending; assert.equal(input.queue.length, 1); }
    else { await assert.rejects(pending); assert.equal(input.queue.length, 0); }
  }
});

test('private discovery uses the same session seam, owned tools-off metadata and no prompt', async () => {
  const { installModels } = await import('./models.mjs');
  const f = fixture(undefined, 'api_key', 100, false);
  const session = f.session;
  f.agent.sessions = {};
  let owned;
  f.agent.newSession = async params => {
    owned = params;
    session.modelInfos = [{ value: 'future', resolvedModel: 'invented-family-23-7', displayName: 'Future', description: '', supportsEffort: true, supportedEffortLevels: ['low', 'high'], supportsAdaptiveThinking: true }];
    session.models = { currentModelId: 'future', availableModels: [{ modelId: 'future' }] };
    session.settingsManager = { getManagedDeniedModels: () => [] };
    session.query.setModel = async () => {};
    session.configOptions = [{ id: 'effort', currentValue: 'low' }];
    f.agent.sessions.s = session;
    return { sessionId: 's' };
  };
  const hooks = installModels(f.agent, (_m, models) => [{ id: 'model', currentValue: models.currentModelId, options: models.availableModels.map(m => ({ value: m.modelId })) }], () => false);
  installIdentity(f.agent, 'api_key', { models: hooks });
  await assert.rejects(hooks.discover({ version: 1, cwd: '/workspace', _meta: {} }));
  const result = await hooks.discover({ version: 1, cwd: '/workspace' });
  assert.equal(result.currentModelId, 'invented-family-23-7');
  assert.deepEqual(result.models, [{ nativeId: 'invented-family-23-7', displayName: 'Future', description: '', capabilities: { supportsEffort: true, supportedEffortLevels: ['low', 'high'], supportsAdaptiveThinking: true } }]);
  assert.deepEqual(owned._meta.claudeCode.options.tools, []);
  assert.deepEqual(owned._meta.claudeCode.options.settingSources, []);
  assert.deepEqual(owned._meta.claudeCode.options.managedSettings.allowedProviders, ['anthropic']);
  assert.deepEqual(owned.mcpServers, []);
  assert.equal(f.input.queue.length, 0);
  await f.agent.dispose();
});

// 4.3/4.4: the admitted effort must survive both fresh observations and the
// final append. Snapshotting only model options or guarding only setModel lets
// these independent config/SDK mutation paths change the admitted turn.
test('effort advertisement bookkeeping and SDK mutations withdraw admission with zero input', async t => {
  const { installModels } = await import('./models.mjs');
  for (const late of [false, true]) {
    for (const mutation of ['current', 'options', 'applied', 'pinned', 'override', 'SDK', 'ultracode']) {
      await t.test(`${mutation} late=${late}`, async () => {
        const f = fixture(undefined, 'api_key', 100, false);
        const s = f.session;
        s.modelInfos = [{ value: 'future', resolvedModel: 'unknown-family-17-3', displayName: 'Future', description: '', supportsEffort: true, supportedEffortLevels: ['low', 'high'] }];
        s.models = { currentModelId: 'future', availableModels: [{ modelId: 'future' }] };
        s.settingsManager = { getManagedDeniedModels: () => [] };
        s.configOptions = [{ id: 'effort', currentValue: 'low' }];
        s.appliedEffortLevel = s.effortPinnedLevel = 'low';
        s.query.setModel = async () => {};
        const flags = [];
        s.query.applyFlagSettings = async value => { flags.push(value); };
        const build = (_m, models) => [{ id: 'model', currentValue: models.currentModelId, options: models.availableModels.map(m => ({ value: m.modelId })) }, { id: 'effort', currentValue: 'low', options: [{ value: 'low' }, { value: 'high' }] }];
        const hooks = installModels(f.agent, build, () => false);
        hooks.prepare(s);
        const observe = installIdentity(f.agent, 'api_key', { models: hooks });
        const tuple = await observe({ sessionId: 's', checkpointId: checkpoint });
        let sdkRejected = false;
        const mutate = async () => {
          const effort = s.configOptions.find(o => o.id === 'effort');
          if (mutation === 'current') effort.currentValue = 'high';
          if (mutation === 'options') effort.options.push({ value: 'unadvertised' });
          if (mutation === 'applied') s.appliedEffortLevel = 'high';
          if (mutation === 'pinned') s.effortPinnedLevel = 'high';
          if (mutation === 'override') s.effortSettingsOverride = { effortLevel: 'high' };
          if (mutation === 'SDK' || mutation === 'ultracode') {
            // A swallowed watcher error must still poison admission; an assertion
            // thrown inside prompt would reject for the wrong reason.
            try { await s.query.applyFlagSettings(mutation === 'SDK' ? { effortLevel: 'high' } : { ultracode: true }); } catch { sdkRejected = true; }
          }
        };
        if (late) f.agent.beforeAppend = mutate;
        else await mutate();
        await assert.rejects(f.prompt(tuple), `${mutation}, late=${late}`);
        assert.equal(f.input.queue.length, 0, `${mutation}, late=${late}`);
        if (mutation === 'SDK' || mutation === 'ultracode') {
          assert.equal(sdkRejected, true);
          assert.deepEqual(flags, [], 'unadmitted mutation reached the SDK');
        }
      });
    }
  }
});

test('admitted effort selection reset and model-dependent SDK rebuild preserve the existing owner', async () => {
  const { installModels } = await import('./models.mjs');
  const f = fixture(undefined, 'api_key', 100, false);
  const s = f.session;
  s.modelInfos = [{ value: 'future', resolvedModel: 'unknown-family-17-3', displayName: 'Future', description: '', supportsEffort: true, supportedEffortLevels: ['low', 'high'] }];
  s.models = { currentModelId: 'future', availableModels: [{ modelId: 'future' }] };
  s.settingsManager = { getManagedDeniedModels: () => [] };
  s.configOptions = [{ id: 'effort', currentValue: 'low' }];
  const flags = [];
  s.query.setModel = async () => {};
  s.query.applyFlagSettings = async value => { flags.push(value); };
  const build = (_m, models) => [{ id: 'model', currentValue: models.currentModelId, options: models.availableModels.map(m => ({ value: m.modelId })) }, { id: 'effort', currentValue: 'low', options: [{ value: 'default' }, { value: 'low' }, { value: 'high' }] }];
  f.agent.setSessionConfigOption = async p => {
    if (p.configId === 'model') {
      await s.query.setModel(p.value);
      s.models.currentModelId = p.value;
      s.configOptions = build(null, s.models);
      await s.query.applyFlagSettings({ effortLevel: null }); // upstream drops an old model's pin
      s.appliedEffortLevel = s.effortPinnedLevel = undefined;
    } else {
      await s.query.applyFlagSettings({ effortLevel: p.value === 'default' ? null : p.value });
      s.configOptions.find(o => o.id === 'effort').currentValue = p.value;
      s.appliedEffortLevel = s.effortPinnedLevel = p.value === 'default' ? undefined : p.value;
    }
    return { configOptions: s.configOptions };
  };
  const hooks = installModels(f.agent, build, () => false);
  hooks.prepare(s);
  const observe = installIdentity(f.agent, 'api_key', { models: hooks });
  for (const [i, value] of ['high', 'default', 'low'].entries()) {
    await f.agent.setSessionConfigOption({ sessionId: 's', configId: 'effort', value });
    const tuple = await observe({ sessionId: 's', checkpointId: String(i + 1).repeat(64) });
    await f.prompt(tuple);
  }
  await f.agent.setSessionConfigOption({ sessionId: 's', configId: 'model', value: 'unknown-family-17-3' });
  const tuple = await observe({ sessionId: 's', checkpointId: checkpoint });
  await s.query.applyFlagSettings({ agent: null, fastMode: false }); // not an effort mutation
  await f.prompt(tuple);
  assert.equal(f.input.queue.length, 4);
  assert.deepEqual(flags, [{ effortLevel: 'high' }, { effortLevel: null }, { effortLevel: 'low' }, { effortLevel: null }, { agent: null, fastMode: false }]);
});

test('fresh source projection is private and exact; two sequential turns reuse the query', async () => {
  for (const [source, account] of [
    ['api_key', { apiProvider: 'firstParty', apiKeySource: 'ANTHROPIC_API_KEY', tokenSource: 'none', email: 'private' }],
    ['subscription_token', { apiProvider: 'firstParty', tokenSource: 'CLAUDE_CODE_OAUTH_TOKEN' }],
    ['host_login', { apiProvider: 'firstParty', subscriptionType: 'Claude Pro' }],
    ['host_login', { apiProvider: 'firstParty', subscriptionType: 'Claude Max' }],
    ['host_login', { apiProvider: 'firstParty', subscriptionType: 'Claude Team' }],
    ['host_login', { apiProvider: 'firstParty', subscriptionType: 'Claude Enterprise' }],
  ]) {
    const f = fixture(account, source);
    const first = await f.observe({ sessionId: 's', checkpointId: checkpoint });
    assert.deepEqual(Object.keys(first).sort(), ['version', 'sessionId', 'checkpointId', 'processGeneration', 'queryGeneration', 'observationSequence', 'provider', 'credentialSource'].sort());
    assert.equal(first.credentialSource, source);
    await f.prompt(first);
    const second = await f.observe({ sessionId: 's', checkpointId: 'b'.repeat(64) });
    assert.equal(second.queryGeneration, first.queryGeneration);
    assert.ok(second.observationSequence > first.observationSequence);
    await f.prompt(second);
    assert.equal(f.input.queue.length, 2);
    await assert.rejects(f.prompt(first));
    assert.equal(f.input.queue.length, 2);
  }
});

// Gateway source: the fresh observation must bind the connector-owned
// gateway endpoint on the adapter's stored override, and the CLI account
// shape stays the pinned first-party bypass placeholder. Wrong endpoint,
// cleared or missing override refuses; the connector-sent authenticate is
// admitted exactly once, before any session exists.
test('gateway admission binds an exact override and refuses anything else', async () => {
  const gatewayBaseUrl = 'https://gw.example.com';
  const account = { apiProvider: 'firstParty', tokenSource: 'ANTHROPIC_AUTH_TOKEN' };
  const pass = fixture(account, 'gateway', 100, true);
  await pass.agent.authenticate({ methodId: 'gateway', _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v' } } } });
  pass.agent.gatewayAuthRequest = { _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v' } } } };
  pass.ensureSession();
  const tuple = await pass.observe({ sessionId: 's', checkpointId: checkpoint });
  assert.equal(tuple.credentialSource, 'gateway');
  await pass.prompt(tuple);
  assert.equal(pass.input.queue.length, 1);
  await pass.agent.dispose();
  for (const [name, override] of [
    ['missing', undefined],
    ['wrong-url', { _meta: { gateway: { baseUrl: 'https://other.example.com', headers: { 'X-Route': 'v' } } } }],
    ['cleared', null],
    ['changed-headers', { _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'other' } } } }],
    ['added-headers', { _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v', 'X-Extra': 'junk' } } } }],
    ['headers-array', { _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: ['X-Route'] } } }],
    ['headers-null', { _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: null } } }],
  ]) {
    const f = fixture(account, 'gateway', 100, true);
    await f.agent.authenticate({ methodId: 'gateway', _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v' } } } });
    f.agent.gatewayAuthRequest = override;
    f.ensureSession();
    await assert.rejects(f.observe({ sessionId: 's', checkpointId: checkpoint }), `${name}`);
    assert.equal(f.input.queue.length, 0, `${name}`);
    await f.agent.dispose();
  }
  // Authenticate: exactly one exact-URL connector call, only before any
  // session exists; every later authenticate, logout or mutation refuses.
  const authAgent = {
    sessions: {},
    async authenticate(params) { authAgent.authenticated = params; return {}; },
    async createSession() {}, async newSession() { return { sessionId: 's' }; }, async dispose() {}, async prompt() {},
    async setSessionConfigOption() { return {}; },
  };
  const posture = { baseUrl: gatewayBaseUrl, snapshot: null };
  const observe = installIdentity(authAgent, 'gateway', { timeout: 100, gateway: posture });
  await authAgent.authenticate({ methodId: 'gateway', _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v' } } } });
  assert.deepEqual(authAgent.authenticated._meta.gateway, { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v' } });
  assert.deepEqual(Object.getPrototypeOf(posture.snapshot.headers), null);
  assert.equal(posture.snapshot.baseUrl, gatewayBaseUrl);
  assert.deepEqual({ ...posture.snapshot.headers }, { 'X-Route': 'v' });
  // Wrong methodId and different payloads refuse; the one-time right is
  // burned even after an upstream failure.
  await assert.rejects(authAgent.authenticate({ methodId: 'gateway', _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v' } } } }));
  await assert.rejects(authAgent.authenticate({ methodId: 'host_login' }));
  assert.throws(() => authAgent.logout({}));
  const burn = {
    sessions: {},
    async authenticate() { throw new Error('upstream-down'); },
    async createSession() {}, async newSession() { return { sessionId: 's' }; }, async dispose() {}, async prompt() {},
    async setSessionConfigOption() { return {}; },
  };
  const burnPosture = { baseUrl: gatewayBaseUrl, snapshot: null };
  installIdentity(burn, 'gateway', { timeout: 100, gateway: burnPosture });
  await burn.authenticate({ methodId: 'gateway', _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v' } } } })
    .then(() => { throw new Error('expected upstream failure to surface'); }, () => {});
  await assert.rejects(burn.authenticate({ methodId: 'gateway', _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v2' } } } }));
  // Concurrent attempts: exactly one claim wins, the other refuses.
  const raceAgent = {
    sessions: {},
    async authenticate() { await new Promise(r => setTimeout(r, 20)); return {}; },
    async createSession() {}, async newSession() { return { sessionId: 's' }; }, async dispose() {}, async prompt() {},
    async setSessionConfigOption() { return {}; },
  };
  const racePosture = { baseUrl: gatewayBaseUrl, snapshot: null };
  installIdentity(raceAgent, 'gateway', { timeout: 100, gateway: racePosture });
  const results = await Promise.allSettled([
    raceAgent.authenticate({ methodId: 'gateway', _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v' } } } }),
    raceAgent.authenticate({ methodId: 'gateway', _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'other' } } } }),
  ]);
  assert.equal(results.filter(r => r.status === 'fulfilled').length, 1);
  assert.equal(results.filter(r => r.status === 'rejected').length, 1);
  assert.equal(observe !== undefined, true);
});

test('gateway accept/refuse account matrix mirrors the pinned CLI shapes', async () => {
  const gatewayBaseUrl = 'https://gw.example.com';
  for (const [name, account, ok] of [
    ['bypass-placeholder', { apiProvider: 'firstParty', tokenSource: 'ANTHROPIC_AUTH_TOKEN' }, true],
    ['no-credentials', { apiProvider: 'firstParty' }, false],
    ['with-key', { apiProvider: 'firstParty', tokenSource: 'ANTHROPIC_AUTH_TOKEN', apiKeySource: 'ANTHROPIC_API_KEY' }, false],
    ['wrong-token', { apiProvider: 'firstParty', tokenSource: 'CLAUDE_CODE_OAUTH_TOKEN' }, false],
    ['bedrock', { apiProvider: 'bedrock', tokenSource: 'ANTHROPIC_AUTH_TOKEN' }, false],
  ]) {
    const f = fixture(account, 'gateway', 100, true);
    await f.agent.authenticate({ methodId: 'gateway', _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v' } } } });
    f.agent.gatewayAuthRequest = { _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: { 'X-Route': 'v' } } } };
    f.ensureSession();
    if (ok) {
      const tuple = await f.observe({ sessionId: 's', checkpointId: checkpoint });
      assert.equal(tuple.credentialSource, 'gateway');
    } else {
      await assert.rejects(f.observe({ sessionId: 's', checkpointId: checkpoint }), `${name}`);
    }
    assert.equal(f.input.queue.length, 0, `${name}`);
    await f.agent.dispose();
  }
});

test('unknown, competing and non-first-party sources admit zero messages', async () => {
  for (const [source, account] of [
    ['api_key', { apiProvider: 'bedrock', apiKeySource: 'ANTHROPIC_API_KEY' }],
    ['api_key', { apiProvider: 'firstParty', apiKeySource: 'ANTHROPIC_API_KEY', tokenSource: 'CLAUDE_CODE_OAUTH_TOKEN' }],
    ['subscription_token', { apiProvider: 'firstParty', tokenSource: 'CLAUDE_CODE_OAUTH_TOKEN', apiKeySource: 'ANTHROPIC_API_KEY' }],
    ['host_login', { apiProvider: 'firstParty', subscriptionType: 'Claude API' }],
    ['host_login', { apiProvider: 'firstParty' }],
    ['host_login', { apiProvider: 'firstParty', subscriptionType: 'Custom' }],
  ]) {
    const f = fixture(account, source);
    await assert.rejects(f.observe({ sessionId: 's', checkpointId: checkpoint }));
    await assert.rejects(f.prompt({}));
    assert.equal(f.input.queue.length, 0);
  }
});

test('duplicate, malformed, mutation and cancellation invalidate issued evidence', async () => {
  for (const mutation of [
    f => f.observe({ sessionId: 's', checkpointId: checkpoint }),
    f => f.agent.setSessionConfigOption({ sessionId: 's' }),
    f => f.agent.cancel({ sessionId: 's' }),
    f => { f.session.query = { async reinitialize() { return {}; } }; },
    f => { f.input.done = true; },
    f => { f.session.needsSignOutRespawn = true; },
    f => f.agent.steer({ sessionId: 's' }),
  ]) {
    const f = fixture();
    const tuple = await f.observe({ sessionId: 's', checkpointId: checkpoint });
    await Promise.resolve().then(() => mutation(f)).catch(() => {});
    await assert.rejects(f.prompt(tuple));
    assert.equal(f.input.queue.length, 0);
  }
  const f = fixture();
  await assert.rejects(f.observe({ sessionId: 's', checkpointId: 'short' }));
  assert.equal(f.input.queue.length, 0);
});

test('second observation and actual final append reject replacement, delayed respawn and direct input', async () => {
  for (const mutate of [
    f => { f.session.query = {}; },
    f => { f.session.needsSignOutRespawn = true; },
    f => { f.session.cancelled = true; },
    f => { f.session.input = { push() { throw new Error('unguarded replacement'); } }; },
  ]) {
    const f = fixture();
    const tuple = await f.observe({ sessionId: 's', checkpointId: checkpoint });
    f.session.query.reinitialize = async () => { mutate(f); return { account: { apiProvider: 'firstParty', apiKeySource: 'ANTHROPIC_API_KEY' } }; };
    await assert.rejects(f.prompt(tuple));
    assert.equal(f.input.queue.length, 0);
  }
  const f = fixture();
  await f.observe({ sessionId: 's', checkpointId: checkpoint });
  assert.throws(() => f.input.push({ type: 'user' }));
  assert.equal(f.input.queue.length, 0);
});

test('upstream awaits cannot bypass final append guard; concurrent mutation withdraws consuming ticket', async () => {
  for (const action of ['late-probe', 'cancel', 'options', 'duplicate-prompt', 'steer', 'respawn']) {
    const f = fixture();
    const tuple = await f.observe({ sessionId: 's', checkpointId: checkpoint });
    let release, entered;
    const reached = new Promise(r => { entered = r; });
    f.agent.beforeAppend = () => { entered(); return new Promise(r => { release = r; }); };
    const pending = f.prompt(tuple);
    await reached;
    if (action === 'late-probe') f.session.needsSignOutRespawn = true;
    if (action === 'cancel') await f.agent.cancel({ sessionId: 's' });
    if (action === 'options') await assert.rejects(f.agent.setSessionConfigOption({ sessionId: 's', configId: 'effort', value: 'low' }));
    if (action === 'duplicate-prompt') await assert.rejects(f.prompt(tuple));
    if (action === 'steer') assert.throws(() => f.agent.steer({ sessionId: 's' }));
    if (action === 'respawn') assert.throws(() => f.agent.recreateSignedOutQuery('s', f.session));
    release();
    await assert.rejects(pending);
    assert.equal(f.input.queue.length, 0);
  }
});

test('every malformed ticket member refuses before actual user append', async () => {
  for (const key of ['version', 'sessionId', 'checkpointId', 'processGeneration', 'queryGeneration', 'observationSequence', 'provider', 'credentialSource', 'extra']) {
    const f = fixture();
    const tuple = await f.observe({ sessionId: 's', checkpointId: checkpoint });
    const bad = { ...tuple, [key]: 'wrong' };
    await assert.rejects(f.prompt(bad));
    assert.equal(f.input.queue.length, 0);
  }
});

test('intervening valid options retire old tuple and allow only a new checkpoint', async () => {
  const f = fixture();
  const old = await f.observe({ sessionId: 's', checkpointId: checkpoint });
  await f.agent.setSessionConfigOption({ sessionId: 's', configId: 'effort', value: 'low' });
  const fresh = await f.observe({ sessionId: 's', checkpointId: 'b'.repeat(64) });
  assert.ok(fresh.observationSequence > old.observationSequence);
  await f.prompt(fresh);
  assert.equal(f.input.queue.length, 1);
});

// Prototype-safety: a JSON-parsed header map carrying an own `__proto__` (or
// `constructor`) name must survive the snapshot copy as a plain own property,
// so the snapshot/exact-override comparison stays consistent and no prototype
// mutation is possible.
test('gateway header copy preserves exotic own property names', async () => {
  const gatewayBaseUrl = 'https://gw.example.com';
  const account = { apiProvider: 'firstParty', tokenSource: 'ANTHROPIC_AUTH_TOKEN' };
  const exotic = JSON.parse('{"__proto__":"v-early","constructor":"v-ctor","X-Route":"v"}');
  // JSON.parse materializes __proto__ as an own data property.
  assert.equal(Object.keys(exotic).includes('__proto__'), true);
  const pass = fixture(account, 'gateway', 100, true);
  await pass.agent.authenticate({
    methodId: 'gateway',
    _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: exotic } },
  });
  pass.agent.gatewayAuthRequest = { _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: exotic } } };
  pass.ensureSession();
  const tuple = await pass.observe({ sessionId: 's', checkpointId: checkpoint });
  assert.equal(tuple.credentialSource, 'gateway');
  await pass.prompt(tuple);
  assert.equal(pass.input.queue.length, 1);
  // A replayed authenticate burns the one-shot claim and invalidates
  // everything: no tampering with the stored override is ever tolerated.
  await assert.rejects(pass.agent.authenticate({
    methodId: 'gateway',
    _meta: { gateway: { baseUrl: gatewayBaseUrl, headers: exotic } },
  }));
  await pass.agent.dispose();
});

test('concurrent probing and late timeout/cancel completions cannot issue tickets', async () => {
  for (const action of ['duplicate', 'cancel', 'timeout']) {
    const f = fixture(undefined, 'api_key', 10);
    let resolve;
    f.session.query.reinitialize = () => new Promise(r => { resolve = r; });
    const pending = f.observe({ sessionId: 's', checkpointId: checkpoint });
    if (action === 'duplicate') await assert.rejects(f.observe({ sessionId: 's', checkpointId: 'b'.repeat(64) }));
    if (action === 'cancel') await f.agent.cancel({ sessionId: 's' });
    if (action === 'timeout') await assert.rejects(pending);
    resolve({ account: { apiProvider: 'firstParty', apiKeySource: 'ANTHROPIC_API_KEY' } });
    if (action !== 'timeout') await assert.rejects(pending);
    await assert.rejects(f.prompt({}));
    assert.equal(f.input.queue.length, 0);
  }
});
