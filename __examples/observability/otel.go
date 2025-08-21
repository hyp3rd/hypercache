package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/pkg/middleware"
)

// This example shows how to wrap HyperCache with OpenTelemetry middleware.
func main() {
	cache, err := hypercache.NewInMemoryWithDefaults(16)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	// Build a service from the cache to apply middleware.
	svc := hypercache.Service(cache)

	// Use noop providers for a minimal example. Replace with real SDK providers in production.
	meter := noop.NewMeterProvider().Meter("hypercache/examples")
	tracer := trace.NewNoopTracerProvider().Tracer("hypercache/examples")

	// Apply OTel tracing and metrics middleware.
	svc = hypercache.ApplyMiddleware(svc,
		func(next hypercache.Service) hypercache.Service {
			return middleware.NewOTelTracingMiddleware(next, tracer, middleware.WithCommonAttributes(
				attribute.String("component", "hypercache"),
			))
		},
		func(next hypercache.Service) hypercache.Service {
			mw, _ := middleware.NewOTelMetricsMiddleware(next, meter)
			return mw
		},
	)
	defer svc.Stop()

	_ = svc.Set(context.Background(), "key", "value", time.Minute)
	if v, ok := svc.Get(context.Background(), "key"); ok {
		fmt.Println("got:", v)
	}
}
