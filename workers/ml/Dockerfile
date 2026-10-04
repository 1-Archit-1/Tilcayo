FROM python:3.12-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Torch CPU-only first — this one you always want to pin the index URL
# otherwise pip grabs the CUDA build which is ~2.5GB
RUN pip install --no-cache-dir \
    torch --index-url https://download.pytorch.org/whl/cpu
    
# Everything else unpinned — pip resolves latest compatible versions
RUN pip install --no-cache-dir \
    sentence-transformers \
    duckdb


# Pre-install extensions at build time — no internet calls at runtime
RUN python -c "import duckdb; con = duckdb.connect(); con.execute('INSTALL vss'); con.execute('INSTALL httpfs');"

# Pre-download the default embedding model so the worker is fully offline-capable
ENV EMBEDDING_MODEL="all-MiniLM-L6-v2"
RUN python -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('all-MiniLM-L6-v2')"

ENV DUCKDB_EXTENSIONS="vss,httpfs"

# Build context must be workers/ root (same as spatial) so shared/ is reachable
COPY shared/ /shared/
COPY ml/entrypoint.py /entrypoint.py

ENTRYPOINT ["python", "/entrypoint.py"]
