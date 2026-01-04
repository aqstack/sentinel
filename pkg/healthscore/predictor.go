// Package healthscore provides failure prediction and health scoring.
// Optimized for resource-constrained edge environments.
package healthscore

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/aqstack/sentinel/pkg/collector"
)

// Prediction represents a failure prediction result.
type Prediction struct {
	Timestamp          time.Time `json:"timestamp"`
	NodeName           string    `json:"node_name"`
	FailureProbability float64   `json:"failure_probability"`
	Confidence         float64   `json:"confidence"`
	TimeToFailure      float64   `json:"time_to_failure_seconds"` // -1 if no failure predicted
	Reasons            []string  `json:"reasons"`
	Recommendation     string    `json:"recommendation"`
}

// PredictionThresholds configures when to trigger alerts/actions.
type PredictionThresholds struct {
	// FailureProbabilityWarn triggers a warning
	FailureProbabilityWarn float64
	// FailureProbabilityCritical triggers preemptive action
	FailureProbabilityCritical float64
	// MinConfidence is the minimum confidence to act on a prediction
	MinConfidence float64
	// TimeToFailureThreshold triggers action if predicted failure is within this window
	TimeToFailureThreshold time.Duration
}

// DefaultThresholds returns sensible defaults for edge environments.
func DefaultThresholds() *PredictionThresholds {
	return &PredictionThresholds{
		FailureProbabilityWarn:     0.3,
		FailureProbabilityCritical: 0.7,
		MinConfidence:              0.6,
		TimeToFailureThreshold:     15 * time.Minute,
	}
}

// Predictor performs failure prediction based on node metrics.
// Uses a lightweight statistical model suitable for edge deployment.
type Predictor struct {
	nodeName   string
	thresholds *PredictionThresholds

	mu         sync.RWMutex
	history    []collector.NodeMetrics
	maxHistory int

	// Feature statistics for normalization
	stats featureStats
}

type featureStats struct {
	cpuTempMean, cpuTempStd   float64
	cpuUsageMean, cpuUsageStd float64
	memUsageMean, memUsageStd float64
	loadMean, loadStd         float64
	samples                   int
}

// NewPredictor creates a new failure predictor.
func NewPredictor(nodeName string, thresholds *PredictionThresholds) *Predictor {
	if thresholds == nil {
		thresholds = DefaultThresholds()
	}
	return &Predictor{
		nodeName:   nodeName,
		thresholds: thresholds,
		maxHistory: 1000, // ~16 minutes at 1 sample/sec
	}
}

// AddSample adds a new metrics sample to the prediction model.
func (p *Predictor) AddSample(m *collector.NodeMetrics) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.history = append(p.history, *m)

	// Trim old samples
	if len(p.history) > p.maxHistory {
		p.history = p.history[len(p.history)-p.maxHistory:]
	}

	// Update running statistics
	p.updateStats(m)
}

func (p *Predictor) updateStats(m *collector.NodeMetrics) {
	n := float64(p.stats.samples + 1)

	// Welford's online algorithm for mean and variance
	p.stats.cpuTempMean, p.stats.cpuTempStd = updateMeanStd(
		p.stats.cpuTempMean, p.stats.cpuTempStd, m.CPUTemperature, n)
	p.stats.cpuUsageMean, p.stats.cpuUsageStd = updateMeanStd(
		p.stats.cpuUsageMean, p.stats.cpuUsageStd, m.CPUUsagePercent, n)
	p.stats.memUsageMean, p.stats.memUsageStd = updateMeanStd(
		p.stats.memUsageMean, p.stats.memUsageStd, m.MemoryUsagePercent, n)
	p.stats.loadMean, p.stats.loadStd = updateMeanStd(
		p.stats.loadMean, p.stats.loadStd, m.LoadAverage1Min, n)

	p.stats.samples++
}

func updateMeanStd(oldMean, oldStd, newValue, n float64) (float64, float64) {
	if n == 1 {
		return newValue, 0
	}
	delta := newValue - oldMean
	newMean := oldMean + delta/n
	delta2 := newValue - newMean
	newVar := (oldStd*oldStd*(n-1) + delta*delta2) / n
	return newMean, math.Sqrt(newVar)
}

// Predict generates a failure prediction based on current metrics and history.
func (p *Predictor) Predict(ctx context.Context, current *collector.NodeMetrics) (*Prediction, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	pred := &Prediction{
		Timestamp:      time.Now(),
		NodeName:       p.nodeName,
		TimeToFailure:  -1, // No failure predicted by default
		Recommendation: "No action required",
	}

	if len(p.history) < 10 {
		pred.Confidence = 0.1
		pred.Reasons = append(pred.Reasons, "insufficient_history")
		return pred, nil
	}

	// Calculate risk factors
	// Weights: thermal=30%, memory=20%, cpu=15%, disk=10%, network=10%, trend=15%
	var riskScore float64
	var confidence float64 = 0.8 // Base confidence
	reasons := make([]string, 0)

	// 1. Thermal risk - critical for edge devices
	thermalRisk, thermalReason := p.calculateThermalRisk(current)
	riskScore += thermalRisk * 0.30
	if thermalReason != "" {
		reasons = append(reasons, thermalReason)
	}

	// 2. Memory pressure risk
	memoryRisk, memoryReason := p.calculateMemoryRisk(current)
	riskScore += memoryRisk * 0.20
	if memoryReason != "" {
		reasons = append(reasons, memoryReason)
	}

	// 3. CPU overload risk
	cpuRisk, cpuReason := p.calculateCPURisk(current)
	riskScore += cpuRisk * 0.15
	if cpuReason != "" {
		reasons = append(reasons, cpuReason)
	}

	// 4. Disk I/O risk
	diskRisk, diskReason := p.calculateDiskRisk(current)
	riskScore += diskRisk * 0.10
	if diskReason != "" {
		reasons = append(reasons, diskReason)
	}

	// 5. Network risk
	networkRisk, networkReason := p.calculateNetworkRisk(current)
	riskScore += networkRisk * 0.10
	if networkReason != "" {
		reasons = append(reasons, networkReason)
	}

	// 6. Trend analysis
	trendRisk, trendReason := p.calculateTrendRisk()
	riskScore += trendRisk * 0.15
	if trendReason != "" {
		reasons = append(reasons, trendReason)
	}

	// Clamp risk score
	if riskScore > 1.0 {
		riskScore = 1.0
	}

	pred.FailureProbability = riskScore
	pred.Confidence = confidence
	pred.Reasons = reasons

	// Estimate time to failure if probability is significant
	if riskScore > p.thresholds.FailureProbabilityWarn {
		ttf := p.estimateTimeToFailure(current, riskScore)
		if ttf > 0 {
			pred.TimeToFailure = ttf.Seconds()
		}
	}

	// Generate recommendation
	pred.Recommendation = p.generateRecommendation(riskScore, reasons)

	return pred, nil
}

func (p *Predictor) calculateThermalRisk(m *collector.NodeMetrics) (float64, string) {
	// Thermal zones (typical for ARM and x86):
	// < 60°C: Normal
	// 60-75°C: Elevated
	// 75-85°C: High risk
	// > 85°C: Critical (throttling imminent)

	temp := m.CPUTemperature
	if temp <= 0 {
		return 0, "" // No thermal data
	}

	var risk float64
	var reason string

	switch {
	case temp > 85:
		risk = 1.0
		reason = "cpu_temp_critical"
	case temp > 75:
		risk = 0.7 + (temp-75)/10*0.3
		reason = "cpu_temp_high"
	case temp > 65:
		risk = 0.3 + (temp-65)/10*0.4
		reason = "cpu_temp_elevated"
	case temp > 55:
		risk = (temp - 55) / 10 * 0.3
	}

	// Check for rapid temperature increase
	if len(p.history) >= 10 {
		recentAvg := 0.0
		for i := len(p.history) - 10; i < len(p.history); i++ {
			recentAvg += p.history[i].CPUTemperature
		}
		recentAvg /= 10

		if m.CPUTemperature-recentAvg > 5 {
			risk += 0.2
			if reason == "" {
				reason = "cpu_temp_rising"
			} else {
				reason += ",cpu_temp_rising"
			}
		}
	}

	// Check for throttling
	if m.CPUThrottled {
		risk += 0.3
		if reason == "" {
			reason = "cpu_throttled"
		} else {
			reason += ",cpu_throttled"
		}
	}

	if risk > 1.0 {
		risk = 1.0
	}

	return risk, reason
}

func (p *Predictor) calculateMemoryRisk(m *collector.NodeMetrics) (float64, string) {
	usage := m.MemoryUsagePercent
	var risk float64
	var reason string

	switch {
	case usage > 95:
		risk = 1.0
		reason = "memory_critical"
	case usage > 90:
		risk = 0.7 + (usage-90)/5*0.3
		reason = "memory_pressure_high"
	case usage > 80:
		risk = 0.3 + (usage-80)/10*0.4
		reason = "memory_pressure"
	case usage > 70:
		risk = (usage - 70) / 10 * 0.3
	}

	// Check OOM events
	if len(p.history) >= 2 {
		prevOOM := p.history[len(p.history)-2].OOMKillCount
		if m.OOMKillCount > prevOOM {
			risk += 0.5
			if reason == "" {
				reason = "oom_events"
			} else {
				reason += ",oom_events"
			}
		}
	}

	// Check swap usage as early warning
	if m.SwapTotalBytes > 0 {
		swapUsage := float64(m.SwapUsedBytes) / float64(m.SwapTotalBytes) * 100
		if swapUsage > 50 {
			risk += 0.2
			if reason == "" {
				reason = "swap_pressure"
			} else {
				reason += ",swap_pressure"
			}
		}
	}

	if risk > 1.0 {
		risk = 1.0
	}

	return risk, reason
}

func (p *Predictor) calculateCPURisk(m *collector.NodeMetrics) (float64, string) {
	var risk float64
	var reason string

	// CPU usage risk
	usage := m.CPUUsagePercent
	switch {
	case usage > 95:
		risk = 0.8
		reason = "cpu_saturated"
	case usage > 85:
		risk = 0.4 + (usage-85)/10*0.4
		reason = "cpu_high"
	case usage > 70:
		risk = (usage - 70) / 15 * 0.4
	}

	// Load average risk (relative to baseline)
	if p.stats.loadMean > 0 && p.stats.loadStd > 0 {
		zScore := (m.LoadAverage1Min - p.stats.loadMean) / p.stats.loadStd
		if zScore > 3 {
			risk += 0.3
			if reason == "" {
				reason = "load_anomaly"
			} else {
				reason += ",load_anomaly"
			}
		} else if zScore > 2 {
			risk += 0.15
		}
	}

	if risk > 1.0 {
		risk = 1.0
	}

	return risk, reason
}

func (p *Predictor) calculateDiskRisk(m *collector.NodeMetrics) (float64, string) {
	var risk float64
	var reason string

	// Disk usage risk
	usage := m.DiskUsagePercent
	switch {
	case usage > 95:
		risk = 1.0
		reason = "disk_full"
	case usage > 90:
		risk = 0.7 + (usage-90)/5*0.3
		reason = "disk_critical"
	case usage > 80:
		risk = 0.3 + (usage-80)/10*0.4
		reason = "disk_high"
	case usage > 70:
		risk = (usage - 70) / 10 * 0.3
	}

	// Disk I/O latency risk (high latency indicates struggling disk)
	latency := m.DiskIOLatencyMs
	switch {
	case latency > 100:
		risk += 0.5
		if reason == "" {
			reason = "disk_io_critical"
		} else {
			reason += ",disk_io_critical"
		}
	case latency > 50:
		risk += 0.3
		if reason == "" {
			reason = "disk_io_high"
		} else {
			reason += ",disk_io_high"
		}
	case latency > 20:
		risk += 0.1
	}

	if risk > 1.0 {
		risk = 1.0
	}

	return risk, reason
}

func (p *Predictor) calculateNetworkRisk(m *collector.NodeMetrics) (float64, string) {
	var risk float64
	var reason string

	// Network latency risk
	latency := m.NetworkLatencyMs
	switch {
	case latency > 500:
		risk = 0.8
		reason = "network_latency_critical"
	case latency > 200:
		risk = 0.4 + (latency-200)/300*0.4
		reason = "network_latency_high"
	case latency > 100:
		risk = (latency - 100) / 100 * 0.4
		reason = "network_latency_elevated"
	}

	// Network error rate risk
	// Calculate error rate from recent history if available
	if len(p.history) >= 2 {
		prev := p.history[len(p.history)-2]
		rxDelta := m.NetworkRxBytes - prev.NetworkRxBytes
		txDelta := m.NetworkTxBytes - prev.NetworkTxBytes
		rxErrDelta := m.NetworkRxErrors - prev.NetworkRxErrors
		txErrDelta := m.NetworkTxErrors - prev.NetworkTxErrors

		totalBytes := rxDelta + txDelta
		totalErrors := rxErrDelta + txErrDelta

		if totalBytes > 0 {
			// Error rate per MB of traffic
			errorRate := float64(totalErrors) / (float64(totalBytes) / 1024 / 1024)
			if errorRate > 10 { // More than 10 errors per MB
				risk += 0.4
				if reason == "" {
					reason = "network_errors_high"
				} else {
					reason += ",network_errors_high"
				}
			} else if errorRate > 1 {
				risk += 0.2
				if reason == "" {
					reason = "network_errors_elevated"
				} else {
					reason += ",network_errors_elevated"
				}
			}
		}
	}

	if risk > 1.0 {
		risk = 1.0
	}

	return risk, reason
}

func (p *Predictor) calculateTrendRisk() (float64, string) {
	if len(p.history) < 30 {
		return 0, ""
	}

	// Calculate trend over last 30 samples
	recent := p.history[len(p.history)-30:]

	// Simple linear regression on temperature
	var sumX, sumY, sumXY, sumX2 float64
	for i, m := range recent {
		x := float64(i)
		y := m.CPUTemperature
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	n := float64(len(recent))
	slope := (n*sumXY - sumX*sumY) / (n*sumX2 - sumX*sumX)

	// Positive slope = rising temperature
	var risk float64
	var reason string

	if slope > 0.1 { // Rising more than 0.1°C per sample
		risk = math.Min(slope*2, 0.5)
		reason = "thermal_trend_rising"
	}

	// Also check memory trend
	sumY = 0
	sumXY = 0
	for i, m := range recent {
		x := float64(i)
		y := m.MemoryUsagePercent
		sumY += y
		sumXY += x * y
	}
	memSlope := (n*sumXY - sumX*sumY) / (n*sumX2 - sumX*sumX)

	if memSlope > 0.5 { // Memory growing > 0.5% per sample
		risk += math.Min(memSlope*0.3, 0.3)
		if reason == "" {
			reason = "memory_trend_rising"
		} else {
			reason += ",memory_trend_rising"
		}
	}

	return risk, reason
}

func (p *Predictor) estimateTimeToFailure(m *collector.NodeMetrics, riskScore float64) time.Duration {
	if len(p.history) < 30 {
		return 0
	}

	// Use thermal trend to estimate time to critical temperature
	recent := p.history[len(p.history)-30:]

	var sumX, sumY, sumXY, sumX2 float64
	for i, sample := range recent {
		x := float64(i)
		y := sample.CPUTemperature
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	n := float64(len(recent))
	slope := (n*sumXY - sumX*sumY) / (n*sumX2 - sumX*sumX)

	if slope <= 0 {
		// Temperature stable or decreasing
		return 0
	}

	// Estimate samples until 85°C (critical threshold)
	currentTemp := m.CPUTemperature
	criticalTemp := 85.0
	samplesToFailure := (criticalTemp - currentTemp) / slope

	if samplesToFailure <= 0 {
		return 30 * time.Second // Already critical
	}

	// Convert samples to time (assuming 1 sample/second)
	return time.Duration(samplesToFailure) * time.Second
}

func (p *Predictor) generateRecommendation(riskScore float64, reasons []string) string {
	if riskScore >= p.thresholds.FailureProbabilityCritical {
		return "CRITICAL: Initiate preemptive workload migration immediately"
	}

	if riskScore >= p.thresholds.FailureProbabilityWarn {
		// Generate specific recommendations based on reasons
		for _, r := range reasons {
			switch {
			case strings.Contains(r, "thermal") || strings.Contains(r, "temp"):
				return "WARNING: Reduce thermal load - consider migrating CPU-intensive workloads"
			case strings.Contains(r, "memory") || strings.Contains(r, "oom"):
				return "WARNING: Memory pressure detected - consider evicting low-priority pods"
			case strings.Contains(r, "cpu") || strings.Contains(r, "load"):
				return "WARNING: CPU saturation - consider spreading workload across cluster"
			case strings.Contains(r, "disk_full") || strings.Contains(r, "disk_critical"):
				return "WARNING: Disk space critical - consider migrating storage-heavy workloads"
			case strings.Contains(r, "disk_io"):
				return "WARNING: Disk I/O degraded - consider migrating I/O-intensive workloads"
			case strings.Contains(r, "network_latency"):
				return "WARNING: Network latency elevated - check connectivity and consider migration"
			case strings.Contains(r, "network_errors"):
				return "WARNING: Network errors detected - investigate network health"
			}
		}
		return "WARNING: Elevated failure risk - prepare for potential migration"
	}

	return "No action required"
}

// ShouldMigrate returns true if prediction indicates workloads should be migrated.
func (p *Predictor) ShouldMigrate(pred *Prediction) bool {
	if pred.Confidence < p.thresholds.MinConfidence {
		return false
	}

	if pred.FailureProbability >= p.thresholds.FailureProbabilityCritical {
		return true
	}

	if pred.TimeToFailure > 0 &&
		time.Duration(pred.TimeToFailure)*time.Second <= p.thresholds.TimeToFailureThreshold {
		return pred.FailureProbability >= p.thresholds.FailureProbabilityWarn
	}

	return false
}

// GetStats returns current feature statistics as JSON for debugging.
func (p *Predictor) GetStats() json.RawMessage {
	p.mu.RLock()
	defer p.mu.RUnlock()

	data, _ := json.Marshal(map[string]interface{}{
		"samples":        p.stats.samples,
		"cpu_temp_mean":  p.stats.cpuTempMean,
		"cpu_temp_std":   p.stats.cpuTempStd,
		"cpu_usage_mean": p.stats.cpuUsageMean,
		"cpu_usage_std":  p.stats.cpuUsageStd,
		"mem_usage_mean": p.stats.memUsageMean,
		"mem_usage_std":  p.stats.memUsageStd,
		"load_mean":      p.stats.loadMean,
		"load_std":       p.stats.loadStd,
	})
	return data
}
