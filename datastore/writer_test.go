package datastore

import (
	"errors"
	"testing"
	"time"
)

func TestWriterCoalescesByKey(t *testing.T) {
	w := NewWriter(WriterConfig{Interval: time.Hour}) // running, no tick during the test
	w.SetBackend(fakeBackend{sink: newFakeSink()})
	stop := w.Start()
	defer stop()

	var applied []string
	for _, v := range []string{"a", "b", "c"} {
		v := v
		w.Submit(Write{Key: "k", Apply: func(Backend) error { applied = append(applied, v); return nil }})
	}
	if w.Pending() != 1 {
		t.Fatalf("a burst on one key must coalesce to 1 pending write, got %d", w.Pending())
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "c" {
		t.Fatalf("coalesced write = %v, want only the latest c", applied)
	}
}

func TestWriterPreservesGlobalOrder(t *testing.T) {
	w := NewWriter(WriterConfig{Interval: time.Hour})
	w.SetBackend(fakeBackend{sink: newFakeSink()})
	stop := w.Start()
	defer stop()

	var applied []string
	for _, k := range []string{"a", "b", "c"} {
		k := k
		w.Submit(Write{Key: k, Apply: func(Backend) error { applied = append(applied, k); return nil }})
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if len(applied) != len(want) {
		t.Fatalf("applied = %v, want %v", applied, want)
	}
	for i := range want {
		if applied[i] != want[i] {
			t.Fatalf("order = %v, want %v", applied, want)
		}
	}
}

func TestWriterSyncWaitsForApply(t *testing.T) {
	w := NewWriter(WriterConfig{Interval: time.Hour})
	w.SetBackend(fakeBackend{sink: newFakeSink()})
	stop := w.Start()
	defer stop()

	applied := false
	if err := w.SubmitSync(Write{Key: "k", Apply: func(Backend) error { applied = true; return nil }}); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("SubmitSync must not return until the write is applied")
	}
}

func TestWriterSyncModeAppliesInline(t *testing.T) {
	w := NewWriter(WriterConfig{Interval: -1})
	w.SetBackend(fakeBackend{sink: newFakeSink()})

	applied := 0
	w.Submit(Write{Key: "k", Apply: func(Backend) error { applied++; return nil }})
	if applied != 1 {
		t.Fatalf("synchronous mode applied %d times, want 1", applied)
	}
	if w.Pending() != 0 {
		t.Fatalf("synchronous mode must queue nothing, pending = %d", w.Pending())
	}
}

func TestWriterFailureReportsAndRetries(t *testing.T) {
	var seen int
	w := NewWriter(WriterConfig{Interval: time.Hour, OnError: func(Write, error) { seen++ }})
	w.SetBackend(fakeBackend{sink: newFakeSink()})
	stop := w.Start()
	defer stop()

	applied := 0
	w.Submit(Write{Key: "k", Apply: func(Backend) error {
		applied++
		if applied == 1 {
			return errors.New("first attempt fails")
		}
		return nil
	}})

	if err := w.Flush(); err == nil {
		t.Fatal("the failed flush must return the error")
	}
	if seen != 1 {
		t.Fatalf("OnError calls = %d, want 1", seen)
	}
	if w.Pending() != 1 {
		t.Fatalf("a failed async write must be re-queued, pending = %d", w.Pending())
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if applied != 2 {
		t.Fatalf("applied = %d, want 2 (fail then retry)", applied)
	}
}

func TestWriterSubmitSyncBeforeBackend(t *testing.T) {
	w := NewWriter(WriterConfig{Interval: time.Hour})
	if err := w.SubmitSync(Write{Key: "k"}); !errors.Is(err, ErrUnbound) {
		t.Fatalf("SubmitSync without a backend = %v, want ErrUnbound", err)
	}
}

func TestWriterStopDrains(t *testing.T) {
	w := NewWriter(WriterConfig{Interval: time.Hour})
	w.SetBackend(fakeBackend{sink: newFakeSink()})
	stop := w.Start()

	applied := 0
	w.Submit(Write{Key: "k", Apply: func(Backend) error { applied++; return nil }})
	if applied != 0 {
		t.Fatal("an async write must not apply before a drain")
	}
	stop()
	if applied != 1 {
		t.Fatal("stopping the writer must drain pending work")
	}
}
