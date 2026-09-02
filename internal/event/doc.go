// Package event owns the append-only run journal.
//
// The journal is the source of truth for replay. Two properties are load
// bearing and are enforced here rather than by convention:
//
//   - Sequence numbers are gapless per run. Seq is allocated by incrementing a
//     counter on the run row inside the appending transaction, so a rolled back
//     append rolls back the number with it. A reader that sees 1..N knows it
//     has the whole history; a gap means data loss, not a concurrent writer.
//   - Every payload is versioned. Payloads carry "v", and the decoder refuses a
//     payload whose version it does not know. Replay that silently ignores a
//     field it does not understand reconstructs the wrong state, which is worse
//     than failing.
//
// Nothing in this package updates or deletes a row.
package event
