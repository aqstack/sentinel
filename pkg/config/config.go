// Package config provides configuration loading from files and flags.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the predictor.
type Config struct {
	Node      NodeConfig      `json:"node" yaml:"node"`
	Server    ServerConfig    `json:"server" yaml:"server"`
	Predictor PredictorConfig `json:"predictor" yaml:"predictor"`
	Consensus ConsensusConfig `json:"consensus" yaml:"consensus"`
	Logging   LoggingConfig   `json:"logging" yaml:"logging"`
}

// NodeConfig holds node identification settings.
type NodeConfig struct {
	Name string `json:"name" yaml:"name"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	ListenAddr  string `json:"listen_addr" yaml:"listen_addr"`
	MetricsAddr string `json:"metrics_addr" yaml:"metrics_addr"`
}

// PredictorConfig holds prediction engine settings.
type PredictorConfig struct {
	Interval              time.Duration `json:"interval" yaml:"interval"`
	WarnThreshold         float64       `json:"warn_threshold" yaml:"warn_threshold"`
	CriticalThreshold     float64       `json:"critical_threshold" yaml:"critical_threshold"`
	MinConfidence         float64       `json:"min_confidence" yaml:"min_confidence"`
	TimeToFailureWindow   time.Duration `json:"time_to_failure_window" yaml:"time_to_failure_window"`
	RiskWeights           RiskWeights   `json:"risk_weights" yaml:"risk_weights"`
}

// RiskWeights holds risk factor weights.
type RiskWeights struct {
	Thermal float64 `json:"thermal" yaml:"thermal"`
	Memory  float64 `json:"memory" yaml:"memory"`
	CPU     float64 `json:"cpu" yaml:"cpu"`
	Disk    float64 `json:"disk" yaml:"disk"`
	Network float64 `json:"network" yaml:"network"`
	Trend   float64 `json:"trend" yaml:"trend"`
}

// ConsensusConfig holds consensus/partition settings.
type ConsensusConfig struct {
	Enabled   bool        `json:"enabled" yaml:"enabled"`
	Addr      string      `json:"addr" yaml:"addr"`
	Peers     []string    `json:"peers" yaml:"peers"`
	RateLimit RateLimitConfig `json:"rate_limit" yaml:"rate_limit"`
}

// RateLimitConfig holds rate limiting settings.
type RateLimitConfig struct {
	Enabled              bool `json:"enabled" yaml:"enabled"`
	MaxMessagesPerSecond int  `json:"max_messages_per_second" yaml:"max_messages_per_second"`
	BurstSize            int  `json:"burst_size" yaml:"burst_size"`
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level  string `json:"level" yaml:"level"`
	Format string `json:"format" yaml:"format"`
}

// Default returns the default configuration.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			ListenAddr:  ":9101",
			MetricsAddr: ":9100",
		},
		Predictor: PredictorConfig{
			Interval:            time.Second,
			WarnThreshold:       0.3,
			CriticalThreshold:   0.7,
			MinConfidence:       0.6,
			TimeToFailureWindow: 15 * time.Minute,
			RiskWeights: RiskWeights{
				Thermal: 0.30,
				Memory:  0.20,
				CPU:     0.15,
				Disk:    0.10,
				Network: 0.10,
				Trend:   0.15,
			},
		},
		Consensus: ConsensusConfig{
			RateLimit: RateLimitConfig{
				Enabled:              true,
				MaxMessagesPerSecond: 100,
				BurstSize:            20,
			},
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "text",
		},
	}
}

// Load loads configuration from a file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := Default()

	ext := filepath.Ext(path)
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse yaml: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported config format: %s", ext)
	}

	return cfg, nil
}

// LoadOrDefault loads config from file if it exists, otherwise returns default.
func LoadOrDefault(path string) *Config {
	if path == "" {
		return Default()
	}
	cfg, err := Load(path)
	if err != nil {
		return Default()
	}
	return cfg
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	weights := c.Predictor.RiskWeights
	sum := weights.Thermal + weights.Memory + weights.CPU + weights.Disk + weights.Network + weights.Trend
	if sum < 0.99 || sum > 1.01 {
		return fmt.Errorf("risk weights sum to %.2f (should be 1.0)", sum)
	}

	if c.Predictor.WarnThreshold >= c.Predictor.CriticalThreshold {
		return fmt.Errorf("warn threshold (%.2f) must be less than critical threshold (%.2f)",
			c.Predictor.WarnThreshold, c.Predictor.CriticalThreshold)
	}

	return nil
}
