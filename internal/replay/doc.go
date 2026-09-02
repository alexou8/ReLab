// Package replay reconstructs run state from the event journal.
//
// The reducer is a pure function: events in, state out, with no I/O, no clock
// access and no randomness. That is not a stylistic preference. A reducer that
// could read the database would be able to paper over a journal that does not
// actually explain the run, and the entire claim of this project is that the
// journal does explain the run.
//
// What replay reconstructs is *logical* state: which tasks ran, how many
// attempts each took, what they produced, which faults fired, how the run
// ended. It does not re-execute handlers, so it cannot reconstruct anything a
// handler did that was not recorded. The README says this in the Limitations
// section, above the benchmarks, because it is the single most important thing
// to be clear about.
package replay
