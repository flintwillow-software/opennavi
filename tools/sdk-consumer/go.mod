module consumer.example/navi-consumer

go 1.26

require github.com/flintwillow-software/opennavi v0.1.0

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.58.0 // indirect
)

// CI/local-dev only: this module simulates an EXTERNAL repo consuming
// the public SDK surface (S000800). The replace is injected here, in
// the consumer's own go.mod — the shipped templates never carry one.
replace github.com/flintwillow-software/opennavi => ../..
