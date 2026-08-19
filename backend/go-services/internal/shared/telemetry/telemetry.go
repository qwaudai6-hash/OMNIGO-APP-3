package telemetry

import (
	"context"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

// InitProvider initializes an OpenTelemetry trace provider for the given service name.
// If the collector is unreachable it degrades gracefully — traces become no-ops.
func InitProvider(serviceName string, collectorEndpoint string) (func(context.Context) error, error) {
	// Graceful skip: don't attempt to connect if no collector is configured.
	// This keeps local/edge deployments from crashing or blocking startup.
	if collectorEndpoint == "" {
		log.Printf("OpenTelemetry disabled for %s (OTEL_EXPORTER_OTLP_ENDPOINT not set)", serviceName)
		return func(context.Context) error { return nil }, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(collectorEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		log.Printf("Warning: OTEL collector unreachable at %s, traces disabled: %v", collectorEndpoint, err)
		return func(ctx context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion("1.0.0"),
		),
	)
	if err != nil {
		log.Printf("Warning: OTEL resource init failed, traces disabled: %v", err)
		return func(ctx context.Context) error { return nil }, nil
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("Initialized OpenTelemetry provider for service: %s", serviceName)

	return tracerProvider.Shutdown, nil
}
