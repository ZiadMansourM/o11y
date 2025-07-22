package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
)

// initializeTelemetry initializes the telemetry components after OTEL SDK setup
func initializeTelemetry() error {
	var err error

	// Initialize meter, tracer, and logger
	meter = otel.Meter(name)
	tracer = otel.Tracer(name)
	logger = otelslog.NewLogger(name)

	rollCnt, err = meter.Int64Counter("dice.rolls",
		metric.WithDescription("The number of rolls by roll value"),
		metric.WithUnit("{roll}"))
	// rollCnt, err = meter.Int64Histogram("dice.rolls",
	// 	metric.WithDescription("The frequency distribution of dice roll values"),
	// 	metric.WithUnit("{roll}"))
	if err != nil {
		return err
	}

	requestCounter, err = meter.Int64Counter("http_requests_total",
		metric.WithDescription("Total number of incoming HTTP requests"),
	)
	if err != nil {
		return err
	}

	requestDuration, err = meter.Float64Histogram("http_request_duration_seconds",
		metric.WithDescription("Duration of HTTP requests in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	requestSize, err = meter.Int64Histogram("http_request_size_bytes",
		metric.WithDescription("Size of incoming HTTP requests in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return err
	}

	responseSize, err = meter.Int64Histogram("http_response_size_bytes",
		metric.WithDescription("Size of outgoing HTTP responses in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalln(err)
	}
}

func run() (err error) {
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

	// Initialize telemetry components after OTEL SDK setup
	err = initializeTelemetry()
	if err != nil {
		return
	}

	// Start HTTP server.
	srv := &http.Server{
		Addr:         "0.0.0.0:3030",
		BaseContext:  func(_ net.Listener) context.Context { return ctx },
		ReadTimeout:  time.Second,
		WriteTimeout: 10 * time.Second,
		Handler:      newHTTPHandler(),
	}
	srvErr := make(chan error, 1)
	go func() {
		srvErr <- srv.ListenAndServe()
	}()

	// Wait for interruption.
	select {
	case err = <-srvErr:
		// Error when starting HTTP server.
		return
	case <-ctx.Done():
		// Wait for first CTRL+C.
		// Stop receiving signal notifications as soon as possible.
		stop()
	}

	// When Shutdown is called, ListenAndServe immediately returns ErrServerClosed.
	err = srv.Shutdown(context.Background())
	return
}

func newHTTPHandler() http.Handler {
	mux := http.NewServeMux()

	// handleFunc is a replacement for mux.HandleFunc
	// which enriches the handler's HTTP instrumentation with the pattern as the http.route.
	// handleFunc := func(pattern string, handlerFunc func(http.ResponseWriter, *http.Request)) {
	// 	// Configure the "http.route" for the HTTP instrumentation.
	// 	handler := otelhttp.WithRouteTag(pattern, http.HandlerFunc(handlerFunc))
	// 	mux.Handle(pattern, handler)
	// }

	// Register handlers.
	mux.Handle("GET /rolldice/", ApplyMiddleware(
		http.HandlerFunc(rolldice),
		instrumentationMiddleware,
	))
	// mux.HandleFunc("GET /rolldice/{player}", rolldice)

	// httpSpanName := func(operation string, r *http.Request) string {
	// 	return fmt.Sprintf("HTTP %s %s", r.Method, r.URL.Path)
	// }

	// // Add HTTP instrumentation for the whole server.
	// handler := otelhttp.NewHandler(
	// 	mux,
	// 	"/",
	// 	otelhttp.WithSpanNameFormatter(httpSpanName),
	// )
	// return handler

	return mux
}

// Middleware function type
type Middleware func(http.Handler) http.Handler

// ApplyMiddleware applies a chain of middleware to a handler.
func ApplyMiddleware(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// === Single Middleware for OpenTelemetry, Metrics, and Logging ===
func instrumentationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// *** Extract the trace context from the incoming request headers ***
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		// Start OpenTelemetry span
		ctx, span := tracer.Start(ctx, fmt.Sprintf("HTTP %s %s", r.Method, r.URL.Path))
		defer span.End()

		// Track request count and size
		requestCounter.Add(ctx, 1)
		requestSize.Record(ctx, r.ContentLength)

		// Logging before request processing
		logger.InfoContext(ctx, "Got new HTTP request", "method", r.Method, "path", r.URL.Path)

		// Wrap response writer to capture response size
		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		r = r.WithContext(ctx)
		next.ServeHTTP(rec, r)

		// Track response size and duration
		responseSize.Record(ctx, int64(rec.bytesWritten))
		elapsedTime := time.Since(start).Seconds()
		requestDuration.Record(ctx, elapsedTime)

		// Logging after request processing
		logger.InfoContext(ctx, "Sending HTTP response", "status", rec.statusCode, "elapsed_time", elapsedTime)
	})
}

// Response Recorder to capture response size
type responseRecorder struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	size, err := r.ResponseWriter.Write(b)
	r.bytesWritten += size
	return size, err
}
