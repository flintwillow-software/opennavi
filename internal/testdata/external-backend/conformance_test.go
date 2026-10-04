package externalbackend

import (
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/conformance"
)

// TestConformance is NAVI-376's acceptance in executable form: a backend
// module outside the repository imports the contract's shared suite and
// runs it against its own implementation — no runtime internals, no in-repo
// backend, nothing but the public contract. If the suite were a private
// helper or demanded knowledge of `datastore/inmem`, this would not build.
func TestConformance(t *testing.T) {
	conformance.Run(t, func() (datastore.Backend, error) { return New(), nil })
}
