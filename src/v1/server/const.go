package main

import (
	"log/slog"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	name             = "github.com/ZiadMansour/bastet/o11y/dice-server"
	serviceName      = "dice-server"
	serviceVersion   = "v1.0.0"
	otelCollectorURL = "localhost:4318"
)

var (
	meter  metric.Meter
	tracer trace.Tracer
	logger *slog.Logger

	rollCnt metric.Int64Counter
	// rollCnt metric.Int64Histogram

	requestCounter  metric.Int64Counter
	requestDuration metric.Float64Histogram
	requestSize     metric.Int64Histogram
	responseSize    metric.Int64Histogram
)
