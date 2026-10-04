module example.com/external-backend

go 1.26

require github.com/flintwillow-software/opennavi v0.1.0

// CI/local-dev only: this module simulates an EXTERNAL datastore backend
// repo (ADR 0004, ADR 0011). The consumer injects its own replace; the
// shipped templates never carry one.
replace github.com/flintwillow-software/opennavi => ../../..
