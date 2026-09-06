// Package defaults holds the baked-in default configuration values
// (build-time layer of the merge chain). Runtime defaults live in
// config.DefaultRuntimeConfig; this file anchors the build-time
// constants that future builders (#140) will override.
package defaults

// Build-time defaults baked into the binary. The builder (#140) will
// generate this file's values from the navi definition.
const (
	// DefaultSessionDBPath is the default session/memory database.
	DefaultSessionDBPath = "./navi.db"

	// DefaultOllamaHost when $OLLAMA_HOST is unset.
	DefaultOllamaHost = "http://localhost:11434"

	// DefaultEmbedModel is the local embedding model.
	DefaultEmbedModel = "nomic-embed-text"

	// DefaultPrimaryProvider when config does not specify one.
	DefaultPrimaryProvider = "ollama"
)
