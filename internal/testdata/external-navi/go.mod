module example.com/external-navi

go 1.26

require (
	example.com/external-component v0.0.0
	github.com/flintwillow-software/opennavi v0.1.0
)

require gopkg.in/yaml.v3 v3.0.1 // indirect

// CI/local-dev only: this module simulates an EXTERNAL navi repo that
// composes the public root and a third-party component. Replaces are
// injected by the consumer, exactly as a downstream repo would.
replace github.com/flintwillow-software/opennavi => ../../..

replace example.com/external-component => ../external-component
