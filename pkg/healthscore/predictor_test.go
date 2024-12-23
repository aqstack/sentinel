package healthscore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aqstack/sentinel/pkg/collector"
)

func TestNewPredictor(t *testing.T) {
	p := NewPredictor("test-node", nil)
	if p.nodeName != "test-node" {
		t.Errorf("nodeName = %v, want test-node", p.nodeName)
	}
	if p.thresholds == nil {
		t.Error("thresholds should not be nil")
	}
}

func TestNewPredictorWithThresholds(t *testing.T) {
	thresholds := &PredictionThresholds{
		FailureProbabilityWarn:     0.5,
		FailureProbabilityCritical: 0.9,
		MinConfidence:              0.7,
		TimeToFailureThreshold:     10 * time.Minute,
	}
	p := NewPredictor("test-node", thresholds)

	if p.thresholds.FailureProbabilityWarn != 0.5 {
		t.Errorf("FailureProbabilityWarn = %v, want 0.5", p.thresholds.FailureProbabilityWarn)
	}
}

func TestPredictInsufficientHistory(t *testing.T) {
	p := NewPredictor("test-node", nil)
	ctx := context.Background()

	// Add only 5 samples (less than required 10)
	for i := 0; i < 5; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0,
			CPUUsagePercent:    30.0,
			MemoryUsagePercent: 40.0,
			LoadAverage1Min:    1.0,
		})
	}

	current := &collector.NodeMetrics{
		CPUTemperature:     50.0,
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 40.0,
	}

	pred, err := p.Predict(ctx, current)
	if err != nil {
		t.Fatalf("Predict() error = %v", err)
	}

	if pred.Confidence != 0.1 {
		t.Errorf("Confidence = %v, want 0.1 (insufficient history)", pred.Confidence)
	}

	found := false
	for _, r := range pred.Reasons {
		if r == "insufficient_history" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'insufficient_history' in reasons")
	}
}

func TestPredictNormalConditions(t *testing.T) {
	p := NewPredictor("test-node", nil)
	ctx := context.Background()

	// Add normal samples
	for i := 0; i < 50; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     45.0,
			CPUUsagePercent:    30.0,
			MemoryUsagePercent: 40.0,
			LoadAverage1Min:    1.0,
		})
	}

	current := &collector.NodeMetrics{
		CPUTemperature:     45.0,
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 40.0,
		LoadAverage1Min:    1.0,
	}

	pred, err := p.Predict(ctx, current)
	if err != nil {
		t.Fatalf("Predict() error = %v", err)
	}

	// Normal conditions should have low failure probability
	if pred.FailureProbability > 0.3 {
		t.Errorf("FailureProbability = %v, want < 0.3 for normal conditions", pred.FailureProbability)
	}

	if pred.TimeToFailure != -1 {
		t.Errorf("TimeToFailure = %v, want -1 (no failure predicted)", pred.TimeToFailure)
	}
}

func TestPredictThermalCritical(t *testing.T) {
	p := NewPredictor("test-node", nil)
	ctx := context.Background()

	// Add samples with normal temperature
	for i := 0; i < 50; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0,
			CPUUsagePercent:    30.0,
			MemoryUsagePercent: 40.0,
			LoadAverage1Min:    1.0,
		})
	}

	// Current sample has critical temperature
	current := &collector.NodeMetrics{
		CPUTemperature:     90.0, // Critical!
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 40.0,
		LoadAverage1Min:    1.0,
		CPUThrottled:       true,
	}

	pred, err := p.Predict(ctx, current)
	if err != nil {
		t.Fatalf("Predict() error = %v", err)
	}

	// Critical temperature should have elevated failure probability
	// (thermal risk is weighted at 30%, plus rapid rise bonus)
	if pred.FailureProbability < 0.3 {
		t.Errorf("FailureProbability = %v, want > 0.3 for critical temperature", pred.FailureProbability)
	}

	// Should have thermal reason
	hasThermalReason := false
	for _, r := range pred.Reasons {
		if strings.Contains(r, "temp") || strings.Contains(r, "thermal") || strings.Contains(r, "throttl") {
			hasThermalReason = true
			break
		}
	}
	if !hasThermalReason {
		t.Errorf("Expected thermal-related reason in %v", pred.Reasons)
	}
}

func TestPredictMemoryPressure(t *testing.T) {
	p := NewPredictor("test-node", nil)
	ctx := context.Background()

	// Add normal samples
	for i := 0; i < 50; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0,
			CPUUsagePercent:    30.0,
			MemoryUsagePercent: 50.0,
			LoadAverage1Min:    1.0,
		})
	}

	// Current sample has high memory
	current := &collector.NodeMetrics{
		CPUTemperature:     50.0,
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 96.0, // Critical!
		LoadAverage1Min:    1.0,
	}

	pred, err := p.Predict(ctx, current)
	if err != nil {
		t.Fatalf("Predict() error = %v", err)
	}

	// High memory should increase failure probability
	// (memory risk is weighted at 20%)
	if pred.FailureProbability < 0.2 {
		t.Errorf("FailureProbability = %v, want > 0.2 for memory pressure", pred.FailureProbability)
	}

	// Should have memory reason
	hasMemoryReason := false
	for _, r := range pred.Reasons {
		if strings.Contains(r, "memory") {
			hasMemoryReason = true
			break
		}
	}
	if !hasMemoryReason {
		t.Errorf("Expected memory-related reason in %v", pred.Reasons)
	}
}

func TestPredictTrendRising(t *testing.T) {
	p := NewPredictor("test-node", nil)
	ctx := context.Background()

	// Add samples with rising temperature trend
	for i := 0; i < 50; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0 + float64(i)*0.5, // Rising 0.5°C per sample
			CPUUsagePercent:    30.0,
			MemoryUsagePercent: 40.0,
			LoadAverage1Min:    1.0,
		})
	}

	current := &collector.NodeMetrics{
		CPUTemperature:     75.0,
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 40.0,
		LoadAverage1Min:    1.0,
	}

	pred, err := p.Predict(ctx, current)
	if err != nil {
		t.Fatalf("Predict() error = %v", err)
	}

	// Rising trend should be detected
	hasTrendReason := false
	for _, r := range pred.Reasons {
		if strings.Contains(r, "trend") || strings.Contains(r, "rising") {
			hasTrendReason = true
			break
		}
	}
	if !hasTrendReason {
		t.Errorf("Expected trend-related reason in %v", pred.Reasons)
	}
}

func TestShouldMigrate(t *testing.T) {
	thresholds := DefaultThresholds()
	p := NewPredictor("test-node", thresholds)

	tests := []struct {
		name       string
		prediction *Prediction
		want       bool
	}{
		{
			name: "low probability",
			prediction: &Prediction{
				FailureProbability: 0.2,
				Confidence:         0.8,
				TimeToFailure:      -1,
			},
			want: false,
		},
		{
			name: "high probability, high confidence",
			prediction: &Prediction{
				FailureProbability: 0.8,
				Confidence:         0.8,
				TimeToFailure:      600,
			},
			want: true,
		},
		{
			name: "high probability, low confidence",
			prediction: &Prediction{
				FailureProbability: 0.8,
				Confidence:         0.3, // Below MinConfidence
				TimeToFailure:      600,
			},
			want: false,
		},
		{
			name: "medium probability, imminent failure",
			prediction: &Prediction{
				FailureProbability: 0.4,
				Confidence:         0.8,
				TimeToFailure:      300, // 5 minutes
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.ShouldMigrate(tt.prediction)
			if got != tt.want {
				t.Errorf("ShouldMigrate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPredictOOMEvent(t *testing.T) {
	p := NewPredictor("test-node", nil)
	ctx := context.Background()

	// Add samples without OOM
	for i := 0; i < 50; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0,
			CPUUsagePercent:    30.0,
			MemoryUsagePercent: 70.0,
			OOMKillCount:       0,
			LoadAverage1Min:    1.0,
		})
	}

	// Current sample has OOM event
	current := &collector.NodeMetrics{
		CPUTemperature:     50.0,
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 85.0,
		OOMKillCount:       1, // OOM happened!
		LoadAverage1Min:    1.0,
	}

	pred, err := p.Predict(ctx, current)
	if err != nil {
		t.Fatalf("Predict() error = %v", err)
	}

	// OOM should increase risk (memory risk weighted at 25%, OOM adds 0.5 to memory risk)
	if pred.FailureProbability < 0.2 {
		t.Errorf("FailureProbability = %v, want > 0.2 after OOM", pred.FailureProbability)
	}
}

func TestGetStats(t *testing.T) {
	p := NewPredictor("test-node", nil)

	// Add some samples
	for i := 0; i < 20; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0 + float64(i%10),
			CPUUsagePercent:    30.0 + float64(i%20),
			MemoryUsagePercent: 40.0,
			LoadAverage1Min:    1.0 + float64(i%5)*0.1,
		})
	}

	stats := p.GetStats()
	if len(stats) == 0 {
		t.Error("GetStats() returned empty result")
	}

	// Stats should be valid JSON
	if stats[0] != '{' {
		t.Errorf("GetStats() should return JSON, got: %s", string(stats))
	}
}

func BenchmarkPredict(b *testing.B) {
	p := NewPredictor("bench-node", nil)
	ctx := context.Background()

	// Pre-populate with history
	for i := 0; i < 100; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0 + float64(i%20),
			CPUUsagePercent:    30.0 + float64(i%30),
			MemoryUsagePercent: 40.0 + float64(i%40),
			LoadAverage1Min:    1.0 + float64(i%10)*0.1,
		})
	}

	current := &collector.NodeMetrics{
		CPUTemperature:     60.0,
		CPUUsagePercent:    50.0,
		MemoryUsagePercent: 60.0,
		LoadAverage1Min:    2.0,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := p.Predict(ctx, current)
		if err != nil {
			b.Fatalf("Predict() error = %v", err)
		}
	}
}

func BenchmarkAddSample(b *testing.B) {
	p := NewPredictor("bench-node", nil)

	sample := &collector.NodeMetrics{
		CPUTemperature:     50.0,
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 40.0,
		LoadAverage1Min:    1.0,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.AddSample(sample)
	}
}

func TestPredictDiskCritical(t *testing.T) {
	p := NewPredictor("test-node", nil)
	ctx := context.Background()

	// Add normal samples
	for i := 0; i < 50; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0,
			CPUUsagePercent:    30.0,
			MemoryUsagePercent: 40.0,
			DiskUsagePercent:   50.0,
			LoadAverage1Min:    1.0,
		})
	}

	// Current sample has critical disk usage
	current := &collector.NodeMetrics{
		CPUTemperature:     50.0,
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 40.0,
		DiskUsagePercent:   96.0, // Critical!
		LoadAverage1Min:    1.0,
	}

	pred, err := p.Predict(ctx, current)
	if err != nil {
		t.Fatalf("Predict() error = %v", err)
	}

	// Critical disk should increase failure probability
	// (disk risk is weighted at 10%)
	if pred.FailureProbability < 0.1 {
		t.Errorf("FailureProbability = %v, want > 0.1 for disk critical", pred.FailureProbability)
	}

	// Should have disk reason
	hasDiskReason := false
	for _, r := range pred.Reasons {
		if strings.Contains(r, "disk") {
			hasDiskReason = true
			break
		}
	}
	if !hasDiskReason {
		t.Errorf("Expected disk-related reason in %v", pred.Reasons)
	}
}

func TestPredictDiskIOLatency(t *testing.T) {
	p := NewPredictor("test-node", nil)
	ctx := context.Background()

	// Add normal samples
	for i := 0; i < 50; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0,
			CPUUsagePercent:    30.0,
			MemoryUsagePercent: 40.0,
			DiskIOLatencyMs:    5.0,
			LoadAverage1Min:    1.0,
		})
	}

	// Current sample has high disk I/O latency
	current := &collector.NodeMetrics{
		CPUTemperature:     50.0,
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 40.0,
		DiskIOLatencyMs:    150.0, // Critical latency!
		LoadAverage1Min:    1.0,
	}

	pred, err := p.Predict(ctx, current)
	if err != nil {
		t.Fatalf("Predict() error = %v", err)
	}

	// Should have disk I/O reason
	hasDiskIOReason := false
	for _, r := range pred.Reasons {
		if strings.Contains(r, "disk_io") {
			hasDiskIOReason = true
			break
		}
	}
	if !hasDiskIOReason {
		t.Errorf("Expected disk_io-related reason in %v", pred.Reasons)
	}
}

func TestPredictNetworkLatency(t *testing.T) {
	p := NewPredictor("test-node", nil)
	ctx := context.Background()

	// Add normal samples
	for i := 0; i < 50; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0,
			CPUUsagePercent:    30.0,
			MemoryUsagePercent: 40.0,
			NetworkLatencyMs:   10.0,
			LoadAverage1Min:    1.0,
		})
	}

	// Current sample has high network latency
	current := &collector.NodeMetrics{
		CPUTemperature:     50.0,
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 40.0,
		NetworkLatencyMs:   600.0, // Critical latency!
		LoadAverage1Min:    1.0,
	}

	pred, err := p.Predict(ctx, current)
	if err != nil {
		t.Fatalf("Predict() error = %v", err)
	}

	// Should have network latency reason
	hasNetworkReason := false
	for _, r := range pred.Reasons {
		if strings.Contains(r, "network_latency") {
			hasNetworkReason = true
			break
		}
	}
	if !hasNetworkReason {
		t.Errorf("Expected network_latency-related reason in %v", pred.Reasons)
	}
}

func TestPredictNetworkErrors(t *testing.T) {
	p := NewPredictor("test-node", nil)
	ctx := context.Background()

	// Add samples with network traffic but no errors
	for i := 0; i < 50; i++ {
		p.AddSample(&collector.NodeMetrics{
			CPUTemperature:     50.0,
			CPUUsagePercent:    30.0,
			MemoryUsagePercent: 40.0,
			NetworkRxBytes:     uint64(i) * 1024 * 1024, // 1MB per sample
			NetworkTxBytes:     uint64(i) * 512 * 1024,
			NetworkRxErrors:    0,
			NetworkTxErrors:    0,
			LoadAverage1Min:    1.0,
		})
	}

	// Current sample has high error rate
	current := &collector.NodeMetrics{
		CPUTemperature:     50.0,
		CPUUsagePercent:    30.0,
		MemoryUsagePercent: 40.0,
		NetworkRxBytes:     50 * 1024 * 1024,
		NetworkTxBytes:     25 * 1024 * 1024,
		NetworkRxErrors:    100, // High errors!
		NetworkTxErrors:    50,
		LoadAverage1Min:    1.0,
	}

	pred, err := p.Predict(ctx, current)
	if err != nil {
		t.Fatalf("Predict() error = %v", err)
	}

	// Should have network errors reason
	hasNetworkErrorReason := false
	for _, r := range pred.Reasons {
		if strings.Contains(r, "network_errors") {
			hasNetworkErrorReason = true
			break
		}
	}
	if !hasNetworkErrorReason {
		t.Errorf("Expected network_errors-related reason in %v", pred.Reasons)
	}
}

func TestCalculateDiskRisk(t *testing.T) {
	p := NewPredictor("test-node", nil)

	tests := []struct {
		name           string
		metrics        *collector.NodeMetrics
		wantMinRisk    float64
		wantMaxRisk    float64
		wantReasonPart string
	}{
		{
			name: "normal disk",
			metrics: &collector.NodeMetrics{
				DiskUsagePercent: 50.0,
				DiskIOLatencyMs:  5.0,
			},
			wantMinRisk:    0.0,
			wantMaxRisk:    0.1,
			wantReasonPart: "",
		},
		{
			name: "disk full",
			metrics: &collector.NodeMetrics{
				DiskUsagePercent: 96.0,
				DiskIOLatencyMs:  5.0,
			},
			wantMinRisk:    0.9,
			wantMaxRisk:    1.0,
			wantReasonPart: "disk_full",
		},
		{
			name: "high latency",
			metrics: &collector.NodeMetrics{
				DiskUsagePercent: 50.0,
				DiskIOLatencyMs:  120.0,
			},
			wantMinRisk:    0.4,
			wantMaxRisk:    0.6,
			wantReasonPart: "disk_io_critical",
		},
		{
			name: "disk full with high latency",
			metrics: &collector.NodeMetrics{
				DiskUsagePercent: 96.0,
				DiskIOLatencyMs:  120.0,
			},
			wantMinRisk:    1.0,
			wantMaxRisk:    1.0,
			wantReasonPart: "disk_full",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			risk, reason := p.calculateDiskRisk(tt.metrics)

			if risk < tt.wantMinRisk || risk > tt.wantMaxRisk {
				t.Errorf("calculateDiskRisk() risk = %v, want between %v and %v",
					risk, tt.wantMinRisk, tt.wantMaxRisk)
			}

			if tt.wantReasonPart != "" && !strings.Contains(reason, tt.wantReasonPart) {
				t.Errorf("calculateDiskRisk() reason = %v, want to contain %v",
					reason, tt.wantReasonPart)
			}
		})
	}
}

func TestCalculateNetworkRisk(t *testing.T) {
	p := NewPredictor("test-node", nil)

	// Add some history for error rate calculation
	for i := 0; i < 10; i++ {
		p.AddSample(&collector.NodeMetrics{
			NetworkRxBytes:  uint64(i) * 1024 * 1024,
			NetworkTxBytes:  uint64(i) * 512 * 1024,
			NetworkRxErrors: 0,
			NetworkTxErrors: 0,
		})
	}

	tests := []struct {
		name           string
		metrics        *collector.NodeMetrics
		wantMinRisk    float64
		wantMaxRisk    float64
		wantReasonPart string
	}{
		{
			name: "normal network",
			metrics: &collector.NodeMetrics{
				NetworkLatencyMs: 20.0,
				NetworkRxBytes:   10 * 1024 * 1024,
				NetworkTxBytes:   5 * 1024 * 1024,
				NetworkRxErrors:  0,
				NetworkTxErrors:  0,
			},
			wantMinRisk:    0.0,
			wantMaxRisk:    0.1,
			wantReasonPart: "",
		},
		{
			name: "critical latency",
			metrics: &collector.NodeMetrics{
				NetworkLatencyMs: 600.0,
				NetworkRxBytes:   10 * 1024 * 1024,
				NetworkTxBytes:   5 * 1024 * 1024,
				NetworkRxErrors:  0,
				NetworkTxErrors:  0,
			},
			wantMinRisk:    0.7,
			wantMaxRisk:    0.9,
			wantReasonPart: "network_latency_critical",
		},
		{
			name: "elevated latency",
			metrics: &collector.NodeMetrics{
				NetworkLatencyMs: 150.0,
				NetworkRxBytes:   10 * 1024 * 1024,
				NetworkTxBytes:   5 * 1024 * 1024,
				NetworkRxErrors:  0,
				NetworkTxErrors:  0,
			},
			wantMinRisk:    0.1,
			wantMaxRisk:    0.3,
			wantReasonPart: "network_latency_elevated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			risk, reason := p.calculateNetworkRisk(tt.metrics)

			if risk < tt.wantMinRisk || risk > tt.wantMaxRisk {
				t.Errorf("calculateNetworkRisk() risk = %v, want between %v and %v",
					risk, tt.wantMinRisk, tt.wantMaxRisk)
			}

			if tt.wantReasonPart != "" && !strings.Contains(reason, tt.wantReasonPart) {
				t.Errorf("calculateNetworkRisk() reason = %v, want to contain %v",
					reason, tt.wantReasonPart)
			}
		})
	}
}
