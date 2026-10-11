package datastore

import (
	"sync"
	"time"
	"weak"

	"github.com/flintwillow-software/opennavi/thought"
)

// The lazy-transcript layer (ADR 0011 decisions 1 and 5; NAVI-377).
//
// The tool-state layer (toolstate_runtime.go) keeps tool state resident for
// the life of the process — small, bounded by activity. Transcripts are the
// opposite: unbounded retention means the RAM a transcript occupies must be
// bounded by *access*, not by history. This layer wraps the active backend's
// Thoughts view and provides:
//
//   - **Metadata without bodies.** TranscriptInfo and TranscriptInfos answer
//     from RAM-known metadata or the backend's metadata projection alone;
//     they never load a conversation.
//   - **Detail loads on access.** LoadTranscript hydrates from the backend
//     on first read.
//   - **Detail is collectable.** A hydrated body is held only through a
//     weak pointer: once no reader holds it, the next GC reclaims it and
//     the next read re-hydrates. Nothing pins a transcript for the life of
//     the process.
//   - **Writes materialize.** SaveTranscript (and SubmitTranscript through
//     the handle) puts the written body in RAM immediately — readers see it
//     before the ordered writer drains — marks it dirty (strongly held so
//     the pending write cannot be collected out from under the queue), and
//     stages the write on the ordered writer. Once the write is applied the
//     strong hold is dropped: the backend now owns the durable copy, and
//     the RAM copy is weakly held and collectable like any other detail.
//
// Thought-row methods pass straight through: the queue persists rows
// through the datastore-level methods, which bypass this layer, and a row
// is small and always hot.

// transcriptBody is the unit the cache weakly holds. Wrapping the slice in
// a struct gives weak.Make a pointer target, so one allocation holds both
// the entries and the weak reference's identity.
type transcriptBody struct {
	entries []Entry
}

// transcriptSlot is one thought's cache entry. The slot itself (and its
// metadata) stays resident for transcripts this process touched; only the
// body comes and goes with access.
type transcriptSlot struct {
	// mu guards this slot's fields. Reads take RLock; transitions
	// (materialize, clean, delete) take Lock.
	mu sync.RWMutex
	// body is the strong hold on a dirty body: a write that the ordered
	// writer has not yet applied must survive regardless of reader
	// activity. Nil when clean.
	body *transcriptBody
	// weak is the access-bounded hold on a clean body. If weak.Value()
	// returns nil, the body was collected and the next read re-hydrates.
	weak weak.Pointer[transcriptBody]
	// info is the metadata this process knows for the transcript — set on
	// write, refreshed on flush. It answers TranscriptInfo without a body
	// read and without pinning the body.
	info TranscriptInfo
	// deleted marks a staged delete: reads report not-found and hydrations
	// are refused until the writer confirms the delete (or a newer write
	// replaces it).
	deleted bool
}

// thoughtsRAM is the lazy, access-bounded Thoughts view the handle serves.
// Constructed only by Bind, over the active backend's own view.
type thoughtsRAM struct {
	sink   Thoughts
	submit func(Write)

	mu    sync.Mutex
	slots map[string]*transcriptSlot

	// loadMu serializes sink I/O for hydration and enumeration so two
	// concurrent first reads of the same transcript cannot both miss, and
	// so a bulk enumeration does not interleave with hydrations. Sink I/O
	// runs outside mu.
	loadMu sync.Mutex
}

func newThoughtsRAM(sink Thoughts, submit func(Write)) *thoughtsRAM {
	return &thoughtsRAM{
		sink:   sink,
		submit: submit,
		slots:  map[string]*transcriptSlot{},
	}
}

// ── thought-row passthrough ─────────────────────────────────────────────

func (r *thoughtsRAM) SaveThought(t *thought.Thought) error      { return r.sink.SaveThought(t) }
func (r *thoughtsRAM) LoadThoughts() ([]*thought.Thought, error) { return r.sink.LoadThoughts() }
func (r *thoughtsRAM) LoadThought(id string) (*thought.Thought, error) {
	return r.sink.LoadThought(id)
}
func (r *thoughtsRAM) DeleteThought(id string) error { return r.sink.DeleteThought(id) }

// ── reads: metadata only until something asks for detail ────────────────

// TranscriptInfo answers from RAM-known metadata when the process has
// touched the transcript, else from the backend's metadata projection. It
// never loads a body and never caches backend metadata for untouched
// transcripts — enumerating a large history leaves nothing behind.
func (r *thoughtsRAM) TranscriptInfo(thoughtID string) (TranscriptInfo, error) {
	r.mu.Lock()
	slot := r.slots[thoughtID]
	r.mu.Unlock()
	if slot != nil {
		slot.mu.RLock()
		info, deleted := slot.info, slot.deleted
		slot.mu.RUnlock()
		if deleted {
			return TranscriptInfo{}, ErrTranscriptNotFound
		}
		return info, nil
	}
	return r.sink.TranscriptInfo(thoughtID)
}

// TranscriptInfos merges the backend's metadata enumeration with the
// process's own pending state (staged writes and deletes), in one pass.
// Nothing is cached: never-accessed transcripts leave no trace in RAM.
func (r *thoughtsRAM) TranscriptInfos() ([]TranscriptInfo, error) {
	r.loadMu.Lock()
	infos, err := r.sink.TranscriptInfos()
	r.loadMu.Unlock()
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	known := make(map[string]int, len(infos))
	for i, info := range infos {
		known[info.ThoughtID] = i
	}
	out := infos
	for id, slot := range r.slots {
		slot.mu.RLock()
		info, deleted := slot.info, slot.deleted
		slot.mu.RUnlock()
		if i, ok := known[id]; ok {
			if deleted {
				out[i] = TranscriptInfo{} // filtered below
				continue
			}
			out[i] = info // RAM state is newer than the enumeration
			continue
		}
		if deleted {
			continue
		}
		known[id] = len(out)
		out = append(out, info)
	}
	// Drop tombstoned entries in place.
	filtered := out[:0]
	for _, info := range out {
		if info.ThoughtID != "" {
			filtered = append(filtered, info)
		}
	}
	return filtered, nil
}

// LoadTranscript serves a resident body when one is alive (dirty or still
// referenced), else hydrates from the backend. The returned entries are a
// clone: a caller mutation cannot corrupt the cache or another reader.
func (r *thoughtsRAM) LoadTranscript(thoughtID string) ([]Entry, error) {
	r.mu.Lock()
	if slot := r.slots[thoughtID]; slot != nil {
		slot.mu.RLock()
		body, deleted := slot.body, slot.deleted
		var weakBody *transcriptBody
		if body == nil && !deleted {
			weakBody = slot.weak.Value()
		}
		slot.mu.RUnlock()
		if deleted {
			r.mu.Unlock()
			return nil, ErrTranscriptNotFound
		}
		if body != nil {
			entries := cloneEntries(body.entries)
			r.mu.Unlock()
			return entries, nil
		}
		if weakBody != nil {
			entries := cloneEntries(weakBody.entries)
			r.mu.Unlock()
			return entries, nil
		}
	}
	r.mu.Unlock()

	// Hydrate: sink I/O outside the cache lock, serialized so a cold start
	// that touches many transcripts does one pass at a time rather than a
	// thundering herd against the backend.
	r.loadMu.Lock()
	defer r.loadMu.Unlock()

	// Re-check under the load lock: a concurrent write or delete may have
	// landed a slot while the I/O was in flight.
	r.mu.Lock()
	if slot := r.slots[thoughtID]; slot != nil {
		slot.mu.RLock()
		body, deleted := slot.body, slot.deleted
		var weakBody *transcriptBody
		if body == nil && !deleted {
			weakBody = slot.weak.Value()
		}
		slot.mu.RUnlock()
		if !deleted && (body != nil || weakBody != nil) {
			r.mu.Unlock()
			if body != nil {
				return cloneEntries(body.entries), nil
			}
			return cloneEntries(weakBody.entries), nil
		}
	}
	r.mu.Unlock()

	entries, err := r.sink.LoadTranscript(thoughtID)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if slot := r.slots[thoughtID]; slot != nil && !slot.deleted {
		// A concurrent write landed the slot while we read the sink: its
		// body is the newer state, so prefer it and discard the sink read.
		slot.mu.RLock()
		body := slot.body
		var weakBody *transcriptBody
		if body == nil {
			weakBody = slot.weak.Value()
		}
		slot.mu.RUnlock()
		if body != nil {
			return cloneEntries(body.entries), nil
		}
		if weakBody != nil {
			return cloneEntries(weakBody.entries), nil
		}
	}
	body := &transcriptBody{entries: entries}
	r.slots[thoughtID] = &transcriptSlot{
		weak: weak.Make(body),
		info: TranscriptInfo{
			ThoughtID:    thoughtID,
			MessageCount: len(entries),
			UpdatedAt:    latestEntryTime(entries),
		},
	}
	return cloneEntries(entries), nil
}

// ── writes: materialize, mark dirty, stage on the ordered writer ────────

// SaveTranscript puts the written body in RAM immediately (readers see it
// without a backend round-trip), holds it strongly until the ordered writer
// applies the write, and stages the write. The write closure captures the
// cloned entries, so the durable copy survives even if every reader drops
// the body before the drain.
func (r *thoughtsRAM) SaveTranscript(thoughtID string, entries []Entry) error {
	copied := cloneEntries(entries)
	body := &transcriptBody{entries: copied}
	info := TranscriptInfo{
		ThoughtID:    thoughtID,
		MessageCount: len(copied),
		UpdatedAt:    latestEntryTime(copied),
	}

	r.mu.Lock()
	slot := r.slots[thoughtID]
	if slot == nil {
		slot = &transcriptSlot{}
		r.slots[thoughtID] = slot
	}
	slot.mu.Lock()
	slot.body = body
	slot.weak = weak.Make(body)
	slot.info = info
	slot.deleted = false
	slot.mu.Unlock()
	r.mu.Unlock()

	sink := r.sink
	r.submit(Write{
		Aggregate: AggregateThoughts,
		Key:       "transcript/" + thoughtID,
		Apply: func(Backend) error {
			if err := sink.SaveTranscript(thoughtID, copied); err != nil {
				return err
			}
			// The durable copy now exists; drop the strong hold so the RAM
			// body is collectable like any other detail. A newer write to
			// the same key replaces this Apply entirely (high-water mark),
			// so a coalesced burst marks clean exactly once, with the
			// newest state.
			r.clean(thoughtID, body, info)
			return nil
		},
	})
	return nil
}

// DeleteTranscript evicts the transcript from RAM immediately — reads report
// not-found from here on — and stages the delete. The tombstone survives
// until the writer confirms the delete, so a hydration in flight cannot
// resurrect what is being removed. Deleting an absent transcript is not an
// error, matching the contract.
func (r *thoughtsRAM) DeleteTranscript(thoughtID string) error {
	r.mu.Lock()
	slot := r.slots[thoughtID]
	if slot == nil {
		slot = &transcriptSlot{}
		r.slots[thoughtID] = slot
	}
	slot.mu.Lock()
	slot.body = nil
	slot.weak = weak.Pointer[transcriptBody]{}
	slot.info = TranscriptInfo{ThoughtID: thoughtID}
	slot.deleted = true
	slot.mu.Unlock()
	r.mu.Unlock()

	sink := r.sink
	r.submit(Write{
		Aggregate: AggregateThoughts,
		Key:       "transcript/" + thoughtID,
		Apply: func(Backend) error {
			if err := sink.DeleteTranscript(thoughtID); err != nil {
				return err
			}
			r.remove(thoughtID)
			return nil
		},
	})
	return nil
}

// ── cache maintenance ───────────────────────────────────────────────────

// clean marks a staged write as applied: the strong hold drops and the
// slot keeps only the weak reference and the persisted metadata. A slot
// replaced meanwhile (a newer write or delete staged before this Apply
// ran — impossible through the writer's coalescing, but guarded anyway) is
// left alone.
func (r *thoughtsRAM) clean(thoughtID string, flushed *transcriptBody, info TranscriptInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	slot := r.slots[thoughtID]
	if slot == nil {
		return
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.body == flushed {
		slot.body = nil
		slot.info = info
	}
}

// remove drops a confirmed delete's tombstone.
func (r *thoughtsRAM) remove(thoughtID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slot := r.slots[thoughtID]; slot != nil {
		slot.mu.Lock()
		deleted := slot.deleted
		slot.mu.Unlock()
		if deleted {
			delete(r.slots, thoughtID)
		}
	}
}

// ResidentTranscripts reports how many transcript bodies are currently
// held — dirty writes and hydrated bodies still referenced by a reader
// (diagnostics; the acceptance tests assert on it).
func (r *thoughtsRAM) ResidentTranscripts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, slot := range r.slots {
		slot.mu.RLock()
		resident := slot.body != nil || slot.weak.Value() != nil
		slot.mu.RUnlock()
		if resident {
			n++
		}
	}
	return n
}

// latestEntryTime returns the newest entry time, or the zero time.
func latestEntryTime(entries []Entry) time.Time {
	var latest time.Time
	for _, e := range entries {
		if e.Time.After(latest) {
			latest = e.Time
		}
	}
	return latest
}
