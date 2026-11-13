// Time arithmetic and wording.
//
// kasad runs its rules by the wall clock of the machine it is on, which need
// not be in the browser's timezone. So the page works from the digits kasad
// sends, not from the browser's idea of local time. A "wall" value below is
// those digits as milliseconds, read as though they were UTC.

const serverTime = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

const MINUTE = 60;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const DAY_MS = DAY * 1000;

const weekdayNames = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];

// parseServerTime reads a time as kasad writes it, such as
// "2030-01-02T19:42:00.5-05:00". It returns the instant ("epoch") and the wall
// clock ("wall"), or null if the text is not such a time.
export function parseServerTime(text) {
  const match = serverTime.exec(text ?? '');
  if (!match) {
    return null;
  }
  const [year, month, day, hour, minute, second] = match.slice(1, 7).map(Number);
  const epoch = Date.parse(text);
  if (Number.isNaN(epoch)) {
    return null;
  }
  return { epoch, wall: Date.UTC(year, month - 1, day, hour, minute, second) };
}

// wallParts splits a wall value into the pieces the page draws with.
// weekday counts from Monday as 0.
export function wallParts(wall) {
  const date = new Date(wall);
  const minuteOfDay = date.getUTCHours() * 60 + date.getUTCMinutes();
  return {
    day: Math.floor(wall / DAY_MS),
    weekday: (date.getUTCDay() + 6) % 7,
    minuteOfDay,
    secondOfDay: minuteOfDay * 60 + date.getUTCSeconds(),
  };
}

// formatMinute writes a minute of the day as HH:MM. 1440 is written 24:00.
export function formatMinute(minute) {
  const hours = String(Math.floor(minute / 60)).padStart(2, '0');
  const minutes = String(minute % 60).padStart(2, '0');
  return `${hours}:${minutes}`;
}

// formatSpan writes a length of time in at most two units: "12 min",
// "1 h 12 min", "2 d 4 h".
export function formatSpan(seconds) {
  const total = Math.max(0, Math.floor(seconds));
  if (total < MINUTE) {
    return 'less than a minute';
  }
  if (total < HOUR) {
    return `${Math.floor(total / MINUTE)} min`;
  }
  if (total < DAY) {
    return join(Math.floor(total / HOUR), 'h', Math.floor((total % HOUR) / MINUTE), 'min');
  }
  return join(Math.floor(total / DAY), 'd', Math.floor((total % DAY) / HOUR), 'h');
}

function join(major, majorUnit, minor, minorUnit) {
  return minor === 0 ? `${major} ${majorUnit}` : `${major} ${majorUnit} ${minor} ${minorUnit}`;
}

// formatAgo writes how long ago something happened: "just now", "40 s ago",
// "3 min ago".
export function formatAgo(seconds) {
  const total = Math.max(0, Math.floor(seconds));
  if (total < 15) {
    return 'just now';
  }
  if (total < MINUTE) {
    return `${total} s ago`;
  }
  if (total < HOUR) {
    return `${Math.floor(total / MINUTE)} min ago`;
  }
  if (total < DAY) {
    return `${Math.floor(total / HOUR)} h ago`;
  }
  return `${Math.floor(total / DAY)} d ago`;
}

// describeOffset says how far the plug's clock is from kasad's. Positive
// seconds mean the plug is ahead.
export function describeOffset(seconds) {
  const size = Math.abs(seconds);
  if (size <= 2) {
    return 'In step';
  }
  const amount = size < MINUTE ? `${size} s` : formatSpan(size);
  return `${amount} ${seconds > 0 ? 'ahead' : 'behind'}`;
}

// describeSignal puts a Wi-Fi signal strength in dBm into a word.
export function describeSignal(rssi) {
  if (rssi >= -55) {
    return 'Strong';
  }
  if (rssi >= -67) {
    return 'Good';
  }
  if (rssi >= -78) {
    return 'Fair';
  }
  return 'Weak';
}

// describeDay says which day a wall value falls on, seen from another: ""
// for the same day, then " tomorrow", " on Sat", " next Wed".
export function describeDay(wall, fromWall) {
  const target = wallParts(wall);
  const distance = target.day - wallParts(fromWall).day;
  if (distance <= 0) {
    return '';
  }
  if (distance === 1) {
    return ' tomorrow';
  }
  const name = weekdayNames[target.weekday];
  return distance < 7 ? ` on ${name}` : ` next ${name}`;
}
