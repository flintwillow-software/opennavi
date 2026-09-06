package config

import "fmt"

// CronScheduleDef is one named cron schedule in the navi definition
// (spec §01 Cron interface: tasks defined in navi code or config).
// Static, developer-authored — navi.yaml is the source of truth for
// schedule definitions (see #239 plan amendment); SQLite persists tick
// state only.
type CronScheduleDef struct {
	Name     string `yaml:"name"`
	Expr     string `yaml:"expr"`
	Prompt   string `yaml:"prompt"`
	Priority int    `yaml:"priority,omitempty"`
	TZ       string `yaml:"tz,omitempty"`
	Enabled  bool   `yaml:"enabled"`
}

// DefinitionCronConfig is the `cron:` block in the navi definition.
type DefinitionCronConfig struct {
	Schedules []CronScheduleDef `yaml:"schedules,omitempty"`
}

// Validate checks the cron block: non-empty names/exprs/prompts,
// unique names, priority range.
func (c DefinitionCronConfig) Validate() error {
	seen := map[string]bool{}
	for _, s := range c.Schedules {
		if s.Name == "" {
			return fmt.Errorf("definition: cron schedule with empty name")
		}
		if seen[s.Name] {
			return fmt.Errorf("definition: duplicate cron schedule %q", s.Name)
		}
		seen[s.Name] = true
		if s.Expr == "" {
			return fmt.Errorf("definition: cron schedule %q: expr is required", s.Name)
		}
		if s.Prompt == "" {
			return fmt.Errorf("definition: cron schedule %q: prompt is required", s.Name)
		}
		if s.Priority < 0 || s.Priority > 10 {
			return fmt.Errorf("definition: cron schedule %q: priority %d out of range [0..10] (0 = default 3)", s.Name, s.Priority)
		}
	}
	return nil
}
