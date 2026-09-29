package cron

import (
	"database/sql"

	"github.com/flintwillow-software/opennavi/store/sqlite"
	"testing"
	"time"
)

func TestStateStoreRoundTrip(t *testing.T) {
	db := testStateDB(t)
	ss := newStateStore(db)

	// lastFired on unknown schedule: zero time, no error.
	got, err := ss.lastFired("never")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Fatalf("lastFired(never) = %v, want zero", got)
	}

	// recordFire advances last_fired and fire_count.
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	if err := ss.recordFire("health-check", at); err != nil {
		t.Fatal(err)
	}
	if err := ss.recordFire("health-check", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	// NextDue round-trip.
	due := time.Date(2026, 9, 6, 13, 0, 0, 0, time.UTC)
	if err := ss.setNextDue("health-check", due); err != nil {
		t.Fatal(err)
	}

	// Row state verified through a raw query (the store exposes only
	// the three primitives; this checks the persisted shape).
	row := db.QueryRow(`SELECT last_fired, next_due, fire_count FROM schedule_state WHERE schedule_name = ?`, "health-check")
	var lastFired, nextDue string
	var fireCount int
	if err := row.Scan(&lastFired, &nextDue, &fireCount); err != nil {
		t.Fatal(err)
	}
	parsedLast, err := time.Parse(time.RFC3339Nano, lastFired)
	if err != nil {
		t.Fatal(err)
	}
	if !parsedLast.Equal(at.Add(time.Minute)) {
		t.Fatalf("last_fired = %v, want %v", parsedLast, at.Add(time.Minute))
	}
	parsedDue, err := time.Parse(time.RFC3339Nano, nextDue)
	if err != nil {
		t.Fatal(err)
	}
	if !parsedDue.Equal(due) {
		t.Fatalf("next_due = %v, want %v", parsedDue, due)
	}
	if fireCount != 2 {
		t.Fatalf("fire_count = %d, want 2", fireCount)
	}
}

func TestStateStoreMultipleSchedules(t *testing.T) {
	db := testStateDB(t)
	ss := newStateStore(db)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	if err := ss.recordFire("a", now); err != nil {
		t.Fatal(err)
	}
	if err := ss.recordFire("b", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// Independent rows.
	a, _ := ss.lastFired("a")
	b, _ := ss.lastFired("b")
	if !a.Equal(now) || !b.Equal(now.Add(time.Second)) {
		t.Fatalf("a=%v b=%v", a, b)
	}
}

func testStateDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := sqlite.Open(t.TempDir()+"/cron.db", sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := migrateState(st.DB()); err != nil {
		t.Fatal(err)
	}
	return st.DB()
}
