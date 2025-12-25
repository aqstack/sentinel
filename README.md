# Sentinel

A lightweight agent for Kubernetes edge nodes that provides **predictive failure detection** and **partition-resilient orchestration**.

## Novel Contributions

### 1. Partition-Resilient Edge Orchestration
When edge nodes lose connectivity to the Kubernetes control plane, they don't just wait—they form a local consensus group and make autonomous decisions:
- Lightweight Raft-inspired consensus (Raft-lite) optimized for 3-10 node clusters
- Autonomous workload management during partitions
- Graceful reconciliation when connectivity is restored

### 2. Predictive Edge Orchestration
ML-based failure prediction optimized for resource-constrained environments:
- Thermal pattern analysis with trend detection
- Memory pressure and OOM prediction
- Preemptive workload migration before failures occur
- Statistical models that run in <64MB RAM

## Why This Matters

**Kubernetes assumes control plane availability.** When edge nodes lose connectivity:
- Nodes go "Unknown" status
- No new scheduling decisions
- Existing workloads continue but can't recover from failures

**Cloud AIOps assumes abundant resources.** Edge environments have:
- 4GB RAM running K8s + workloads + monitoring
- No datacenter cooling (thermal throttling is common)
- Unreliable power and network

Sentinel bridges this gap with autonomous, predictive edge operations.

## Quick Start

```bash
# Build
make build

# Run locally (collects metrics from host)
./bin/predictor -node=my-node

# Deploy to K3s cluster
helm install sentinel ./deploy/helm/sentinel
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Per-Node Components                       │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐   │
│  │  Telemetry   │  │  Predictor   │  │  Consensus Node  │   │
│  │  Collector   │──▶│  (ML Model)  │──▶│  (Raft-lite)    │   │
│  └──────────────┘  └──────────────┘  └──────────────────┘   │
│         │                  │                   │             │
│         ▼                  ▼                   ▼             │
│  ┌─────────────────────────────────────────────────────┐    │
│  │              Prometheus Metrics Export               │    │
│  └─────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
```

## Components

### Telemetry Collector (`cmd/telemetry`)
Lightweight metrics collection from Linux `/proc` and `/sys`:
- CPU: temperature, frequency, throttling, usage
- Memory: usage, available, swap, OOM events
- Disk: I/O latency, throughput
- Network: errors, latency

### Predictor (`cmd/predictor`)
Failure prediction engine:
- Collects metrics and maintains history
- Runs statistical prediction model
- Exposes predictions via API and Prometheus
- Triggers autonomous actions during partitions

### Consensus (`pkg/consensus`)
Raft-lite implementation for edge:
- Leader election with randomized timeouts
- Decision replication with quorum
- Partition detection and autonomous mode
- Decision log for reconciliation

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /health` | Health check |
| `GET /prediction` | Latest failure prediction |
| `GET /metrics/latest` | Latest raw metrics |
| `GET /consensus` | Consensus status (if enabled) |
| `GET /decisions` | Committed autonomous decisions |
| `GET /metrics` | Prometheus metrics (port 9100) |

## Prometheus Metrics

### Prediction Metrics
- `sentinel_prediction_failure_probability` - Predicted failure probability (0-1)
- `sentinel_prediction_confidence` - Prediction confidence level
- `sentinel_prediction_time_to_failure_seconds` - Estimated time until failure
- `sentinel_prediction_preemptive_migrations_total` - Migration count

### Partition Metrics
- `sentinel_partition_detected` - Partition status (0/1)
- `sentinel_partition_duration_seconds` - Current partition duration
- `sentinel_consensus_is_leader` - Leader status
- `sentinel_partition_autonomous_decisions_total` - Autonomous decisions made

## Configuration

```bash
./bin/predictor \
  --node=edge-node-1 \
  --listen=:9101 \
  --metrics=:9100 \
  --interval=1s \
  --warn-threshold=0.3 \
  --critical-threshold=0.7 \
  --consensus-addr=:9200 \
  --peers=edge-node-2:9200,edge-node-3:9200
```

## Development

```bash
# Run tests
make test

# Build for ARM64 (edge devices)
make build-arm64

# Build Docker images
make docker-build
```

## Research Context

This project explores the intersection of:
1. **AIOps** - ML-based operations, typically cloud-focused
2. **Edge Computing** - Resource-constrained, partition-prone environments
3. **Distributed Consensus** - Adapted for small, unreliable clusters

The combination is underexplored: cloud AIOps assumes resources; edge research focuses on deployment.

## License

MIT License
