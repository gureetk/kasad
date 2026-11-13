package plug

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNoSuchRule is returned by DeleteRule and EditRule when the device has no
// rule with the id they were given.
var ErrNoSuchRule = errors.New("the device has no rule with that id")

// ErrNotWeekly is returned by EditRule for a rule that is not of the plain
// weekly kind, which it would have to turn into one.
var ErrNotWeekly = errors.New("the rule is not a plain weekly one")

// ErrNotStored is returned by AddRule and EditRule when the device accepts
// the rule but then does not list it as it was sent.
var ErrNotStored = errors.New("the device did not store the rule as it was sent")

// RuleAction is what a rule on the device does to the relay.
type RuleAction int

// The actions a rule can take. Any other value is one this package does not
// know the meaning of.
const (
	RuleNone RuleAction = -1
	RuleOff  RuleAction = 0
	RuleOn   RuleAction = 1
)

// RuleTime says what a rule's moment is measured by.
type RuleTime int

// The ways a rule can be timed. Any other value is one this package does not
// know the meaning of.
const (
	NoTime      RuleTime = -1
	AtClockTime RuleTime = 0
	AtSunrise   RuleTime = 1
	AtSunset    RuleTime = 2
)

// Rule is one rule in the schedule a device keeps and runs by itself, by its
// own clock.
//
// These are not the rules of package schedule, which kasad runs. A device's
// own rules go on working whether or not anything else on the network does.
type Rule struct {
	// ID is the device's name for the rule.
	ID string

	// Name is the description the rule was given when it was made.
	Name string

	// Enabled reports whether the rule is switched on.
	Enabled bool

	// Repeat reports whether the rule runs every week on its Days. A rule
	// that does not repeat runs once.
	Repeat bool

	// Days are the days of the week the rule runs on, indexed by
	// time.Weekday.
	Days [7]bool

	// Time says how the rule is timed. Minute is the time of day it runs,
	// in minutes after midnight by the device's clock, and means that only
	// when Time is AtClockTime.
	Time   RuleTime
	Minute int

	// Action is what the rule does when it runs.
	Action RuleAction

	// EndAction is what the rule does a second time, later on, if
	// anything. EndTime and EndMinute say when, as Time and Minute do.
	EndAction RuleAction
	EndTime   RuleTime
	EndMinute int
}

// Weekly reports whether the rule is the plain kind: one that switches the
// relay on or off at a time of day on chosen days of every week, and does
// nothing else. That is the kind package schedule can also express.
func (r Rule) Weekly() bool {
	return r.Repeat &&
		r.Days != [7]bool{} &&
		r.Time == AtClockTime && r.Minute >= 0 && r.Minute < 24*60 &&
		(r.Action == RuleOn || r.Action == RuleOff) &&
		r.EndAction == RuleNone
}

// Rules is the schedule a device keeps.
type Rules struct {
	// Enabled reports whether the device's schedule as a whole is
	// switched on. When it is off, none of the rules run.
	Enabled bool

	// List holds the rules in the order the device reports them.
	List []Rule
}

// wireRules is the result of schedule.get_rules.
type wireRules struct {
	Enable *int       `json:"enable"`
	Rules  []wireRule `json:"rule_list"`
}

// wireRule is a rule as the device describes it. Fields a device leaves out
// are taken to mean "none".
type wireRule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enable  int    `json:"enable"`
	Repeat  int    `json:"repeat"`
	Weekday []int  `json:"wday"` // seven flags, Sunday first
	Time    *int   `json:"stime_opt"`
	Minute  int    `json:"smin"`
	Action  *int   `json:"sact"`
	EndTime *int   `json:"etime_opt"`
	EndMin  int    `json:"emin"`
	EndAct  *int   `json:"eact"`
}

func (w wireRule) rule() Rule {
	r := Rule{
		ID:        w.ID,
		Name:      w.Name,
		Enabled:   w.Enable != 0,
		Repeat:    w.Repeat != 0,
		Time:      RuleTime(orNone(w.Time)),
		Minute:    w.Minute,
		Action:    RuleAction(orNone(w.Action)),
		EndAction: RuleAction(orNone(w.EndAct)),
		EndTime:   RuleTime(orNone(w.EndTime)),
		EndMinute: w.EndMin,
	}
	for day := time.Sunday; day <= time.Saturday && int(day) < len(w.Weekday); day++ {
		r.Days[day] = w.Weekday[day] != 0
	}
	return r
}

func orNone(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

// Rules reads the schedule the device keeps.
func (p *Plug) Rules(ctx context.Context) (Rules, error) {
	var wire wireRules
	err := p.call(ctx, command{module: "schedule", method: "get_rules", result: &wire})
	if err != nil {
		return Rules{}, err
	}
	rules := Rules{
		// A device that does not say is taken to have its schedule on.
		Enabled: wire.Enable == nil || *wire.Enable != 0,
		List:    make([]Rule, 0, len(wire.Rules)),
	}
	for _, rule := range wire.Rules {
		rules.List = append(rules.List, rule.rule())
	}
	return rules, nil
}

// DeleteRule removes the rule with the given id from the device's schedule.
//
// It reads the schedule first and only asks the device to delete a rule the
// device has just listed, so that the device is never sent an id it does not
// know. If there is no such rule it returns an error that wraps
// ErrNoSuchRule, and the device's schedule is left as it was.
func (p *Plug) DeleteRule(ctx context.Context, id string) error {
	rules, err := p.Rules(ctx)
	if err != nil {
		return err
	}
	if _, found := rules.find(id); !found {
		return fmt.Errorf("plug: %w: %q", ErrNoSuchRule, id)
	}
	return p.call(ctx, command{
		module: "schedule",
		method: "delete_rule",
		params: map[string]string{"id": id},
	})
}

// WeeklyRule describes a rule of the plain kind for AddRule and EditRule:
// one that switches the relay on or off at a time of day on chosen days of
// every week.
type WeeklyRule struct {
	// Name is a description for people. It may be empty.
	Name string

	// Enabled says whether the rule is switched on.
	Enabled bool

	// Days are the days of the week the rule runs on, indexed by
	// time.Weekday. At least one must be set.
	Days [7]bool

	// Minute is the time of day the rule runs, in minutes after midnight
	// by the device's clock.
	Minute int

	// Action is RuleOn or RuleOff.
	Action RuleAction
}

func (w WeeklyRule) validate() error {
	switch {
	case w.Days == [7]bool{}:
		return errors.New("plug: a rule must run on at least one day")
	case w.Minute < 0 || w.Minute >= 24*60:
		return fmt.Errorf("plug: %d is not a minute of the day", w.Minute)
	case w.Action != RuleOn && w.Action != RuleOff:
		return errors.New("plug: a rule must switch the relay on or off")
	}
	return nil
}

// matches reports whether r is w as the device should have stored it. The
// name is left out of the comparison, since a device may shorten or tidy one.
func (w WeeklyRule) matches(r Rule) bool {
	return r.Weekly() && r.Enabled == w.Enabled &&
		r.Days == w.Days && r.Minute == w.Minute && r.Action == w.Action
}

// wireNewRule is the argument of schedule.add_rule and schedule.edit_rule.
// The device expects every field, including the ones a plain weekly rule has
// no use for.
type wireNewRule struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Enable    int    `json:"enable"`
	Repeat    int    `json:"repeat"`
	Weekday   [7]int `json:"wday"` // Sunday first
	Time      int    `json:"stime_opt"`
	Minute    int    `json:"smin"`
	Action    int    `json:"sact"`
	EndTime   int    `json:"etime_opt"`
	EndMinute int    `json:"emin"`
	EndAction int    `json:"eact"`
	Year      int    `json:"year"`
	Month     int    `json:"month"`
	Day       int    `json:"day"`
	Force     int    `json:"force"`
	Longitude int    `json:"longitude"`
	Latitude  int    `json:"latitude"`
}

func (w WeeklyRule) wire(id string) wireNewRule {
	rule := wireNewRule{
		ID:        id,
		Name:      w.Name,
		Repeat:    1,
		Time:      int(AtClockTime),
		Minute:    w.Minute,
		Action:    int(w.Action),
		EndTime:   int(NoTime),
		EndAction: int(RuleNone),
	}
	if w.Enabled {
		rule.Enable = 1
	}
	for day, set := range w.Days {
		if set {
			rule.Weekday[day] = 1
		}
	}
	return rule
}

// find returns the rule with the given id.
func (r Rules) find(id string) (Rule, bool) {
	for _, rule := range r.List {
		if rule.ID == id {
			return rule, true
		}
	}
	return Rule{}, false
}

// AddRule adds a rule to the device's schedule and returns it as the device
// then lists it, with the id the device gave it.
//
// It reads the schedule back afterwards. If the new rule is not there as it
// was sent, AddRule returns an error that wraps ErrNotStored, along with
// whatever the device did store.
func (p *Plug) AddRule(ctx context.Context, rule WeeklyRule) (Rule, error) {
	if err := rule.validate(); err != nil {
		return Rule{}, err
	}
	before, err := p.Rules(ctx)
	if err != nil {
		return Rule{}, err
	}

	var added struct {
		ID string `json:"id"`
	}
	err = p.call(ctx, command{
		module: "schedule",
		method: "add_rule",
		params: rule.wire(""),
		result: &added,
	})
	if err != nil {
		return Rule{}, err
	}

	after, err := p.Rules(ctx)
	if err != nil {
		return Rule{}, fmt.Errorf("plug: sent the rule, but could not confirm it was stored: %w", err)
	}
	stored, found := after.find(added.ID)
	if !found {
		// The device did not say which rule is the new one, or named one
		// it does not list. Look for a rule that was not there before.
		for _, candidate := range after.List {
			if _, old := before.find(candidate.ID); !old {
				stored, found = candidate, true
				break
			}
		}
	}
	if !found {
		return Rule{}, fmt.Errorf("plug: %w: it accepted the rule but does not list it", ErrNotStored)
	}
	if !rule.matches(stored) {
		return stored, fmt.Errorf("plug: %w", ErrNotStored)
	}
	return stored, nil
}

// EditRule replaces the rule with the given id, which must be a plain weekly
// one, and returns it as the device then lists it.
//
// Like DeleteRule, it reads the schedule first and only asks the device to
// change a rule the device has just listed. It returns an error that wraps
// ErrNoSuchRule if there is no such rule and ErrNotWeekly if the rule is of
// another kind; either way the schedule is left as it was. It reads the
// schedule back afterwards, and returns an error that wraps ErrNotStored if
// the rule is not there as it was sent.
func (p *Plug) EditRule(ctx context.Context, id string, rule WeeklyRule) (Rule, error) {
	if err := rule.validate(); err != nil {
		return Rule{}, err
	}
	before, err := p.Rules(ctx)
	if err != nil {
		return Rule{}, err
	}
	existing, found := before.find(id)
	if !found {
		return Rule{}, fmt.Errorf("plug: %w: %q", ErrNoSuchRule, id)
	}
	if !existing.Weekly() {
		return existing, fmt.Errorf("plug: %w: %q", ErrNotWeekly, id)
	}

	err = p.call(ctx, command{
		module: "schedule",
		method: "edit_rule",
		params: rule.wire(id),
	})
	if err != nil {
		return Rule{}, err
	}

	after, err := p.Rules(ctx)
	if err != nil {
		return Rule{}, fmt.Errorf("plug: sent the change, but could not confirm it was stored: %w", err)
	}
	stored, found := after.find(id)
	if !found {
		return Rule{}, fmt.Errorf("plug: %w: it accepted the change but no longer lists the rule", ErrNotStored)
	}
	if !rule.matches(stored) {
		return stored, fmt.Errorf("plug: %w", ErrNotStored)
	}
	return stored, nil
}
