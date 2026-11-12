// Package schedule decides when a plug should be switched, from a list of
// weekly rules, and runs those rules against a clock.
//
// A rule switches the plug on or off at a time of day on chosen days of the
// week. Times are wall-clock times in the timezone of the machine that runs
// the schedule.
package schedule

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Action is what a rule does to the plug.
type Action string

// The actions a rule can take.
const (
	On  Action = "on"
	Off Action = "off"
)

// TimeOfDay is a wall-clock time, to the minute. In JSON it is a string such
// as "07:30" or "18:30".
type TimeOfDay struct {
	Hour   int
	Minute int
}

// ParseTimeOfDay parses a 24-hour time written as H:MM or HH:MM.
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	bad := fmt.Errorf("schedule: time %q is not in 24-hour HH:MM form", s)
	hour, minute, ok := strings.Cut(s, ":")
	if !ok || len(hour) < 1 || len(hour) > 2 || len(minute) != 2 {
		return TimeOfDay{}, bad
	}
	h, err := strconv.ParseUint(hour, 10, 8)
	if err != nil || h > 23 {
		return TimeOfDay{}, bad
	}
	m, err := strconv.ParseUint(minute, 10, 8)
	if err != nil || m > 59 {
		return TimeOfDay{}, bad
	}
	return TimeOfDay{Hour: int(h), Minute: int(m)}, nil
}

// String returns the time as HH:MM.
func (t TimeOfDay) String() string {
	return fmt.Sprintf("%02d:%02d", t.Hour, t.Minute)
}

func (t TimeOfDay) valid() bool {
	return t.Hour >= 0 && t.Hour <= 23 && t.Minute >= 0 && t.Minute <= 59
}

// MarshalText implements encoding.TextMarshaler.
func (t TimeOfDay) MarshalText() ([]byte, error) {
	if !t.valid() {
		return nil, fmt.Errorf("schedule: time %d:%d is out of range", t.Hour, t.Minute)
	}
	return []byte(t.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *TimeOfDay) UnmarshalText(text []byte) error {
	parsed, err := ParseTimeOfDay(string(text))
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// Days is a set of days of the week, indexed by time.Weekday. In JSON it is a
// list of three-letter day names, such as ["mon", "wed", "fri"].
type Days [7]bool

// dayNames are the JSON names of the days, indexed by time.Weekday.
var dayNames = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// weekOrder is the order in which days are written out: Monday first.
var weekOrder = [7]time.Weekday{
	time.Monday, time.Tuesday, time.Wednesday, time.Thursday,
	time.Friday, time.Saturday, time.Sunday,
}

// EveryDay returns the set of all seven days.
func EveryDay() Days {
	return Days{true, true, true, true, true, true, true}
}

// Empty reports whether the set has no days in it.
func (d Days) Empty() bool {
	return d == Days{}
}

// MarshalJSON implements json.Marshaler.
func (d Days) MarshalJSON() ([]byte, error) {
	names := make([]string, 0, len(d))
	for _, day := range weekOrder {
		if d[day] {
			names = append(names, dayNames[day])
		}
	}
	return json.Marshal(names)
}

// UnmarshalJSON implements json.Unmarshaler. Day names are matched without
// regard to case.
func (d *Days) UnmarshalJSON(data []byte) error {
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return errors.New(`schedule: days must be a list of day names, such as ["mon", "tue"]`)
	}
	var days Days
	for _, name := range names {
		found := false
		for day, known := range dayNames {
			if strings.EqualFold(name, known) {
				days[day] = true
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("schedule: unknown day %q (use sun, mon, tue, wed, thu, fri or sat)", name)
		}
	}
	*d = days
	return nil
}

// Rule switches the plug at one time of day on chosen days of the week.
type Rule struct {
	// ID identifies the rule. It must be unique within a schedule.
	ID string `json:"id"`

	// Name is an optional description for people.
	Name string `json:"name,omitempty"`

	// Disabled turns the rule off without deleting it.
	Disabled bool `json:"disabled,omitempty"`

	// Days are the days of the week the rule applies on.
	Days Days `json:"days"`

	// Time is when the rule fires on those days.
	Time TimeOfDay `json:"time"`

	// Action is what the rule does.
	Action Action `json:"action"`
}

// Validate reports the first problem with the rule, if any.
func (r Rule) Validate() error {
	switch {
	case r.ID == "":
		return errors.New("schedule: rule has no id")
	case !r.Time.valid():
		return fmt.Errorf("schedule: rule %q: time %d:%d is out of range", r.ID, r.Time.Hour, r.Time.Minute)
	case r.Days.Empty():
		return fmt.Errorf("schedule: rule %q applies on no days", r.ID)
	case r.Action != On && r.Action != Off:
		return fmt.Errorf(`schedule: rule %q: action %q is not "on" or "off"`, r.ID, r.Action)
	}
	return nil
}

// Schedule is an ordered list of rules. When two rules fire at the same
// moment, the one later in the list wins.
type Schedule struct {
	Rules []Rule `json:"rules"`
}

// Validate reports the first problem with the schedule, if any.
func (s Schedule) Validate() error {
	seen := make(map[string]bool, len(s.Rules))
	for _, rule := range s.Rules {
		if err := rule.Validate(); err != nil {
			return err
		}
		if seen[rule.ID] {
			return fmt.Errorf("schedule: more than one rule has the id %q", rule.ID)
		}
		seen[rule.ID] = true
	}
	return nil
}

// Digest returns a fingerprint of the rules. Two schedules have the same
// digest exactly when their rules are the same.
func (s Schedule) Digest() string {
	rules := s.Rules
	if rules == nil {
		rules = []Rule{}
	}
	encoded, err := json.Marshal(rules)
	if err != nil {
		// Only an out-of-range time can fail to encode, and such a rule
		// does not pass Validate. Fall back to Go's own rendering so the
		// digest still changes when the rules do.
		encoded = []byte(fmt.Sprintf("%#v", rules))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Parse decodes a schedule from JSON and validates it. Unknown fields are an
// error, so that a misspelt field name is not silently ignored.
func Parse(data []byte) (Schedule, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var s Schedule
	if err := decoder.Decode(&s); err != nil {
		return Schedule{}, fmt.Errorf("schedule: %w", describeJSONError(err))
	}
	if _, err := decoder.Token(); err != io.EOF {
		return Schedule{}, errors.New("schedule: unexpected content after the schedule")
	}
	if err := s.Validate(); err != nil {
		return Schedule{}, err
	}
	return s, nil
}

// describeJSONError strips the package prefix from errors this package
// produced while decoding, so that it does not appear twice.
func describeJSONError(err error) error {
	return errors.New(strings.TrimPrefix(err.Error(), "schedule: "))
}

// Load reads and parses the schedule in the file at path.
func Load(path string) (Schedule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Schedule{}, err
	}
	s, err := Parse(data)
	if err != nil {
		return Schedule{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}
