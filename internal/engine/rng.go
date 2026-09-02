package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math/rand/v2"

	"github.com/google/uuid"

	"github.com/alexou8/relab/internal/store"
)

// runRNG returns a random source for a run, derived from the run's recorded
// seed.
//
// The stream is positioned by the caller through DerivedRand rather than being
// a single sequence consumed in order. A single sequence would be deterministic
// only if every draw happened in the same order every time, which is exactly
// what a distributed system does not guarantee: two workers finishing in the
// other order would swap their jitter values and the run would not reproduce.
// Deriving each draw from (seed, purpose, ordinal) makes every value a pure
// function of where it is used.
func (e *Engine) runRNG(ctx context.Context, conn store.Conn, runID uuid.UUID, parts ...string) (*rand.Rand, error) {
	var seed int64
	if err := conn.QueryRow(ctx, `SELECT seed FROM runs WHERE id = $1`, runID).Scan(&seed); err != nil {
		return nil, fmt.Errorf("read seed of run %s: %w", runID, store.Classify(err))
	}
	return DerivedRand(seed, append([]string{runID.String()}, parts...)...), nil
}

// DerivedRand returns a source seeded by the run seed together with the parts
// naming this particular draw. Callers pass enough parts to make the draw
// unique: a fault point plus a task name, a purpose plus an attempt number.
//
// The same arguments always produce the same stream, on any machine and in any
// order, which is what makes a seeded scenario reproducible in CI.
func DerivedRand(seed int64, parts ...string) *rand.Rand {
	h := fnv.New128a()
	var seedBytes [8]byte
	binary.BigEndian.PutUint64(seedBytes[:], uint64(seed))
	_, _ = h.Write(seedBytes[:])
	for _, part := range parts {
		// A length prefix keeps ("ab", "c") from hashing the same as
		// ("a", "bc"), which would silently share a stream between two
		// different draws.
		var lenBytes [4]byte
		binary.BigEndian.PutUint32(lenBytes[:], uint32(len(part)))
		_, _ = h.Write(lenBytes[:])
		_, _ = h.Write([]byte(part))
	}
	sum := h.Sum(nil)
	hi := binary.BigEndian.Uint64(sum[0:8])
	lo := binary.BigEndian.Uint64(sum[8:16])
	return rand.New(rand.NewPCG(hi, lo))
}
