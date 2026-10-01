FROM python:3.12-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir \
    duckdb==1.1.1 \
    sentence-transformers==3.0.1 \
    numpy==1.26.4

# Pre-install extensions at build time — no internet calls at runtime
RUN python -c "import duckdb; con = duckdb.connect(); con.execute('INSTALL vss'); con.execute('INSTALL httpfs');"

# Pre-download the default embedding model so the worker is fully offline-capable
ENV EMBEDDING_MODEL="all-MiniLM-L6-v2"
RUN python -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('all-MiniLM-L6-v2')"

ENV DUCKDB_EXTENSIONS="vss,httpfs"

COPY entrypoint.py /entrypoint.py

ENTRYPOINT ["python", "/entrypoint.py"]
