import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  cleanRule,
  describeDays,
  describeRule,
  minuteOfDay,
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
