// Package engine owns run and task state in the database.
//
// Everything that changes a run — creating it, claiming a task, recording an
// outcome, deciding a run is finished — goes through here, and every one of
// those changes writes its events in the same transaction as the state it
// describes. That coupling is the reason the event journal can be trusted as a
// record of what happened rather than as a log of what someone remembered to
// write down.
//
// The engine does not execute handlers. `relab run` executes them in process
// and package worker executes them across processes; both drive the same
// operations here, so the recorded history of a single-process run and a
// distributed one have the same shape.
package engine
