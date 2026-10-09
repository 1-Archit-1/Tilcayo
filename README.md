# Tilcayo

Tilcayo is a self-hostable, stateless SQL query engine platform. You submit a SQL query and an engine type via a REST API. A Go control plane selects the appropriate worker image, spins up an ephemeral job, executes the query against files in object storage or a local data folder, stores the result, and tears the worker down. No persistent database. No idle compute. A single Docker host can run it with no object store at all.

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
  - reads input files from S3-compatible object storage and/or a read-only /data folder
  - writes the result to object storage (or, store-free, to a per-job local folder)
  - exits 0 on success, 1 on failure
     │
     ▼
Control Plane
  - detects worker completion
  - returns a result URL: presigned (object storage) or served by the control plane (store-free)
  - fires optional webhook to caller
```

Adding a new engine means a new Dockerfile and one line in the engine registry. The control plane never changes.

---

## User guide

> **Planned v1, not usable yet.** This guide describes how Tilcayo v1 is designed to work. The control plane, the compose file and the K8s manifests are not built, so the commands below do not run today. Settings marked *provisional* may still be renamed. Anything not decided yet is marked as such rather than guessed.

### Using Tilcayo

**1. Submit a query.** Send the engine and one `SELECT` statement:

```bash
curl -X POST "$TILCAYO_URL/jobs" \
  -H 'Content-Type: application/json' \
  -d '{
        "engine": "general",
        "sql": "SELECT city, count(*) AS orders FROM read_csv('\''/data/orders.csv'\'') GROUP BY city",
        "webhook_url": "https://example.com/hooks/tilcayo"
      }'
```

`webhook_url` is optional. The response contains the job id. `TILCAYO_URL` is wherever you reach the control plane; the default port is not decided yet.

**2. Check its status.**

```bash
curl "$TILCAYO_URL/jobs/<job_id>"
```

A job is `queued`, `running`, `succeeded`, `failed` or `cancelled`. A failed job reports why: a worker error with the worker's message, out of memory, timed out, or crashed. A cancelled job has its own state, not `failed`. When it succeeds, the response includes a `result_url`.

**3. Download the result.**

```bash
curl -L -o result.parquet "<result_url>"
```

Results are always Parquet. `result_url` is a presigned object-storage URL when results go to object storage, or `$TILCAYO_URL/jobs/<job_id>/result` in a store-free setup. You use it the same way either way.

**Optional: get notified instead of polling.** If you passed `webhook_url`, the control plane calls it once the job finishes, with the job id and final state. Delivery is best-effort: a webhook due while the control plane is down is not retried, so poll `GET /jobs/<job_id>` if you need certainty.

**Optional: cancel a job.** `POST /jobs/<job_id>/cancel` stops a running job; it then reports `cancelled`. Cancelling a finished job changes nothing.

**Results are kept for 1 hour.** After that the job and its result are deleted and `GET /jobs/<job_id>` returns 404. Download what you need within the hour.

Not decided yet: the exact JSON shape of the responses and webhook payload, and the default port.

### Writing queries

- **One `SELECT` per job.** Anything else (several statements, `COPY`, `CREATE`, `ATTACH`, `INSTALL`) is rejected and the job fails. Multi-statement pipelines may come later.
- **Size limit.** SQL is limited to 128,000 bytes and may not contain NUL bytes. Larger queries are rejected when submitted.
- **Local files** live under `/data`, the data folder the operator configures. Refer to them by that path: `read_csv('/data/sales/2026.csv')`, `read_parquet('/data/events/*.parquet')`, `ST_Read('/data/regions.geojson')`. Nothing outside `/data` is readable, and `/data` is read-only.
- **Object storage** files are referred to as `s3://bucket/key`, and only from the locations the deployment allows. The operator sets those for the whole deployment; a request cannot add more.
- **Mixing works.** One query can join a local CSV with Parquet in object storage.
- **Pick the engine** for the functions you need: `general` (CSV, Parquet, JSON), `spatial` (`ST_*`, GDAL formats), `ml` (`array_distance`, `embed('text')`). See [Engines](#engines).

### Choosing a setup

| Setup | Orchestrator | Inputs | Results | Object store needed |
|---|---|---|---|---|
| Store-free single server | Docker | `/data` | local folder, served by the control plane | no |
| Single server with a store | Docker | `/data` and/or object storage | object storage (presigned URL) | yes |
| K3s / Kubernetes | Kubernetes | `/data` (from a PVC) and/or object storage | object storage (presigned URL) | yes |

Store-free results are Docker only in v1: sharing a results folder across cluster nodes would need ReadWriteMany storage.

### Configuration reference

The control plane reads all settings from its environment and passes what each worker needs. Workers are never configured by hand.

| Variable | Needed when | Meaning | Status |
|---|---|---|---|
| `TILCAYO_ORCHESTRATOR` | always | `docker` or `kubernetes` | decided |
| `TILCAYO_RESULTS` | optional | `s3` (default) or `local`. `local` means store-free and is refused in Kubernetes mode | provisional |
| `TILCAYO_RESULTS_DIR` | `TILCAYO_RESULTS=local` | Host folder for results. Each job gets its own subfolder, deleted after 1h | provisional |
| `TILCAYO_DATA_SOURCE` | optional | The data folder mounted read-only at `/data`. Docker: an absolute host path. Kubernetes: the name of a PVC. Unset means no `/data` | provisional |
| `TILCAYO_S3_KEY_ID`, `TILCAYO_S3_SECRET` | `TILCAYO_RESULTS=s3`, or to read object storage | Static access keys | decided |
| `TILCAYO_S3_REGION` | with the keys | Required, no default. The control plane refuses to start without it | decided |
| `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_PUBLIC_ENDPOINT`, `TILCAYO_S3_URL_STYLE`, `TILCAYO_S3_USE_SSL` | depends on the provider | See [Storage](#storage) | decided |
| `TILCAYO_OUTPUT_PREFIX` | `TILCAYO_RESULTS=s3` | Where results are written: `s3://bucket/` or `s3://bucket/path/`, ending in `/`. Each job writes `<prefix><job_id>/result.parquet`. Refused in store-free mode | decided |
| `TILCAYO_INPUT_PREFIXES` | to read object storage | Comma-separated `s3://` prefixes queries may read, for every job, each ending in `/`. May not overlap the output prefix | decided |
| `TILCAYO_IMAGE_<ENGINE>` | optional | Overrides the worker image for one engine, for example `TILCAYO_IMAGE_ML` | decided |
| `TILCAYO_JOB_CPU`, `TILCAYO_JOB_MEMORY` | optional | CPU and memory limit of each worker. Defaults are per engine: 1 CPU each; 1 GiB for `general` and `spatial`, 4 GiB for `ml` (provisional until measured). When set, they override every engine, so setting memory also resizes `ml` | provisional |
| `TILCAYO_MEMORY_FRACTION` | optional | Share of a worker's memory limit DuckDB may use before spilling to disk, default `0.5`. See [How workers run](#how-workers-run) | provisional |
| `TILCAYO_MAX_CONCURRENT_JOBS` | optional, Docker mode | Jobs over the limit wait in a queue | provisional |
| `TILCAYO_DOCKER_NETWORK` | Docker mode | Network workers join to reach the store. The control plane is not on it | provisional |

The control plane refuses to start, naming the missing setting, when a required one is unset or does not fit the mode (for example a PVC name given as a path).

### Docker mode setup

**Store-free.** Create two folders on the host: one with your data, one for results.

```bash
mkdir -p /srv/tilcayo/data /srv/tilcayo/results
cp orders.csv /srv/tilcayo/data/
```

```bash
# tilcayo.env
TILCAYO_ORCHESTRATOR=docker
TILCAYO_RESULTS=local
TILCAYO_RESULTS_DIR=/srv/tilcayo/results
TILCAYO_DATA_SOURCE=/srv/tilcayo/data
```

**With object storage.** Leave out `TILCAYO_RESULTS` and `TILCAYO_RESULTS_DIR`, keep `TILCAYO_DATA_SOURCE` if you also want local inputs, and add the store settings. The two keys go in their own file, which compose loads with `env_file` and Kubernetes turns into a Secret:

```bash
# s3.env  (secret: keep it out of version control)
TILCAYO_S3_KEY_ID=...
TILCAYO_S3_SECRET=...
```

```bash
# tilcayo.env  (example for a Garage store on the same compose network)
TILCAYO_ORCHESTRATOR=docker
TILCAYO_DATA_SOURCE=/srv/tilcayo/data
TILCAYO_S3_REGION=garage
TILCAYO_S3_ENDPOINT=garage:3900
TILCAYO_S3_PUBLIC_ENDPOINT=localhost:3900
TILCAYO_S3_URL_STYLE=path
TILCAYO_S3_USE_SSL=false
TILCAYO_OUTPUT_PREFIX=s3://tilcayo-results/
TILCAYO_INPUT_PREFIXES=s3://tilcayo-inputs/
```

Things to know:

- **Both folder settings are host paths**, even when the control plane runs in a container: the Docker daemon resolves them on the host. If the control plane runs in a container, mount the results folder into it **at the same path** (`/srv/tilcayo/results:/srv/tilcayo/results`), so it can serve and clean up results. It does not need the data folder.
- **Workers run as the same user as the control plane**, so result files are owned by that user, not root. Files in the data folder must be readable by that user, or queries on them fail.
- **A wrong data folder path fails the first job that uses it**, not the startup, because the control plane cannot check host paths from inside its container.
- **Do not expose the control plane publicly.** v1 has no authentication, it runs any submitted SQL, and in Docker mode it holds the Docker socket, which is root-equivalent on the host. Keep its port on an internal network or bound to `127.0.0.1`. Anyone who can reach it and knows a job id can download that job's result. A request can only choose the engine, the SQL and a webhook URL, never the image, mounts or environment of a worker, and workers never get the socket.

Spawned workers join the network named by `TILCAYO_DOCKER_NETWORK`. Not decided yet: the compose file itself, including the control plane image name.

### K3s / Kubernetes setup

Results always go to object storage in Kubernetes mode. Local files can still be used as inputs through a PVC.

**Storage credentials.** Put the keys in the fixed-name Secret `tilcayo-s3`, using the same `s3.env` file as Docker mode:

```bash
kubectl create secret generic tilcayo-s3 --from-env-file=s3.env
```

The control plane loads it with `envFrom`, and each worker gets the keys through `secretKeyRef`, so the values never appear in a pod spec. Non-secret settings (`TILCAYO_ORCHESTRATOR=kubernetes`, `TILCAYO_DATA_SOURCE`, `TILCAYO_S3_REGION`, the endpoints) go in a ConfigMap, like `tilcayo.env` in Docker mode. To rotate keys: create the new key at the provider, update the Secret, restart the control plane with `kubectl rollout restart`, wait for running jobs and issued result URLs to expire (longest job timeout plus 1h), then revoke the old key.

**Local data through a PVC.** Set `TILCAYO_DATA_SOURCE` to the name of a PVC in the same namespace as the worker Jobs. It is mounted read-only at `/data`. On single-node K3s, the simplest way is a PersistentVolume pointing at a host folder:

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: tilcayo-data
spec:
  capacity:
    storage: 10Gi
  accessModes: [ReadOnlyMany]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: ""
  hostPath:
    path: /srv/tilcayo/data
    type: Directory
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: tilcayo-data
spec:
  accessModes: [ReadOnlyMany]
  storageClassName: ""
  volumeName: tilcayo-data
  resources:
    requests:
      storage: 10Gi
```

```bash
# in the ConfigMap
TILCAYO_DATA_SOURCE=tilcayo-data
```

On several nodes, either add `nodeAffinity` to that PersistentVolume so every job runs on the node holding the folder, or use storage every node can reach (NFS, Longhorn or any ReadOnlyMany / ReadWriteMany storage class) and create the PVC from it. Tilcayo only needs the PVC name. Workers run as the control plane's user, so the files must be readable by that user ID.

**Reaching the control plane.** It runs as a ClusterIP service only, never publicly exposed, for the same reason as in Docker mode. From your machine, use `kubectl port-forward`. A NetworkPolicy stops worker pods from calling it (enforced on clusters whose network plugin supports NetworkPolicy, which K3s does).

Not decided yet: the manifests themselves, including the namespace, service account and RBAC, and the ConfigMap name.

---

## Deployment

Tilcayo runs in two modes, selected by the `TILCAYO_ORCHESTRATOR` environment variable.

### Docker mode (`TILCAYO_ORCHESTRATOR=docker`)

Workers run as ephemeral Docker containers on the same host as the control plane. The control plane and workers come up with a single `docker compose up`. You can run it with no object store at all (inputs and results live in local folders), or point it at an S3-compatible store you already have (see [Storage](#storage)). Tilcayo does not currently ship a store; bundling one may be added in the future. This is the easiest path for a single server.

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

Object storage is optional in store-free Docker setups and required otherwise. Workers and the control plane are provider-agnostic: any S3-compatible object store works. Tilcayo does not currently ship a store; you bring your own and configure it with the `TILCAYO_S3_*` settings.

| Provider | Configuration |
|---|---|
| AWS S3 | `TILCAYO_S3_KEY_ID`, `TILCAYO_S3_SECRET`, `TILCAYO_S3_REGION` |
| MinIO | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_URL_STYLE=path` |
| Garage | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_URL_STYLE=path`, `TILCAYO_S3_REGION=garage` (its default region) |
| Cloudflare R2 | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_REGION=auto` |
| GCS (HMAC) | + `TILCAYO_S3_ENDPOINT=storage.googleapis.com`, `TILCAYO_S3_URL_STYLE=path` |
| Backblaze B2 | + `TILCAYO_S3_ENDPOINT`, `TILCAYO_S3_URL_STYLE=path` |

Workers are tested against two stores: a Chainguard build of MinIO (`cgr.dev/chainguard/minio`, since upstream MinIO is archived) and Garage (`dxflrs/garage:v2.4.1`). The other providers follow the same code path but are not part of the test suite.

Set `TILCAYO_S3_REGION` explicitly. The default is `us-east-1`, which MinIO accepts for any region but Garage rejects. `TILCAYO_S3_PUBLIC_ENDPOINT` is optional and defaults to `TILCAYO_S3_ENDPOINT`. Set it when the address workers use differs from the one the caller can reach, for example `garage:3900` inside a Docker network and `localhost:3900` outside.

Keys are static access keys passed as `TILCAYO_S3_KEY_ID` and `TILCAYO_S3_SECRET`, the same names for every provider. Temporary credentials (session tokens) and AWS pod identity are not supported yet. The worker ignores `AWS_*` variables: it removes them before DuckDB starts and logs a warning naming them, because DuckDB's `httpfs` would otherwise copy them into settings user SQL can read.

Credentials are set up using DuckDB's secrets manager (`CREATE SECRET`) rather than the legacy `SET s3_*` approach, and the connection is locked after setup, so user-submitted SQL cannot read back or override them. The keys do remain in the worker's process environment. User SQL is kept away from it (for example `/proc/self/environ`) by the path allow-list, not by removing the variables. Anyone who can inspect the container can see them: in Kubernetes, inject them from a Secret with `secretKeyRef` so the values stay out of the pod spec.

**Scoping access.** Where the provider allows it, give workers a key that is read-only on input locations and writable only where results go. Garage can only scope a key per bucket (read, write, owner), not per prefix, so on Garage keep inputs and outputs in separate buckets and give the key read-only access to the input bucket. A shared bucket with separate input and output prefixes passes Tilcayo's checks, but loses this per-bucket scoping, so use separate buckets. This is recommended, not required: a job is a single `SELECT`, and no way is known for one to write anywhere but its own result. The workers' path checks would allow writes under an input prefix, though, so the key is the safety net if that ever changes.

**What the workers enforce.** A job must be exactly one `SELECT` statement. User SQL can only touch the output path and the input locations the worker is given (S3 prefixes, and `/data` when a data folder is configured). It cannot read any other local file, open HTTP URLs, attach databases, install extensions or create secrets. A worker given any local path other than `/data` as an allowed input refuses it and fails the job. This is covered by `workers/test_security.sh`. On local paths, DuckDB itself refuses `..` and symlinks that lead outside an allowed folder. On object storage, `..` is not blocked by Tilcayo; MinIO and Garage refuse it because the request signature no longer matches.

### How workers run

- **Non-root, locked down.** Workers run as the control plane's user (the images default to uid 65532), with a read-only root filesystem, all Linux capabilities dropped and no privilege escalation. In Kubernetes they meet the Pod Security "restricted" profile and get no service-account token.
- **Spilling to disk.** DuckDB writes intermediate data to `/tmp/duckdb` once a query outgrows its memory limit. `/tmp` is a disk-backed volume (a Kubernetes `emptyDir`, a Docker volume), not tmpfs, because tmpfs counts against the worker's memory.
- **Memory limit.** DuckDB uses `TILCAYO_MEMORY_FRACTION` (default `0.5`) of the worker's memory limit. Its real memory use runs well above that number, about 1.6x in tests on large sorts, and a worker that crosses its container limit is killed without an error message. The limit is read from cgroup v2; on older cgroup v1 hosts the worker logs a warning and DuckDB uses its own default. In Docker mode swap is disabled for workers (the swap limit equals the memory limit), matching Kubernetes.
- **Cancel and stop.** `tini` runs as PID 1 so a stop signal ends the worker at once.

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
