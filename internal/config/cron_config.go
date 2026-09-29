package config

import (
	"fmt"

	"github.com/flintwillow-software/opennavi/internal/cron"
)

// CronSchedule is one named cron schedule in the runtime config
// (.config/navi/<name>/config.yaml, cron interface). Under the
// static-definition vs runtime-config split, schedules
// are deployment-time settings owned by the operator — the compiled
// navi definition declares whether the cron interface exists, not
// which schedules it runs. SQLite persists tick state only.
//
// Enabled is *bool with nil = enabled: schedules are ON unless turned
// off (cron convention), and an omitted field must never silently
// disable a schedule.
type CronSchedule struct {
	Name     string `yaml:"name"`
	Expr     string `yaml:"expr"`
	Prompt   string `yaml:"prompt"`
	Priority int    `yaml:"priority,omitempty"`
	TZ       string `yaml:"tz,omitempty"`
	Enabled  *bool  `yaml:"enabled"`
}

// IsEnabled implements the nil-true convention.
func (s CronSchedule) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// CronConfig is the `cron:` block in the runtime config.
type CronConfig struct {
	Schedules []CronSchedule `yaml:"schedules,omitempty"`
}

// Validate checks the cron block: non-empty names/exprs/prompts,
// unique names, priority range.
func (c CronConfig) Validate() error {
	seen := map[string]bool{}
	for _, s := range c.Schedules {
		if s.Name == "" {
			return fmt.Errorf("config: cron schedule with empty name")
		}
		if seen[s.Name] {
			return fmt.Errorf("config: duplicate cron schedule %q", s.Name)
		}
		seen[s.Name] = true
		if s.Expr == "" {
			return fmt.Errorf("config: cron schedule %q: expr is required", s.Name)
		}
		if s.Prompt == "" {
			return fmt.Errorf("config: cron schedule %q: prompt is required", s.Name)
		}
		if s.Priority < 0 || s.Priority > 10 {
			return fmt.Errorf("config: cron schedule %q: priority %d out of range [0..10] (0 = default 3)", s.Name, s.Priority)
		}
	}
	return nil
}

// ToSchedulerDefs converts validated runtime schedules to the
// scheduler's ScheduleDef shape (nil-Enabled → not disabled).
func (c CronConfig) ToSchedulerDefs() []cron.ScheduleDef {
	out := make([]cron.ScheduleDef, 0, len(c.Schedules))
	for _, s := range c.Schedules {
		out = append(out, cron.ScheduleDef{
			Name:     s.Name,
			Expr:     s.Expr,
			Prompt:   s.Prompt,
			Priority: s.Priority,
			TZ:       s.TZ,
			Disabled: s.Enabled != nil && !*s.Enabled,
		})
	}
	return out
}
