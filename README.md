# Tilcayo

Tilcayo is a self-hostable, stateless SQL query engine platform. You submit a SQL query and an engine type via a REST API. A Go control plane selects the appropriate worker image, spins up an ephemeral job, executes the query against object-storage-hosted files, writes the result back to object storage, and tears the worker down. No persistent database. No idle compute.

It runs in two modes: **Docker compose** for a single server, and **K3s or Kubernetes** for single or multi-node deployments. Both are first-class. You pick the one that fits your setup.

This project is under active development. The worker layer (Phase 1) is complete. The Go control plane, job watching, and deployment infrastructure are in progress.

---

## How it works

```
POST /jobs  { "engine": "spatial", "sql": "...", "webhook_url": "https://..." (optional) }
     │
     ▼
Go Control Plane
  - validates request
  - selects worker image from engine registry
  - spawns an ephemeral worker (Docker container or K8s Job, depending on mode)
     │
     ▼
Ephemeral Worker  (lives only for the duration of the query)
  - DuckDB + engine-specific extensions
  - reads input files from S3-compatible object storage
  - writes results back to object storage
  - exits 0 on success, 1 on failure
     │
     ▼
Control Plane
  - detects worker completion
  - generates a presigned result URL
  - fires optional webhook to caller
```

Adding a new engine means a new Dockerfile and one line in the engine registry. The control plane never changes.

---

## Deployment

Tilcayo runs in two modes, selected by the `TILCAYO_ORCHESTRATOR` environment variable.

### Docker mode (`TILCAYO_ORCHESTRATOR=docker`)

Workers run as ephemeral Docker containers on the same host as the control plane. The whole stack — control plane, MinIO, and workers — comes up with a single `docker compose up`. This is the easiest path for a single server.

### Kubernetes / K3s mode (`TILCAYO_ORCHESTRATOR=kubernetes`)

Workers run as ephemeral `batch/v1` Jobs. The control plane talks to the K8s API via the in-cluster config. K3s is the recommended distribution for self-hosted setups — it installs in minutes on any Linux machine and runs identically on one node or many. As you add nodes to the cluster, K8s automatically schedules worker pods across them with no changes to Tilcayo.

Plain K8s manifests are provided (no Helm required).

---

## Engines

### spatial

DuckDB with the `spatial` extension and GDAL. Enables `ST_Read()` for GeoPackage, Shapefile, and GeoJSON from object storage, plus the full suite of geospatial functions (`ST_Area`, `ST_Intersects`, `ST_Within`, etc.) and spatial joins across multiple files in a single query.

### ml

DuckDB with the `vss` extension, `sentence-transformers`, and a CPU-only PyTorch install. Enables vector similarity search via `array_distance()` over parquet files that store embeddings. Also supports an `embed('...')` macro that generates a query-time embedding from text using a pre-downloaded sentence-transformers model, so the caller never has to supply raw vectors.

### general

Plain DuckDB with `httpfs`. CSV, Parquet, JSON — no domain-specific extensions.

---

## Storage

Workers and the control plane are provider-agnostic. Any S3-compatible object store works.

| Provider | Configuration |
|---|---|
| AWS S3 | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `TILCAYO_S3_REGION` |
| MinIO | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_URL_STYLE=path` |
| Cloudflare R2 | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_REGION=auto` |
| GCS (HMAC) | + `TILCAYO_S3_ENDPOINT=storage.googleapis.com`, `TILCAYO_S3_URL_STYLE=path` |
| Backblaze B2 | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_URL_STYLE=path` |

Credentials are set up using DuckDB's secrets manager (`CREATE SECRET`) rather than the legacy `SET s3_*` approach. The connection is locked after setup so user-submitted SQL cannot read back or override credentials.

---

## Repository structure

```
workers/
  shared/
    entrypoint.py       # single shared worker script — all engines use this
    storage.py          # S3 credential setup via DuckDB secrets manager
  spatial/
    Dockerfile
  ml/
    Dockerfile
    entrypoint.py       # ml-specific: resolves embed() macros before DuckDB runs
  general/
    Dockerfile
  test_workers.sh       # build + run all worker tests locally (no S3 needed)

cmd/
  server/
    main.go             # Go control plane entrypoint (planned)

internal/
  api/                  # HTTP handlers (planned)
  jobs/                 # job manager, builder, engine registry (planned)
  orchestrator/         # Orchestrator interface + Docker and K8s implementations (planned)
  s3/                   # presigned URL generation (planned)
  webhook/              # completion notification (planned)

deploy/
  docker/               # docker-compose.yml for Docker mode (planned)
  kubernetes/           # K8s manifests for K3s/K8s mode (planned)
```

---

## Development status

| Phase | Description | Status |
|---|---|---|
| 1 | Workers — Dockerfiles, shared entrypoint, storage config | Complete |
| 2 | Go control plane — API, job builder, engine registry | Planned |
| 3 | Job watching, presigned URLs, webhook delivery | Planned |
| 4 | SQL validation and security hardening | Planned |
| 5 | Cold start optimisation | Planned |
| 6 | Deployment manifests and configuration | Planned |
| 7 | Unit and integration tests | Planned |

---

## Testing workers locally

```bash
cd workers/
chmod +x test_workers.sh
./test_workers.sh
```

Builds all three images and runs a suite of local-file tests (no object storage or credentials needed).
