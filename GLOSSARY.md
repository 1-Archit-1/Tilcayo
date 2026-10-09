# Glossary

**Control plane**: The Go service that accepts jobs over the REST API, starts a worker for each one, watches it, and returns the result or failure.
_Avoid_: server, orchestrator (the orchestrator is the Docker or Kubernetes backend the control plane drives).

**Worker**: The container that runs one job's SQL with DuckDB and writes the result. Workers are never configured by hand; the control plane sets everything they need.

**Engine**: The kind of worker a job asks for, chosen by the functions the query needs. `general` is plain DuckDB for CSV, Parquet and JSON. `spatial` adds geospatial functions and GDAL formats. `ml` adds vector similarity search and query-time text embeddings.
_Avoid_: image (an engine maps to an image, but is not one).

**Job**: One submitted query: an engine, one `SELECT` statement and an optional webhook URL, plus its state and result.
_Avoid_: task, query (when meaning the whole submission).

**Ephemeral worker**: A worker that exists only for the duration of one job and is removed afterwards. Every worker in Tilcayo is ephemeral.

**Store-free mode**: A Docker-mode setup with no object store. Inputs come from the data folder and results are kept in a local folder and served by the control plane.

**Engine registry**: The control plane's mapping from engine name to worker image. Adding an engine means a new image and one entry here.

**Input prefixes**: The object-storage locations the operator configures that every job may read. A request cannot add more.

**Allowed inputs**: The locations a worker lets user SQL read: the input prefixes, plus the data folder when one is configured. The worker's own output location is also readable.
_Avoid_: input prefixes (those are only the object-storage part).

**Output prefix**: The object-storage location results are written under, with one folder per job.
_Avoid_: output bucket (the prefix may include a path inside the bucket).

## Failure kinds

A failed job reports one of these. The orchestrator's signals decide the kind; the worker only supplies the message for a worker error.

**Worker error**: The worker stopped the job itself, for example because the SQL was invalid or failed while running. The job carries the worker's message.

**Out of memory**: The worker used more memory than its limit and was killed. There is no message from the worker.

**Timeout**: The job ran longer than its time limit and was stopped.
_Avoid_: cancelled (a timeout is a failure, not a user action).

**Cancelled**: The caller cancelled the job. This is its own job state, not a failed job.

**Crash**: The worker ended in any other unexpected way. The job carries how the worker exited and the end of its log.
