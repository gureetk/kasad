// What a schedule means over a week, and how to say it.
//
// This mirrors what kasad does with the rules closely enough to draw them. It
// leaves out clock changes, which move things by an hour twice a year.

const MINUTES_PER_DAY = 24 * 60;
const MINUTES_PER_WEEK = 7 * MINUTES_PER_DAY;

// DAYS are the days as the API names them, Monday first.
export const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];

export const DAY_NAMES = {
  mon: { short: 'Mon', long: 'Monday' },
  tue: { short: 'Tue', long: 'Tuesday' },
  wed: { short: 'Wed', long: 'Wednesday' },
  thu: { short: 'Thu', long: 'Thursday' },
  fri: { short: 'Fri', long: 'Friday' },
  sat: { short: 'Sat', long: 'Saturday' },
  sun: { short: 'Sun', long: 'Sunday' },
};

// PRESETS are the sets of days the editor offers in one press.
export const PRESETS = {
  all: DAYS,
  weekdays: ['mon', 'tue', 'wed', 'thu', 'fri'],
  weekends: ['sat', 'sun'],
};

// minuteOfDay turns "HH:MM" into minutes after midnight, or NaN.
export function minuteOfDay(time) {
  const match = /^(\d{1,2}):(\d{2})$/.exec(time ?? '');
  if (!match) {
    return NaN;
  }
  const hours = Number(match[1]);
  const minutes = Number(match[2]);
  return hours < 24 && minutes < 60 ? hours * 60 + minutes : NaN;
}

// sortDays puts day names in week order and drops repeats and strangers.
export function sortDays(days) {
  return DAYS.filter((day) => days.includes(day));
}

// presetOf names the preset that is exactly these days, or returns null.
export function presetOf(days) {
  const sorted = sortDays(days).join();
  for (const [name, preset] of Object.entries(PRESETS)) {
    if (preset.join() === sorted) {
      return name;
    }
  }
  return null;
}

// weekEvents lists the moments in a week at which the schedule switches the
// plug, as minutes after Monday 00:00, in order. Rules that are disabled take
// no part. Where two rules share a moment, the later rule wins, as in kasad.
export function weekEvents(rules) {
  const byMinute = new Map();
  for (const rule of rules) {
    const minute = minuteOfDay(rule.time);
    if (rule.disabled || Number.isNaN(minute)) {
      continue;
    }
    for (const day of sortDays(rule.days ?? [])) {
      byMinute.set(DAYS.indexOf(day) * MINUTES_PER_DAY + minute, rule.action);
    }
  }
  return [...byMinute]
    .map(([minute, action]) => ({ minute, action }))
    .sort((a, b) => a.minute - b.minute);
}

// weekPlan works out what to draw for each day, Monday first. Each day has
// "spans", the stretches the schedule keeps the plug on, as {start, end} in
// minutes after midnight, and "marks", single moments as {minute, action}.
//
// A schedule that only ever switches one way has no stretches to show: it
// says when the plug goes on, say, but never when it goes off. Its events
// are given as marks instead.
export function weekPlan(rules) {
  const days = DAYS.map(() => ({ spans: [], marks: [] }));
  const events = weekEvents(rules);
  if (events.length === 0) {
    return days;
  }

  if (events.every((event) => event.action === events[0].action)) {
    for (const event of events) {
      days[Math.floor(event.minute / MINUTES_PER_DAY)].marks.push({
        minute: event.minute % MINUTES_PER_DAY,
        action: event.action,
      });
    }
    return days;
  }

  // The week repeats, so it begins in the state its last event leaves.
  let on = events[events.length - 1].action === 'on';
  let since = 0;
  const stretches = [];
  for (const event of events) {
    const next = event.action === 'on';
    if (next === on) {
      continue;
    }
    if (on) {
      stretches.push([since, event.minute]);
    }
    on = next;
    since = event.minute;
  }
  if (on) {
    stretches.push([since, MINUTES_PER_WEEK]);
  }

  for (const [start, end] of stretches) {
    for (let day = Math.floor(start / MINUTES_PER_DAY); day * MINUTES_PER_DAY < end; day++) {
      const midnight = day * MINUTES_PER_DAY;
      const span = {
        start: Math.max(start, midnight) - midnight,
        end: Math.min(end, midnight + MINUTES_PER_DAY) - midnight,
      };
      if (span.end > span.start) {
        days[day].spans.push(span);
      }
    }
  }
  return days;
}

// describeDays names a set of days briefly: "Every day", "Weekdays",
// "Weekends" or "Mon, Wed, Fri".
export function describeDays(days) {
  switch (presetOf(days)) {
    case 'all':
      return 'Every day';
    case 'weekdays':
      return 'Weekdays';
    case 'weekends':
      return 'Weekends';
    default:
      return sortDays(days).map((day) => DAY_NAMES[day].short).join(', ');
  }
}

// describeRule says in a sentence or two what a rule does.
export function describeRule(rule) {
  const days = sortDays(rule.days ?? []);
  if (days.length === 0) {
    return 'Pick at least one day.';
  }
  if (Number.isNaN(minuteOfDay(rule.time))) {
    return 'Pick a time.';
  }

  let when;
  switch (presetOf(days)) {
    case 'all':
      when = 'every day';
      break;
    case 'weekdays':
      when = 'on weekdays';
      break;
    default:
      when = `on ${list(days.map((day) => `${DAY_NAMES[day].long}s`))}`;
  }
  const sentence = `Turns the plug ${rule.action === 'off' ? 'off' : 'on'} at ${rule.time} ${when}.`;
  return rule.disabled ? `${sentence} It is paused, so it will not run.` : sentence;
}

function list(words) {
  if (words.length < 2) {
    return words.join('');
  }
  return `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`;
}

// ruleLabel is what to call a rule: its name if it has one, or what it does.
export function ruleLabel(rule) {
  return rule.name || `${rule.action === 'off' ? 'Off' : 'On'} at ${rule.time}`;
}

// cleanRule returns a rule with only the fields the API knows, leaving out
// the optional ones when they are empty.
export function cleanRule(rule) {
  const clean = { id: rule.id };
  const name = (rule.name ?? '').trim();
  if (name) {
    clean.name = name;
  }
  if (rule.disabled) {
    clean.disabled = true;
  }
  clean.days = sortDays(rule.days ?? []);
  clean.time = rule.time;
  clean.action = rule.action === 'off' ? 'off' : 'on';
  return clean;
}
