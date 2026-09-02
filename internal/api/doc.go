// Package api serves the read surface over runs, tasks, events and workers, and
// the small number of write endpoints the dashboard and CI need.
//
// The API is read-mostly by design. Everything that changes state goes through
// package engine, which the CLI also calls, so there is exactly one
// implementation of each state change rather than one per entry point.
//
// There is no authentication in v1. The intended deployment is a developer's
// machine or a CI runner on a private network, and `SECURITY.md` says so
// explicitly rather than implying safety the code does not provide.
package api
