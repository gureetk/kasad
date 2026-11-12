import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  describeDay,
  describeOffset,
  describeSignal,
  formatAgo,
  formatMinute,
  formatSpan,
  parseServerTime,
  wallParts,
} from '../static/clock.js';

test('parseServerTime keeps both the instant and the wall clock', () => {
  const time = parseServerTime('2030-01-02T19:42:05.25-05:00');
  assert.equal(time.epoch, Date.UTC(2030, 0, 3, 0, 42, 5, 250));
  assert.equal(time.wall, Date.UTC(2030, 0, 2, 19, 42, 5));
});

test('parseServerTime reads UTC and whole seconds', () => {
  const time = parseServerTime('2030-06-30T08:00:00Z');
  assert.equal(time.epoch, Date.UTC(2030, 5, 30, 8));
  assert.equal(time.wall, time.epoch);
});

test('parseServerTime rejects what is not a time', () => {
  for (const bad of ['', 'yesterday', '2030-01-02', '2030-01-02T19:42:05', undefined, null]) {
    assert.equal(parseServerTime(bad), null, `${bad} should not parse`);
  }
});

test('wallParts counts the week from Monday', () => {
  // 2 January 2030 is a Wednesday.
  const parts = wallParts(parseServerTime('2030-01-02T19:42:05+09:00').wall);
  assert.equal(parts.weekday, 2);
  assert.equal(parts.minuteOfDay, 19 * 60 + 42);
  assert.equal(wallParts(parseServerTime('2030-01-06T00:00:00Z').wall).weekday, 6);
  assert.equal(wallParts(parseServerTime('2030-01-07T00:00:00Z').wall).weekday, 0);
});

test('formatMinute writes HH:MM', () => {
  assert.equal(formatMinute(0), '00:00');
  assert.equal(formatMinute(555), '09:15');
  assert.equal(formatMinute(1440), '24:00');
});

test('formatSpan uses at most two units', () => {
  assert.equal(formatSpan(0), 'less than a minute');
  assert.equal(formatSpan(59), 'less than a minute');
  assert.equal(formatSpan(60), '1 min');
  assert.equal(formatSpan(72 * 60 + 30), '1 h 12 min');
  assert.equal(formatSpan(2 * 3600), '2 h');
  assert.equal(formatSpan(52 * 3600 + 120), '2 d 4 h');
  assert.equal(formatSpan(48 * 3600), '2 d');
  assert.equal(formatSpan(-5), 'less than a minute');
});

test('formatAgo settles on "just now" for the first seconds', () => {
  assert.equal(formatAgo(0), 'just now');
  assert.equal(formatAgo(14.9), 'just now');
  assert.equal(formatAgo(40), '40 s ago');
  assert.equal(formatAgo(185), '3 min ago');
  assert.equal(formatAgo(2 * 3600 + 5), '2 h ago');
  assert.equal(formatAgo(3 * 86400), '3 d ago');
});

test('describeOffset says which way the plug\'s clock is out', () => {
  assert.equal(describeOffset(0), 'In step');
  assert.equal(describeOffset(-2), 'In step');
  assert.equal(describeOffset(30), '30 s ahead');
  assert.equal(describeOffset(-150), '2 min behind');
  assert.equal(describeOffset(-3 * 3600 - 60), '3 h 1 min behind');
});

test('describeSignal grades the signal', () => {
  assert.equal(describeSignal(-40), 'Strong');
  assert.equal(describeSignal(-60), 'Good');
  assert.equal(describeSignal(-70), 'Fair');
  assert.equal(describeSignal(-85), 'Weak');
});

test('describeDay names a later day', () => {
  const from = parseServerTime('2030-01-02T19:42:00Z').wall; // a Wednesday
  const on = (text) => describeDay(parseServerTime(text).wall, from);
  assert.equal(on('2030-01-02T22:15:00Z'), '');
  assert.equal(on('2030-01-03T00:05:00Z'), ' tomorrow');
  assert.equal(on('2030-01-05T09:15:00Z'), ' on Sat');
  assert.equal(on('2030-01-09T09:15:00Z'), ' next Wed');
});
