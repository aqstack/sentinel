// Package metrics provides Prometheus metrics export for Sentinel telemetry.
package metrics

import (
	"strings"

	"github.com/aqstack/sentinel/pkg/collector"
	"github.com/prometheus/client_golang/prometheus"
)

// Exporter exports node metrics to Prometheus.
type Exporter struct {
	nodeName string

	// CPU metrics
	cpuTemperature *prometheus.GaugeVec
	cpuUsage       *prometheus.GaugeVec
	cpuThrottled   *prometheus.GaugeVec
	cpuFrequency   *prometheus.GaugeVec
	loadAverage    *prometheus.GaugeVec

	// Memory metrics
	memoryTotal     *prometheus.GaugeVec
	memoryAvailable *prometheus.GaugeVec
	memoryUsage     *prometheus.GaugeVec
	swapTotal       *prometheus.GaugeVec
	swapUsed        *prometheus.GaugeVec
	oomKillCount    *prometheus.CounterVec

	// Disk metrics
	diskTotal     *prometheus.GaugeVec
	diskUsed      *prometheus.GaugeVec
	diskUsage     *prometheus.GaugeVec
	diskIORead    *prometheus.CounterVec
	diskIOWrite   *prometheus.CounterVec
	diskIOLatency *prometheus.GaugeVec

	// Network metrics
	networkRxBytes  *prometheus.CounterVec
	networkTxBytes  *prometheus.CounterVec
	networkRxErrors *prometheus.CounterVec
	networkTxErrors *prometheus.CounterVec
	networkLatency  *prometheus.GaugeVec

	// Partition/consensus metrics (novel contribution)
	partitionDetected    *prometheus.GaugeVec
	partitionDuration    *prometheus.GaugeVec
	consensusLeader      *prometheus.GaugeVec
	consensusTerm        *prometheus.GaugeVec
	autonomousDecisions  *prometheus.CounterVec
	reconciliationEvents *prometheus.CounterVec

	// Prediction metrics
	failureProbability     *prometheus.GaugeVec
	predictionConfidence   *prometheus.GaugeVec
	predictedTimeToFailure *prometheus.GaugeVec
	preemptiveMigrations   *prometheus.CounterVec

	// Collection metadata
	collectionDuration *prometheus.GaugeVec
	collectionErrors   *prometheus.CounterVec
}

// NewExporter creates a new Prometheus exporter for node metrics.
func NewExporter(nodeName string) *Exporter {
	labels := []string{"node"}

	e := &Exporter{
		nodeName: nodeName,

		// CPU metrics
		cpuTemperature: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "cpu",
			Name:      "temperature_celsius",
			Help:      "CPU temperature in Celsius",
		}, labels),
		cpuUsage: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "cpu",
			Name:      "usage_percent",
			Help:      "CPU usage percentage",
		}, labels),
		cpuThrottled: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "cpu",
			Name:      "throttled",
			Help:      "CPU thermal throttling status (1 = throttled)",
		}, labels),
		cpuFrequency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "cpu",
			Name:      "frequency_mhz",
			Help:      "Current CPU frequency in MHz",
		}, labels),
		loadAverage: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "cpu",
			Name:      "load_average",
			Help:      "System load average",
		}, append(labels, "period")),

		// Memory metrics
		memoryTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "memory",
			Name:      "total_bytes",
			Help:      "Total memory in bytes",
		}, labels),
		memoryAvailable: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "memory",
			Name:      "available_bytes",
			Help:      "Available memory in bytes",
		}, labels),
		memoryUsage: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "memory",
			Name:      "usage_percent",
			Help:      "Memory usage percentage",
		}, labels),
		swapTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "memory",
			Name:      "swap_total_bytes",
			Help:      "Total swap in bytes",
		}, labels),
		swapUsed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "memory",
			Name:      "swap_used_bytes",
			Help:      "Used swap in bytes",
		}, labels),
		oomKillCount: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "memory",
			Name:      "oom_kill_total",
			Help:      "Total OOM kill events",
		}, labels),

		// Disk metrics
		diskTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "disk",
			Name:      "total_bytes",
			Help:      "Total disk space in bytes",
		}, labels),
		diskUsed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "disk",
			Name:      "used_bytes",
			Help:      "Used disk space in bytes",
		}, labels),
		diskUsage: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "disk",
			Name:      "usage_percent",
			Help:      "Disk usage percentage",
		}, labels),
		diskIORead: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "disk",
			Name:      "io_read_bytes_total",
			Help:      "Total bytes read from disk",
		}, labels),
		diskIOWrite: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "disk",
			Name:      "io_write_bytes_total",
			Help:      "Total bytes written to disk",
		}, labels),
		diskIOLatency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "disk",
			Name:      "io_latency_ms",
			Help:      "Disk I/O latency in milliseconds",
		}, labels),

		// Network metrics
		networkRxBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "network",
			Name:      "rx_bytes_total",
			Help:      "Total bytes received",
		}, labels),
		networkTxBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "network",
			Name:      "tx_bytes_total",
			Help:      "Total bytes transmitted",
		}, labels),
		networkRxErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "network",
			Name:      "rx_errors_total",
			Help:      "Total receive errors",
		}, labels),
		networkTxErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "network",
			Name:      "tx_errors_total",
			Help:      "Total transmit errors",
		}, labels),
		networkLatency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "network",
			Name:      "latency_ms",
			Help:      "Network latency in milliseconds",
		}, labels),

		// Partition/consensus metrics - NOVEL CONTRIBUTION
		partitionDetected: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "partition",
			Name:      "detected",
			Help:      "Network partition detected (1 = partitioned from control plane)",
		}, labels),
		partitionDuration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "partition",
			Name:      "duration_seconds",
			Help:      "Duration of current network partition",
		}, labels),
		consensusLeader: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "consensus",
			Name:      "is_leader",
			Help:      "Whether this node is the local consensus leader (1 = leader)",
		}, labels),
		consensusTerm: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "consensus",
			Name:      "term",
			Help:      "Current Raft-lite consensus term",
		}, labels),
		autonomousDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "partition",
			Name:      "autonomous_decisions_total",
			Help:      "Total autonomous decisions made during partition",
		}, append(labels, "decision_type")),
		reconciliationEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "partition",
			Name:      "reconciliation_events_total",
			Help:      "Total reconciliation events after partition heal",
		}, append(labels, "result")),

		// Prediction metrics - NOVEL CONTRIBUTION
		failureProbability: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "prediction",
			Name:      "failure_probability",
			Help:      "Predicted probability of node failure (0-1)",
		}, labels),
		predictionConfidence: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "prediction",
			Name:      "confidence",
			Help:      "Confidence level of failure prediction (0-1)",
		}, labels),
		predictedTimeToFailure: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "prediction",
			Name:      "time_to_failure_seconds",
			Help:      "Predicted seconds until failure (-1 if no failure predicted)",
		}, labels),
		preemptiveMigrations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "prediction",
			Name:      "preemptive_migrations_total",
			Help:      "Total preemptive workload migrations triggered by predictions",
		}, append(labels, "reason")),

		// Collection metadata
		collectionDuration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sentinel",
			Subsystem: "collector",
			Name:      "duration_ms",
			Help:      "Time taken to collect metrics in milliseconds",
		}, labels),
		collectionErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sentinel",
			Subsystem: "collector",
			Name:      "errors_total",
			Help:      "Total metric collection errors",
		}, append(labels, "subsystem")),
	}

	return e
}

// Register registers all metrics with the Prometheus registry.
func (e *Exporter) Register(reg prometheus.Registerer) error {
	collectors := []prometheus.Collector{
		e.cpuTemperature,
		e.cpuUsage,
		e.cpuThrottled,
		e.cpuFrequency,
		e.loadAverage,
		e.memoryTotal,
		e.memoryAvailable,
		e.memoryUsage,
		e.swapTotal,
		e.swapUsed,
		e.oomKillCount,
		e.diskTotal,
		e.diskUsed,
		e.diskUsage,
		e.diskIORead,
		e.diskIOWrite,
		e.diskIOLatency,
		e.networkRxBytes,
		e.networkTxBytes,
		e.networkRxErrors,
		e.networkTxErrors,
		e.networkLatency,
		e.partitionDetected,
		e.partitionDuration,
		e.consensusLeader,
		e.consensusTerm,
		e.autonomousDecisions,
		e.reconciliationEvents,
		e.failureProbability,
		e.predictionConfidence,
		e.predictedTimeToFailure,
		e.preemptiveMigrations,
		e.collectionDuration,
		e.collectionErrors,
	}

	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			return err
		}
	}

	return nil
}

// Update updates all metrics from a collected NodeMetrics snapshot.
func (e *Exporter) Update(m *collector.NodeMetrics) {
	node := e.nodeName

	// CPU metrics
	e.cpuTemperature.WithLabelValues(node).Set(m.CPUTemperature)
	e.cpuUsage.WithLabelValues(node).Set(m.CPUUsagePercent)
	if m.CPUThrottled {
		e.cpuThrottled.WithLabelValues(node).Set(1)
	} else {
		e.cpuThrottled.WithLabelValues(node).Set(0)
	}
	e.cpuFrequency.WithLabelValues(node).Set(m.CPUFrequencyMHz)
	e.loadAverage.WithLabelValues(node, "1m").Set(m.LoadAverage1Min)
	e.loadAverage.WithLabelValues(node, "5m").Set(m.LoadAverage5Min)
	e.loadAverage.WithLabelValues(node, "15m").Set(m.LoadAverage15Min)

	// Memory metrics
	e.memoryTotal.WithLabelValues(node).Set(float64(m.MemoryTotalBytes))
	e.memoryAvailable.WithLabelValues(node).Set(float64(m.MemoryAvailableBytes))
	e.memoryUsage.WithLabelValues(node).Set(m.MemoryUsagePercent)
	e.swapTotal.WithLabelValues(node).Set(float64(m.SwapTotalBytes))
	e.swapUsed.WithLabelValues(node).Set(float64(m.SwapUsedBytes))

	// Disk metrics
	e.diskTotal.WithLabelValues(node).Set(float64(m.DiskTotalBytes))
	e.diskUsed.WithLabelValues(node).Set(float64(m.DiskUsedBytes))
	e.diskUsage.WithLabelValues(node).Set(m.DiskUsagePercent)
	e.diskIOLatency.WithLabelValues(node).Set(m.DiskIOLatencyMs)

	// Network metrics
	e.networkLatency.WithLabelValues(node).Set(m.NetworkLatencyMs)

	// Collection metadata
	e.collectionDuration.WithLabelValues(node).Set(m.CollectionDurationMs)
	for _, errMsg := range m.Errors {
		// Extract subsystem from error message
		subsystem := "unknown"
		if len(errMsg) > 0 {
			parts := strings.SplitN(errMsg, ":", 2)
			if len(parts) > 0 {
				subsystem = parts[0]
			}
		}
		e.collectionErrors.WithLabelValues(node, subsystem).Inc()
	}
}

// UpdatePartitionStatus updates partition-related metrics.
func (e *Exporter) UpdatePartitionStatus(detected bool, durationSec float64, isLeader bool, term uint64) {
	node := e.nodeName

	if detected {
		e.partitionDetected.WithLabelValues(node).Set(1)
	} else {
		e.partitionDetected.WithLabelValues(node).Set(0)
	}
	e.partitionDuration.WithLabelValues(node).Set(durationSec)

	if isLeader {
		e.consensusLeader.WithLabelValues(node).Set(1)
	} else {
		e.consensusLeader.WithLabelValues(node).Set(0)
	}
	e.consensusTerm.WithLabelValues(node).Set(float64(term))
}

// UpdatePrediction updates prediction-related metrics.
func (e *Exporter) UpdatePrediction(probability, confidence, timeToFailure float64) {
	node := e.nodeName

	e.failureProbability.WithLabelValues(node).Set(probability)
	e.predictionConfidence.WithLabelValues(node).Set(confidence)
	e.predictedTimeToFailure.WithLabelValues(node).Set(timeToFailure)
}

// RecordAutonomousDecision records an autonomous decision made during partition.
func (e *Exporter) RecordAutonomousDecision(decisionType string) {
	e.autonomousDecisions.WithLabelValues(e.nodeName, decisionType).Inc()
}

// RecordReconciliation records a reconciliation event after partition heals.
func (e *Exporter) RecordReconciliation(result string) {
	e.reconciliationEvents.WithLabelValues(e.nodeName, result).Inc()
}

// RecordPreemptiveMigration records a preemptive migration triggered by prediction.
func (e *Exporter) RecordPreemptiveMigration(reason string) {
	e.preemptiveMigrations.WithLabelValues(e.nodeName, reason).Inc()
}
