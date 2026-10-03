package navi

import (
	"fmt"
	"strings"

	"github.com/flintwillow-software/opennavi/datastore"
)

// DatastoreCollection is the navi's datastore candidate set: the compiled
// backends declared at build time (ADR 0011 decision 3), plus the stable
// handle consumers take views from.
//
//	n.Datastores.Add(inmem.Candidate())
//	ds := n.Datastores.Handle()
//	n.Tools.Add(todo.Tools(naviID, ds.Stateful())...)
//
// Which candidate is active is a runtime config decision, made at
// assembly. The handle is stable across that decision, so a component
// declaring a view at build time keeps working when the backend changes.
type DatastoreCollection struct {
	*Collection[datastore.Candidate]
	handle *datastore.Datastore
}

func newDatastoreCollection() *DatastoreCollection {
	return &DatastoreCollection{
		Collection: newCollection("datastore",
			func(c datastore.Candidate) string { return c.Name },
			validateDatastoreCandidate),
		handle: datastore.NewDatastore(),
	}
}

// Handle returns the stable view handle. A consumer takes one view per
// aggregate from it — typically at declaration, before Run.
func (d *DatastoreCollection) Handle() *datastore.Datastore { return d.handle }

// StartToolStateFlusher starts the RAM-first tool-state flusher (a no-op
// when no backend is bound). The runtime calls it at serve and the stop
// function at shutdown.
func (d *DatastoreCollection) StartToolStateFlusher() func() {
	return d.handle.StartToolStateFlusher()
}

// activate resolves the compiled candidates against runtime config, opens
// the active backend, and binds the handle. It runs at assembly, after
// validation: registration validates shape, assembly validates binding
// (ADR 0011 decision 4).
func (d *DatastoreCollection) activate(cfg datastore.Config) error {
	candidates := d.All()
	if len(candidates) == 0 {
		if strings.TrimSpace(cfg.Backend) != "" {
			return fmt.Errorf("%w: datastore: config names backend %q but no candidates are compiled in",
				ErrInvalid, cfg.Backend)
		}
		if d.handle.Claimed() {
			return fmt.Errorf("%w: datastore: a consumer requires a datastore but no candidates are declared",
				ErrInvalid)
		}
		return nil
	}

	candidate, err := datastore.Resolve(candidates, cfg)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	backend, err := candidate.Open(cfg.Params)
	if err != nil {
		return fmt.Errorf("navi: datastore %q: open: %w", candidate.Name, err)
	}
	d.handle.Bind(backend)
	return nil
}

func validateDatastoreCandidate(c datastore.Candidate) error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("%w: datastore candidate requires a name", ErrInvalid)
	}
	if c.Open == nil {
		return fmt.Errorf("%w: datastore candidate %q requires an Open func", ErrInvalid, c.Name)
	}
	return nil
}
