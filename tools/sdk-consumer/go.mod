module consumer.example/navi-consumer

go 1.26

require github.com/flintwillow-software/opennavi v0.1.0

// CI/local-dev only: this module simulates an EXTERNAL repo consuming
// the public SDK surface (S000800). The replace is injected here, in
// the consumer's own go.mod — the shipped templates never carry one.
replace github.com/flintwillow-software/opennavi => ../..
