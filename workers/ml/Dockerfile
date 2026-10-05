FROM python:3.12-slim

ENV DEBIAN_FRONTEND=noninteractive

# tini as PID 1: Python as PID 1 ignores SIGTERM, so cancel/stop would wait
# for the grace period and end in SIGKILL.
RUN apt-get update && apt-get install -y \
    build-essential \
    tini \
    && rm -rf /var/lib/apt/lists/*

# Torch CPU-only first — this one you always want to pin the index URL
# otherwise pip grabs the CUDA build which is ~2.5GB
RUN pip install --no-cache-dir \
    torch --index-url https://download.pytorch.org/whl/cpu
    
# Everything else unpinned — pip resolves latest compatible versions
RUN pip install --no-cache-dir \
    sentence-transformers \
    duckdb


# Pre-install extensions at build time — no internet calls at runtime.
# Installed outside any home directory so a non-root worker can load them.
ENV TILCAYO_EXTENSION_DIR=/opt/duckdb/extensions
RUN python -c "import duckdb, os; con = duckdb.connect(config={'extension_directory': os.environ['TILCAYO_EXTENSION_DIR']}); con.execute('INSTALL vss'); con.execute('INSTALL httpfs');"

# Pre-download the default embedding model so the worker is fully offline-capable.
# The cache lives in /opt (readable by any uid), not in root's home.
ENV HF_HOME=/opt/huggingface
ENV EMBEDDING_MODEL="all-MiniLM-L6-v2"
RUN python -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('all-MiniLM-L6-v2')"
# Never call the Hub at runtime: the cache is read-only for the worker, so only
# models baked into the image can be used anyway.
ENV HF_HUB_OFFLINE=1

ENV DUCKDB_EXTENSIONS="vss,httpfs"

# Build context must be workers/ root (same as spatial) so shared/ is reachable
COPY shared/ /shared/
COPY ml/entrypoint.py /entrypoint.py

# Non-root by default; the control plane sets its own uid:gid at runtime.
USER 65532:65532

ENTRYPOINT ["tini", "--", "python", "/entrypoint.py"]
