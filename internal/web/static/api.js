// Calls to kasad's JSON API.
//
// Paths are relative, so the page also works when kasad is published under a
// path of some other server.

const TOKEN_KEY = 'kasad.token';
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

// The token is kept in this browser so that it need only be typed once. If
// the browser offers no storage, it lasts until the page is closed.
let fallbackToken = '';

export function getToken() {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? '';
  } catch {
    return fallbackToken;
  }
}

export function setToken(token) {
  fallbackToken = token;
  try {
    if (token) {
      localStorage.setItem(TOKEN_KEY, token);
    } else {
      localStorage.removeItem(TOKEN_KEY);
    }
  } catch {
    // Without storage the token above still serves this visit.
  }
}

// A header can only carry bytes, so send the token as UTF-8, one character
// per byte. That is also what a command-line client would put on the wire.
function headerBytes(text) {
  return String.fromCharCode(...new TextEncoder().encode(text));
}

async function call(method, path, body) {
  const headers = { Accept: 'application/json' };
  const token = getToken();
  if (token) {
    headers.Authorization = `Bearer ${headerBytes(token)}`;
  }
  const request = { method, headers, cache: 'no-store', credentials: 'omit' };
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
