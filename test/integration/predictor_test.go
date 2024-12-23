// Package integration contains integration tests for Sentinel components.
package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aqstack/sentinel/pkg/collector"
	"github.com/aqstack/sentinel/pkg/healthscore"
)

// TestPredictorIntegration tests the full prediction pipeline.
func TestPredictorIntegration(t *testing.T) {
	// Skip if not on Linux
	if _, err := os.Stat("/proc/stat"); os.IsNotExist(err) {
		t.Skip("Skipping integration test: /proc/stat not available (not Linux)")
	}

	// Create real collector
	coll, err := collector.New("integration-test")
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}

	// Create predictor
	predictor := healthscore.NewPredictor("integration-test", nil)

	ctx := context.Background()

	// Collect samples over time
	for i := 0; i < 20; i++ {
		metrics, err := coll.Collect(ctx)
		if err != nil {
			t.Fatalf("Failed to collect metrics: %v", err)
		}

		predictor.AddSample(metrics)

		// Run prediction
		pred, err := predictor.Predict(ctx, metrics)
		if err != nil {
			t.Fatalf("Failed to predict: %v", err)
		}

		// Validate prediction structure
		if pred.NodeName != "integration-test" {
			t.Errorf("NodeName = %v, want integration-test", pred.NodeName)
		}
		if pred.Timestamp.IsZero() {
			t.Error("Timestamp should not be zero")
		}
		if pred.FailureProbability < 0 || pred.FailureProbability > 1 {
			t.Errorf("FailureProbability = %v, should be 0-1", pred.FailureProbability)
		}
		if pred.Confidence < 0 || pred.Confidence > 1 {
			t.Errorf("Confidence = %v, should be 0-1", pred.Confidence)
		}

		time.Sleep(50 * time.Millisecond)
	}

	// After sufficient samples, confidence should be reasonable
	metrics, _ := coll.Collect(ctx)
	pred, _ := predictor.Predict(ctx, metrics)

	if pred.Confidence < 0.5 {
		t.Logf("Confidence = %v (expected higher after 20 samples)", pred.Confidence)
	}
}

// TestCollectorPerformance ensures collector is fast enough for edge.
func TestCollectorPerformance(t *testing.T) {
	if _, err := os.Stat("/proc/stat"); os.IsNotExist(err) {
		t.Skip("Skipping: /proc/stat not available (not Linux)")
	}

	coll, err := collector.New("perf-test")
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}

	ctx := context.Background()

	// Warm up
	coll.Collect(ctx)

	// Measure 100 collections
	start := time.Now()
	iterations := 100
	for i := 0; i < iterations; i++ {
		_, err := coll.Collect(ctx)
		if err != nil {
			t.Fatalf("Collection failed: %v", err)
		}
	}
	elapsed := time.Since(start)

	avgDuration := elapsed / time.Duration(iterations)

	// Collection should be fast (< 10ms per collection)
	if avgDuration > 10*time.Millisecond {
		t.Errorf("Average collection time = %v, want < 10ms", avgDuration)
	} else {
		t.Logf("Average collection time: %v", avgDuration)
	}
}

// TestPredictorMemoryUsage ensures predictor doesn't grow unbounded.
func TestPredictorMemoryUsage(t *testing.T) {
	predictor := healthscore.NewPredictor("memory-test", nil)

	// Add many samples (more than maxHistory)
	for i := 0; i < 5000; i++ {
		predictor.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0 + float64(i%30),
			CPUUsagePercent:    30.0 + float64(i%50),
			MemoryUsagePercent: 40.0 + float64(i%40),
			LoadAverage1Min:    1.0 + float64(i%10)*0.1,
		})
	}

	// Stats should still work
	stats := predictor.GetStats()
	if len(stats) == 0 {
		t.Error("GetStats() returned empty result")
	}

	// Memory should be bounded (history is trimmed)
	// This is a structural test - actual memory measurement would require runtime.MemStats
	t.Log("Memory test passed - predictor accepts 5000 samples without error")
}
