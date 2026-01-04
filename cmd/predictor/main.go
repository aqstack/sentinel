// Command predictor runs the predictive failure engine.
// It collects metrics, runs predictions, and can trigger preemptive actions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aqstack/sentinel/pkg/collector"
	"github.com/aqstack/sentinel/pkg/consensus"
	"github.com/aqstack/sentinel/pkg/healthscore"
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

	mu               sync.RWMutex
	latestMetrics    *collector.NodeMetrics
	latestPrediction *healthscore.Prediction
}

func main() {
	var (
		nodeName      = flag.String("node", "", "Node name (default: hostname)")
		listenAddr    = flag.String("listen", ":9101", "API listen address")
		metricsAddr   = flag.String("metrics", ":9100", "Prometheus metrics listen address")
		interval      = flag.Duration("interval", time.Second, "Metrics collection interval")
		consensusAddr = flag.String("consensus-addr", "", "Consensus listen address (enables partition handling)")
		peers         = flag.String("peers", "", "Comma-separated list of peer addresses for consensus")

		// Prediction thresholds
		warnThreshold     = flag.Float64("warn-threshold", 0.3, "Failure probability warning threshold")
		criticalThreshold = flag.Float64("critical-threshold", 0.7, "Failure probability critical threshold")
		minConfidence     = flag.Float64("min-confidence", 0.6, "Minimum confidence to act on predictions")
	)
	flag.Parse()

	// Get node name
	if *nodeName == "" {
		hostname, err := os.Hostname()
		if err != nil {
			log.Fatalf("Failed to get hostname: %v", err)
		}
		*nodeName = hostname
	}

	log.Printf("Starting AEOP predictor on node %s", *nodeName)

	// Create collector
	c, err := collector.New(*nodeName)
	if err != nil {
		log.Fatalf("Failed to create collector: %v", err)
	}

	// Create predictor with thresholds
	thresholds := &healthscore.PredictionThresholds{
		FailureProbabilityWarn:     *warnThreshold,
		FailureProbabilityCritical: *criticalThreshold,
		MinConfidence:              *minConfidence,
		TimeToFailureThreshold:     15 * time.Minute,
	}
	predictor := healthscore.NewPredictor(*nodeName, thresholds)

	// Create metrics exporter
	exporter := metrics.NewExporter(*nodeName)
	reg := prometheus.NewRegistry()
	if err := exporter.Register(reg); err != nil {
		log.Fatalf("Failed to register metrics: %v", err)
	}

	s := &server{
		nodeName:  *nodeName,
		collector: c,
		predictor: predictor,
		exporter:  exporter,
	}

	// Initialize consensus if configured
	if *consensusAddr != "" {
		config := consensus.DefaultConfig(*nodeName)
		config.ListenAddr = *consensusAddr
		if *peers != "" {
			config.Peers = splitPeers(*peers)
		}
		config.DecisionCallback = s.onDecision
		config.PartitionCallback = s.onPartitionChange

		consensusNode, err := consensus.NewNode(config)
		if err != nil {
			log.Fatalf("Failed to create consensus node: %v", err)
		}
		s.consensus = consensusNode

		if err := consensusNode.Start(); err != nil {
			log.Fatalf("Failed to start consensus: %v", err)
		}
		log.Printf("Consensus enabled, listening on %s", *consensusAddr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start collection and prediction loop
	go s.runLoop(ctx, *interval)

	// Start API server
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/prediction", s.handlePrediction)
	mux.HandleFunc("/metrics/latest", s.handleLatestMetrics)
	mux.HandleFunc("/stats", s.handleStats)
	if s.consensus != nil {
		mux.HandleFunc("/consensus", s.handleConsensus)
		mux.HandleFunc("/decisions", s.handleDecisions)
	}

	apiServer := &http.Server{
		Addr:    *listenAddr,
		Handler: mux,
	}

	go func() {
		log.Printf("Serving API on %s", *listenAddr)
		if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("API server error: %v", err)
		}
	}()

	// Start metrics server
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	metricsServer := &http.Server{
		Addr:    *metricsAddr,
		Handler: metricsMux,
	}

	go func() {
		log.Printf("Serving Prometheus metrics on %s/metrics", *metricsAddr)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Metrics server error: %v", err)
		}
	}()

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")
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
			// Collect metrics
			m, err := s.collector.Collect(ctx)
			if err != nil {
				log.Printf("Collection error: %v", err)
				continue
			}

			// Update exporter
			s.exporter.Update(m)

			// Add to predictor history
			s.predictor.AddSample(m)

			// Run prediction
			pred, err := s.predictor.Predict(ctx, m)
			if err != nil {
				log.Printf("Prediction error: %v", err)
				continue
			}

			// Update prediction metrics
			s.exporter.UpdatePrediction(
				pred.FailureProbability,
				pred.Confidence,
				pred.TimeToFailure,
			)

			// Store latest values
			s.mu.Lock()
			s.latestMetrics = m
			s.latestPrediction = pred
			s.mu.Unlock()

			// Update consensus/partition metrics if enabled
			if s.consensus != nil {
				s.exporter.UpdatePartitionStatus(
					s.consensus.IsPartitioned(),
					s.consensus.PartitionDuration().Seconds(),
					s.consensus.IsLeader(),
					s.consensus.Term(),
				)
			}

			// Check if action needed
			if s.predictor.ShouldMigrate(pred) {
				log.Printf("ALERT: Migration recommended - probability=%.2f confidence=%.2f ttf=%.0fs reasons=%v",
					pred.FailureProbability, pred.Confidence, pred.TimeToFailure, pred.Reasons)

				// If partitioned and we're leader, make autonomous decision
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
		log.Printf("Failed to propose autonomous decision: %v", err)
		return
	}

	log.Printf("Autonomous decision committed: %s", decision.ID)
	s.exporter.RecordAutonomousDecision(string(decision.Type))
}

func (s *server) onDecision(d consensus.Decision) {
	log.Printf("Decision committed: type=%s id=%s", d.Type, d.ID)
}

func (s *server) onPartitionChange(partitioned bool) {
	if partitioned {
		log.Printf("PARTITION DETECTED: Entering autonomous mode")
	} else {
		log.Printf("PARTITION HEALED: Reconnected to cluster")
		s.exporter.RecordReconciliation("success")
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
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
