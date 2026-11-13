// Calls to kasad's JSON API.
//
// Paths are relative, so the page also works when kasad is published under a
// path of some other server.

// Storing a rule on the plug takes kasad several exchanges with it, each of
// which may wait a while for an answer.
const TIMEOUT_MS = 30000;

// ApiError is an answer from kasad that says the request failed.
export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

// Unreachable means no answer came back from kasad at all.
export class Unreachable extends Error {
  constructor() {
    super('kasad did not answer');
    this.name = 'Unreachable';
  }
}

// The page shows who it is with the cookie that signing in sets. The browser
// keeps that cookie out of reach of scripts and sends it by itself, so there
// is nothing here to store or attach.
async function call(method, path, body) {
  const headers = { Accept: 'application/json' };
  const request = { method, headers, cache: 'no-store', credentials: 'same-origin' };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    request.body = JSON.stringify(body);
  }

  const timeout = new AbortController();
  const timer = setTimeout(() => timeout.abort(), TIMEOUT_MS);
  let response;
  try {
    response = await fetch(path, { ...request, signal: timeout.signal });
  } catch {
    throw new Unreachable();
  } finally {
    clearTimeout(timer);
  }

  let answer = null;
  try {
    answer = await response.json();
  } catch {
    // Dealt with below: an answer that is not JSON is not kasad's.
  }
  if (!response.ok) {
    throw new ApiError(response.status, answer?.error ?? `kasad answered with status ${response.status}`);
  }
  if (answer === null) {
    throw new Unreachable();
  }
  return answer;
}

export const getStatus = () => call('GET', 'api/status');
export const setRelay = (on) => call('POST', 'api/relay', { on });
export const getSchedule = () => call('GET', 'api/schedule');
export const putSchedule = (schedule) => call('PUT', 'api/schedule', schedule);

// The rules the plug stores and runs by itself.
const plugRule = (id) => `api/plug/rules/${encodeURIComponent(id)}`;
export const getPlugRules = () => call('GET', 'api/plug/rules');
export const addPlugRule = (rule) => call('POST', 'api/plug/rules', rule);
export const putPlugRule = (id, rule) => call('PUT', plugRule(id), rule);
export const deletePlugRule = (id) => call('DELETE', plugRule(id));

// Signing in and out. getSession says whether kasad wants a sign-in, whether
// it has a password to sign in with, and whether this browser is signed in.
export const getSession = () => call('GET', 'api/session');
export const signIn = (password) => call('POST', 'api/session', { password });
export const signOut = () => call('DELETE', 'api/session');
