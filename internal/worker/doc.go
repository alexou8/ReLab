// Package worker is the process that executes tasks.
//
// A worker does four things concurrently: claims tasks up to its concurrency
// limit, executes them, renews the leases on the ones it holds, and heartbeats.
// The renewal and heartbeat loops are separate from execution on purpose — a
// handler that blocks must not stop the worker from telling the coordinator it
// is alive, because the coordinator's only remedy for silence is to take the
// work away.
//
// Nothing here decides recovery policy. A worker that loses a lease discards
// its result and moves on; what happens to the task is the reaper's decision,
// made from database state, so that it is the same whether the worker noticed
// or simply died.
package worker
