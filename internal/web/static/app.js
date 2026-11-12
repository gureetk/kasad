// kasad's web app: shows the plug, switches it, and edits the schedule.
//
// The page keeps one copy of what kasad last said (state), asks again every
// few seconds while it is on screen, and redraws from that copy.

import * as api from './api.js';
import { ApiError } from './api.js';
import {
  describeDay,
  describeOffset,
  describeSignal,
  formatAgo,
  formatMinute,
  formatSpan,
  parseServerTime,
  wallParts,
} from './clock.js';
import {
  DAYS,
  DAY_NAMES,
  PRESETS,
  cleanRule,
  describeDays,
  describeRule,
  minuteOfDay,
  presetOf,
  ruleLabel,
  sortDays,
  weekPlan,
} from './week.js';

const REFRESH_MS = 10000;
const RETRY_MS = 5000;
const MEMORY_KEY = 'kasad.plug';

const byId = (id) => document.getElementById(id);

const state = {
  // screen is what fills the window: nothing yet, the app, or the request
  // for a token.
  screen: 'starting',
  // link is whether kasad itself answers: not asked yet, yes, or no.
  link: 'starting',
  // status is kasad's last answer to GET api/status.
  status: null,
  // at is when that answer arrived, by this device's clock (local) and by
  // kasad's (epoch and wall).
  at: null,
  schedule: { rules: [] },
  // memory is what this browser last saw of the plug, kept between visits.
  memory: readMemory(),
  switching: false,
};

// changes counts the changes made from this page. An answer that was asked
// for before a change and arrives after it is out of date, and is dropped.
let changes = 0;

// ---------------------------------------------------------------------------
// Keeping up to date

let loading = null;
let pollTimer = 0;

// refresh asks kasad for the latest and redraws. It resolves to 'ok',
// 'token' (kasad wants a token it was not given), 'down' or 'stale'.
function refresh(manual = false) {
  if (!loading) {
    byId('refresh').classList.toggle('is-busy', manual);
    loading = load().finally(() => {
      loading = null;
      byId('refresh').classList.remove('is-busy');
      render();
      planRefresh();
    });
  }
  return loading;
}

// refreshAfterChange refreshes with an answer asked for after the change.
async function refreshAfterChange() {
  if (loading) {
    await loading;
  }
  return refresh();
}

async function load() {
  const started = changes;
  try {
    const [status, schedule] = await Promise.all([api.getStatus(), api.getSchedule()]);
    if (started !== changes) {
      return 'stale';
    }
    const time = parseServerTime(status.time);
    const local = Date.now();
    state.status = status;
    state.at = time ? { local, ...time } : { local, epoch: local, wall: local };
    state.schedule = { rules: schedule.rules ?? [] };
    state.link = 'ok';
    state.screen = 'app';
    if (status.plug.reachable) {
      remember(status.plug);
    }
    return 'ok';
  } catch (error) {
    if (unauthorized(error)) {
      return 'token';
    }
    state.link = 'down';
    if (state.screen === 'starting') {
      state.screen = 'app';
    }
    return 'down';
  }
}

function planRefresh() {
  clearTimeout(pollTimer);
  if (document.hidden || state.screen === 'token') {
    return;
  }
  pollTimer = setTimeout(refresh, state.link === 'down' ? RETRY_MS : REFRESH_MS);
}

// unauthorized reports whether an error is kasad asking for a token, and if
// so turns to the token screen.
function unauthorized(error) {
  if (!(error instanceof ApiError) || error.status !== 401) {
    return false;
  }
  if (state.screen !== 'token') {
    const hadToken = api.getToken() !== '';
    showProblem(byId('token-error'), hadToken ? 'The token saved in this browser was not accepted.' : '');
  }
  state.screen = 'token';
  return true;
}

function readMemory() {
  try {
    const memory = JSON.parse(localStorage.getItem(MEMORY_KEY));
    return memory && typeof memory === 'object' ? memory : {};
  } catch {
    return {};
  }
}

function remember(plug) {
  state.memory = { alias: plug.alias, on: plug.on, seenAt: Date.now() };
  try {
    localStorage.setItem(MEMORY_KEY, JSON.stringify(state.memory));
  } catch {
    // Without storage the memory still lasts for this visit.
  }
}

// now is the present moment by kasad's clock, carried forward from its last
// answer, or null before any answer.
function now() {
  if (!state.at) {
    return null;
  }
  const elapsed = Date.now() - state.at.local;
  return { elapsed, epoch: state.at.epoch + elapsed, wall: state.at.wall + elapsed };
}

// livePlug is the plug's state if it is known to be current, or null.
function livePlug() {
  return state.link === 'ok' && state.status.plug.reachable ? state.status.plug : null;
}

function plugName() {
  return livePlug()?.alias || state.memory.alias || '';
}

// ---------------------------------------------------------------------------
// Drawing

function setText(element, text) {
  if (element.textContent !== text) {
    element.textContent = text;
  }
}

function make(tag, className, text) {
  const element = document.createElement(tag);
  if (className) {
    element.className = className;
  }
  if (text !== undefined) {
    element.textContent = text;
  }
  return element;
}

function icon(name) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('class', 'icon');
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  use.setAttribute('href', `#i-${name}`);
  svg.append(use);
  return svg;
}

function currentView() {
  return location.hash === '#schedule' ? 'schedule' : 'plug';
}

function render() {
  byId('token-screen').hidden = state.screen !== 'token';
  byId('app').hidden = state.screen !== 'app';
  if (state.screen !== 'app') {
    if (editor.dialog.open) {
      editor.dialog.close();
    }
    return;
  }
  renderView();
  renderHead();
  renderAlert();
  renderPower();
  renderDetails();
  renderBars();
  renderNext();
  renderRules();
}

function renderView() {
  const view = currentView();
  byId('view-plug').hidden = view !== 'plug';
  byId('view-schedule').hidden = view !== 'schedule';
  for (const link of document.querySelectorAll('.nav__link')) {
    if (link.dataset.view === view) {
      link.setAttribute('aria-current', 'page');
    } else {
      link.removeAttribute('aria-current');
    }
  }
}

function renderHead() {
  const name = plugName();
  setText(byId('plug-name'), name || 'Plug');

  let kind = '';
  let words = 'Connecting…';
  if (state.link === 'down') {
    kind = 'trouble';
    words = "Can't reach kasad · retrying";
  } else if (state.link === 'ok' && !state.status.plug.reachable) {
    kind = 'trouble';
    words = 'Not answering';
    if (state.memory.seenAt) {
      words += ` · last seen ${formatAgo((Date.now() - state.memory.seenAt) / 1000)}`;
    }
  } else if (state.link === 'ok') {
    kind = 'ok';
    words = `Connected · updated ${formatAgo(now().elapsed / 1000)}`;
  }
  const line = byId('plug-status');
  line.classList.toggle('status--ok', kind === 'ok');
  line.classList.toggle('status--trouble', kind === 'trouble');
  setText(byId('plug-status-text'), words);

  const rules = state.schedule.rules;
  const active = rules.filter((rule) => !rule.disabled).length;
  const count =
    rules.length === 0
      ? 'No rules yet'
      : `${active} of ${rules.length} ${rules.length === 1 ? 'rule' : 'rules'} active`;
  setText(byId('schedule-summary'), name ? `${name} · ${count}` : count);

  const title = currentView() === 'schedule' ? 'Schedule · kasad' : name ? `${name} · kasad` : 'kasad';
  if (document.title !== title) {
    document.title = title;
  }
}

function renderAlert() {
  let title = '';
  let body = '';
  if (state.link === 'down') {
    title = "Can't reach kasad";
    body = 'It may have stopped, or this device may be off its network. This page keeps trying.';
  } else if (state.link === 'ok' && !state.status.plug.reachable) {
    title = "Can't reach the plug";
    body = 'It may be unplugged or off the Wi-Fi. kasad keeps trying and will catch up when it answers.';
  }
  setText(byId('alert-title'), title);
  setText(byId('alert-body'), body);
  byId('alert').hidden = title === '';
}

function renderPower() {
  const plug = livePlug();
  const card = byId('power');
  card.classList.toggle('is-on', plug !== null && plug.on);
  card.classList.toggle('is-unknown', plug === null);
  card.classList.toggle('is-busy', state.switching);

  const dial = byId('dial');
  dial.disabled = plug === null || state.switching;
  dial.setAttribute('aria-pressed', String(plug !== null && plug.on));

  let label = 'Switch unavailable while the plug is not answering';
  let name = '';
  let detail = '';
  if (plug !== null && plug.on) {
    label = 'Turn off';
    name = 'On';
    detail = `for ${formatSpan(plug.on_seconds + now().elapsed / 1000)}`;
  } else if (plug !== null) {
    label = 'Turn on';
    name = 'Off';
  } else if (state.link !== 'starting') {
    name = 'No answer';
    if (typeof state.memory.on === 'boolean') {
      detail = `It was ${state.memory.on ? 'on' : 'off'} when last seen`;
    }
  }
  if (dial.getAttribute('aria-label') !== label) {
    dial.setAttribute('aria-label', label);
  }
  // A blank line keeps its height, so the card does not jump.
  setText(byId('power-state'), name || ' ');
  setText(byId('power-detail'), detail || ' ');
}

function renderDetails() {
  const plug = livePlug();
  byId('details').hidden = plug === null;
  if (plug === null) {
    return;
  }
  setText(byId('detail-clock'), describeOffset(plug.clock_offset_seconds));
  const signal = byId('detail-signal');
  setText(signal, describeSignal(plug.rssi));
  signal.title = `${plug.rssi} dBm`;
}

let drawnBars = '';

function renderBars() {
  const moment = now();
  const today = moment ? wallParts(moment.wall) : null;
  const key = JSON.stringify([state.schedule.rules, today?.weekday, today?.minuteOfDay]);
  if (key === drawnBars) {
    return;
  }
  drawnBars = key;

  const plan = weekPlan(state.schedule.rules);
  const todayPlan = today ? plan[today.weekday] : { spans: [], marks: [] };
  const bar = byId('today-bar');
  drawBar(bar, todayPlan, today?.minuteOfDay);
  bar.setAttribute('aria-label', describeDayPlan(todayPlan, today?.minuteOfDay));

  const rows = byId('week-rows');
  rows.setAttribute('role', 'img');
  rows.setAttribute(
    'aria-label',
    'When the schedule keeps the plug on, day by day.' +
      (today ? ` Today is ${DAY_NAMES[DAYS[today.weekday]].long}.` : ''),
  );
  rows.replaceChildren(
    ...DAYS.map((day, index) => {
      const isToday = today?.weekday === index;
      const row = make('div', isToday ? 'week__row is-today' : 'week__row');
      const strip = make('div', 'bar bar--thin');
      drawBar(strip, plan[index], isToday ? today.minuteOfDay : undefined);
      row.append(make('span', 'week__day', DAY_NAMES[day].short), strip);
      return row;
    }),
  );
}

// drawBar fills a bar that stands for one day, midnight to midnight.
function drawBar(bar, day, nowMinute) {
  const percent = (minutes) => `${(minutes / 1440) * 100}%`;
  const pieces = [make('span', 'bar__track')];
  for (const span of day.spans) {
    const piece = make('span', 'bar__on');
    piece.style.left = percent(span.start);
    piece.style.width = percent(span.end - span.start);
    pieces.push(piece);
  }
  for (const mark of day.marks) {
    const piece = make('span', `bar__mark bar__mark--${mark.action === 'off' ? 'off' : 'on'}`);
    piece.style.left = percent(mark.minute);
    pieces.push(piece);
  }
  if (nowMinute !== undefined) {
    const piece = make('span', 'bar__now');
    piece.style.left = percent(nowMinute);
    pieces.push(piece);
  }
  bar.replaceChildren(...pieces);
}

function describeDayPlan(day, nowMinute) {
  let what = 'The plug is not scheduled to be on today';
  if (day.spans.length > 0) {
    what = `On ${day.spans.map((span) => `from ${formatMinute(span.start)} to ${formatMinute(span.end)}`).join(' and ')}`;
  } else if (day.marks.length > 0) {
    what = `Turns ${day.marks.map((mark) => `${mark.action} at ${formatMinute(mark.minute)}`).join(' and ')}`;
  }
  return nowMinute === undefined ? `${what}.` : `${what}. It is now ${formatMinute(nowMinute)}.`;
}

function renderNext() {
  const what = byId('next-what');
  const when = byId('next-when');
  const moment = now();
  const next = state.status?.next;
  const at = next ? parseServerTime(next.at) : null;
  if (!moment || !at) {
    const idle = state.link === 'starting' ? ' ' : state.schedule.rules.length > 0 ? 'Nothing coming up' : 'No rules yet';
    setText(what, idle);
    setText(when, '');
    return;
  }
  const time = formatMinute(wallParts(at.wall).minuteOfDay);
  setText(what, `${next.action === 'off' ? 'Off' : 'On'} at ${time}${describeDay(at.wall, moment.wall)}`);
  const left = (at.epoch - moment.epoch) / 1000;
  setText(when, left > 0 ? `in ${formatSpan(left)}` : 'due now');

  // Once the moment has passed, find out what happened.
  if (left < -2 && moment.elapsed > 3000) {
    refresh();
  }
}

let drawnRules = null;

function renderRules() {
  const rules = state.schedule.rules;
  const key = JSON.stringify(rules);
  if (key === drawnRules) {
    return;
  }
  drawnRules = key;

  for (const list of document.querySelectorAll('[data-rules]')) {
    // Redrawing replaces the buttons, so put the focus back where it was.
    const focused = list.contains(document.activeElement) ? document.activeElement : null;
    const focusedRule = focused?.dataset.rule;
    const focusedPart = focused?.dataset.part;

    const items = rules.map(ruleItem);
    if (items.length === 0) {
      items.push(make('li', 'rules__empty', 'No rules yet. Add one to switch the plug at a set time.'));
    }
    items.push(addItem());
    list.replaceChildren(...items);

    if (focusedPart) {
      const again = [...list.querySelectorAll('[data-part]')].find(
        (element) => element.dataset.part === focusedPart && element.dataset.rule === focusedRule,
      );
      again?.focus();
    }
  }
}

function ruleItem(rule) {
  const on = rule.action !== 'off';
  const item = make('li', rule.disabled ? 'rule is-paused' : 'rule');

  const open = make('button', 'rule__open');
  open.type = 'button';
  open.dataset.part = 'open';
  open.dataset.rule = rule.id;
  const top = make('span', 'rule__top');
  top.append(make('span', 'rule__time', rule.time), make('span', on ? 'pill pill--on' : 'pill', on ? 'On' : 'Off'));
  const about = [rule.name, describeDays(rule.days ?? []), rule.disabled ? 'Paused' : ''].filter(Boolean);
  open.append(top, make('span', 'rule__about', about.join(' · ')));

  const toggle = make('button', 'switch');
  toggle.type = 'button';
  toggle.dataset.part = 'switch';
  toggle.dataset.rule = rule.id;
  toggle.setAttribute('role', 'switch');
  toggle.setAttribute('aria-checked', String(!rule.disabled));
  toggle.setAttribute('aria-label', `${ruleLabel(rule)} is active`);
  const track = make('span', 'switch__track');
  track.setAttribute('aria-hidden', 'true');
  track.append(make('span', 'switch__knob'));
  toggle.append(track);

  item.append(open, toggle);
  return item;
}

function addItem() {
  const item = make('li', 'rules__add');
  const button = make('button');
  button.type = 'button';
  button.dataset.part = 'add';
  button.append(icon('plus'), 'Add a rule');
  item.append(button);
  return item;
}

let toastTimer = 0;

function toast(message) {
  const element = byId('toast');
  element.textContent = message;
  element.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    element.hidden = true;
  }, 5000);
}

function showProblem(element, message) {
  element.textContent = message;
  element.hidden = message === '';
}

// ---------------------------------------------------------------------------
// Changing things

async function switchPlug() {
  const plug = livePlug();
  if (plug === null || state.switching) {
    return;
  }
  state.switching = true;
  changes++;
  render();
  try {
    await api.setRelay(!plug.on);
  } catch (error) {
    if (!unauthorized(error)) {
      toast(
        error instanceof ApiError
          ? 'The plug did not switch. It may not be answering.'
          : "Can't reach kasad, so the plug was not switched.",
      );
    }
  }
  changes++;
  await refreshAfterChange();
  state.switching = false;
  render();
}

// saveRules replaces the schedule. It shows the new rules at once and puts
// the old ones back if kasad does not take them. It resolves to '' on
// success, or to what went wrong.
async function saveRules(rules) {
  const before = state.schedule;
  state.schedule = { rules };
  changes++;
  render();

  let problem = '';
  try {
    const saved = await api.putSchedule({ rules: rules.map(cleanRule) });
    state.schedule = { rules: saved.rules ?? [] };
  } catch (error) {
    state.schedule = before;
    if (unauthorized(error)) {
      problem = 'kasad asked for a token.';
    } else if (error instanceof ApiError) {
      problem = `kasad did not accept the change: ${error.message}`;
    } else {
      problem = "Can't reach kasad, so nothing was saved.";
    }
  }
  changes++;
  render();
  refreshAfterChange();
  return problem;
}

async function toggleRule(id) {
  const rules = state.schedule.rules.map((rule) =>
    rule.id === id ? { ...rule, disabled: !rule.disabled } : rule,
  );
  const problem = await saveRules(rules);
  if (problem && state.screen === 'app') {
    toast(problem);
  }
}

// ---------------------------------------------------------------------------
// The rule editor

const editor = {
  dialog: byId('editor'),
  draft: null,
  isNew: false,
  saving: false,
  confirmingDelete: false,
  confirmTimer: 0,
};

function newRuleId() {
  for (;;) {
    const bytes = crypto.getRandomValues(new Uint8Array(4));
    const id = `rule-${[...bytes].map((byte) => byte.toString(16).padStart(2, '0')).join('')}`;
    if (!state.schedule.rules.some((rule) => rule.id === id)) {
      return id;
    }
  }
}

// suggestTime offers the next quarter of an hour, by kasad's clock.
function suggestTime() {
  const moment = now();
  if (!moment) {
    return '18:00';
  }
  const minute = wallParts(moment.wall).minuteOfDay;
  return formatMinute((Math.floor(minute / 15) + 1) * 15 % 1440);
}

function openEditor(id) {
  const existing = state.schedule.rules.find((rule) => rule.id === id);
  editor.isNew = !existing;
  editor.draft = existing
    ? {
        id: existing.id,
        name: existing.name ?? '',
        disabled: Boolean(existing.disabled),
        days: sortDays(existing.days ?? []),
        time: existing.time,
        action: existing.action === 'off' ? 'off' : 'on',
      }
    : { id: newRuleId(), name: '', disabled: false, days: [...DAYS], time: suggestTime(), action: 'on' };
  editor.saving = false;
  endDeleteConfirmation();
  showProblem(byId('editor-error'), '');
  byId('rule-time').value = editor.draft.time;
  byId('rule-name').value = editor.draft.name;
  renderEditor();
  editor.dialog.showModal();
}

function draftIsComplete() {
  return editor.draft.days.length > 0 && !Number.isNaN(minuteOfDay(editor.draft.time));
}

function renderEditor() {
  const draft = editor.draft;
  setText(byId('editor-title'), editor.isNew ? 'New rule' : 'Edit rule');
  for (const radio of editor.dialog.querySelectorAll('input[name="action"]')) {
    radio.checked = radio.value === draft.action;
  }
  for (const button of editor.dialog.querySelectorAll('[data-day]')) {
    button.setAttribute('aria-pressed', String(draft.days.includes(button.dataset.day)));
  }
  const preset = presetOf(draft.days);
  for (const button of editor.dialog.querySelectorAll('[data-preset]')) {
    button.setAttribute('aria-pressed', String(button.dataset.preset === preset));
  }
  byId('rule-active').setAttribute('aria-checked', String(!draft.disabled));
  setText(byId('rule-summary'), describeRule(draft));
  byId('editor-save').disabled = editor.saving || !draftIsComplete();
  byId('rule-delete-group').hidden = editor.isNew;
  setText(byId('rule-delete'), editor.confirmingDelete ? 'Press again to delete' : 'Delete rule');
}

function endDeleteConfirmation() {
  clearTimeout(editor.confirmTimer);
  editor.confirmingDelete = false;
}

async function saveDraft() {
  if (editor.saving || !draftIsComplete()) {
    return;
  }
  editor.saving = true;
  showProblem(byId('editor-error'), '');
  renderEditor();

  const rule = cleanRule(editor.draft);
  const rules = state.schedule.rules.some((other) => other.id === rule.id)
    ? state.schedule.rules.map((other) => (other.id === rule.id ? rule : other))
    : [...state.schedule.rules, rule];
  const problem = await saveRules(rules);

  editor.saving = false;
  if (problem && state.screen === 'app') {
    showProblem(byId('editor-error'), problem);
    renderEditor();
  } else {
    editor.dialog.close();
  }
}

async function deleteDraft() {
  if (!editor.confirmingDelete) {
    editor.confirmingDelete = true;
    editor.confirmTimer = setTimeout(() => {
      editor.confirmingDelete = false;
      renderEditor();
    }, 4000);
    renderEditor();
    return;
  }
  endDeleteConfirmation();
  const id = editor.draft.id;
  editor.dialog.close();
  const problem = await saveRules(state.schedule.rules.filter((rule) => rule.id !== id));
  if (problem && state.screen === 'app') {
    toast(problem);
  }
}

function wireEditor() {
  const dialog = editor.dialog;

  byId('editor-form').addEventListener('submit', (event) => {
    event.preventDefault();
    saveDraft();
  });
  byId('editor-cancel').addEventListener('click', () => dialog.close());
  byId('rule-delete').addEventListener('click', deleteDraft);
  dialog.addEventListener('close', endDeleteConfirmation);
  // A press on the dimmed surround lands on the dialog itself.
  dialog.addEventListener('click', (event) => {
    if (event.target === dialog) {
      dialog.close();
    }
  });

  dialog.addEventListener('change', (event) => {
    if (event.target.name === 'action') {
      editor.draft.action = event.target.value;
      renderEditor();
    }
  });
  byId('rule-time').addEventListener('input', (event) => {
    editor.draft.time = event.target.value.slice(0, 5);
    renderEditor();
  });
  byId('rule-name').addEventListener('input', (event) => {
    editor.draft.name = event.target.value;
  });
  byId('rule-active').addEventListener('click', () => {
    editor.draft.disabled = !editor.draft.disabled;
    renderEditor();
  });
  for (const button of dialog.querySelectorAll('[data-day]')) {
    button.addEventListener('click', () => {
      const day = button.dataset.day;
      const days = editor.draft.days;
      editor.draft.days = sortDays(days.includes(day) ? days.filter((other) => other !== day) : [...days, day]);
      renderEditor();
    });
  }
  for (const button of dialog.querySelectorAll('[data-preset]')) {
    button.addEventListener('click', () => {
      editor.draft.days = [...PRESETS[button.dataset.preset]];
      renderEditor();
    });
  }
}

// ---------------------------------------------------------------------------
// The token screen

function wireTokenScreen() {
  const input = byId('token-input');
  const reveal = byId('token-reveal');
  const submit = byId('token-submit');

  reveal.addEventListener('click', () => {
    const show = input.type === 'password';
    input.type = show ? 'text' : 'password';
    reveal.setAttribute('aria-pressed', String(show));
    reveal.setAttribute('aria-label', show ? 'Hide token' : 'Show token');
  });

  byId('token-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    const token = input.value.trim();
    if (token === '') {
      showProblem(byId('token-error'), 'Enter the token.');
      return;
    }
    api.setToken(token);
    submit.disabled = true;
    showProblem(byId('token-error'), '');
    if (loading) {
      await loading;
    }
    const outcome = await refresh();
    submit.disabled = false;
    if (outcome === 'token') {
      showProblem(byId('token-error'), 'That token was not accepted.');
    } else if (outcome === 'down') {
      state.screen = 'token';
      render();
      showProblem(byId('token-error'), "Can't reach kasad.");
    } else {
      input.value = '';
    }
  });
}

// ---------------------------------------------------------------------------
// Starting up

function wire() {
  byId('dial').addEventListener('click', switchPlug);
  byId('refresh').addEventListener('click', () => refresh(true));

  document.addEventListener('click', (event) => {
    const button = event.target.closest('button');
    if (!button) {
      return;
    }
    if (button.hasAttribute('data-add') || button.dataset.part === 'add') {
      openEditor(null);
    } else if (button.dataset.part === 'open') {
      openEditor(button.dataset.rule);
    } else if (button.dataset.part === 'switch') {
      toggleRule(button.dataset.rule);
    }
  });

  window.addEventListener('hashchange', () => {
    render();
    window.scrollTo(0, 0);
  });
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) {
      clearTimeout(pollTimer);
    } else if (state.screen !== 'token') {
      refresh();
    }
  });
  window.addEventListener('online', () => {
    if (state.screen !== 'token') {
      refresh();
    }
  });

  // Once a second, move on the things that are worded by the clock.
  setInterval(() => {
    if (state.screen !== 'app' || document.hidden) {
      return;
    }
    renderHead();
    renderPower();
    renderBars();
    renderNext();
  }, 1000);

  wireEditor();
  wireTokenScreen();
}

wire();
refresh();
// If kasad is slow to answer, show the page anyway, saying it is connecting.
setTimeout(() => {
  if (state.screen === 'starting') {
    state.screen = 'app';
    render();
  }
}, 400);
