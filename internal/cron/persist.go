package cron

import (
	"database/sql"

	"fmt"
	"github.com/flintwillow-software/opennavi/thought"
	"time"
)

// StateStore persists schedule TICK STATE (last_fired/next_due/
// fire_count — not definitions; schedule definitions are runtime
// config under .config/navi/ (S000200 §04). Backed by the shared SQLite DB
// (migration v6).
type StateStore struct {
	db *sql.DB
}

// NewStateStore wraps the shared connection.
func NewStateStore(db *sql.DB) *StateStore {
	return &StateStore{db: db}
}

// MigrateState applies migration v6 (idempotent): the schedule_state
// table. Exposed separately so the daemon startup chain (step 2) can
// control ordering.
func MigrateState(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schedule_state (
		schedule_name TEXT PRIMARY KEY,
		last_fired DATETIME,
		next_due DATETIME,
		fire_count INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("cron: create schedule_state: %w", err)
	}
	return nil
}

// LastFired returns the last-fired timestamp for a schedule (zero if
// never fired).
func (ss *StateStore) LastFired(name string) (time.Time, error) {
	var last sql.NullString
	err := ss.db.QueryRow(`SELECT last_fired FROM schedule_state WHERE schedule_name = ?`, name).Scan(&last)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("cron: last_fired %q: %w", name, err)
	}
	if !last.Valid || last.String == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, last.String)
}

// RecordFire records a tick for a schedule (last_fired + fire_count).
func (ss *StateStore) RecordFire(name string, at time.Time) error {
	_, err := ss.db.Exec(`INSERT INTO schedule_state (schedule_name, last_fired, fire_count, updated_at)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(schedule_name) DO UPDATE SET
			last_fired=excluded.last_fired, fire_count=fire_count+1, updated_at=excluded.updated_at`,
		name, formatTimestamp(at), formatTimestamp(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("cron: record fire %q: %w", name, err)
	}
	return nil
}

// NextDue records the computed next-due timestamp for a schedule.
func (ss *StateStore) SetNextDue(name string, due time.Time) error {
	_, err := ss.db.Exec(`INSERT INTO schedule_state (schedule_name, next_due, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(schedule_name) DO UPDATE SET next_due=excluded.next_due, updated_at=excluded.updated_at`,
		name, formatTimestamp(due), formatTimestamp(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("cron: set next_due %q: %w", name, err)
	}
	return nil
}

// MissedTick is a caller-invoked primitive: it enqueues ONE coalescing
// thought for a schedule whose next_due passed while the daemon was
// down. State tracking (RecordFire/next_due advancement) is wired by
// the daemon core in #240 — nothing advances state rows today, and
// repeated calls in the same wall second WILL collide on the thought
// ID (the monotonic-counter fix lands with that wiring).
func (s *Scheduler) MissedTick(name string) error {
	s.mu.Lock()
	sd, ok := s.schedules[name]
	now := s.clock()
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("cron: schedule %q not found", name)
	}
	s.dbg("missed tick coalesced", map[string]any{"schedule": name})

	t := &thought.Thought{
		ID:        fmt.Sprintf("cron-%s-%s", name, now.UTC().Format("20060102T150405")),
		Prompt:    sd.def.Prompt,
		Priority:  sd.def.Priority,
		Status:    thought.ThoughtPending,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
		Metadata:  map[string]string{"schedule": name, "missed": "true"},
	}
	return s.queue.Enqueue(t)
}

// context echo for future daemon wiring (step 2 will pass ctx through
// the tick loop).

// formatTimestamp is the persistence-side time format (RFC3339Nano for
// state rows — parse compatibility matters more than lexical ordering
// here, unlike the memory tier's SQL comparisons).
func formatTimestamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
