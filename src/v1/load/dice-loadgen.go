// # Pure Poisson around 50 rps, 2 minutes
// go run dice-loadgen.go -c 20 -r 50 -d 2m

// # Same but add ±20 % jitter
// go run dice-loadgen.go -c 20 -r 50 -v 0.2 -d 2m
// Traffic panel (PromQL: sum(rate(http_requests_total[1m]))) ≈ 50 rps
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
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
	serviceVersion   = "v1.0.1"
	otelCollectorURL = "localhost:4318"
	targetDefaultURL = "http://127.0.0.1:3030/rolldice/"
)

var (
	// Simulation parameters
	variability = flag.Float64("v", 0.0,
		"relative jitter (e.g. 0.2 = ±20%) around target RPS; 0 = pure Poisson")

	// CLI flags
	concurrency = flag.Int("c", 10, "parallel workers")
	reqRate     = flag.Int("r", 20, "requests per second (total)")
	duration    = flag.Duration("d", 60*time.Second, "test duration (0 = until CTRL-C)")
	endpoint    = flag.String("u", targetDefaultURL, "target URL")

	// OpenTelemetry globals
	name   = "github.com/ZiadMansour/bastet/o11y/dice-loadgen"
	meter  = otel.Meter(name)
	tracer = otel.Tracer(name)
	logger = otelslog.NewLogger(name)

	clientReqCounter metric.Int64Counter
	clientLatency    metric.Float64Histogram
	clientErrCounter metric.Int64Counter
)

// -----------------------------------------------------------------------------
// Instrumentation setup (copied from dice‑cli; minimal tweaks)
// -----------------------------------------------------------------------------
func init() {
	var err error

	clientReqCounter, err = meter.Int64Counter("client_requests_total",
		metric.WithDescription("Total number of outgoing HTTP requests"))
	must(err)

	clientLatency, err = meter.Float64Histogram("client_request_latency_seconds",
		metric.WithDescription("Latency of outgoing HTTP requests in seconds"),
		metric.WithUnit("s"))
	must(err)

	clientErrCounter, err = meter.Int64Counter("client_request_errors_total",
		metric.WithDescription("Total number of failed outgoing HTTP requests"))
	must(err)
}

func main() {
	flag.Parse()

	// ── Graceful shutdown boilerplate ──────────────────────────────────────────
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	shutdown, err := setupOTelSDK(rootCtx)
	must(err)
	defer shutdown(context.Background()) // nolint:errcheck

	httpClient := http.Client{
		Transport: ApplyMiddleware(http.DefaultTransport, clientInstrumentationMiddleware),
	}

	// ── Load generator parameters ─────────────────────────────────────────────
	fmt.Printf("Starting load: %d workers, %d req/s total, %s duration → %s\n",
		*concurrency, *reqRate, duration.String(), *endpoint)

	perReq := time.Second / time.Duration(*reqRate)
	ticker := time.NewTicker(perReq)
	defer ticker.Stop()

	stopCh := make(chan struct{})
	var once sync.Once
	closeStop := func() { once.Do(func() { close(stopCh) }) }

	if *duration > 0 {
		go func() {
			time.Sleep(*duration)
			closeStop()
		}()
	}
	go func() {
		<-rootCtx.Done()
		closeStop()
	}()

	// ── Worker pool ───────────────────────────────────────────────────────────
	var wg sync.WaitGroup
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return
				default:
					callDiceServer(rootCtx, &httpClient)
					time.Sleep(nextInterval(*reqRate))
				}
			}
		}()
	}

	wg.Wait()
	fmt.Println("Load test finished — shutting down.")
}

// -----------------------------------------------------------------------------
// Helpers (middleware, OTel setup, etc.)
// -----------------------------------------------------------------------------

// nextInterval returns how long to sleep before the next request
func nextInterval(baseRPS int) time.Duration {
	// 1) Poisson: exponential with mean = 1/R
	lambda := float64(baseRPS)
	dt := rand.ExpFloat64() / lambda // seconds

	// 2) Optional ± jitter% around the sample
	if *variability > 0 {
		jitter := 1 + (*variability)*(rand.Float64()*2-1) // 1 ± v
		dt *= jitter
	}
	return time.Duration(dt * float64(time.Second))
}

// callDiceServer performs one HTTP GET and records span/metrics/log.
func callDiceServer(ctx context.Context, client *http.Client) {
	ctx, span := tracer.Start(ctx, "CallDiceServer")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *endpoint, nil)
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

// ── HTTP client middleware ───────────────────────────────────────────────────
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

		clientLatency.Record(ctx, time.Since(start).Seconds())
		return resp, nil
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// ── OpenTelemetry pipeline ───────────────────────────────────────────────────
func setupOTelSDK(ctx context.Context) (func(context.Context) error, error) {
	var cbs []func(context.Context) error
	add := func(fn func(context.Context) error) { cbs = append(cbs, fn) }
	cleanup := func(ctx context.Context) error {
		var err error
		for _, fn := range cbs {
			err = errors.Join(err, fn(ctx))
		}
		return err
	}

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))

	// Traces
	tExp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(otelCollectorURL),
		otlptracehttp.WithInsecure())
	if err != nil {
		return nil, err
	}
	tp := trace.NewTracerProvider(
		trace.WithBatcher(tExp, trace.WithBatchTimeout(time.Second)),
		trace.WithResource(resAttrs()))
	add(tp.Shutdown)
	otel.SetTracerProvider(tp)

	// Metrics
	mExp, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(otelCollectorURL),
		otlpmetrichttp.WithInsecure())
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(mExp, sdkmetric.WithInterval(3*time.Second))),
		sdkmetric.WithResource(resAttrs()))
	add(mp.Shutdown)
	otel.SetMeterProvider(mp)

	// Logs (optional)
	lExp, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpoint(otelCollectorURL),
		otlploghttp.WithInsecure())
	if err == nil { // logs are best‑effort
		lp := log.NewLoggerProvider(
			log.WithProcessor(log.NewBatchProcessor(lExp)),
			log.WithResource(resAttrs()))
		add(lp.Shutdown)
		global.SetLoggerProvider(lp)
	}

	return cleanup, nil
}

func resAttrs() *resource.Resource {
	return resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	)
}

// tiny panic‑on‑error helper
func must(err error) {
	if err != nil {
		panic(err)
	}
}
