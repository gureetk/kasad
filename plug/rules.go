package plug

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNoSuchRule is returned by DeleteRule when the device has no rule with
// the id it was given.
var ErrNoSuchRule = errors.New("the device has no rule with that id")

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
	found := false
	for _, rule := range rules.List {
		if rule.ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("plug: %w: %q", ErrNoSuchRule, id)
	}
	return p.call(ctx, command{
		module: "schedule",
		method: "delete_rule",
		params: map[string]string{"id": id},
	})
}
