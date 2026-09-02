package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	// The semconv version must match the one resource.Default() was built with,
	// or resource.Merge refuses the merge with a schema URL conflict. It is
	// pinned to the SDK's, and moves when the SDK does.
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// ServiceName identifies ReLab in a trace backend.
const ServiceName = "relab"

// Config controls what telemetry is produced and where it goes.
type Config struct {
	// ServiceVersion is the binary's version, recorded on every span.
	ServiceVersion string
	// Component distinguishes the control plane from a worker.
	Component string
	// OTLPEndpoint, when set, sends traces and metrics there. Empty means the
	// stdout exporter.
	OTLPEndpoint string
	// TraceOutput is where the stdout exporter writes. Nil means io.Discard:
	// span dumps on stdout are extremely noisy, and a developer who wants them
	// asks for them.
	TraceOutput io.Writer
	// MetricInterval is how often metrics are exported.
	MetricInterval time.Duration
}

// Providers holds what Setup created, so it can be shut down.
type Providers struct {
	tracer  *sdktrace.TracerProvider
	metrics *sdkmetric.MeterProvider
}

// Setup installs global trace and meter providers and returns them for
// shutdown. It is safe to call with a zero Config.
func Setup(ctx context.Context, cfg Config) (*Providers, error) {
	if cfg.MetricInterval <= 0 {
		cfg.MetricInterval = 30 * time.Second
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
		attribute.String("relab.component", cfg.Component),
	))
	if err != nil {
		return nil, fmt.Errorf("telemetry: build resource: %w", err)
	}

	tracerProvider, err := newTracerProvider(ctx, cfg, res)
	if err != nil {
		return nil, err
	}
	meterProvider, err := newMeterProvider(ctx, cfg, res)
	if err != nil {
		// The tracer is already installed; shut it down rather than leaking its
		// background exporter goroutine.
		_ = tracerProvider.Shutdown(context.WithoutCancel(ctx))
		return nil, err
	}

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	// Propagating W3C trace context is what lets a trace started by the API
	// continue into the worker that executes the task.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	// An exporter that cannot reach its collector must not take the process
	// down or fill the logs. Telemetry is diagnostic; the workflows are the job.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Debug("telemetry error", "error", err)
	}))

	return &Providers{tracer: tracerProvider, metrics: meterProvider}, nil
}

func newTracerProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	var exporter sdktrace.SpanExporter
	var err error
	if cfg.OTLPEndpoint != "" {
		exporter, err = otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
			// Insecure because the intended deployment is a developer's machine
			// or a CI runner talking to a collector on the same host.
			// SECURITY.md says so rather than implying TLS that is not there.
			otlptracegrpc.WithInsecure())
		if err != nil {
			return nil, fmt.Errorf("telemetry: create otlp trace exporter: %w", err)
		}
	} else {
		out := cfg.TraceOutput
		if out == nil {
			out = io.Discard
		}
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(out))
		if err != nil {
			return nil, fmt.Errorf("telemetry: create stdout trace exporter: %w", err)
		}
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	), nil
}

func newMeterProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	var reader sdkmetric.Reader
	if cfg.OTLPEndpoint != "" {
		exporter, err := otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpoint(cfg.OTLPEndpoint),
			otlpmetricgrpc.WithInsecure())
		if err != nil {
			return nil, fmt.Errorf("telemetry: create otlp metric exporter: %w", err)
		}
		reader = sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(cfg.MetricInterval))
	} else {
		exporter, err := stdoutmetric.New(stdoutmetric.WithWriter(io.Discard))
		if err != nil {
			return nil, fmt.Errorf("telemetry: create stdout metric exporter: %w", err)
		}
		reader = sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(cfg.MetricInterval))
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	), nil
}

// Shutdown flushes and stops both providers. It is safe on a nil receiver, so
// a caller that failed to set up telemetry can still defer it.
func (p *Providers) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	// A bounded, detached context: shutdown usually runs while the process's
	// own context is already cancelled, and an unbounded flush would hang a
	// terminating worker.
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	var errs []error
	if p.tracer != nil {
		if err := p.tracer.Shutdown(flushCtx); err != nil {
			errs = append(errs, fmt.Errorf("shut down tracer: %w", err))
		}
	}
	if p.metrics != nil {
		if err := p.metrics.Shutdown(flushCtx); err != nil {
			errs = append(errs, fmt.Errorf("shut down meter: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("telemetry: %w", errors.Join(errs...))
	}
	return nil
}

// Tracer returns the tracer every package in this repository uses.
func Tracer() trace.Tracer { return otel.Tracer("github.com/alexou8/relab") }

// ConfigFromEnv builds a Config from the environment.
func ConfigFromEnv(component, version string) Config {
	cfg := Config{
		ServiceVersion: version,
		Component:      component,
		OTLPEndpoint:   os.Getenv("RELAB_OTLP_ENDPOINT"),
	}
	// Span dumps on stdout are unreadable next to the ordinary logs, so they
	// are opt-in even when the stdout exporter is the one in use.
	if os.Getenv("RELAB_TRACE_STDOUT") != "" {
		cfg.TraceOutput = os.Stderr
	}
	return cfg
}

// otelMeter returns the meter every instrument in this repository is created
// from.
func otelMeter() metric.Meter { return otel.Meter("github.com/alexou8/relab") }
