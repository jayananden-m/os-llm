<h1 align="center">llmctl</h1>

<p align="center">
    An inference platform for serving LLMs efficiently and observably.
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-00ADD8" />
  <img src="https://img.shields.io/badge/Python-3776AB" />
  <img src="https://img.shields.io/badge/vLLM-7C3AED" />
  <img src="https://img.shields.io/badge/Kubernetes-326CE5" />
  <img src="https://img.shields.io/badge/Prometheus-E6522C" />
  <img src="https://img.shields.io/badge/Grafana-F46800" />
  <img src="https://img.shields.io/badge/OpenTelemetry-000000" />
</p>



## Core Components

### Control Plane
- Go service: inference server lifecycle management (deploying, scaling, swapping)
- Go CLI: operator interface for the platform

### Inference
- vLLM instances: serving models via API

### Infrastructure
- K8s: workload orchestration

### Observability
- Prometheus: metrics
- Grafana: dashboards
- OpenTelemetry: traces

![alt concept overview](images/concept.png "Overview Concept")

## Prerequisites
## Installation
## Usage
## Benchmarks
## Roadmap
## Learnings

