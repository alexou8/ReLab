// Package api serves the read surface over runs, tasks, events and workers.
//
// The API is read-mostly by design. Everything that changes state goes through
// package engine, which the CLI also calls, so there is exactly one
// implementation of each state change rather than one per entry point.
//
// Read routes accept role-bearing bearer tokens. Probes stay unauthenticated,
// and config rejects an unauthenticated non-loopback listener unless the
// operator explicitly opts out of that boundary.
package api
