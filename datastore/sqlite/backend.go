package sqlite

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/flintwillow-software/opennavi/datastore"
)

// ParamPath is the runtime-config datastore parameter naming the database
// file. It is required: a backend with no path would have to guess where to
// write a navi's state, and guessing is worse than failing (ADR 0009's
// stance).
const ParamPath = "path"

// Candidate returns the backend descriptor. It is deliberately not the
// default: `fs` is the compiled default (ADR 0011 — markdown + frontmatter,
// human-inspectable), and `sqlite` is the single-file transactional option
// selected with `datastore.backend: sqlite`.
func Candidate() datastore.Candidate {
	return datastore.Candidate{Name: "sqlite", Open: OpenParams}
}

// CandidateAt returns the descriptor with a compiled-in path, for a
// composition root that knows its database location at declaration (from a
// flag, say) and has no runtime config file. A runtime config `path`
// parameter still wins when present: config narrows or overrides, it never
// has to restate a compiled default.
func CandidateAt(path string) datastore.Candidate {
	c := Candidate()
	inner := c.Open
	c.Open = func(p datastore.Params) (datastore.Backend, error) {
		if strings.TrimSpace(p[ParamPath]) == "" && strings.TrimSpace(path) != "" {
			p = datastore.Params{ParamPath: path}
		}
		return inner(p)
	}
	return c
}

// OpenParams builds the backend for the configured database path
// (Params["path"]), resolving the encryption cipher from the environment
// (NAVI_SESSION_KEY; absent means plaintext).
func OpenParams(p datastore.Params) (datastore.Backend, error) {
	path := strings.TrimSpace(p[ParamPath])
	if path == "" {
		return nil, fmt.Errorf("sqlite: datastore parameter %q is required (the database file)", ParamPath)
	}
	cipher, err := CipherFromEnv()
	if err != nil {
		return nil, err
	}
	return Open(path, Options{Cipher: cipher})
}

// ── the backend ─────────────────────────────────────────────────────────

var _ datastore.Backend = (*Store)(nil)

func (s *Store) Thoughts() datastore.Thoughts   { return &ThoughtStore{store: s} }
func (s *Store) ToolState() datastore.ToolState { return &StatefulStore{db: s.db} }
func (s *Store) Memory() datastore.Memory       { return &MemoryStore{store: s} }

// ResetReads clears the transcript body-read counter
// (conformance.ReadReporter).
func (s *Store) ResetReads() { atomic.StoreInt64(&s.bodyReads, 0) }

// BodyReads reports transcript body loads since the last ResetReads. Reading
// thought rows or transcript metadata is deliberately not a body read: a cold
// start must not touch a conversation (ADR 0011 decision 5).
func (s *Store) BodyReads() int { return int(atomic.LoadInt64(&s.bodyReads)) }

func (s *Store) noteBodyRead() { atomic.AddInt64(&s.bodyReads, 1) }
