import { randomBytes } from 'node:crypto';

const stateKey = Symbol('aipIdentity');
const tupleKeys = ['version', 'sessionId', 'checkpointId', 'processGeneration', 'queryGeneration', 'observationSequence', 'provider', 'credentialSource'];
const refused = () => new Error('Claude effective identity admission refused');

// The gateway endpoint is connector-owned non-secret config. It must be an
// absolute http(s) URL: exactly one endpoint binds identity evidence, and any
// mutation later refuses admission.
function validGatewayBaseUrl(value) {
  if (typeof value !== 'string' || value === '') return false;
  try { const parsed = new URL(value); return parsed.protocol === 'http:' || parsed.protocol === 'https:'; } catch { return false; }
}

function classify(account, source) {  if (!account || account.apiProvider !== 'firstParty') throw refused();
  const key = account.apiKeySource;
  const token = account.tokenSource;
  const noKey = key === undefined || key === 'none';
  const noToken = token === undefined || token === 'none';
  if (source === 'api_key' && key === 'ANTHROPIC_API_KEY' && noToken) return;
  if (source === 'subscription_token' && token === 'CLAUDE_CODE_OAUTH_TOKEN' && noKey) return;
  // Pinned CLI stored-subscription branch omits tokenSource. The synthetic
  // real-runtime matrix binds these labels; none proves policy absence.
  if (source === 'host_login' && token === undefined && noKey && ['Claude Pro', 'Claude Max', 'Claude Team', 'Claude Enterprise'].includes(account.subscriptionType)) return;
  // The gateway source never changes the CLI's own first-party account shape:
  // the adapter injects its bypass token placeholder into the prepared root.
  // Routing evidence comes from the fresh gateway override observation, never
  // from this classification alone.
  if (source === 'gateway' && noKey && token === 'ANTHROPIC_AUTH_TOKEN') return;
  throw refused();
}

function sameTuple(a, b) {
  return a && b && Object.keys(a).length === tupleKeys.length && tupleKeys.every(k => a[k] === b[k]);
}

// These are control outcomes, not OS termination proofs. The per-runtime Linux
// supervisor owns detached descendants and verifies their extinction on retire.
export async function stopSDKTasks(session, timeout = 1000) {
  return Promise.all([...(session.liveBackgroundTasks?.keys() ?? [])].map(async id => {
    let timer;
    try {
      const outcome = await Promise.race([
        Promise.resolve().then(() => session.query.stopTask(id)).then(() => 'acknowledged', () => 'rejected'),
        new Promise(resolve => { timer = setTimeout(() => resolve('timed_out'), timeout); }),
      ]);
      return { id, outcome };
    } finally { clearTimeout(timer); }
  }));
}

// A gateway override's headers must be a JSON object of bounded string values.
// Arrays, null and non-string values all refuse. The copy uses a prototype-less
// object so JSON-parsed own `__proto__` (or any other Object.prototype trap
// name) survives as a plain own data property instead of being swallowed by the
// prototype setter during ordinary assignment.
function gatewayHeadersOf(gateway) {
  if (!gateway || typeof gateway !== 'object' || Array.isArray(gateway) || !gateway.headers ||
      typeof gateway.headers !== 'object' || Array.isArray(gateway.headers) || gateway.headers === null) return null;
  const headers = Object.create(null);
  for (const name of Object.keys(gateway.headers)) {
    const value = gateway.headers[name];
    if (typeof value !== 'string') return null;
    headers[name] = value;
  }
  return headers;
}

// Fresh, exact one-use override observation: the adapter's stored gateway
// payload must still be byte-equals this connector's one successful
// authentication (endpoint and every header name/value). Login state,
// provider switching, logout and any later credential change cannot
// resurrect evidence, because the stored object must Earth exactly the
// captured snapshot with no additions, deletions or mutations.
function gatewayOverride(agent, snapshot) {
  if (!snapshot) return null;
  const stored = agent?.gatewayAuthRequest?._meta?.gateway;
  const headers = gatewayHeadersOf(stored);
  if (!headers || stored.baseUrl !== snapshot.baseUrl || !sameHeaders(headers, snapshot.headers)) return null;
  return stored;
}

// Prototype-agnostic exact comparison of two header maps: same own key set and
// same string values. Deliberately not isDeepStrictEqual, which is
// prototype-sensitive and would misfire between the null-prototype snapshots
// and any plain-object replay of the stored map.
function sameHeaders(a, b) {
  if (!a || !b) return false;
  const aKeys = Object.keys(a), bKeys = Object.keys(b);
  if (aKeys.length !== bKeys.length) return false;
  for (const key of aKeys) {
    if (!Object.hasOwn(b, key) || a[key] !== b[key]) return false;
  }
  return true;
}

// State stays on the existing session. The upstream agent remains the only
// query/disposal owner. No notification or independent CLI read feeds admission.
export function installIdentity(agent, source, { timeout = 5000, drainTimeout = 5000, models, gateway } = {}) {
  const sources = ['api_key', 'subscription_token', 'host_login', 'gateway'];
  const gatewayBaseUrl = gateway?.baseUrl;
  if (source === 'gateway' ? !validGatewayBaseUrl(gatewayBaseUrl) : gateway !== undefined) throw refused();
  if (!sources.includes(source) || !Number.isSafeInteger(timeout) || timeout <= 0 || !Number.isSafeInteger(drainTimeout) || drainTimeout <= 0) throw refused();
  const processGeneration = randomBytes(32).toString('hex');
  // The captured one-shot connector-owned gateway payload; null unless the
  // gateway source has consumed its single authentic admission.
  let gatewaySnapshot = null;
  let generation = 0;
  let sequence = 0;
  let disposed = false;
  let creating = false;
  const invalidate = s => { if (s?.[stateKey]) s[stateKey].phase = 'invalid'; };
  const invalidateAll = () => { for (const s of Object.values(agent.sessions)) invalidate(s); };
  function state(sessionId) {
    const session = agent.sessions[sessionId];
    if (disposed || !session?.query || !session.input || session.queryClosed || session.input.done || session.cancelled || session.needsSignOutRespawn || session.signOutRespawn || agent.providerUpdate) {
      invalidate(session);
      throw refused();
    }
    if (!session[stateKey]) {
      const st = session[stateKey] = { phase: 'empty', query: session.query, input: session.input, generation: ++generation, ticket: null, active: false };
      // Replacement must fail before a new unguarded input can be published.
      for (const key of ['query', 'input']) {
        const original = session[key];
        Object.defineProperty(session, key, {
          enumerable: true, configurable: false, get: () => original,
          set: value => { if (value !== original) { invalidate(session); throw refused(); } },
        });
      }
      const push = st.input.push;
      if (typeof push !== 'function') { invalidate(session); throw refused(); }
      Object.defineProperty(st.input, 'push', { configurable: false, writable: false, value(message) {
        check(sessionId, session, st, 'consuming');
        if (!st.active || !st.ticket || message?.type !== 'user') { invalidate(session); throw refused(); }
        // Consume synchronously before the irreversible existing append.
        st.phase = 'consumed';
        return push.call(this, message);
      } });
    }
    return [session, session[stateKey]];
  }
  function check(id, session, st, phase, checkModels = true) {
    try { if (checkModels) models?.check(session); } catch { invalidate(session); throw refused(); }
    if (disposed || agent.sessions[id] !== session || session[stateKey] !== st || st.phase !== phase || session.query !== st.query || session.input !== st.input || session.queryClosed || st.input.done || session.cancelled || session.needsSignOutRespawn || session.signOutRespawn || agent.providerUpdate) {
      invalidate(session);
      throw refused();
    }
  }
  async function fresh(id, session, st, phase) {
    check(id, session, st, phase);
    let timer;
    try {
      const result = await Promise.race([
        st.query.reinitialize(),
        new Promise((_, reject) => { timer = setTimeout(() => { invalidate(session); reject(refused()); }, timeout); }),
      ]);
      check(id, session, st, phase);
      // Requirement 3.1: fresh evidence is the adapter's stored override
      // matching the captured payload exactly — endpoint AND headers —
      // plus the pinned first-party account shape.
      if (source === 'gateway' && gatewayOverride(agent, gatewaySnapshot) === null) { invalidate(session); throw refused(); }
      classify(result?.account, source);
      if (!Number.isSafeInteger(++sequence)) throw refused();
    } catch {
      invalidate(session);
      throw refused();
    } finally { clearTimeout(timer); }
  }
  async function stopTasks(session) {
    const outcomes = await stopSDKTasks(session);
    if (outcomes.some(result => result.outcome !== 'acknowledged')) agent.logger?.error('Claude SDK task stop failed; OS supervisor cleanup required');
  }
  const prompt = agent.prompt.bind(agent);
  agent.prompt = async params => {
    const [session, st] = state(params.sessionId);
    let draining = false;
    try {
      check(params.sessionId, session, st, 'issued');
      if (!sameTuple(params._meta?.aipIdentity, st.ticket)) throw refused();
      st.phase = 'consuming';
      await fresh(params.sessionId, session, st, 'consuming');
      st.active = true;
      const result = await prompt(params);
      if (st.phase !== 'consumed') throw refused();
      draining = true;
      // The pinned adapter settles foreground results before background Bash
      // ends. Keep the ACP prompt (and therefore the Go pool claim) open until
      // the existing SDK consumer reports idle AND its live-task registry drains.
      // No second SDK reader, task owner, or synthetic conversation is created.
      const deadline = Date.now() + drainTimeout;
      while (true) {
        check(params.sessionId, session, st, 'consumed');
        if (!(session.liveBackgroundTasks instanceof Map)) throw refused();
        const live = [...session.liveBackgroundTasks.values()].some(task => !task.endedPerLevel);
        if (session.lastSessionState === 'idle' && !live) break;
        if (Date.now() >= deadline) throw refused();
        await new Promise(resolve => setTimeout(resolve, 10));
      }
      return result;
    } catch {
      invalidate(session);
      // Try the existing SDK owner first. Rejected, timed-out and incomplete
      // stops are covered by the per-runtime supervisor's adopted-child reaper,
      // not by assuming that an acknowledgement proves OS process termination.
      await stopTasks(session);
      // Withdraw the SDK query before an error terminal can release the claim.
      // The existing Go admission-failure Close then kills/reaps the entire
      // runtime process group and forces honest transcript replay on next use.
      agent.closeQueryStream(session);
      if (draining) throw new Error('Claude background drain failed; runtime retired');
      throw refused();
    } finally { st.active = false; }
  };
  const options = agent.setSessionConfigOption.bind(agent);
  agent.setSessionConfigOption = async params => {
    const [session, st] = state(params.sessionId);
    if (st.active || !['empty', 'issued', 'consumed'].includes(st.phase) || !['model', 'effort'].includes(params.configId)) {
      invalidate(session); throw refused();
    }
    st.phase = 'mutating';
    st.ticket = null;
    try {
      models?.begin(session, params, () => check(params.sessionId, session, st, 'mutating', false));
      const result = await options(params);
      models?.finish(session, params, result);
      check(params.sessionId, session, st, 'mutating');
      st.phase = 'empty';
      return result;
    } catch { models?.abort(session); invalidate(session); throw refused(); }
  };
  for (const name of ['cancel', 'cancelTurns', 'closeSession']) {
    if (typeof agent[name] !== 'function') continue;
    const original = agent[name].bind(agent);
    agent[name] = (...args) => { invalidate(agent.sessions[args[0]?.sessionId]); return original(...args); };
  }
  const dispose = agent.dispose.bind(agent);
  agent.dispose = async (...args) => {
    disposed = true; invalidateAll();
    await Promise.all(Object.values(agent.sessions).map(stopTasks));
    return dispose(...args);
  };
  // Only the connector-sent authentication is admitted, exactly once and only
  // before any session exists, carrying the operator-owned endpoint. The
  // allowance is claimed synchronously before any await, so concurrent
  // attempts cannot both pass the guard, and a failed first attempt burns
  // the one-time right: the connector refuses after failure, never retries.
  const rawAuthenticate = typeof agent.authenticate === 'function' ? agent.authenticate.bind(agent) : null;
  // These real dynamically-dispatched methods include the upstream internal
  // signed-out respawn, not only its externally registered RPC wrapper.
  for (const name of ['steer', 'loadSession', 'resumeSession', 'unstable_forkSession', 'authenticate', 'logout', 'unstable_setProvider', 'unstable_disableProvider', 'setSessionMode', 'awaitSignOutRespawn', 'recreateSignedOutQuery', 'goal', 'stopAsyncTask']) {
    agent[name] = () => { invalidateAll(); throw refused(); };
  }
  if (source === 'gateway' && rawAuthenticate) {
    // Immutable snapshot of the one successful connector-owned payload. It
    // binds every later observation to exactly these endpoint bytes and
    // header names/values: additions, deletions or mutations of the stored
    // override refuse admission. The caller's params stay untouched.
    agent.authenticate = async params => {
      const payload = params?._meta?.gateway;
      const headers = gatewayHeadersOf(payload);
      // Exactly one connector-sent authentication per process: the claim is
      // set synchronously here, before the first await, so concurrent
      // attempts cannot both pass, and an upstream failure still burns the
      // one-time right because the connector never retries the handshake.
      if (gatewaySnapshot || params?.methodId !== 'gateway' || !headers || payload.baseUrl !== gatewayBaseUrl || Object.keys(agent.sessions).length || disposed) {
        invalidateAll();
        throw refused();
      }
      gatewaySnapshot = { baseUrl: payload.baseUrl, headers };
      Object.freeze(gatewaySnapshot.headers);
      Object.freeze(gatewaySnapshot);
      if (gateway) (gateway.snapshot = gatewaySnapshot); // discovery reads the same bound
      try { return await rawAuthenticate(params); } finally { invalidateAll(); }
    };
  }
  agent.respawnSignedOutSession = (id, session) => {
    if (session.needsSignOutRespawn || session.signOutRespawn) { invalidate(session); throw refused(); }
    return undefined;
  };
  const create = agent.createSession.bind(agent);
  agent.createSession = (...args) => {
    if (Object.keys(agent.sessions).length) { invalidateAll(); throw refused(); }
    // The pinned adapter filters SDK options.permissionMode. Its session
    // creation option is the authority; upstream still clamps administrator-
    // disabled or otherwise unavailable bypass to default mode.
    const options = args[0]?._meta?.claudeCode?.options;
    if (options?.permissionMode === 'bypassPermissions' && options.allowDangerouslySkipPermissions === true) {
      args[1] = { ...args[1], permissionMode: 'bypassPermissions' };
    }
    return create(...args);
  };
  const newSession = agent.newSession.bind(agent);
  agent.newSession = async params => {
    if (disposed || creating || Object.keys(agent.sessions).length) { disposed = true; invalidateAll(); throw refused(); }
    creating = true;
    try {
      const result = await newSession(params);
      models?.prepare(agent.sessions[result.sessionId]);
      state(result.sessionId); // install the input guard before publishing success
      const session = agent.sessions[result.sessionId];
      return models ? { ...result, models: session.models, configOptions: session.configOptions } : result;
    } finally { creating = false; }
  };
  return async params => {
    const [session, st] = state(params?.sessionId);
    try {
      if (Object.keys(params).length !== 2 || typeof params.checkpointId !== 'string' || !/^[0-9a-f]{64}$/.test(params.checkpointId) || st.ticket?.checkpointId === params.checkpointId || st.active || !['empty', 'consumed'].includes(st.phase)) throw refused();
      st.phase = 'probing';
      st.ticket = null;
      await fresh(params.sessionId, session, st, 'probing');
      st.ticket = Object.freeze({ version: 1, sessionId: params.sessionId, checkpointId: params.checkpointId, processGeneration, queryGeneration: st.generation, observationSequence: sequence, provider: 'firstParty', credentialSource: source });
      st.phase = 'issued';
      return st.ticket;
    } catch { invalidate(session); throw refused(); }
  };
}
