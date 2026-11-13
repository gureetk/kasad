import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  asWeeklyRule,
  cleanRule,
  describeDays,
  describePlugRule,
  describePlugRuleDays,
  describeRule,
  minuteOfDay,
  nextEvent,
  plugRuleBody,
  plugRuleTime,
  presetOf,
  ruleLabel,
  sortDays,
  weekEvents,
  weekPlan,
} from '../static/week.js';

const everyDay = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
const rule = (action, time, days = everyDay, extra = {}) => ({ id: `${action}-${time}`, action, time, days, ...extra });

test('minuteOfDay reads 24-hour times and rejects the rest', () => {
  assert.equal(minuteOfDay('00:00'), 0);
  assert.equal(minuteOfDay('9:05'), 545);
  assert.equal(minuteOfDay('23:59'), 1439);
  for (const bad of ['', '24:00', '12:60', '12', 'noon', undefined]) {
    assert.ok(Number.isNaN(minuteOfDay(bad)), `${bad} should not be a time`);
  }
});

test('sortDays puts days in week order and drops repeats and strangers', () => {
  assert.deepEqual(sortDays(['sun', 'mon', 'funday', 'mon']), ['mon', 'sun']);
});

test('presetOf recognises the three presets whatever the order', () => {
  assert.equal(presetOf(everyDay), 'all');
  assert.equal(presetOf(['fri', 'mon', 'tue', 'wed', 'thu']), 'weekdays');
  assert.equal(presetOf(['sun', 'sat']), 'weekends');
  assert.equal(presetOf(['mon', 'wed']), null);
  assert.equal(presetOf([]), null);
});

test('weekEvents lists events in order through the week', () => {
  const events = weekEvents([rule('off', '22:15', ['tue']), rule('on', '18:30', ['mon', 'tue'])]);
  assert.deepEqual(events, [
    { minute: 1110, action: 'on' },
    { minute: 1440 + 1110, action: 'on' },
    { minute: 1440 + 1335, action: 'off' },
  ]);
});

test('weekEvents leaves out disabled rules and rules it cannot read', () => {
  const rules = [rule('on', '18:30', ['mon'], { disabled: true }), rule('on', 'soon', ['mon'])];
  assert.deepEqual(weekEvents(rules), []);
});

test('when two rules share a moment, the later rule wins', () => {
  const events = weekEvents([rule('on', '07:00', ['mon']), rule('off', '07:00', ['mon'])]);
  assert.deepEqual(events, [{ minute: 420, action: 'off' }]);
});

test('weekPlan draws an evening on every day', () => {
  const plan = weekPlan([rule('on', '18:30'), rule('off', '22:15')]);
  assert.equal(plan.length, 7);
  for (const day of plan) {
    assert.deepEqual(day, { spans: [{ start: 1110, end: 1335 }], marks: [] });
  }
});

test('weekPlan carries a stretch over midnight into the next day', () => {
  const plan = weekPlan([rule('on', '22:00', ['mon']), rule('off', '06:00', ['tue'])]);
  assert.deepEqual(plan[0].spans, [{ start: 1320, end: 1440 }]);
  assert.deepEqual(plan[1].spans, [{ start: 0, end: 360 }]);
  assert.deepEqual(plan[2].spans, []);
});

test('weekPlan carries a stretch from Sunday round to Monday', () => {
  const plan = weekPlan([rule('on', '23:30', ['sun']), rule('off', '01:00', ['mon'])]);
  assert.deepEqual(plan[0].spans, [{ start: 0, end: 60 }]);
  assert.deepEqual(plan[6].spans, [{ start: 1410, end: 1440 }]);
});

test('weekPlan keeps the plug on across days with no off', () => {
  const plan = weekPlan([rule('on', '08:00', ['mon']), rule('off', '20:00', ['wed'])]);
  assert.deepEqual(plan[0].spans, [{ start: 480, end: 1440 }]);
  assert.deepEqual(plan[1].spans, [{ start: 0, end: 1440 }]);
  assert.deepEqual(plan[2].spans, [{ start: 0, end: 1200 }]);
  assert.deepEqual(plan[3].spans, []);
});

test('weekPlan ignores a second on while already on', () => {
  const plan = weekPlan([rule('on', '08:00', ['mon']), rule('on', '09:00', ['mon']), rule('off', '10:00', ['mon'])]);
  assert.deepEqual(plan[0].spans, [{ start: 480, end: 600 }]);
});

test('weekPlan marks the events of a schedule that only switches one way', () => {
  const plan = weekPlan([rule('off', '23:30', ['mon', 'wed'])]);
  assert.deepEqual(plan[0], { spans: [], marks: [{ minute: 1410, action: 'off' }] });
  assert.deepEqual(plan[1], { spans: [], marks: [] });
  assert.deepEqual(plan[2], { spans: [], marks: [{ minute: 1410, action: 'off' }] });
});

test('weekPlan of no rules is seven empty days', () => {
  assert.deepEqual(weekPlan([]), Array.from({ length: 7 }, () => ({ spans: [], marks: [] })));
});

test('describeDays is brief', () => {
  assert.equal(describeDays(everyDay), 'Every day');
  assert.equal(describeDays(['mon', 'tue', 'wed', 'thu', 'fri']), 'Weekdays');
  assert.equal(describeDays(['sat', 'sun']), 'Weekends');
  assert.equal(describeDays(['fri', 'mon', 'wed']), 'Mon, Wed, Fri');
});

test('describeRule says what a rule does', () => {
  assert.equal(describeRule(rule('on', '18:30')), 'Turns the plug on at 18:30 every day.');
  assert.equal(
    describeRule(rule('off', '08:00', ['mon', 'tue', 'wed', 'thu', 'fri'])),
    'Turns the plug off at 08:00 on weekdays.',
  );
  assert.equal(
    describeRule(rule('on', '09:15', ['sat', 'sun'], { disabled: true })),
    'Turns the plug on at 09:15 on Saturdays and Sundays. It is paused, so it will not run.',
  );
  assert.equal(describeRule(rule('on', '07:00', ['mon', 'wed', 'fri'])), 'Turns the plug on at 07:00 on Mondays, Wednesdays and Fridays.');
  assert.equal(describeRule(rule('on', '07:00', ['thu'])), 'Turns the plug on at 07:00 on Thursdays.');
});

test('describeRule asks for what is missing', () => {
  assert.equal(describeRule(rule('on', '07:00', [])), 'Pick at least one day.');
  assert.equal(describeRule(rule('on', '', ['mon'])), 'Pick a time.');
});

test('ruleLabel prefers the name', () => {
  assert.equal(ruleLabel(rule('on', '18:30', everyDay, { name: 'Evening on' })), 'Evening on');
  assert.equal(ruleLabel(rule('off', '22:15')), 'Off at 22:15');
});

test('cleanRule keeps only what the API knows', () => {
  const draft = { id: 'a', name: '  Lamp  ', disabled: false, days: ['sun', 'mon'], time: '07:00', action: 'on', isNew: true };
  assert.deepEqual(cleanRule(draft), { id: 'a', name: 'Lamp', days: ['mon', 'sun'], time: '07:00', action: 'on' });
  assert.deepEqual(cleanRule({ id: 'b', name: '', disabled: true, days: ['sat'], time: '09:15', action: 'off' }), {
    id: 'b',
    disabled: true,
    days: ['sat'],
    time: '09:15',
    action: 'off',
  });
});

// The plug's own rules, as the API gives them.
const plugRule = (extra = {}) => ({
  id: 'A1',
  name: 'morning',
  enabled: true,
  weekly: true,
  repeats: true,
  days: ['mon', 'tue', 'wed', 'thu', 'fri'],
  at: 'time',
  time: '07:30',
  action: 'on',
  ...extra,
});

test('asWeeklyRule puts a plug rule in the form of kasad\'s own', () => {
  assert.deepEqual(asWeeklyRule(plugRule()), {
    id: 'A1',
    name: 'morning',
    disabled: false,
    days: ['mon', 'tue', 'wed', 'thu', 'fri'],
    time: '07:30',
    action: 'on',
  });
  const off = asWeeklyRule(plugRule({ enabled: false, name: undefined, action: 'off', days: ['sun', 'sat'] }));
  assert.deepEqual(off, { id: 'A1', name: '', disabled: true, days: ['sat', 'sun'], time: '07:30', action: 'off' });
});

test('plugRuleBody is what the API takes for a rule on the plug', () => {
  const draft = { id: 'rule-1', name: '  Lamp ', disabled: true, days: ['sun', 'mon'], time: '18:05', action: 'off', where: 'plug' };
  assert.deepEqual(plugRuleBody(draft), { name: 'Lamp', enabled: false, days: ['mon', 'sun'], time: '18:05', action: 'off' });
  assert.deepEqual(plugRuleBody(rule('on', '07:00', ['thu'])), {
    name: '',
    enabled: true,
    days: ['thu'],
    time: '07:00',
    action: 'on',
  });
});

test('a rule goes to the plug and comes back the same', () => {
  const draft = { id: 'x', name: 'Lamp', disabled: false, days: ['mon', 'fri'], time: '18:05', action: 'on' };
  const stored = { id: 'B2', weekly: true, repeats: true, at: 'time', ...plugRuleBody(draft) };
  assert.deepEqual(asWeeklyRule(stored), { ...draft, id: 'B2' });
});

test('plugRuleTime is short', () => {
  assert.equal(plugRuleTime(plugRule()), '07:30');
  assert.equal(plugRuleTime(plugRule({ at: 'sunrise', time: undefined })), 'Sunrise');
  assert.equal(plugRuleTime(plugRule({ at: 'sunset', time: undefined })), 'Sunset');
  assert.equal(plugRuleTime(plugRule({ at: 'unknown', time: undefined })), '?');
});

test('describePlugRuleDays marks a rule that runs once', () => {
  assert.equal(describePlugRuleDays(plugRule()), 'Weekdays');
  assert.equal(describePlugRuleDays(plugRule({ repeats: false, days: ['wed'] })), 'Once (Wed)');
  assert.equal(describePlugRuleDays(plugRule({ days: [] })), 'No days');
});

test('describePlugRule covers the kinds kasad has no equivalent of', () => {
  assert.equal(describePlugRule(plugRule()), 'Turns the plug on at 07:30 on weekdays.');
  assert.equal(
    describePlugRule(plugRule({ at: 'sunset', time: undefined, action: 'off', days: ['sat', 'sun'], enabled: false })),
    'Turns the plug off at sunset on Saturdays and Sundays. It is switched off on the plug, so it will not run.',
  );
  assert.equal(
    describePlugRule(plugRule({ repeats: false, time: '21:05', days: ['wed'], end: { at: 'time', time: '22:00', action: 'off' } })),
    'Turns the plug on at 21:05 once. Then turns it off at 22:00.',
  );
  assert.equal(
    describePlugRule(plugRule({ at: 'unknown', time: undefined, action: 'unknown', days: everyDay, end: { at: 'unknown', action: 'off' } })),
    'Does something this page cannot read every day. Then turns it off.',
  );
});

test('nextEvent finds the next switch, counting from now', () => {
  const rules = [rule('on', '18:30'), rule('off', '22:15')];
  // Wednesday 19:42:30.
  assert.deepEqual(nextEvent(rules, 2, 19 * 3600 + 42 * 60 + 30), {
    action: 'off',
    minuteOfDay: 1335,
    secondsUntil: 2 * 3600 + 32 * 60 + 30,
  });
  // Just after the off, the next is tomorrow's on.
  assert.deepEqual(nextEvent(rules, 2, 22 * 3600 + 15 * 60), {
    action: 'on',
    minuteOfDay: 1110,
    secondsUntil: 20 * 3600 + 15 * 60,
  });
});

test('nextEvent wraps round the end of the week', () => {
  const rules = [rule('on', '09:15', ['mon'])];
  // Sunday 23:59:00: Monday 09:15 is 9 h 16 min away.
  assert.equal(nextEvent(rules, 6, 23 * 3600 + 59 * 60).secondsUntil, 9 * 3600 + 16 * 60);
  // Monday 09:15:00 exactly: the same rule again, a week on.
  assert.equal(nextEvent(rules, 0, 9 * 3600 + 15 * 60).secondsUntil, 7 * 86400);
});

test('nextEvent of rules that never run is null', () => {
  assert.equal(nextEvent([], 0, 0), null);
  assert.equal(nextEvent([rule('on', '09:15', ['mon'], { disabled: true })], 0, 0), null);
});
