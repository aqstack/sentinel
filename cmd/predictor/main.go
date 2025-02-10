// Command predictor runs the predictive failure engine.
// It collects metrics, runs predictions, and can trigger preemptive actions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aqstack/sentinel/pkg/collector"
	"github.com/aqstack/sentinel/pkg/config"
	"github.com/aqstack/sentinel/pkg/consensus"
	"github.com/aqstack/sentinel/pkg/health"
	"github.com/aqstack/sentinel/pkg/healthscore"
	"github.com/aqstack/sentinel/pkg/logging"
	"github.com/aqstack/sentinel/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type server struct {
	nodeName  string
	collector *collector.Collector
	predictor *healthscore.Predictor
	exporter  *metrics.Exporter
	consensus *consensus.Node
	health    *health.Checker
	log       *logging.Logger

	mu               sync.RWMutex
	latestMetrics    *collector.NodeMetrics
	latestPrediction *healthscore.Prediction
	lastCollectTime  time.Time

	// Track rate limit stats for delta calculation
	lastRateLimitDropped uint64
}

func main() {
	var (
		configFile = flag.String("config", "", "Path to config file (YAML or JSON)")
		nodeName   = flag.String("node", "", "Node name (default: hostname)")
		listenAddr = flag.String("listen", ":9101", "API listen address")
		metricsAddr = flag.String("metrics", ":9100", "Prometheus metrics listen address")
		interval   = flag.Duration("interval", time.Second, "Metrics collection interval")
		consensusAddr = flag.String("consensus-addr", "", "Consensus listen address")
		peers      = flag.String("peers", "", "Comma-separated peer addresses")
		logLevel   = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
		logFormat  = flag.String("log-format", "text", "Log format (text, json)")

		// Prediction thresholds
		warnThreshold     = flag.Float64("warn-threshold", 0.3, "Warning threshold")
		criticalThreshold = flag.Float64("critical-threshold", 0.7, "Critical threshold")
		minConfidence     = flag.Float64("min-confidence", 0.6, "Minimum confidence")

		// Risk weights
		weightThermal = flag.Float64("weight-thermal", 0.30, "Thermal weight")
		weightMemory  = flag.Float64("weight-memory", 0.20, "Memory weight")
		weightCPU     = flag.Float64("weight-cpu", 0.15, "CPU weight")
		weightDisk    = flag.Float64("weight-disk", 0.10, "Disk weight")
		weightNetwork = flag.Float64("weight-network", 0.10, "Network weight")
		weightTrend   = flag.Float64("weight-trend", 0.15, "Trend weight")

		// Rate limiting
		rateLimitEnabled = flag.Bool("rate-limit", true, "Enable rate limiting")
		rateLimitMax     = flag.Int("rate-limit-max", 100, "Max messages/sec")
		rateLimitBurst   = flag.Int("rate-limit-burst", 20, "Burst size")
	)
	flag.Parse()

	// Load config file if provided, then override with flags
	cfg := config.LoadOrDefault(*configFile)

	// Setup logging
	logCfg := &logging.Config{
		Format: *logFormat,
	}
	switch *logLevel {
	case "debug":
		logCfg.Level = logging.LevelDebug
	case "warn":
		logCfg.Level = logging.LevelWarn
	case "error":
		logCfg.Level = logging.LevelError
	default:
		logCfg.Level = logging.LevelInfo
	}
	log := logging.New(logCfg)
	logging.SetDefault(log)

	// Override config with flags if provided
	if *nodeName != "" {
		cfg.Node.Name = *nodeName
	}
	if cfg.Node.Name == "" {
		hostname, err := os.Hostname()
		if err != nil {
			log.Error("failed to get hostname", "error", err)
			os.Exit(1)
		}
		cfg.Node.Name = hostname
	}

	if *listenAddr != ":9101" {
		cfg.Server.ListenAddr = *listenAddr
	}
	if *metricsAddr != ":9100" {
		cfg.Server.MetricsAddr = *metricsAddr
	}
	if *interval != time.Second {
		cfg.Predictor.Interval = *interval
	}
	if *warnThreshold != 0.3 {
		cfg.Predictor.WarnThreshold = *warnThreshold
	}
	if *criticalThreshold != 0.7 {
		cfg.Predictor.CriticalThreshold = *criticalThreshold
	}
	if *minConfidence != 0.6 {
		cfg.Predictor.MinConfidence = *minConfidence
	}
	if *weightThermal != 0.30 {
		cfg.Predictor.RiskWeights.Thermal = *weightThermal
	}
	if *weightMemory != 0.20 {
		cfg.Predictor.RiskWeights.Memory = *weightMemory
	}
	if *weightCPU != 0.15 {
		cfg.Predictor.RiskWeights.CPU = *weightCPU
	}
	if *weightDisk != 0.10 {
		cfg.Predictor.RiskWeights.Disk = *weightDisk
	}
	if *weightNetwork != 0.10 {
		cfg.Predictor.RiskWeights.Network = *weightNetwork
	}
	if *weightTrend != 0.15 {
		cfg.Predictor.RiskWeights.Trend = *weightTrend
	}
	if *consensusAddr != "" {
		cfg.Consensus.Enabled = true
		cfg.Consensus.Addr = *consensusAddr
	}
	if *peers != "" {
		cfg.Consensus.Peers = splitPeers(*peers)
	}
	cfg.Consensus.RateLimit.Enabled = *rateLimitEnabled
	if *rateLimitMax != 100 {
		cfg.Consensus.RateLimit.MaxMessagesPerSecond = *rateLimitMax
	}
	if *rateLimitBurst != 20 {
		cfg.Consensus.RateLimit.BurstSize = *rateLimitBurst
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		log.Warn("config validation warning", "error", err)
	}

	log = log.WithNode(cfg.Node.Name)
	log.Info("starting predictor", "node", cfg.Node.Name)

	// Create collector
	c, err := collector.New(cfg.Node.Name)
	if err != nil {
		log.Error("failed to create collector", "error", err)
		os.Exit(1)
	}

	// Create predictor
	thresholds := &healthscore.PredictionThresholds{
		FailureProbabilityWarn:     cfg.Predictor.WarnThreshold,
		FailureProbabilityCritical: cfg.Predictor.CriticalThreshold,
		MinConfidence:              cfg.Predictor.MinConfidence,
		TimeToFailureThreshold:     cfg.Predictor.TimeToFailureWindow,
		RiskWeights: &healthscore.RiskWeights{
			Thermal: cfg.Predictor.RiskWeights.Thermal,
			Memory:  cfg.Predictor.RiskWeights.Memory,
			CPU:     cfg.Predictor.RiskWeights.CPU,
			Disk:    cfg.Predictor.RiskWeights.Disk,
			Network: cfg.Predictor.RiskWeights.Network,
			Trend:   cfg.Predictor.RiskWeights.Trend,
		},
	}
	predictor := healthscore.NewPredictor(cfg.Node.Name, thresholds)

	// Create metrics exporter
	exporter := metrics.NewExporter(cfg.Node.Name)
	reg := prometheus.NewRegistry()
	if err := exporter.Register(reg); err != nil {
		log.Error("failed to register metrics", "error", err)
		os.Exit(1)
	}

	// Create health checker
	healthChecker := health.NewChecker()

	s := &server{
		nodeName:  cfg.Node.Name,
		collector: c,
		predictor: predictor,
		exporter:  exporter,
		health:    healthChecker,
		log:       log.WithComponent("server"),
	}

	// Register health checks
	healthChecker.Register("collector", health.CollectorCheck(
		func() time.Time {
			s.mu.RLock()
			defer s.mu.RUnlock()
			return s.lastCollectTime
		},
		5*time.Second,
	))

	healthChecker.Register("predictor", health.PredictorCheck(
		func() int {
			stats := s.predictor.GetStats()
			var data map[string]interface{}
			json.Unmarshal(stats, &data)
			if samples, ok := data["samples"].(float64); ok {
				return int(samples)
			}
			return 0
		},
		10,
	))

	// Initialize consensus if enabled
	if cfg.Consensus.Enabled && cfg.Consensus.Addr != "" {
		consensusCfg := consensus.DefaultConfig(cfg.Node.Name)
		consensusCfg.ListenAddr = cfg.Consensus.Addr
		consensusCfg.Peers = cfg.Consensus.Peers
		consensusCfg.RateLimit = &consensus.RateLimitConfig{
			Enabled:              cfg.Consensus.RateLimit.Enabled,
			MaxMessagesPerSecond: cfg.Consensus.RateLimit.MaxMessagesPerSecond,
			BurstSize:            cfg.Consensus.RateLimit.BurstSize,
		}
		consensusCfg.DecisionCallback = s.onDecision
		consensusCfg.PartitionCallback = s.onPartitionChange

		consensusNode, err := consensus.NewNode(consensusCfg)
		if err != nil {
			log.Error("failed to create consensus node", "error", err)
			os.Exit(1)
		}
		s.consensus = consensusNode

		if err := consensusNode.Start(); err != nil {
			log.Error("failed to start consensus", "error", err)
			os.Exit(1)
		}
		log.Info("consensus enabled", "addr", cfg.Consensus.Addr)

		// Add consensus health check
		healthChecker.Register("consensus", health.ConsensusCheck(
			s.consensus.IsPartitioned,
			s.consensus.PartitionDuration,
		))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start collection loop
	go s.runLoop(ctx, cfg.Predictor.Interval)

	// Setup HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthChecker.HealthHandler())
	mux.HandleFunc("/healthz", healthChecker.LivenessHandler())
	mux.HandleFunc("/readyz", healthChecker.ReadinessHandler())
	mux.HandleFunc("/prediction", s.handlePrediction)
	mux.HandleFunc("/metrics/latest", s.handleLatestMetrics)
	mux.HandleFunc("/stats", s.handleStats)
	if s.consensus != nil {
		mux.HandleFunc("/consensus", s.handleConsensus)
		mux.HandleFunc("/decisions", s.handleDecisions)
	}

	apiServer := &http.Server{
		Addr:    cfg.Server.ListenAddr,
		Handler: mux,
	}

	go func() {
		log.Info("serving API", "addr", cfg.Server.ListenAddr)
		if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("API server error", "error", err)
		}
	}()

	// Start metrics server
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	metricsServer := &http.Server{
		Addr:    cfg.Server.MetricsAddr,
		Handler: metricsMux,
	}

	go func() {
		log.Info("serving metrics", "addr", cfg.Server.MetricsAddr)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server error", "error", err)
		}
	}()

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info("shutting down")
	cancel()

	if s.consensus != nil {
		s.consensus.Stop()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	apiServer.Shutdown(shutdownCtx)
	metricsServer.Shutdown(shutdownCtx)
}

func (s *server) runLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m, err := s.collector.Collect(ctx)
			if err != nil {
				s.log.Error("collection error", "error", err)
				continue
			}

			s.exporter.Update(m)
			s.predictor.AddSample(m)

			pred, err := s.predictor.Predict(ctx, m)
			if err != nil {
				s.log.Error("prediction error", "error", err)
				continue
			}

			s.exporter.UpdatePrediction(
				pred.FailureProbability,
				pred.Confidence,
				pred.TimeToFailure,
			)

			s.mu.Lock()
			s.latestMetrics = m
			s.latestPrediction = pred
			s.lastCollectTime = time.Now()
			s.mu.Unlock()

			if s.consensus != nil {
				s.exporter.UpdatePartitionStatus(
					s.consensus.IsPartitioned(),
					s.consensus.PartitionDuration().Seconds(),
					s.consensus.IsLeader(),
					s.consensus.Term(),
				)

				stats := s.consensus.GetRateLimitStats()
				droppedDelta := stats.DroppedMessages - s.lastRateLimitDropped
				s.lastRateLimitDropped = stats.DroppedMessages
				s.exporter.UpdateRateLimitStats(droppedDelta, stats.Enabled)
			}

			if s.predictor.ShouldMigrate(pred) {
				s.log.Warn("migration recommended",
					"probability", pred.FailureProbability,
					"confidence", pred.Confidence,
					"ttf", pred.TimeToFailure,
					"reasons", pred.Reasons,
				)

				if s.consensus != nil && s.consensus.IsPartitioned() && s.consensus.IsLeader() {
					s.proposeAutonomousAction(ctx, pred)
				}
			}
		}
	}
}

func (s *server) proposeAutonomousAction(ctx context.Context, pred *healthscore.Prediction) {
	payload, _ := json.Marshal(map[string]interface{}{
		"node":        s.nodeName,
		"probability": pred.FailureProbability,
		"reasons":     pred.Reasons,
		"action":      "cordon_and_drain",
	})

	decision, err := s.consensus.ProposeDecision(ctx, consensus.DecisionNodeCordon, payload)
	if err != nil {
		s.log.Error("failed to propose decision", "error", err)
		return
	}

	s.log.Info("autonomous decision committed", "id", decision.ID)
	s.exporter.RecordAutonomousDecision(string(decision.Type))
}

func (s *server) onDecision(d consensus.Decision) {
	s.log.Info("decision committed", "type", d.Type, "id", d.ID)
}

func (s *server) onPartitionChange(partitioned bool) {
	if partitioned {
		s.log.Warn("partition detected, entering autonomous mode")
	} else {
		s.log.Info("partition healed, reconnected to cluster")
		s.exporter.RecordReconciliation("success")
	}
}

func (s *server) handlePrediction(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	pred := s.latestPrediction
	s.mu.RUnlock()

	if pred == nil {
		http.Error(w, "no prediction available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pred)
}

func (s *server) handleLatestMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	m := s.latestMetrics
	s.mu.RUnlock()

	if m == nil {
		http.Error(w, "no metrics available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := s.predictor.GetStats()
	w.Header().Set("Content-Type", "application/json")
	w.Write(stats)
}

func (s *server) handleConsensus(w http.ResponseWriter, r *http.Request) {
	if s.consensus == nil {
		http.Error(w, "consensus not enabled", http.StatusNotFound)
		return
	}

	status := map[string]interface{}{
		"node_id":            s.nodeName,
		"state":              s.consensus.State().String(),
		"term":               s.consensus.Term(),
		"is_leader":          s.consensus.IsLeader(),
		"leader_id":          s.consensus.LeaderID(),
		"partitioned":        s.consensus.IsPartitioned(),
		"partition_duration": s.consensus.PartitionDuration().String(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *server) handleDecisions(w http.ResponseWriter, r *http.Request) {
	if s.consensus == nil {
		http.Error(w, "consensus not enabled", http.StatusNotFound)
		return
	}

	decisions := s.consensus.GetDecisions()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(decisions)
}

func splitPeers(s string) []string {
	if s == "" {
		return nil
	}
	var peers []string
	for _, peer := range strings.Split(s, ",") {
		peer = strings.TrimSpace(peer)
		if peer != "" {
			peers = append(peers, peer)
		}
	}
	return peers
}
