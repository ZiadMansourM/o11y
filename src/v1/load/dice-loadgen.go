package main

// go run dice-loadgen.go -c 20 -r 50 -d 2m
// With -c 20 -r 50 for two minutes you’ll push ~6000 requests:
//   20 workers, each firing 50 requests per second
// total = duration * rate  = 120s * 50 rps  ≈ 6000 requests
//    each worker fires ≈ 3 rps (50 / 20)
// The Traffic panel (PromQL: sum(rate(http_requests_total[1m])))
// should stabilise around 50 req/s, the latency histogram will show
// p95, and any 5xx responses will tick up your error panel.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
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

// -----------------------------------------------------------------------------
// Adjust these to match your environment
// -----------------------------------------------------------------------------
const (
	serviceName      = "dice-loadgen"
	serviceVersion   = "v1.0.0"
	otelCollectorURL = "localhost:4318"
	targetDefaultURL = "http://127.0.0.1:3030/rolldice/"
)

var (
	// Flags
	concurrency = flag.Int("c", 10, "parallel workers")
	reqRate     = flag.Int("r", 20, "requests per second (total)")
	duration    = flag.Duration("d", 60*time.Second, "test duration")
	endpoint    = flag.String("u", targetDefaultURL, "target URL")

	// OTel globals (re‑using names from your dice‑cli file so import‑cycle‑free)
	name   = "github.com/ZiadMansour/bastet/o11y/dice-loadgen"
	meter  = otel.Meter(name)
	tracer = otel.Tracer(name)
	logger = otelslog.NewLogger(name)

	clientReqCounter metric.Int64Counter
	clientLatency    metric.Float64Histogram
	clientErrCounter metric.Int64Counter
)

// -----------------------------------------------------------------------------
// Instrumentation setup (copied from your dice‑cli, minimal tweaks)
// -----------------------------------------------------------------------------
func init() {
	var err error

	clientReqCounter, err = meter.Int64Counter(
		"client_requests_total",
		metric.WithDescription("Total number of outgoing HTTP requests"),
	)
	must(err)

	clientLatency, err = meter.Float64Histogram(
		"client_request_latency_seconds",
		metric.WithDescription("Latency of outgoing HTTP requests"),
		metric.WithUnit("s"),
	)
	must(err)

	clientErrCounter, err = meter.Int64Counter(
		"client_request_errors_total",
		metric.WithDescription("Total number of failed outgoing HTTP requests"),
	)
	must(err)
}

func main() {
	flag.Parse()

	// Ctrl‑C handling
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// OpenTelemetry boot‑strap
	shutdown, err := setupOTelSDK(rootCtx)
	must(err)
	defer shutdown(context.Background()) // nolint:errcheck

	// HTTP client with OTel middleware
	client := http.Client{
		Transport: ApplyMiddleware(http.DefaultTransport, clientInstrumentationMiddleware),
	}

	// -------------------------------------------------------------------------
	// Load‑generation loop
	// -------------------------------------------------------------------------
	fmt.Printf("Starting load: %d workers, %d req/s total, %s duration → %s\n",
		*concurrency, *reqRate, duration.String(), *endpoint)

	var wg sync.WaitGroup
	perReq := time.Second / time.Duration(*reqRate)
	ticker := time.NewTicker(perReq)
	defer ticker.Stop()

	stopAfter := time.After(*duration)

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopAfter:
					return
				case <-ticker.C:
					callDiceServer(rootCtx, &client)
				}
			}
		}()
	}

	wg.Wait()
	fmt.Println("Load test finished.  Shutting down …")
}

// -----------------------------------------------------------------------------
// --- Re‑used helpers (middleware, OTel SDK boot‑strap, etc.) ------------------
// -----------------------------------------------------------------------------

// callDiceServer performs one HTTP GET and records span/metrics/log.
func callDiceServer(ctx context.Context, client *http.Client) {
	ctx, span := tracer.Start(ctx, "CallDiceServer")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, "GET", *endpoint, nil)
	if err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "failed to create request", "error", err)
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "request failed")
		clientErrCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("error.type", err.Error())))
		logger.ErrorContext(ctx, "request failed", "error", err)
		return
	}
	resp.Body.Close()
}

// --- HTTP client middleware ---------------------------------------------------

type clientMW func(http.RoundTripper) http.RoundTripper

func ApplyMiddleware(rt http.RoundTripper, mws ...clientMW) http.RoundTripper {
	for i := len(mws) - 1; i >= 0; i-- {
		rt = mws[i](rt)
	}
	return rt
}

func clientInstrumentationMiddleware(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		start := time.Now()
		ctx, span := tracer.Start(req.Context(), fmt.Sprintf("HTTP %s %s", req.Method, req.URL.Path))
		defer span.End()

		otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

		clientReqCounter.Add(ctx, 1)

		resp, err := next.RoundTrip(req)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "HTTP request failed")
			clientErrCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("error.type", err.Error())))
			return nil, err
		}

		elapsed := time.Since(start).Seconds()
		clientLatency.Record(ctx, elapsed)

		return resp, nil
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// --- OpenTelemetry pipeline ---------------------------------------------------

func setupOTelSDK(ctx context.Context) (func(context.Context) error, error) {
	var cleanups []func(context.Context) error
	add := func(fn func(context.Context) error) { cleanups = append(cleanups, fn) }

	// composite shutdown func
	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range cleanups {
			err = errors.Join(err, fn(ctx))
		}
		return err
	}

	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	otel.SetTextMapPropagator(prop)

	// --- Traces
	traceExp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(otelCollectorURL),
		otlptracehttp.WithInsecure())
	if err != nil {
		return nil, err
	}
	tp := trace.NewTracerProvider(
		trace.WithBatcher(traceExp, trace.WithBatchTimeout(time.Second)),
		trace.WithResource(resAttrs()),
	)
	add(tp.Shutdown)
	otel.SetTracerProvider(tp)

	// --- Metrics
	metricExp, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(otelCollectorURL),
		otlpmetrichttp.WithInsecure())
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(3*time.Second))),
		sdkmetric.WithResource(resAttrs()),
	)
	add(mp.Shutdown)
	otel.SetMeterProvider(mp)

	// --- Logs (optional)
	logExp, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpoint(otelCollectorURL),
		otlploghttp.WithInsecure())
	if err != nil {
		return nil, err
	}
	lp := log.NewLoggerProvider(
		log.WithProcessor(log.NewBatchProcessor(logExp)),
		log.WithResource(resAttrs()),
	)
	add(lp.Shutdown)
	global.SetLoggerProvider(lp)

	return shutdown, nil
}

func resAttrs() *resource.Resource {
	return resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	)
}

// --- tiny helper --------------------------------------------------------------

func must(err error) {
	if err != nil {
		panic(err)
	}
}
