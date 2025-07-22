package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

const (
	serviceName      = "dice-cli"
	serviceVersion   = "v1.0.0"
	otelCollectorURL = "localhost:4318"
	name             = "github.com/ZiadMansour/bastet/o11y/dice-cli"
)

var (
	meter  = otel.Meter(name)
	tracer = otel.Tracer(name)
	logger = otelslog.NewLogger(name)

	clientReqCounter metric.Int64Counter
	clientLatency    metric.Float64Histogram
	clientErrorCount metric.Int64Counter
)

func init() {
	var err error

	clientReqCounter, err = meter.Int64Counter("client_requests_total",
		metric.WithDescription("Total number of outgoing HTTP requests"),
	)
	if err != nil {
		panic(err)
	}

	clientLatency, err = meter.Float64Histogram("client_request_latency_seconds",
		metric.WithDescription("Latency of outgoing HTTP requests in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(err)
	}

	clientErrorCount, err = meter.Int64Counter("client_request_errors_total",
		metric.WithDescription("Total number of failed outgoing HTTP requests"),
	)
	if err != nil {
		panic(err)
	}
}

func main() {
	// Handle SIGINT (CTRL+C) gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Set up OpenTelemetry.
	otelShutdown, err := setupOTelSDK(ctx)
	if err != nil {
		return
	}
	// Handle shutdown properly so nothing leaks.
	defer func() {
		err = errors.Join(err, otelShutdown(context.Background()))
	}()

	// Create an instrumented HTTP client
	client := http.Client{
		// Transport: otelhttp.NewTransport(http.DefaultTransport),
		Transport: ApplyMiddleware(http.DefaultTransport, clientInstrumentationMiddleware),
	}

	// Make 5 requests to the dice server
	for i := 0; i < 5; i++ {
		callDiceServer(context.Background(), &client)
		// time.Sleep(500 * time.Millisecond) // Add delay between calls to visualize traces more clearly
	}
}

func callDiceServer(ctx context.Context, client *http.Client) {
	// Start a span for the client request
	ctx, span := tracer.Start(ctx, "CallDiceServer")
	defer span.End()

	// Make the HTTP request
	req, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:3030/rolldice/", nil)
	if err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "Failed to create request", "error", err)
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Request failed")
		logger.ErrorContext(ctx, "Request failed", "error", err)
		return
	}
	defer resp.Body.Close()

	// Read and print the response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "Failed to read response", "error", err)
		return
	}

	logger.InfoContext(ctx, fmt.Sprintf("Response: %s", body))
}

// Middleware function type
type ClientMiddleware func(http.RoundTripper) http.RoundTripper

// ApplyMiddleware applies middleware in sequence for HTTP client
func ApplyMiddleware(rt http.RoundTripper, middlewares ...ClientMiddleware) http.RoundTripper {
	for i := len(middlewares) - 1; i >= 0; i-- {
		rt = middlewares[i](rt)
	}
	return rt
}

// === Single Middleware for OpenTelemetry, Metrics, and Logging ===
func clientInstrumentationMiddleware(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		start := time.Now()
		ctx, span := tracer.Start(req.Context(), fmt.Sprintf("Client HTTP %s %s", req.Method, req.URL.Path))
		defer span.End()

		// Inject the current trace context into the outgoing request headers.
		otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

		// Increment request count
		clientReqCounter.Add(ctx, 1)

		logger.InfoContext(ctx, "Sending HTTP request",
			"method", req.Method,
			"url", req.URL.String(),
		)

		resp, err := next.RoundTrip(req)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "HTTP request failed")
			clientErrorCount.Add(ctx, 1, metric.WithAttributes(attribute.String("error.type", err.Error())))
			logger.ErrorContext(ctx, "HTTP request failed", "error", err)
			return nil, err
		}

		// Measure request latency
		elapsedTime := time.Since(start).Seconds()
		clientLatency.Record(ctx, elapsedTime)

		logger.InfoContext(ctx, "Received HTTP response",
			"status", resp.StatusCode,
			"elapsed_time", elapsedTime,
		)

		return resp, nil
	})
}

// Helper type to implement RoundTripper interface
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// setupOTelSDK bootstraps the OpenTelemetry pipeline.
// If it does not return an error, make sure to call shutdown for proper cleanup.
func setupOTelSDK(ctx context.Context) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// The errors from the calls are joined.
	// Each registered cleanup will be invoked once.
	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	// handleErr calls shutdown for cleanup and makes sure that all errors are returned.
	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	// Set up propagator.
	prop := newPropagator()
	otel.SetTextMapPropagator(prop)

	// Set up trace provider.
	tracerProvider, err := newTraceProvider(ctx)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// Set up meter provider.
	meterProvider, err := newMeterProvider(ctx)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	// Set up logger provider.
	loggerProvider, err := newLoggerProvider()
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, loggerProvider.Shutdown)
	global.SetLoggerProvider(loggerProvider)

	return
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func newTraceProvider(ctx context.Context) (*trace.TracerProvider, error) {
	// ---> INPUT: context.Context
	// [1]: Exporter
	// [2]: Resource
	// [3]: TracerProvider
	// ---> OUTPUT: (*trace.TracerProvider, error)
	traceExporter, err := otlptracehttp.New(
		ctx,
		otlptracehttp.WithEndpoint(otelCollectorURL),
		otlptracehttp.WithInsecure(),
	)

	if err != nil {
		return nil, err
	}

	traceProvider := trace.NewTracerProvider(
		trace.WithBatcher(
			traceExporter,
			trace.WithBatchTimeout(time.Second),
		),
		trace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		)),
	)
	return traceProvider, nil
}

func newMeterProvider(ctx context.Context) (*sdkmetric.MeterProvider, error) {
	// metricExporter, err := stdoutmetric.New()
	metricExporter, err := otlpmetrichttp.New(
		ctx,
		otlpmetrichttp.WithEndpoint(otelCollectorURL),
		otlpmetrichttp.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			metricExporter,
			// Default is 1m. Set to 3s for demonstrative purposes.
			sdkmetric.WithInterval(3*time.Second),
		)),
		sdkmetric.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		)),
		// sdkmetric.WithExemplarFilter(exemplar.TraceBasedFilter),
	)
	return meterProvider, nil
}

func newLoggerProvider() (*log.LoggerProvider, error) {
	logExporter, err := otlploghttp.New(
		context.Background(),
		otlploghttp.WithEndpoint(otelCollectorURL),
		otlploghttp.WithInsecure(),
	)
	// logExporter, err := stdoutlog.New()
	if err != nil {
		return nil, err
	}

	loggerProvider := log.NewLoggerProvider(
		log.WithProcessor(log.NewBatchProcessor(logExporter)),
		log.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		)),
	)
	return loggerProvider, nil
}
