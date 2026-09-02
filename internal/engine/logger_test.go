package engine_test

import (
	"io"
	"log/slog"
)

// discardLogger keeps expected-failure tests from filling the output with the
// warnings they are asserting on.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
