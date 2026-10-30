// Session-local model policy, borrowed by identity's existing admission owner.
// Never consult raw initialization rows or implement upstream fuzzy matching.
import { isDeepStrictEqual } from 'node:util';

const key = Symbol('aipModels');
const refused = () => new Error('Claude exact model admission refused');
const native = id => typeof id === 'string' && id.length <= 256 && /^[a-zA-Z][a-zA-Z0-9_.-]*\d[a-zA-Z0-9_.-]*(?:\[\d+[mk]\])?$/.test(id);
const text = v => typeof v === 'string' && v.length <= 2048 && !/[\x00-\x1f\x7f]/.test(v);
const canonical = id => id.replace(/^([a-zA-Z][a-zA-Z-]*)-(\d{1,2})-(\d{1,2})((?:-\d{8})?(?:-\d+[mk]|\[\d+[mk]\])?)$/, '$1-$2.$3$4');

function validateCapabilities(row) {
  for (const [name, value] of Object.entries(row)) {
    if (name.startsWith('supports') && typeof value !== 'boolean') throw refused();
    if (name === 'supportedEffortLevels' && (!Array.isArray(value) || value.some(v => !text(v) || !v) || new Set(value).size !== value.length)) throw refused();
  }
}

export function exactModels(session, isDeniedModel) {
  const infos = session?.modelInfos;
  const available = session?.models?.availableModels;
  const denied = session?.settingsManager?.getManagedDeniedModels();
  if (!Array.isArray(infos) || !Array.isArray(available) || !Array.isArray(denied) || infos.length > 256 || available.length > 256 || denied.some(v => !text(v))) throw refused();
  const selectable = new Set();
  for (const option of available) {
    if (!text(option?.modelId) || !option.modelId || selectable.has(option.modelId)) throw refused();
    selectable.add(option.modelId);
  }
  const exact = new Map();
  const seen = new Set();
  const canonicals = new Map();
  let currentModelId;
  for (const row of infos) {
    if (!selectable.has(row?.value)) continue; // current-only fallback is not selectable
    if (seen.has(row.value) || !text(row.displayName) || !text(row.description)) throw refused();
    seen.add(row.value);
    const id = row.resolvedModel === undefined ? row.value : row.resolvedModel;
    // Without independent resolution evidence, require a numeric ID segment,
    // not merely a digit embedded in a potentially moving family alias.
    if (row.resolvedModel === undefined && !/-\d+(?:[.-]|$|\[)/.test(id)) throw refused();
    if (!native(id) || isDeniedModel(row.value, denied) || isDeniedModel(id, denied)) throw refused();
    validateCapabilities(row);
    if (canonicals.has(canonical(id)) && canonicals.get(canonical(id)) !== id) throw refused();
    canonicals.set(canonical(id), id);
    const copied = structuredClone({ ...row, value: id, resolvedModel: id });
    if (JSON.stringify(copied).length > 16384) throw refused();
    // Labels may differ between default/family aliases; all capability fields
    // (including future flags) must agree before collapsing a resolution.
    const capabilities = r => Object.fromEntries(Object.entries(r).filter(([k]) => !['value', 'resolvedModel', 'displayName', 'description'].includes(k)));
    if (exact.has(id) && !isDeepStrictEqual(capabilities(exact.get(id)), capabilities(copied))) throw refused();
    if (!exact.has(id)) exact.set(id, copied);
    if (row.value === session.models.currentModelId) currentModelId = id;
  }
  if (seen.size !== selectable.size || !exact.size || !currentModelId || !exact.has(currentModelId)) throw refused();
  return { rows: [...exact.values()], currentModelId };
}

// Prototype-agnostic exact comparison of two gateway header maps: same own key
// set and same string values, mirroring the identity extension's snapshot
// comparison (never prototype-sensitive equalities).
function sameHeaderMap(a, b) {
  if (!a || !b) return false;
  const aKeys = Object.keys(a), bKeys = Object.keys(b);
  if (aKeys.length !== bKeys.length) return false;
  for (const key of aKeys) {
    if (!Object.hasOwn(b, key) || a[key] !== b[key]) return false;
  }
  return true;
}

export function installModels(agent, buildConfigOptions, isDeniedModel, { gateway } = {}) {
  const effortState = session => structuredClone({
    options: session.configOptions?.filter(o => o.id === 'effort'),
    applied: session.appliedEffortLevel, pinned: session.effortPinnedLevel,
    override: session.effortSettingsOverride,
  });
  // Upstream RPC selection uses applyConfigOptionValue directly. This method
  // is the watcher/external-switch notification route, never turn admission.
  if (typeof agent.updateConfigOption === 'function') {
    const update = agent.updateConfigOption.bind(agent);
    agent.updateConfigOption = async (id, configId, ...args) => {
      if (['model', 'effort'].includes(configId)) {
        const st = agent.sessions[id]?.[key];
        if (st) st.invalid = true;
        throw refused();
      }
      return update(id, configId, ...args);
    };
  }
  function check(session) {
    const st = session?.[key];
    const fail = () => { if (st) st.invalid = true; throw refused(); };
    if (!st || st.invalid || session.models.currentModelId !== st.current || !isDeepStrictEqual(session.modelInfos, st.rows) || !isDeepStrictEqual(session.models.availableModels, st.available)) fail();
    const denied = session.settingsManager.getManagedDeniedModels();
    if (!Array.isArray(denied) || denied.some(v => !text(v)) || st.rows.some(row => isDeniedModel(row.value, denied) || st.original.some(r => r.resolvedModel === row.value && isDeniedModel(r.value, denied)))) fail();
    const options = session.configOptions?.filter(o => o.id === 'model');
    const model = options?.[0];
    const values = model?.options?.flatMap(o => Array.isArray(o.options) ? o.options : [o]).map(o => o.value);
    if (options?.length !== 1 || model.currentValue !== st.current || !isDeepStrictEqual(values, st.rows.map(r => r.value))) fail();
    if (!isDeepStrictEqual(effortState(session), st.effort)) fail();
  }
  function prepare(session) {
    if (session[key]) throw refused();
    const { rows, currentModelId } = exactModels(session, isDeniedModel);
    const selectable = new Set(session.models.availableModels.map(m => m.modelId));
    const original = structuredClone(session.modelInfos.filter(r => selectable.has(r.value)).map(r => ({ ...r, resolvedModel: r.resolvedModel ?? r.value })));
    session.modelInfos = rows;
    session.models = { currentModelId, availableModels: rows.map(r => ({ modelId: r.value, name: r.displayName, description: r.description })) };
    const current = rows.find(r => r.value === currentModelId);
    const effort = session.configOptions?.find(o => o.id === 'effort')?.currentValue;
    session.configOptions = buildConfigOptions(session.modes, session.models, rows, effort, session.agents, session.currentAgent, { supported: current.supportsFastMode ?? false, enabled: session.fastModeEnabled, disabledReason: session.fastModeDisabledReason });
    const st = session[key] = { rows: structuredClone(rows), available: structuredClone(session.models.availableModels), original, current: currentModelId, selecting: null, effort: effortState(session), exchange: null, invalid: false };
    const setModel = session.query.setModel.bind(session.query);
    Object.defineProperty(session.query, 'setModel', { configurable: false, writable: false, value: async value => {
      if (st.invalid || st.selecting !== value || !st.rows.some(r => r.value === value)) { st.invalid = true; throw refused(); }
      st.selecting = null; // exactly one upstream SDK call per admitted option
      return setModel(value);
    } });
    const applyFlags = session.query.applyFlagSettings.bind(session.query);
    Object.defineProperty(session.query, 'applyFlagSettings', { configurable: false, writable: false, value: async flags => {
      if (!flags || !['effortLevel', 'ultracode'].some(k => k in flags)) return applyFlags(flags);
      const exchange = st.exchange;
      try {
        if (st.invalid || !exchange || exchange.effortCalls !== 0) throw refused();
        exchange.admitted(); // borrow identity's mutation phase, never a second owner
        const level = flags.effortLevel;
        const expected = exchange.configId === 'effort'
          ? (exchange.value === 'default' ? null : exchange.value)
          : session.configOptions?.find(o => o.id === 'effort')?.currentValue;
        // Model rebuilding can clear an unsupported pin (legacy path), or
        // synchronize the newly displayed value. Explicit effort is exact.
        if (!('effortLevel' in flags) || (level !== expected && !(exchange.configId === 'model' && level === null)) || level === undefined) throw refused();
        exchange.effortCalls++;
        await applyFlags(structuredClone(flags));
        exchange.admitted();
      } catch { st.invalid = true; throw refused(); }
    } });
    check(session);
  }
  function begin(session, params, admitted) {
    check(session);
    const st = session[key];
    if (st.exchange || typeof admitted !== 'function') { st.invalid = true; throw refused(); }
    st.exchange = { configId: params.configId, value: params.value, admitted, effortCalls: 0 };
    if (params.configId === 'model') {
      if (!st.rows.some(r => r.value === params.value)) { st.invalid = true; throw refused(); }
      st.selecting = params.value;
    } else {
      const option = session.configOptions.find(o => o.id === 'effort');
      const values = option?.options?.flatMap(o => Array.isArray(o.options) ? o.options : [o]);
      if (!values?.some(o => o.value === params.value)) { st.invalid = true; throw refused(); }
    }
  }
  function finish(session, params, result) {
    const st = session[key];
    const exchange = st.exchange;
    if (st.invalid || !exchange) throw refused();
    exchange.admitted();
    if (params.configId === 'model') {
      if (st.selecting !== null) { st.invalid = true; throw refused(); }
      st.current = params.value;
    } else {
      const selected = session.configOptions.find(o => o.id === 'effort')?.currentValue;
      const applied = params.value === 'default' ? undefined : params.value;
      if (exchange.effortCalls !== 1 || selected !== params.value || session.appliedEffortLevel !== applied || session.effortPinnedLevel !== applied) { st.invalid = true; throw refused(); }
    }
    st.effort = effortState(session); // pin the refreshed upstream advertisement/bookkeeping
    st.exchange = null;
    check(session);
    if (!isDeepStrictEqual(result?.configOptions, session.configOptions)) { st.invalid = true; throw refused(); }
  }
  function abort(session) {
    if (session?.[key]) { session[key].exchange = null; session[key].invalid = true; }
  }
  // Handler creates no owner: identity wraps newSession; disposal stays upstream.
  // The caller owns process cancellation/deadline and eventual dispose.
  async function discover(params) {
    if (!params || Object.keys(params).length !== 2 || params.version !== 1 || !text(params.cwd) || !params.cwd) throw refused();
    if (gateway !== undefined) {
      const stored = agent.gatewayAuthRequest?._meta?.gateway;
      const headers = stored && typeof stored === 'object' ? stored.headers : null;
      const bound = gateway.snapshot;
      const valid = stored && !!bound && typeof headers === 'object' && headers !== null && !Array.isArray(headers) &&
        stored.baseUrl === bound.baseUrl && sameHeaderMap(headers, bound.headers); // pin exact credential bytes
      if (!valid) throw refused();
    }
    if (gateway === undefined && agent.gatewayAuthRequest) throw refused();
    const managed = { allowManagedPermissionRulesOnly: true, disableAllHooks: true, permissions: { disableBypassPermissionsMode: 'disable' } };
    if (gateway === undefined) managed.allowedProviders = ['anthropic'];
    const result = await agent.newSession({ cwd: params.cwd, mcpServers: [], _meta: { claudeCode: { options: {
      agents: {}, allowDangerouslySkipPermissions: false, disallowedTools: ['Agent', 'Task', 'TaskOutput', 'TaskStop'],
      managedSettings: managed,
      mcpServers: {}, permissionMode: 'default', plugins: [], settingSources: [],
      settings: { apiKeyHelper: '', env: {}, hooks: {}, permissions: { allow: [], deny: [], ask: [] }, plugins: [] },
      skills: [], strictMcpConfig: true, tools: [],
    } } } });
    const session = agent.sessions[result.sessionId];
    check(session);
    const st = session[key];
    return { version: 1, currentModelId: st.current, models: st.rows.map(row => ({
      nativeId: row.value, displayName: row.displayName, description: row.description,
      capabilities: structuredClone(Object.fromEntries(Object.entries(row).filter(([k]) => !['value', 'resolvedModel', 'displayName', 'description'].includes(k)))),
    })) };
  }
  return { prepare, check, begin, finish, abort, discover };
}
