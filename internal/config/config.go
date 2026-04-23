// Package config holds runtime configuration for rivolt.
package config

// Config captures all runtime knobs. Loaded from flags + env in cmd/rivolt.
type Config struct {
	// Addr is the HTTP listen address, e.g. ":8080".
	Addr string
	// DataDir is the directory for the SQLite database and on-disk caches.
	DataDir string
}
