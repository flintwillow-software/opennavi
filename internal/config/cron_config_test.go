package config

import (
	"strings"
	"testing"
)

func TestCronBlockValidation(t *testing.T) {
	valid := CronConfig{
		Schedules: []CronSchedule{
			{Name: "health-check", Expr: "*/5 * * * *", Prompt: "Check service health.", Priority: 3, Enabled: boolPtr(true)},
			{Name: "nightly-report", Expr: "0 6 * * *", Prompt: "Generate the nightly report.", TZ: "America/New_York"},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		cc      CronConfig
		wantErr string
	}{
		{"empty name", CronConfig{Schedules: []CronSchedule{{Expr: "* * * * *", Prompt: "p"}}}, "empty name"},
		{"duplicate name", CronConfig{Schedules: []CronSchedule{
			{Name: "a", Expr: "* * * * *", Prompt: "p"},
			{Name: "a", Expr: "* * * * *", Prompt: "p"},
		}}, "duplicate cron schedule"},
		{"missing expr", CronConfig{Schedules: []CronSchedule{{Name: "a", Prompt: "p"}}}, "expr is required"},
		{"missing prompt", CronConfig{Schedules: []CronSchedule{{Name: "a", Expr: "* * * * *"}}}, "prompt is required"},
		{"priority out of range", CronConfig{Schedules: []CronSchedule{{Name: "a", Expr: "* * * * *", Prompt: "p", Priority: 11}}}, "out of range"},
	}
	for _, c := range cases {
		err := c.cc.Validate()
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error = %v, want containing %q", c.name, err, c.wantErr)
		}
	}
}

func TestCronBlockEmptyIsValid(t *testing.T) {
	if err := (CronConfig{}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCronIsEnabledNilTrue(t *testing.T) {
	// nil Enabled = enabled (cron convention: omitted never disables).
	if !((CronSchedule{Name: "a"}).IsEnabled()) {
		t.Fatal("nil Enabled must be enabled")
	}
	if (CronSchedule{Name: "a", Enabled: boolPtr(false)}).IsEnabled() {
		t.Fatal("explicit false must be disabled")
	}
}

func boolPtr(b bool) *bool { return &b }
