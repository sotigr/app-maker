// Package observability wires OpenTelemetry (spec 14.3): traces and
// metrics over OTLP/HTTP, the kernel.Telemetry facade used by every layer,
// HTTP server spans with RED metrics, and trace IDs in log records.
package observability

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"organon/internal/kernel"
)

// Config selects the exporters.
type Config struct {
	// Endpoint is the OTLP/HTTP collector ("http://localhost:4318");
	// empty also honours OTEL_EXPORTER_OTLP_ENDPOINT, otherwise telemetry
	// stays in-process (spans and metrics are created but not exported).
	Endpoint       string
	ServiceName    string
	ServiceVersion string
	SampleRatio    float64 // default 1
}

const scope = "organon"

// Setup installs the providers and kernel.Tel; shutdown flushes them.
func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", cfg.ServiceName), attribute.String("service.version", cfg.ServiceVersion)))
	if err != nil {
		return nil, err
	}
	ratio := cfg.SampleRatio
	if ratio <= 0 {
		ratio = 1
	}
	topts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio)))}
	mopts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if cfg.Endpoint != "" || os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		var to []otlptracehttp.Option
		var mo []otlpmetrichttp.Option
		if cfg.Endpoint != "" {
			to = append(to, otlptracehttp.WithEndpointURL(cfg.Endpoint))
			mo = append(mo, otlpmetrichttp.WithEndpointURL(cfg.Endpoint))
		}
		te, err := otlptracehttp.New(ctx, to...)
		if err != nil {
			return nil, err
		}
		me, err := otlpmetrichttp.New(ctx, mo...)
		if err != nil {
			return nil, err
		}
		topts = append(topts, sdktrace.WithBatcher(te))
		mopts = append(mopts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me, sdkmetric.WithInterval(30*time.Second))))
	}
	tp := sdktrace.NewTracerProvider(topts...)
	mp := sdkmetric.NewMeterProvider(mopts...)
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	kernel.Tel = New()
	return func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}, nil
}

// Tel implements kernel.Telemetry with the global OpenTelemetry providers.
type Tel struct {
	tracer trace.Tracer
	meter  metric.Meter
	mu     sync.Mutex
	counts map[string]metric.Int64Counter
	hists  map[string]metric.Float64Histogram
}

var _ kernel.Telemetry = (*Tel)(nil)

// New returns a Telemetry on the current global providers.
func New() *Tel {
	return &Tel{tracer: otel.Tracer(scope), meter: otel.Meter(scope),
		counts: map[string]metric.Int64Counter{}, hists: map[string]metric.Float64Histogram{}}
}

func attrs(as []kernel.Attr) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(as))
	for _, a := range as {
		switch v := a.Value.(type) {
		case string:
			out = append(out, attribute.String(a.Key, v))
		case bool:
			out = append(out, attribute.Bool(a.Key, v))
		case int:
			out = append(out, attribute.Int(a.Key, v))
		case int64:
			out = append(out, attribute.Int64(a.Key, v))
		case float64:
			out = append(out, attribute.Float64(a.Key, v))
		default:
			out = append(out, attribute.String(a.Key, "?"))
		}
	}
	return out
}

// StartSpan implements kernel.Telemetry.
func (t *Tel) StartSpan(ctx context.Context, name string, as ...kernel.Attr) (context.Context, func(error)) {
	ctx, span := t.tracer.Start(ctx, name, trace.WithAttributes(attrs(as)...))
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// Inject implements kernel.Telemetry.
func (t *Tel) Inject(ctx context.Context) map[string]string {
	c := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, c)
	if len(c) == 0 {
		return nil
	}
	return c
}

// Extract implements kernel.Telemetry.
func (t *Tel) Extract(ctx context.Context, carrier map[string]string) context.Context {
	if len(carrier) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(carrier))
}

// Count implements kernel.Telemetry.
func (t *Tel) Count(ctx context.Context, name string, n int64, as ...kernel.Attr) {
	t.mu.Lock()
	c, ok := t.counts[name]
	if !ok {
		c, _ = t.meter.Int64Counter(name)
		t.counts[name] = c
	}
	t.mu.Unlock()
	if c != nil {
		c.Add(ctx, n, metric.WithAttributes(attrs(as)...))
	}
}

// Record implements kernel.Telemetry.
func (t *Tel) Record(ctx context.Context, name string, v float64, as ...kernel.Attr) {
	t.mu.Lock()
	h, ok := t.hists[name]
	if !ok {
		h, _ = t.meter.Float64Histogram(name)
		t.hists[name] = h
	}
	t.mu.Unlock()
	if h != nil {
		h.Record(ctx, v, metric.WithAttributes(attrs(as)...))
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(s int) { w.status = s; w.ResponseWriter.WriteHeader(s) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Hijack supports WebSocket upgrades.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

// HTTP wraps a handler with a server span per request (continuing an
// incoming traceparent) and RED metrics per route pattern.
func HTTP(next http.Handler) http.Handler {
	tracer := otel.Tracer(scope)
	meter := otel.Meter(scope)
	dur, _ := meter.Float64Histogram("http.server.request.duration", metric.WithUnit("s"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracer.Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method)))
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		var route string
		r = r.WithContext(context.WithValue(ctx, routeKey{}, &route))
		next.ServeHTTP(sw, r)
		if route == "" {
			route = "unmatched"
		}
		span.SetName(route)
		span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.response.status_code", sw.status))
		if sw.status >= 500 {
			span.SetStatus(codes.Error, strconv.Itoa(sw.status))
		}
		span.End()
		dur.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("http.request.method", r.Method),
			attribute.String("http.route", route), attribute.Int("http.response.status_code", sw.status)))
	})
}

type routeKey struct{}

// Route wraps the ServeMux so HTTP learns the matched route pattern (the
// mux sets it on the request copy it receives).
func Route(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		if p, ok := r.Context().Value(routeKey{}).(*string); ok {
			*p = r.Pattern
		}
	})
}

// LogHandler adds trace_id and span_id to log records with a span.
func LogHandler(h slog.Handler) slog.Handler { return logHandler{h} }

type logHandler struct{ slog.Handler }

func (l logHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return l.Handler.Handle(ctx, r)
}

func (l logHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return logHandler{l.Handler.WithAttrs(as)}
}
func (l logHandler) WithGroup(n string) slog.Handler { return logHandler{l.Handler.WithGroup(n)} }
