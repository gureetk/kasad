package schedule

import (
	"testing"
	"time"
	_ "time/tzdata" // the tests need a real timezone wherever they run
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

// eveningSchedule switches on at 18:30 and off at 22:15 every day.
func eveningSchedule() Schedule {
	return Schedule{Rules: []Rule{
		{ID: "on", Days: EveryDay(), Time: TimeOfDay{18, 30}, Action: On},
		{ID: "off", Days: EveryDay(), Time: TimeOfDay{22, 15}, Action: Off},
	}}
}

func days(weekdays ...time.Weekday) Days {
	var d Days
	for _, day := range weekdays {
		d[day] = true
	}
	return d
}

func TestLastAndNext(t *testing.T) {
	loc := mustLocation(t, "America/New_York")
	s := eveningSchedule()
	// Wednesday 14 January 2026, well away from any clock change.
	at := func(day, hour, minute int) time.Time {
		return time.Date(2026, time.January, day, hour, minute, 0, 0, loc)
	}

	cases := []struct {
		name     string
		now      time.Time
		lastID   string
		lastAt   time.Time
		nextID   string
		nextAt   time.Time
		describe string
	}{
		{
			name:   "between the two rules",
			now:    at(14, 20, 0),
			lastID: "on", lastAt: at(14, 18, 30),
			nextID: "off", nextAt: at(14, 22, 15),
		},
		{
			name:   "before either rule today",
			now:    at(14, 10, 0),
			lastID: "off", lastAt: at(13, 22, 15),
			nextID: "on", nextAt: at(14, 18, 30),
		},
		{
			name:   "after both rules today",
			now:    at(14, 23, 30),
			lastID: "off", lastAt: at(14, 22, 15),
			nextID: "on", nextAt: at(15, 18, 30),
		},
		{
			name:   "exactly on a rule",
			now:    at(14, 22, 15),
			lastID: "off", lastAt: at(14, 22, 15), // at or before
			nextID: "on", nextAt: at(15, 18, 30), // strictly after
		},
		{
			name:   "one second before a rule",
			now:    at(14, 18, 29).Add(59 * time.Second),
			lastID: "off", lastAt: at(13, 22, 15),
			nextID: "on", nextAt: at(14, 18, 30),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			last, ok := s.Last(tc.now)
			if !ok || last.Rule.ID != tc.lastID || !last.At.Equal(tc.lastAt) {
				t.Errorf("Last = %s at %v (ok=%v), want %s at %v", last.Rule.ID, last.At, ok, tc.lastID, tc.lastAt)
			}
			next, ok := s.Next(tc.now)
			if !ok || next.Rule.ID != tc.nextID || !next.At.Equal(tc.nextAt) {
				t.Errorf("Next = %s at %v (ok=%v), want %s at %v", next.Rule.ID, next.At, ok, tc.nextID, tc.nextAt)
			}
		})
	}
}

func TestRulesOnlyFireOnTheirDays(t *testing.T) {
	loc := mustLocation(t, "America/New_York")
	s := Schedule{Rules: []Rule{
		{ID: "mwf", Days: days(time.Monday, time.Wednesday, time.Friday), Time: TimeOfDay{8, 0}, Action: On},
	}}

	// Tuesday 13 January 2026.
	now := time.Date(2026, time.January, 13, 12, 0, 0, 0, loc)
	last, ok := s.Last(now)
	if want := time.Date(2026, time.January, 12, 8, 0, 0, 0, loc); !ok || !last.At.Equal(want) {
		t.Errorf("Last = %v (ok=%v), want Monday %v", last.At, ok, want)
	}
	next, ok := s.Next(now)
	if want := time.Date(2026, time.January, 14, 8, 0, 0, 0, loc); !ok || !next.At.Equal(want) {
		t.Errorf("Next = %v (ok=%v), want Wednesday %v", next.At, ok, want)
	}

	// Friday evening: the next firing is Monday, across the weekend.
	now = time.Date(2026, time.January, 16, 20, 0, 0, 0, loc)
	next, ok = s.Next(now)
	if want := time.Date(2026, time.January, 19, 8, 0, 0, 0, loc); !ok || !next.At.Equal(want) {
		t.Errorf("Next = %v (ok=%v), want Monday %v", next.At, ok, want)
	}
}

func TestWeeklyRuleIsFoundAFullWeekAway(t *testing.T) {
	loc := mustLocation(t, "America/New_York")
	s := Schedule{Rules: []Rule{
		{ID: "sunday", Days: days(time.Sunday), Time: TimeOfDay{9, 0}, Action: On},
	}}

	// Sunday 18 January 2026, just before and just after the rule.
	before := time.Date(2026, time.January, 18, 8, 59, 0, 0, loc)
	after := time.Date(2026, time.January, 18, 9, 1, 0, 0, loc)

	last, ok := s.Last(before)
	if want := time.Date(2026, time.January, 11, 9, 0, 0, 0, loc); !ok || !last.At.Equal(want) {
		t.Errorf("Last just before = %v (ok=%v), want the previous Sunday %v", last.At, ok, want)
	}
	next, ok := s.Next(after)
	if want := time.Date(2026, time.January, 25, 9, 0, 0, 0, loc); !ok || !next.At.Equal(want) {
		t.Errorf("Next just after = %v (ok=%v), want the following Sunday %v", next.At, ok, want)
	}
}

func TestDisabledAndMissingRules(t *testing.T) {
	loc := mustLocation(t, "America/New_York")
	now := time.Date(2026, time.January, 14, 12, 0, 0, 0, loc)

	disabled := Schedule{Rules: []Rule{
		{ID: "off", Disabled: true, Days: EveryDay(), Time: TimeOfDay{8, 0}, Action: Off},
	}}
	for name, s := range map[string]Schedule{"no rules": {}, "only disabled rules": disabled} {
		if event, ok := s.Last(now); ok {
			t.Errorf("%s: Last returned %+v", name, event)
		}
		if event, ok := s.Next(now); ok {
			t.Errorf("%s: Next returned %+v", name, event)
		}
		if event, ok := s.Due(time.Time{}, now); ok {
			t.Errorf("%s: Due returned %+v", name, event)
		}
	}
}

func TestLaterRuleWinsATie(t *testing.T) {
	loc := mustLocation(t, "America/New_York")
	now := time.Date(2026, time.January, 14, 12, 0, 0, 0, loc)
	s := Schedule{Rules: []Rule{
		{ID: "first", Days: EveryDay(), Time: TimeOfDay{8, 0}, Action: On},
		{ID: "second", Days: EveryDay(), Time: TimeOfDay{8, 0}, Action: Off},
	}}
	last, ok := s.Last(now)
	if !ok || last.Rule.ID != "second" {
		t.Errorf("Last = %q (ok=%v), want the later rule \"second\"", last.Rule.ID, ok)
	}
}

func TestDue(t *testing.T) {
	loc := mustLocation(t, "America/New_York")
	s := eveningSchedule()
	now := time.Date(2026, time.January, 14, 20, 0, 0, 0, loc)
	onAt := time.Date(2026, time.January, 14, 18, 30, 0, 0, loc)

	if event, ok := s.Due(onAt.Add(-time.Minute), now); !ok || event.Rule.ID != "on" {
		t.Errorf("Due with an older since = %q (ok=%v), want \"on\"", event.Rule.ID, ok)
	}
	if event, ok := s.Due(onAt, now); ok {
		t.Errorf("Due with since equal to the event returned %q", event.Rule.ID)
	}
	if event, ok := s.Due(onAt.Add(time.Minute), now); ok {
		t.Errorf("Due with a newer since returned %q", event.Rule.ID)
	}

	// Several events were missed; only the latest is due.
	longAgo := now.AddDate(0, 0, -3)
	later := time.Date(2026, time.January, 15, 10, 0, 0, 0, loc)
	event, ok := s.Due(longAgo, later)
	if want := time.Date(2026, time.January, 14, 22, 15, 0, 0, loc); !ok || event.Rule.ID != "off" || !event.At.Equal(want) {
		t.Errorf("Due after a long gap = %q at %v (ok=%v), want \"off\" at %v", event.Rule.ID, event.At, ok, want)
	}
}

func TestTimesAreReadInTheClocksLocation(t *testing.T) {
	newYork := mustLocation(t, "America/New_York")
	s := Schedule{Rules: []Rule{
		{ID: "morning", Days: EveryDay(), Time: TimeOfDay{8, 0}, Action: On},
	}}
	// The same instant, read on two clocks.
	instant := time.Date(2026, time.January, 14, 12, 0, 0, 0, time.UTC) // 07:00 in New York

	next, _ := s.Next(instant.In(newYork))
	if want := time.Date(2026, time.January, 14, 8, 0, 0, 0, newYork); !next.At.Equal(want) {
		t.Errorf("Next on a New York clock = %v, want %v", next.At, want)
	}
	next, _ = s.Next(instant)
	if want := time.Date(2026, time.January, 15, 8, 0, 0, 0, time.UTC); !next.At.Equal(want) {
		t.Errorf("Next on a UTC clock = %v, want %v", next.At, want)
	}
}

// eventsBetween walks the schedule with Next and returns every event after
// from, up to and including to.
func eventsBetween(s Schedule, from, to time.Time) []Event {
	var events []Event
	for cursor := from; ; {
		event, ok := s.Next(cursor)
		if !ok || event.At.After(to) {
			return events
		}
		events = append(events, event)
		cursor = event.At
	}
}

func TestClocksGoingForward(t *testing.T) {
	// In New York on 8 March 2026 the clocks go from 02:00 straight to
	// 03:00, so 02:30 does not exist that day.
	loc := mustLocation(t, "America/New_York")
	s := Schedule{Rules: []Rule{
		{ID: "skipped", Days: EveryDay(), Time: TimeOfDay{2, 30}, Action: On},
	}}

	dayStart := time.Date(2026, time.March, 8, 0, 0, 0, 0, loc)
	dayEnd := time.Date(2026, time.March, 8, 23, 59, 0, 0, loc)
	events := eventsBetween(s, dayStart, dayEnd)
	if len(events) != 1 {
		t.Fatalf("the rule fired %d times on the day clocks went forward, want once: %v", len(events), events)
	}

	// It fires when the old clock would have read 02:30, which the new
	// clock shows as 03:30.
	if want := time.Date(2026, time.March, 8, 3, 30, 0, 0, loc); !events[0].At.Equal(want) {
		t.Errorf("the rule fired at %v, want %v", events[0].At, want)
	}

	// The days either side are unaffected.
	for _, day := range []int{7, 9} {
		want := time.Date(2026, time.March, day, 2, 30, 0, 0, loc)
		got := eventsBetween(s, want.Add(-time.Hour), want.Add(time.Hour))
		if len(got) != 1 || !got[0].At.Equal(want) {
			t.Errorf("on %d March the rule fired at %v, want once at %v", day, got, want)
		}
	}
}

func TestClocksGoingBack(t *testing.T) {
	// In New York on 1 November 2026 the clocks go from 02:00 back to
	// 01:00, so 01:30 happens twice that day.
	loc := mustLocation(t, "America/New_York")
	s := Schedule{Rules: []Rule{
		{ID: "repeated", Days: EveryDay(), Time: TimeOfDay{1, 30}, Action: On},
	}}

	dayStart := time.Date(2026, time.November, 1, 0, 0, 0, 0, loc)
	dayEnd := time.Date(2026, time.November, 1, 23, 59, 0, 0, loc)
	events := eventsBetween(s, dayStart, dayEnd)
	if len(events) != 1 {
		t.Fatalf("the rule fired %d times on the day clocks went back, want once: %v", len(events), events)
	}
	// It fires at the first 01:30, while daylight time is still in force.
	first := time.Date(2026, time.November, 1, 5, 30, 0, 0, time.UTC)
	if !events[0].At.Equal(first) {
		t.Errorf("the rule fired at %v, want the first 01:30, %v", events[0].At, first.In(loc))
	}

	// Last agrees with Next throughout the repeated hour: the event it
	// reports is never in the future and never changes.
	fired := events[0].At
	for cursor := fired; !cursor.After(fired.Add(3 * time.Hour)); cursor = cursor.Add(10 * time.Minute) {
		last, ok := s.Last(cursor)
		if !ok || !last.At.Equal(fired) {
			t.Errorf("Last at %v = %v (ok=%v), want %v", cursor, last.At, ok, fired)
		}
	}
}

func TestEveryEventIsReportedOnceAcrossAYear(t *testing.T) {
	// Walking a whole year with Next must visit each rule once per day it
	// applies on, clock changes included.
	loc := mustLocation(t, "America/New_York")
	s := Schedule{Rules: []Rule{
		{ID: "early", Days: EveryDay(), Time: TimeOfDay{1, 30}, Action: On},
		{ID: "gap", Days: EveryDay(), Time: TimeOfDay{2, 30}, Action: Off},
		{ID: "evening", Days: EveryDay(), Time: TimeOfDay{18, 30}, Action: On},
	}}
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, loc)
	to := time.Date(2026, time.December, 31, 23, 59, 59, 0, loc)

	counts := make(map[string]int)
	previous := from
	for _, event := range eventsBetween(s, from, to) {
		if !event.At.After(previous) && !event.At.Equal(from) {
			t.Fatalf("events out of order: %v after %v", event.At, previous)
		}
		previous = event.At
		counts[event.Rule.ID]++
	}
	for _, rule := range s.Rules {
		if counts[rule.ID] != 365 {
			t.Errorf("rule %q fired %d times in 2026, want 365", rule.ID, counts[rule.ID])
		}
	}
}

func TestLocalTime(t *testing.T) {
	utc := func(year int, month time.Month, day, hour, minute int) time.Time {
		return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	}
	cases := []struct {
		name         string
		zone         string
		year         int
		month        time.Month
		day          int
		hour, minute int
		want         time.Time
	}{
		// New York: 02:00 -> 03:00 on 8 March 2026, 02:00 -> 01:00 on 1 November 2026.
		{"ordinary day", "America/New_York", 2026, time.January, 14, 18, 30, utc(2026, time.January, 14, 23, 30)},
		{"before the clocks go forward", "America/New_York", 2026, time.March, 8, 1, 59, utc(2026, time.March, 8, 6, 59)},
		{"start of the skipped hour", "America/New_York", 2026, time.March, 8, 2, 0, utc(2026, time.March, 8, 7, 0)},
		{"inside the skipped hour", "America/New_York", 2026, time.March, 8, 2, 30, utc(2026, time.March, 8, 7, 30)},
		{"end of the skipped hour", "America/New_York", 2026, time.March, 8, 2, 59, utc(2026, time.March, 8, 7, 59)},
		{"after the clocks go forward", "America/New_York", 2026, time.March, 8, 3, 0, utc(2026, time.March, 8, 7, 0)},
		{"later that day", "America/New_York", 2026, time.March, 8, 4, 0, utc(2026, time.March, 8, 8, 0)},
		{"before the clocks go back", "America/New_York", 2026, time.November, 1, 0, 59, utc(2026, time.November, 1, 4, 59)},
		{"start of the repeated hour", "America/New_York", 2026, time.November, 1, 1, 0, utc(2026, time.November, 1, 5, 0)},
		{"inside the repeated hour", "America/New_York", 2026, time.November, 1, 1, 30, utc(2026, time.November, 1, 5, 30)},
		{"after the clocks go back", "America/New_York", 2026, time.November, 1, 2, 0, utc(2026, time.November, 1, 7, 0)},
		{"later that day too", "America/New_York", 2026, time.November, 1, 4, 0, utc(2026, time.November, 1, 9, 0)},

		// London: 01:00 -> 02:00 on 29 March 2026, 02:00 -> 01:00 on 25 October 2026.
		{"London skipped", "Europe/London", 2026, time.March, 29, 1, 30, utc(2026, time.March, 29, 1, 30)},
		{"London repeated", "Europe/London", 2026, time.October, 25, 1, 30, utc(2026, time.October, 25, 0, 30)},

		// Lord Howe Island moves its clocks by half an hour:
		// 02:00 -> 02:30 on 4 October 2026.
		{"half-hour change, skipped", "Australia/Lord_Howe", 2026, time.October, 4, 2, 15, utc(2026, time.October, 3, 15, 45)},
		{"half-hour change, after", "Australia/Lord_Howe", 2026, time.October, 4, 2, 45, utc(2026, time.October, 3, 15, 45)},

		// Havana changes at midnight: 00:00 -> 01:00 on 8 March 2026.
		{"skipped just after midnight", "America/Havana", 2026, time.March, 8, 0, 30, utc(2026, time.March, 8, 5, 30)},
		{"after a change at midnight", "America/Havana", 2026, time.March, 8, 1, 30, utc(2026, time.March, 8, 5, 30)},
		{"the evening before a change at midnight", "America/Havana", 2026, time.March, 7, 23, 30, utc(2026, time.March, 8, 4, 30)},

		// A zone with no clock changes at all.
		{"no clock changes", "UTC", 2026, time.March, 8, 2, 30, utc(2026, time.March, 8, 2, 30)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc := mustLocation(t, tc.zone)
			got := localTime(tc.year, tc.month, tc.day, tc.hour, tc.minute, loc)
			if !got.Equal(tc.want) {
				t.Errorf("localTime(%d-%02d-%02d %02d:%02d in %s) = %v, want %v",
					tc.year, tc.month, tc.day, tc.hour, tc.minute, tc.zone, got.UTC(), tc.want)
			}
		})
	}
}
