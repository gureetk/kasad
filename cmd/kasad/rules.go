package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gureetk/kasad/plug"
)

// listRules prints the rules the plug keeps and runs by itself.
func listRules(ctx context.Context, p *plug.Plug, env environment) error {
	rules, err := p.Rules(ctx)
	if err != nil {
		return err
	}
	w := env.stdout
	if len(rules.List) == 0 {
		fmt.Fprintln(w, "The plug has no rules of its own.")
		return nil
	}

	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tWHEN\tDOES\tDAYS\tNAME")
	for _, rule := range rules.List {
		name := rule.Name
		if name == "" {
			name = "-"
		}
		if !rule.Enabled {
			name += " (switched off)"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n",
			rule.ID,
			describeRuleTime(rule.Time, rule.Minute),
			describeRuleActions(rule),
			describeRuleDays(rule),
			name)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if !rules.Enabled {
		fmt.Fprintln(w, "\nThe plug's schedule as a whole is switched off, so none of these run.")
	}
	return nil
}

// deleteRule removes one rule from the plug's own schedule.
func deleteRule(ctx context.Context, p *plug.Plug, id string, env environment) error {
	err := p.DeleteRule(ctx, id)
	if errors.Is(err, plug.ErrNoSuchRule) {
		return fmt.Errorf("the plug has no rule with the id %q; the rules command lists the ones it has", id)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(env.stdout, "Deleted rule %s from the plug\n", id)
	return nil
}

func describeRuleTime(timed plug.RuleTime, minute int) string {
	switch {
	case timed == plug.AtClockTime && minute >= 0 && minute < 24*60:
		return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
	case timed == plug.AtSunrise:
		return "sunrise"
	case timed == plug.AtSunset:
		return "sunset"
	default:
		return "unknown"
	}
}

func describeRuleAction(action plug.RuleAction) string {
	switch action {
	case plug.RuleOn:
		return "on"
	case plug.RuleOff:
		return "off"
	default:
		return "unknown"
	}
}

// describeRuleActions says what a rule does, including the second thing it
// does if it has one.
func describeRuleActions(rule plug.Rule) string {
	does := describeRuleAction(rule.Action)
	if rule.EndAction == plug.RuleNone {
		return does
	}
	then := describeRuleAction(rule.EndAction)
	if when := describeRuleTime(rule.EndTime, rule.EndMinute); when != "unknown" {
		then += " at " + when
	}
	return does + ", then " + then
}

// weekOrder is the order days are listed in: Monday first.
var weekOrder = [7]time.Weekday{
	time.Monday, time.Tuesday, time.Wednesday, time.Thursday,
	time.Friday, time.Saturday, time.Sunday,
}

func describeRuleDays(rule plug.Rule) string {
	var names []string
	for _, day := range weekOrder {
		if rule.Days[day] {
			names = append(names, day.String()[:3])
		}
	}
	days := strings.Join(names, " ")
	switch len(names) {
	case 0:
		days = "no days"
	case 7:
		days = "every day"
	}
	if !rule.Repeat {
		return "once (" + days + ")"
	}
	return days
}
