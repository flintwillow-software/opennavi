module example.com/external-component

go 1.26

require github.com/flintwillow-software/opennavi v0.1.0

// CI/local-dev only: this module simulates an EXTERNAL component repo
// (ADR 0004). The consumer injects its own replace; the shipped
// templates never carry one.
replace github.com/flintwillow-software/opennavi => ../..
