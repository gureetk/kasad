package schedule

import "time"

// Event is one firing of a rule.
type Event struct {
	// At is the moment the rule fires.
	At time.Time

	// Rule is the rule that fires.
	Rule Rule
}

// horizon is how many days either side of a moment are searched for events.
// Rules repeat weekly, so a rule that fires at all fires within seven days.
const horizon = 7

// Last returns the most recent event at or before now, among the enabled
// rules. It reports false if there are no enabled rules.
//
// Times are taken in now's location.
func (s Schedule) Last(now time.Time) (Event, bool) {
	var (
		best  Event
		found bool
	)
	// The day after is searched too: on a day when clocks change, an event
	// can land just the other side of midnight from the day it belongs to.
	for offset := -horizon; offset <= 1; offset++ {
		for _, event := range s.eventsOn(now, offset) {
			// Later rules win ties, hence "not before" rather than "after".
			if !event.At.After(now) && (!found || !event.At.Before(best.At)) {
				best, found = event, true
			}
		}
	}
	return best, found
}

// Next returns the first event strictly after now, among the enabled rules.
// It reports false if there are no enabled rules.
//
// Times are taken in now's location.
func (s Schedule) Next(now time.Time) (Event, bool) {
	var (
		best  Event
		found bool
	)
	for offset := -1; offset <= horizon; offset++ {
		for _, event := range s.eventsOn(now, offset) {
			if event.At.After(now) && (!found || event.At.Before(best.At)) {
				best, found = event, true
			}
		}
	}
	return best, found
}

// Due returns the event that should be applied at now, given that every
// event up to and including since has already been dealt with. That is the
// most recent event, if it is newer than since: older events that were also
// missed are superseded by it.
func (s Schedule) Due(since, now time.Time) (Event, bool) {
	event, ok := s.Last(now)
	if !ok || !event.At.After(since) {
		return Event{}, false
	}
	return event, true
}

// eventsOn returns the events of the enabled rules on the calendar day that
// is offset days from ref's, in ref's location and in rule order.
func (s Schedule) eventsOn(ref time.Time, offset int) []Event {
	loc := ref.Location()
	year, month, day := ref.Date()
	// Midday exists on every calendar day, including days when clocks
	// change, so it is a safe way to name the day.
	midday := time.Date(year, month, day+offset, 12, 0, 0, 0, loc)
	year, month, day = midday.Date()
	weekday := midday.Weekday()

	var events []Event
	for _, rule := range s.Rules {
		if rule.Disabled || !rule.Days[weekday] {
			continue
		}
		at := localTime(year, month, day, rule.Time.Hour, rule.Time.Minute, loc)
		events = append(events, Event{At: at, Rule: rule})
	}
	return events
}

// changeWindow is how far either side of a time localTime looks for a change
// of UTC offset. It only has to be longer than the largest change.
const changeWindow = 3 * time.Hour

// localTime returns the moment a clock in loc reads hour:minute on the given
// day. Two cases need a decision, and time.Date leaves both unspecified:
//
//   - Clocks go forward over the time, so it never appears that day. The
//     result is the moment the old clock would have read it, which lands
//     just after the skipped period: with a change from 02:00 to 03:00, the
//     time 02:30 becomes 03:30.
//   - Clocks go back over the time, so it appears twice. The result is the
//     first of the two.
//
// Either way, a rule fires exactly once on such a day.
func localTime(year int, month time.Month, day, hour, minute int, loc *time.Location) time.Time {
	guess := time.Date(year, month, day, hour, minute, 0, 0, loc)
	_, oldOffset := guess.Add(-changeWindow).Zone()
	_, newOffset := guess.Add(changeWindow).Zone()
	if oldOffset == newOffset {
		return guess
	}

	// The clocks change near this time. Read the wall-clock time with each
	// of the two offsets and see which readings a clock would really show.
	wall := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	withOld := wall.Add(-time.Duration(oldOffset) * time.Second).In(loc)
	withNew := wall.Add(-time.Duration(newOffset) * time.Second).In(loc)
	shows := func(t time.Time) bool {
		y, m, d := t.Date()
		return y == year && m == month && d == day && t.Hour() == hour && t.Minute() == minute
	}
	if shows(withNew) && !shows(withOld) {
		return withNew
	}
	// Otherwise the old reading is right (the time is before the change, or
	// is the first of two), or neither is (the time is skipped), and the old
	// reading is the answer in each case.
	return withOld
}
