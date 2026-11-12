package schedule

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseTimeOfDay(t *testing.T) {
	valid := map[string]TimeOfDay{
		"00:00": {0, 0},
		"07:30": {7, 30},
		"7:30":  {7, 30},
		"18:30": {18, 30},
		"23:59": {23, 59},
	}
	for input, want := range valid {
		got, err := ParseTimeOfDay(input)
		if err != nil {
			t.Errorf("ParseTimeOfDay(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParseTimeOfDay(%q) = %v, want %v", input, got, want)
		}
	}

	invalid := []string{
		"", "7", "0730", "24:00", "12:60", "12:5", "122:15", "-1:00", "+7:30",
		"ab:cd", "12:30:00", "7.30", " 7:30", "7:30pm",
	}
	for _, input := range invalid {
		if got, err := ParseTimeOfDay(input); err == nil {
			t.Errorf("ParseTimeOfDay(%q) = %v, want an error", input, got)
		}
	}
}

func TestTimeOfDayJSON(t *testing.T) {
	encoded, err := json.Marshal(TimeOfDay{Hour: 7, Minute: 5})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(encoded) != `"07:05"` {
		t.Errorf("Marshal = %s, want \"07:05\"", encoded)
	}

	var decoded TimeOfDay
	if err := json.Unmarshal([]byte(`"18:30"`), &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if want := (TimeOfDay{18, 30}); decoded != want {
		t.Errorf("Unmarshal = %v, want %v", decoded, want)
	}

	if _, err := json.Marshal(TimeOfDay{Hour: 24}); err == nil {
		t.Error("Marshal accepted an out-of-range time")
	}
}

func TestDaysJSON(t *testing.T) {
	var days Days
	if err := json.Unmarshal([]byte(`["sun", "Wed", "MON"]`), &days); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := Days{}
	want[time.Sunday], want[time.Monday], want[time.Wednesday] = true, true, true
	if days != want {
		t.Errorf("Unmarshal = %v, want %v", days, want)
	}

	// Written out in week order, Monday first, whatever order they came in.
	encoded, err := json.Marshal(days)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(encoded) != `["mon","wed","sun"]` {
		t.Errorf("Marshal = %s", encoded)
	}

	encoded, err = json.Marshal(EveryDay())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(encoded) != `["mon","tue","wed","thu","fri","sat","sun"]` {
		t.Errorf("Marshal(EveryDay()) = %s", encoded)
	}

	for _, bad := range []string{`["monday"]`, `["mon", "xyz"]`, `"mon"`, `[1, 2]`, `{"mon": true}`} {
		var d Days
		if err := json.Unmarshal([]byte(bad), &d); err == nil {
			t.Errorf("Unmarshal(%s) succeeded, want an error", bad)
		}
	}
}

const validSchedule = `{
  "rules": [
    {"id": "evening-on", "name": "Evening", "days": ["mon", "tue", "wed", "thu", "fri", "sat", "sun"], "time": "18:30", "action": "on"},
    {"id": "night-off", "days": ["mon", "tue", "wed", "thu", "fri", "sat", "sun"], "time": "22:15", "action": "off"},
    {"id": "weekend-lie-in", "disabled": true, "days": ["sat", "sun"], "time": "9:15", "action": "on"}
  ]
}`

func TestParse(t *testing.T) {
	s, err := Parse([]byte(validSchedule))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	weekend := Days{}
	weekend[time.Saturday], weekend[time.Sunday] = true, true
	want := []Rule{
		{ID: "evening-on", Name: "Evening", Days: EveryDay(), Time: TimeOfDay{18, 30}, Action: On},
		{ID: "night-off", Days: EveryDay(), Time: TimeOfDay{22, 15}, Action: Off},
		{ID: "weekend-lie-in", Disabled: true, Days: weekend, Time: TimeOfDay{9, 15}, Action: On},
	}
	if len(s.Rules) != len(want) {
		t.Fatalf("Parse returned %d rules, want %d", len(s.Rules), len(want))
	}
	for i := range want {
		if s.Rules[i] != want[i] {
			t.Errorf("rule %d\n got %+v\nwant %+v", i, s.Rules[i], want[i])
		}
	}
}

func TestParseAcceptsAnEmptySchedule(t *testing.T) {
	for _, input := range []string{`{}`, `{"rules": []}`, `{"rules": null}`} {
		s, err := Parse([]byte(input))
		if err != nil {
			t.Errorf("Parse(%s): %v", input, err)
		}
		if len(s.Rules) != 0 {
			t.Errorf("Parse(%s) returned %d rules", input, len(s.Rules))
		}
	}
}

func TestParseRejectsBadSchedules(t *testing.T) {
	cases := map[string]struct {
		input string
		want  string // text the error must contain
	}{
		"not JSON":        {`rules:`, "schedule:"},
		"misspelt field":  {`{"rules": [{"id": "a", "day": ["mon"], "time": "07:00", "action": "on"}]}`, `unknown field "day"`},
		"unknown top key": {`{"rule": []}`, `unknown field "rule"`},
		"no id":           {`{"rules": [{"days": ["mon"], "time": "07:00", "action": "on"}]}`, "no id"},
		"duplicate id":    {`{"rules": [{"id": "a", "days": ["mon"], "time": "07:00", "action": "on"}, {"id": "a", "days": ["tue"], "time": "08:00", "action": "off"}]}`, `more than one rule has the id "a"`},
		"no days":         {`{"rules": [{"id": "a", "days": [], "time": "07:00", "action": "on"}]}`, "applies on no days"},
		"days missing":    {`{"rules": [{"id": "a", "time": "07:00", "action": "on"}]}`, "applies on no days"},
		"unknown day":     {`{"rules": [{"id": "a", "days": ["funday"], "time": "07:00", "action": "on"}]}`, `unknown day "funday"`},
		"bad time":        {`{"rules": [{"id": "a", "days": ["mon"], "time": "25:00", "action": "on"}]}`, "HH:MM"},
		"bad action":      {`{"rules": [{"id": "a", "days": ["mon"], "time": "07:00", "action": "toggle"}]}`, `action "toggle"`},
		"no action":       {`{"rules": [{"id": "a", "days": ["mon"], "time": "07:00"}]}`, `action ""`},
		"trailing data":   {`{"rules": []} {"rules": []}`, "unexpected content"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.input))
			if err == nil {
				t.Fatal("Parse succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if strings.Count(err.Error(), "schedule: ") > 1 {
				t.Errorf("error repeats the package prefix: %q", err)
			}
		})
	}
}

func TestDigest(t *testing.T) {
	a, err := Parse([]byte(validSchedule))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b, err := Parse([]byte(validSchedule))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Digest() != b.Digest() {
		t.Error("the same rules gave different digests")
	}

	changes := map[string]func(*Schedule){
		"time":     func(s *Schedule) { s.Rules[0].Time.Minute++ },
		"action":   func(s *Schedule) { s.Rules[0].Action = Off },
		"days":     func(s *Schedule) { s.Rules[0].Days[time.Monday] = false },
		"disabled": func(s *Schedule) { s.Rules[0].Disabled = true },
		"id":       func(s *Schedule) { s.Rules[0].ID = "renamed" },
		"order":    func(s *Schedule) { s.Rules[0], s.Rules[1] = s.Rules[1], s.Rules[0] },
		"removal":  func(s *Schedule) { s.Rules = s.Rules[:2] },
	}
	for name, change := range changes {
		c, err := Parse([]byte(validSchedule))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		change(&c)
		if c.Digest() == a.Digest() {
			t.Errorf("changing the %s did not change the digest", name)
		}
	}

	if (Schedule{}).Digest() != (Schedule{Rules: []Rule{}}).Digest() {
		t.Error("a nil and an empty rule list gave different digests")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(validSchedule), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(good)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Rules) != 3 {
		t.Errorf("Load returned %d rules, want 3", len(s.Rules))
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"rules": [{"id": "a"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("Load error = %v, want one naming the file", err)
	}

	if _, err := Load(filepath.Join(dir, "missing.json")); !os.IsNotExist(err) {
		t.Errorf("Load of a missing file = %v, want a not-exist error", err)
	}
}
