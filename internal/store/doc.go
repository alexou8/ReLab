// Package store owns every connection to PostgreSQL.
//
// It provides three things and nothing else: a configured pool, a transaction
// helper with correct rollback semantics, and the translation of driver errors
// into the typed errors declared in errors.go. Query construction lives in the
// packages that own the tables — store deliberately does not grow a method per
// query, which would make it the single writer for the whole system.
//
// All SQL in this repository is hand written and parameterised. There is no
// ORM and no query builder: the reliability behaviour under test depends on
// exactly which rows are locked and in what order, which a generated query
// cannot be trusted to preserve.
package store
