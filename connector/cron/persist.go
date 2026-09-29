package cron

import (
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/flintwillow-software/opennavi/thought"
)

// stateStore persists schedule TICK STATE (last_fired/next_due/
// fire_count — not definitions; schedule definitions are runtime
// config). Backed by the shared SQLite DB (migration v6).
//
// Wired by connector/cron when it is constructed with WithStateStore
// (NAVI-358): Start migrates the table, records a fire on every schedule
// thought's started event, and coalesces missed ticks at startup.
type stateStore struct {
	db *sql.DB
}

// newStateStore wraps the shared connection.
func newStateStore(db *sql.DB) *stateStore {
	return &stateStore{db: db}
}

// migrateState applies migration v6 (idempotent): the schedule_state
// table. Exposed separately so the run-startup chain can control ordering.
func migrateState(db *sql.DB) error {
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

// lastFired returns the last-fired timestamp for a schedule (zero if
// never fired).
func (ss *stateStore) lastFired(name string) (time.Time, error) {
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

// recordFire records a tick for a schedule (last_fired + fire_count).
func (ss *stateStore) recordFire(name string, at time.Time) error {
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
func (ss *stateStore) setNextDue(name string, due time.Time) error {
	_, err := ss.db.Exec(`INSERT INTO schedule_state (schedule_name, next_due, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(schedule_name) DO UPDATE SET next_due=excluded.next_due, updated_at=excluded.updated_at`,
		name, formatTimestamp(due), formatTimestamp(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("cron: set next_due %q: %w", name, err)
	}
	return nil
}

// missedTick is the startup-coalescing primitive: it enqueues ONE coalescing
// thought for a schedule whose next-due passed while the navi was down. The
// caller decides which schedules missed (lastFired + parseExpr/Next) and invokes
// this once per schedule before the scheduler starts. The thought id carries
// the scheduler's monotonic counter, so a missedTick and a fireNow landing in
// the same wall second cannot collide.
func (s *scheduler) missedTick(name string) error {
	s.mu.Lock()
	sd, ok := s.schedules[name]
	now := s.clock()
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("cron: schedule %q not found", name)
	}
	s.dbg("missed tick coalesced", map[string]any{"schedule": name})

	t := &thought.Thought{
		ID:        fmt.Sprintf("cron-%s-%s-%d", name, now.UTC().Format("20060102T150405"), atomic.AddUint64(&s.idSeq, 1)),
		Prompt:    sd.def.Prompt,
		Priority:  sd.def.Priority,
		Status:    thought.ThoughtPending,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
		Metadata:  map[string]string{"schedule": name, "missed": "true"},
	}
	return s.queue.Enqueue(t)
}

// formatTimestamp is the persistence-side time format (RFC3339Nano for
// state rows — parse compatibility matters more than lexical ordering
// here, unlike the memory tier's SQL comparisons).
func formatTimestamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
