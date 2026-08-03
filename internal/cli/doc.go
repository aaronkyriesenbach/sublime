// Package cli wires Sublime's subcommands: the daemon entrypoint (serve)
// and thin HTTP clients (status, reprocess, libraries) that talk to it over
// the API in internal/api. It is imported by cmd/sublime and exercised
// directly in tests, without needing to exec the built binary.
package cli
