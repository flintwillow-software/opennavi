// Package cron implements a pure-Go 5-field cron scheduler (spec
// 01-agent-runtime/00-thought-queue.md, daemon-navi cron interface):
// it wakes on a cron schedule and enqueues thoughts on the thought
// queue. Standard 5-field format (minute hour dom mon dow), numbers
// only; timezone-aware with per-schedule TZ overrides.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronExpr is a parsed 5-field cron expression.
type CronExpr struct {
	minute, hour, dom, mon, dow fieldSet
	// domOrDow tracks Vixie-cron semantics: when both dom and dow are
	// restricted (non-*), a time matches if EITHER matches.
	domOrDow bool
}

// fieldSet is the set of allowed values for one field.
type fieldSet struct {
	any  bool
	vals map[int]bool
}

func (f fieldSet) matches(v int) bool {
	if f.any {
		return true
	}
	return f.vals[v]
}

type fieldSpec struct {
	min, max int
	name     string
}

var fieldSpecs = [5]fieldSpec{
	{0, 59, "minute"},
	{0, 23, "hour"},
	{1, 31, "day-of-month"},
	{1, 12, "month"},
	{0, 6, "day-of-week"}, // 0=Sunday
}

// Parse parses a standard 5-field cron expression ("m h dom mon dow").
// Supported syntax per field: *, exact values, ranges (a-b), steps
// (*\/n and a-b/n), comma lists. Numbers only.
func Parse(expr string) (*CronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: %q: expected 5 fields, got %d", expr, len(fields))
	}
	var ce CronExpr
	for i, spec := range fieldSpecs {
		fs, err := parseField(fields[i], spec)
		if err != nil {
			return nil, fmt.Errorf("cron: %q: %s: %w", expr, spec.name, err)
		}
		switch i {
		case 0:
			ce.minute = fs
		case 1:
			ce.hour = fs
		case 2:
			ce.dom = fs
		case 3:
			ce.mon = fs
		case 4:
			ce.dow = fs
		}
	}
	if !ce.dom.any && !ce.dow.any {
		ce.domOrDow = true
	}
	return &ce, nil
}

// Matches reports whether t fires under this expression. Sub-minute
// precision is truncated (cron fires at second 0).
func (c *CronExpr) Matches(t time.Time) bool {
	if !c.minute.matches(t.Minute()) || !c.hour.matches(t.Hour()) || !c.mon.matches(int(t.Month())) {
		return false
	}
	dayOK := c.dom.matches(t.Day())
	dowOK := c.dow.matches(int(t.Weekday()))
	if c.domOrDow {
		return dayOK || dowOK
	}
	return dayOK && dowOK
}

// Next returns the next fire time strictly after t (minute-resolution
// scan, capped at ~5 years to bound pathological expressions). Timezone
// aware: minute-of-day matching uses the time's own location, so DST
// gaps (skipped wall times) resolve to the next existing wall time and
// DST overlaps fire once per wall-time pass.
func (c *CronExpr) Next(t time.Time) time.Time {
	tt := t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for tt.Before(limit) {
		if c.Matches(tt) {
			return tt
		}
		tt = tt.Add(time.Minute)
	}
	return time.Time{}
}

// parseField expands one field into its allowed value set.
func parseField(s string, spec fieldSpec) (fieldSet, error) {
	fs := fieldSet{vals: make(map[int]bool)}
	if s == "*" {
		fs.any = true
		return fs, nil
	}
	for _, part := range strings.Split(s, ",") {
		step := 1
		rangePart := part
		if i := strings.Index(part, "/"); i >= 0 {
			rangePart = part[:i]
			stepStr := part[i+1:]
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return fs, fmt.Errorf("invalid step %q", stepStr)
			}
			step = n
		}
		lo, hi := spec.min, spec.max
		if rangePart != "*" {
			var err error
			lo, hi, err = parseRange(rangePart, spec)
			if err != nil {
				return fs, err
			}
		}
		if step == 1 && lo == hi {
			fs.vals[lo] = true
			continue
		}
		for v := lo; v <= hi; v += step {
			fs.vals[v] = true
		}
	}
	if len(fs.vals) == 0 {
		return fs, fmt.Errorf("empty field")
	}
	return fs, nil
}

func parseRange(s string, spec fieldSpec) (int, int, error) {
	lo, hi := spec.min, spec.max
	if i := strings.Index(s, "-"); i > 0 {
		var err error
		lo, err = strconv.Atoi(s[:i])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid range start %q", s[:i])
		}
		hi, err = strconv.Atoi(s[i+1:])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid range end %q", s[i+1:])
		}
	} else {
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid value %q", s)
		}
		lo, hi = v, v
	}
	if lo < spec.min || hi > spec.max || lo > hi {
		return 0, 0, fmt.Errorf("value out of range [%d..%d]", spec.min, spec.max)
	}
	return lo, hi, nil
}
