package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

func rolldice(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "roll_dice")
	defer span.End()

	// Validate HTTP method (example of 405 Method Not Allowed)
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		span.SetStatus(codes.Error, "Method not allowed")
		logger.ErrorContext(ctx, "Method not allowed", "method", r.Method)
		return
	}

	// Add an event to indicate the dice roll has started
	span.AddEvent("Dice rolling started")

	// Roll the dice four times and collect the results
	results := make([]int, 4)
	hasError := false
	for i := range results {
		roll, err := rollSingleDice(ctx, i+1)
		if err != nil {
			// Mark that we had an error, but continue rolling
			hasError = true
			span.RecordError(err)
			// ", "roll_number", i+1, "error", err
			logger.ErrorContext(ctx, fmt.Sprintf("Dice roll error occurred in roll_number: %d", i+1), "error", err)
		}
		results[i] = roll
	}

	// Add an event to indicate all rolls are complete
	span.AddEvent(
		"Dice rolling completed",
		trace.WithAttributes(attribute.IntSlice("results", results)),
	)

	// Set status code based on whether any errors occurred
	if hasError {
		w.WriteHeader(http.StatusInternalServerError)
		span.SetStatus(codes.Error, "One or more dice rolls had errors")
	} else {
		span.SetStatus(codes.Ok, "Roll dice handler completed successfully")
	}

	// Generate a response message with the actual results
	response := fmt.Sprintf("Dice rolls: %v\n", results)
	if _, err := io.WriteString(w, response); err != nil {
		w.WriteHeader(http.StatusInternalServerError) // Set 500 status code
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to write response")
		logger.ErrorContext(ctx, "Failed to write response", "error", err)
		return
	}
}

func rollSingleDice(ctx context.Context, rollNumber int) (int, error) {
	// Start a child span for this roll
	ctx, span := tracer.Start(ctx, fmt.Sprintf("roll_%d", rollNumber))
	defer span.End()

	// Add an event to indicate the roll has started
	span.AddEvent("Roll started", trace.WithAttributes(attribute.Int("roll.number", rollNumber)))

	// Simulate rolling a dice
	roll := 1 + rand.Intn(6)

	// Add attributes and metrics for this roll
	rollValueAttr := attribute.Int("roll.value", roll)
	span.SetAttributes(
		rollValueAttr,
		attribute.Int("roll.number", rollNumber),
	)
	rollCnt.Add(ctx, 1, metric.WithAttributes(rollValueAttr))
	// rollCnt.Record(ctx, int64(roll), metric.WithAttributes(rollValueAttr))

	// Log the roll result
	logger.InfoContext(ctx, fmt.Sprintf("Dice rolled: %d", roll), "roll_number", rollNumber, "result", roll)
	span.AddEvent("Roll completed", trace.WithAttributes(rollValueAttr))

	// Simulate an exception for certain conditions (example: roll of 1)
	if roll == 1 {
		err := fmt.Errorf("unlucky roll: %d", roll)
		span.RecordError(err)
		span.SetStatus(codes.Error, "Critical roll error")
		logger.ErrorContext(ctx, "Critical roll error", "roll_number", rollNumber, "error", err)
		return roll, err // Return the error
	}

	return roll, nil
}
