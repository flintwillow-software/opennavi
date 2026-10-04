package inmem

import (
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/conformance"
)

// TestConformance runs the shared backend conformance suite against the
// reference backend. This is the in-repo proof that the suite runs end to
// end and that the optional ReadReporter instrumentation works; the
// durable backends (NAVI-365 `fs`, NAVI-364 `sqlite`) run the same suite.
func TestConformance(t *testing.T) {
	conformance.Run(t, func() (datastore.Backend, error) { return New(), nil })
}
