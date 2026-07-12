// Package telemetry is the shared observability wiring for every Bravo Dispatch service (ADR-077 Layer 1 /
// P14).
//
// It runs the OpenTelemetry SDK so each inbound request opens a server span exported to the platform OTLP
// collector (OTEL_EXPORTER_OTLP_ENDPOINT, injected by the OTel Operator via the pod's inject-sdk annotation),
// a MeterProvider on the same endpoint so otelhttp also emits RED metrics (http.server.request.duration →
// http_server_request_duration_seconds in Mimir — the series the ADR-056 canary metric-gate and the Bravo
// Dispatch dashboards query), and the Pyroscope SDK for continuous profiling (PYROSCOPE_SERVER_ADDRESS) with
// per-span flame graphs (trace→profiles). Logs are structured JSON via slog with the active span's
// trace_id/span_id stamped on every line, so a log in Loki links straight to its trace in Tempo. Factored
// into one package because Bravo Dispatch is a fleet of services (tracker, shipments, …) that all want the
// identical, correct wiring.
//
// Everything degrades cleanly when the endpoints are unset (local dev / tests): traces/profiles become no-ops.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"

	otelpyroscope "github.com/grafana/otel-profiling-go"
	"github.com/grafana/pyroscope-go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Logger is the process-wide structured logger. Its traceHandler stamps the active span's trace/span IDs onto
// each record (the trace_id the Loki→Tempo derived field links on); a no-op when there's no active span.
var Logger = slog.New(traceHandler{slog.NewJSONHandler(os.Stdout, nil)})

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

// Setup wires the global tracer provider (+ W3C propagator + Pyroscope trace→profiles link) and starts the
// Pyroscope profiler, and installs Logger as slog's default. serviceName is the fallback OTEL_SERVICE_NAME for
// local runs (in-cluster the OTel Operator injects it). The returned shutdown flushes the trace batch + stops
// the profiler; it is safe to call once on exit. Never fatal — a failed exporter/profiler just disables that
// signal so the service still serves.
func Setup(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	slog.SetDefault(Logger)

	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return func(context.Context) error { return nil }, fmt.Errorf("otlp exporter: %w", err)
	}
	res, err := resource.New(ctx,
		resource.WithFromEnv(), // OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES (set by the manifest in-cluster)
		resource.WithAttributes(semconv.ServiceName(getenv("OTEL_SERVICE_NAME", serviceName))),
	)
	if err != nil {
		return func(context.Context) error { return nil }, fmt.Errorf("otel resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	// otelpyroscope stamps a `pyroscope.profile.id` on each span — the key Grafana links trace→profiles on.
	otel.SetTracerProvider(otelpyroscope.NewTracerProvider(tp))
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	// Metrics on the same OTLP endpoint: a MeterProvider makes otelhttp emit RED metrics
	// (http.server.request.duration + counts) exported to the collector → Mimir. Without it the SDK uses a
	// no-op meter and the canary metric-gate / dashboards have no data. Same graceful degradation as traces —
	// an unset/unreachable endpoint just fails the periodic export; the service still serves.
	metricExp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return func(context.Context) error { return tp.Shutdown(ctx) }, fmt.Errorf("otlp metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
	)
	otel.SetMeterProvider(mp)

	profiler := startProfiler(serviceName)

	return func(ctx context.Context) error {
		if profiler != nil {
			_ = profiler.Stop()
		}
		// Flush both signals; return the first error but always attempt both shutdowns.
		errMP := mp.Shutdown(ctx)
		errTP := tp.Shutdown(ctx)
		if errTP != nil {
			return errTP
		}
		return errMP
	}, nil
}

// startProfiler starts continuous profiling (full Go profile suite) when PYROSCOPE_SERVER_ADDRESS is set;
// a no-op otherwise. Paired with the otelpyroscope tracer above, each span links to its flame graph.
func startProfiler(serviceName string) *pyroscope.Profiler {
	addr := os.Getenv("PYROSCOPE_SERVER_ADDRESS")
	if addr == "" {
		return nil
	}
	// Mutex/block profiles are off by default in the Go runtime — enable light sampling so Pyroscope has them.
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(5)
	p, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: getenv("OTEL_SERVICE_NAME", serviceName),
		ServerAddress:   addr,
		Logger:          nil,
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU, pyroscope.ProfileAllocObjects, pyroscope.ProfileAllocSpace,
			pyroscope.ProfileInuseObjects, pyroscope.ProfileInuseSpace, pyroscope.ProfileGoroutines,
			pyroscope.ProfileMutexCount, pyroscope.ProfileMutexDuration,
			pyroscope.ProfileBlockCount, pyroscope.ProfileBlockDuration,
		},
	})
	if err != nil {
		Logger.Error("pyroscope start failed; continuing without profiling", "err", err)
		return nil
	}
	return p
}

// WrapHandler opens an otelhttp server span per request (and puts the trace in the request context handlers
// log with). spanName is the root span-name formatter (e.g. "http.server").
func WrapHandler(h http.Handler, spanName string) http.Handler {
	return otelhttp.NewHandler(h, spanName)
}

// Client returns an *http.Client whose Transport propagates the active trace to the downstream service
// (W3C traceparent), so an east-west call (tracker→shipments) shows up as one connected trace.
func Client() *http.Client {
	return &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
