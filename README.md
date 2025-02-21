# Sentinel

Predictive failure detection and autonomous orchestration for Kubernetes edge nodes.

## What it does

Sentinel runs on each edge node and does three things:

1. **Predicts failures** - Monitors CPU thermals, memory pressure, disk I/O, and network health. Uses lightweight statistical models to predict failures before they happen.

2. **Survives partitions** - When nodes lose contact with the control plane, they form a local consensus group and continue operating autonomously.

3. **Takes action** - Triggers preemptive workload migrations, cordons unhealthy nodes, and logs decisions for reconciliation when connectivity returns.

## Why

Kubernetes assumes the control plane is always reachable. Edge environments break this assumption constantly - spotty networks, power issues, harsh conditions. When a node goes "Unknown", Kubernetes stops making decisions for it.

Cloud AIOps tools assume unlimited resources. Edge nodes run K8s + workloads + monitoring in 4GB RAM with no datacenter cooling.

Sentinel fills the gap.

## What's different

Most edge/IoT platforms focus on deployment and connectivity. Most AIOps tools assume cloud-scale resources. Sentinel combines ideas from both:

- **Predictive, not reactive** - Catches thermal throttling and memory pressure before they cause failures
- **Autonomous, not dependent** - Continues operating during control plane partitions instead of going dark
- **Lightweight, not bloated** - Runs statistical models in <64MB RAM, no GPUs or cloud inference needed
- **Consensus-aware** - Nodes coordinate decisions locally using Raft-lite when disconnected

## Quick Start

```bash
# Build
make build

# Run locally
./bin/predictor --node=my-node

# With config file
./bin/predictor --config=config.yaml

# Deploy to cluster
helm install sentinel ./deploy/helm/sentinel
```

## Configuration

Sentinel supports YAML/JSON config files with CLI flag overrides:

```yaml
node:
  name: edge-node-1

server:
  listen: ":9101"
  metrics: ":9100"

predictor:
  interval: 1s
  warn_threshold: 0.3
  critical_threshold: 0.7
  risk_weights:
    thermal: 0.3
    memory: 0.3
    disk: 0.2
    network: 0.2

consensus:
  enabled: true
  addr: ":9200"
  peers:
    - edge-node-2:9200
    - edge-node-3:9200

logging:
  level: info
  format: json
```

CLI flags:

```bash
--config          Config file path
--node            Node name
--listen          API listen address (default :9101)
--metrics         Prometheus metrics address (default :9100)
--interval        Collection interval (default 1s)
--log-level       Log level: debug, info, warn, error
--log-format      Log format: text, json
--consensus-addr  Consensus listen address
--peers           Comma-separated peer addresses
```

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│                        Sentinel                          │
│                                                          │
│  ┌────────────┐  ┌────────────┐  ┌───────────────────┐   │
│  │ Collector  │─▶│ Predictor  │─▶│ Consensus (Raft)  │   │
│  │            │  │            │  │                   │   │
│  │ /proc, /sys│  │ ML model   │  │ Leader election   │   │
│  │ thermals   │  │ risk calc  │  │ Decision log      │   │
│  └────────────┘  └────────────┘  └───────────────────┘   │
│        │               │                  │              │
│        └───────────────┼──────────────────┘              │
│                        ▼                                 │
│            ┌─────────────────────┐                       │
│            │ Prometheus Metrics  │                       │
│            │ Health Endpoints    │                       │
│            │ K8s Client          │                       │
│            └─────────────────────┘                       │
└──────────────────────────────────────────────────────────┘
```

## API

| Endpoint | Description |
|----------|-------------|
| `/health` | Detailed health status with component checks |
| `/healthz` | Liveness probe |
| `/readyz` | Readiness probe |
| `/prediction` | Current failure prediction |
| `/metrics/latest` | Raw metrics snapshot |
| `/consensus` | Consensus state and peers |
| `/decisions` | Autonomous decisions log |
| `/metrics` | Prometheus metrics |

## Metrics

Prediction:
- `sentinel_prediction_failure_probability` - 0 to 1
- `sentinel_prediction_confidence` - model confidence
- `sentinel_prediction_time_to_failure_seconds` - estimated TTF
- `sentinel_prediction_preemptive_migrations_total` - migration count

Partition:
- `sentinel_partition_detected` - 0 or 1
- `sentinel_partition_duration_seconds` - how long partitioned
- `sentinel_consensus_is_leader` - leader status
- `sentinel_partition_autonomous_decisions_total` - decisions made offline

Rate limiting:
- `sentinel_ratelimit_dropped_total` - messages dropped by rate limiter
- `sentinel_ratelimit_enabled` - rate limiter state

## Key Features

**Circuit Breaker** - Protects against cascading failures when the K8s API is unreachable. Configurable thresholds with automatic recovery.

**Exponential Backoff** - Peer reconnection uses backoff with jitter to prevent thundering herd during network recovery.

**Rate Limiting** - Token bucket rate limiter on consensus messages to handle bursty traffic.

**Structured Logging** - JSON or text output with log levels, component tags, and context propagation.

**Health Checks** - Kubernetes-native liveness and readiness probes with detailed component status.

## Development

```bash
make test          # Run tests
make build         # Build for current platform
make build-arm64   # Build for ARM64 edge devices
make docker-build  # Build container image
```

Requires Go 1.21+.

## License

MIT
