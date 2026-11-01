package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"app-maker/internal/kernel"
)

// Spec 14.3: request spans named by route, and a trace that continues
// through a job carrier.
func TestTracing(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	if _, err := Setup(context.Background(), Config{ServiceName: "test"}); err != nil {
		t.Fatal(err)
	}
	otel.SetTracerProvider(tp) // Setup installed its own provider; record with ours
	kernel.Tel = New()
	t.Cleanup(func() { kernel.Tel = New() })

	var carrier map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, r *http.Request) {
		carrier = kernel.Tel.Inject(r.Context())
	})
	h := HTTP(Route(mux))
	req := httptest.NewRequest("GET", "/v1/things/42", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	h.ServeHTTP(httptest.NewRecorder(), req)

	ctx := kernel.Tel.Extract(context.Background(), carrier)
	_, end := kernel.Tel.StartSpan(ctx, "job publish_events")
	end(nil)

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("spans: %d", len(spans))
	}
	if spans[0].Name() != "GET /v1/things/{id}" || spans[0].SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("server span %q in trace %s", spans[0].Name(), spans[0].SpanContext().TraceID())
	}
	if spans[1].SpanContext().TraceID() != spans[0].SpanContext().TraceID() || spans[1].Parent().SpanID() != spans[0].SpanContext().SpanID() {
		t.Fatal("job span does not continue the request trace")
	}
}
