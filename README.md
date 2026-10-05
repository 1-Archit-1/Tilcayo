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

Workers run as ephemeral Docker containers on the same host as the control plane. The control plane and workers come up with a single `docker compose up`. Tilcayo does not currently ship an object store: you point it at one you already have (see [Storage](#storage)). Bundling a store may be added in the future. This is the easiest path for a single server.

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

Workers and the control plane are provider-agnostic. Any S3-compatible object store works. Tilcayo does not currently ship a store; you bring your own and configure it with the `TILCAYO_S3_*` settings.

| Provider | Configuration |
|---|---|
| AWS S3 | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `TILCAYO_S3_REGION` |
| MinIO | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_URL_STYLE=path` |
| Garage | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_URL_STYLE=path`, `TILCAYO_S3_REGION=garage` (its default region) |
| Cloudflare R2 | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_REGION=auto` |
| GCS (HMAC) | + `TILCAYO_S3_ENDPOINT=storage.googleapis.com`, `TILCAYO_S3_URL_STYLE=path` |
| Backblaze B2 | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_URL_STYLE=path` |

Workers are tested against two stores: a Chainguard build of MinIO (`cgr.dev/chainguard/minio`, since upstream MinIO is archived) and Garage (`dxflrs/garage:v2.4.1`). The other providers follow the same code path but are not part of the test suite.

Set `TILCAYO_S3_REGION` explicitly. The default is `us-east-1`, which MinIO accepts for any region but Garage rejects. `TILCAYO_S3_PUBLIC_ENDPOINT` is optional and defaults to `TILCAYO_S3_ENDPOINT`. Set it when the address workers use differs from the one the caller can reach, for example `garage:3900` inside a Docker network and `localhost:3900` outside.

Credentials are set up using DuckDB's secrets manager (`CREATE SECRET`) rather than the legacy `SET s3_*` approach. The credential environment variables are removed from the worker process before DuckDB starts, and the connection is locked after setup, so user-submitted SQL cannot read back or override credentials.

**Scoping access.** Where the provider allows it, give workers a key that is read-only on input locations and writable only where results go. Garage can only scope a key per bucket (read, write, owner), not per prefix, so on Garage keep inputs and outputs in separate buckets and give the key read-only access to the input bucket. The workers' own checks (below) do not stop user SQL from overwriting files under an input prefix.

**What the workers enforce.** A job must be exactly one `SELECT` statement. User SQL can only touch the output path and the input prefixes the worker is given, and cannot read local files, open HTTP URLs, attach databases, install extensions or create secrets. This is covered by `workers/test_security.sh`. Path tricks such as `..` are not blocked by Tilcayo itself; MinIO and Garage refuse them because the request signature no longer matches.

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
  test_security.sh      # attack and regression checks against MinIO or Garage (needs Docker)
  testdata/             # inputs generated by test_workers.sh

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
| 4 | SQL validation and security hardening | Worker side complete (single-`SELECT` guard, allow-list, credential isolation); control plane side planned |
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

## Testing worker security against an object store

```bash
cd workers/
BUILD=1 ./test_security.sh                       # Chainguard MinIO, path-style
STORE=garage ./test_security.sh                  # Garage
STORE=garage URL_STYLE=vhost ./test_security.sh  # virtual-hosted addressing
```

The script starts a throwaway store on a private Docker network, seeds some input files, and runs each attack and regression case through the real worker entrypoints. It prints a PASS/FAIL table and exits non-zero on any failure. `BUILD=1` rebuilds the worker images first; do that whenever worker code has changed, because stale `:test` images can hide a change. The script uses throwaway credentials and needs Docker only.
