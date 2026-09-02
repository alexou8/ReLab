package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/alexou8/relab/internal/telemetry"
)

// TestLogLinesCarryTheTraceID is the join between logs and traces. Without it a
// reader has to correlate by timestamp, which fails exactly when several runs
// are in flight.
func TestLogLinesCarryTheTraceID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(telemetry.WrapHandler(slog.NewJSONHandler(&buf, nil)))

	tracer := sdktrace.NewTracerProvider().Tracer("test")
	ctx, span := tracer.Start(context.Background(), "task")
	defer span.End()

	logger.InfoContext(ctx, "task started", "task", "analyze")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decode log line %q: %v", buf.String(), err)
	}
	traceID, ok := line["trace_id"].(string)
	if !ok || traceID == "" {
		t.Fatalf("log line has no trace_id: %v", line)
	}
	if traceID != span.SpanContext().TraceID().String() {
		t.Fatalf("log line carries trace_id %s, want the span's %s",
			traceID, span.SpanContext().TraceID())
	}
	if line["task"] != "analyze" {
		t.Fatalf("the wrapper lost the caller's attributes: %v", line)
	}
}

func TestLogLinesOutsideASpanHaveNoTraceID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(telemetry.WrapHandler(slog.NewJSONHandler(&buf, nil)))

	logger.InfoContext(context.Background(), "no span here")

	if strings.Contains(buf.String(), "trace_id") {
		t.Fatalf("a log line outside a span carries a trace_id: %s", buf.String())
	}
}

func TestWithAttrsSurvivesTheWrapper(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(telemetry.WrapHandler(slog.NewJSONHandler(&buf, nil))).
		With("worker_id", "w-1").WithGroup("run").With("id", "r-1")

	logger.InfoContext(context.Background(), "hello")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if line["worker_id"] != "w-1" {
		t.Fatalf("WithAttrs was lost: %v", line)
	}
	group, ok := line["run"].(map[string]any)
	if !ok || group["id"] != "r-1" {
		t.Fatalf("WithGroup was lost: %v", line)
	}
}
