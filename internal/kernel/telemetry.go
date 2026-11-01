package kernel

import "context"

// Telemetry is the tracing and metrics facade used by every layer (spec
// 14.3). platform/observability installs the OpenTelemetry implementation
// at startup; the default does nothing.
type Telemetry interface {
	// StartSpan starts a span; end records an error, if any, and ends it.
	StartSpan(ctx context.Context, name string, attrs ...Attr) (context.Context, func(err error))
	// Inject returns the trace context of ctx as a carrier for jobs and
	// events; Extract continues it.
	Inject(ctx context.Context) map[string]string
	Extract(ctx context.Context, carrier map[string]string) context.Context
	// Count adds to a counter; Record observes a histogram value.
	Count(ctx context.Context, name string, n int64, attrs ...Attr)
	Record(ctx context.Context, name string, v float64, attrs ...Attr)
}

// Attr is a span or metric attribute.
type Attr struct {
	Key   string
	Value any // string, bool, int64, int, float64
}

// A builds an attribute.
func A(key string, value any) Attr { return Attr{Key: key, Value: value} }

type noTelemetry struct{}

func (noTelemetry) StartSpan(ctx context.Context, _ string, _ ...Attr) (context.Context, func(error)) {
	return ctx, func(error) {}
}
func (noTelemetry) Inject(context.Context) map[string]string                         { return nil }
func (noTelemetry) Extract(ctx context.Context, _ map[string]string) context.Context { return ctx }
func (noTelemetry) Count(context.Context, string, int64, ...Attr)                    {}
func (noTelemetry) Record(context.Context, string, float64, ...Attr)                 {}

// Tel is the process-wide telemetry; set once at startup.
var Tel Telemetry = noTelemetry{}
