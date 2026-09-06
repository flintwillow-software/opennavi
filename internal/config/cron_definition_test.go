package config

import (
	"strings"
	"testing"
)

func TestCronBlockValidation(t *testing.T) {
	valid := []byte(`
name: daemon-navi
cron:
  schedules:
    - name: health-check
      expr: "*/5 * * * *"
      prompt: "Check service health."
      priority: 3
      enabled: true
    - name: nightly-report
      expr: "0 6 * * *"
      prompt: "Generate the nightly report."
      tz: "America/New_York"
`)
	if _, err := ParseDefinition(valid); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ name, yaml, wantErr string }{
		{"empty name", "name: x\ncron:\n  schedules:\n    - expr: \"* * * * *\"\n      prompt: p", "empty name"},
		{"duplicate name", "name: x\ncron:\n  schedules:\n    - name: a\n      expr: \"* * * * *\"\n      prompt: p\n    - name: a\n      expr: \"* * * * *\"\n      prompt: p", "duplicate cron schedule"},
		{"missing expr", "name: x\ncron:\n  schedules:\n    - name: a\n      prompt: p", "expr is required"},
		{"missing prompt", "name: x\ncron:\n  schedules:\n    - name: a\n      expr: \"* * * * *\"", "prompt is required"},
		{"priority out of range", "name: x\ncron:\n  schedules:\n    - name: a\n      expr: \"* * * * *\"\n      prompt: p\n      priority: 11", "out of range"},
	}
	for _, c := range cases {
		_, err := ParseDefinition([]byte(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error = %v, want containing %q", c.name, err, c.wantErr)
		}
	}
}

func TestCronBlockEmptyIsValid(t *testing.T) {
	if _, err := ParseDefinition([]byte("name: x")); err != nil {
		t.Fatal(err)
	}
}
