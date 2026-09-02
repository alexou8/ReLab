// Package workflow parses and validates workflow definitions.
//
// A definition is a DAG of named steps. Validation is deliberately strict and
// happens once, at registration, rather than being discovered halfway through a
// run: a workflow that references a handler nobody registered, or that contains
// a cycle, fails at `relab workflow register` where a human is watching, not at
// task 7 of 12 where the only evidence is a stuck run.
//
// This package has no dependencies on the database or the scheduler. It turns
// bytes into a validated Definition, and answers questions about the shape of
// that graph.
package workflow
