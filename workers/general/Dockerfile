FROM python:3.12-slim

RUN pip install --no-cache-dir duckdb

# Pre-install extensions at build time — no internet calls at runtime
RUN python -c "import duckdb; con = duckdb.connect(); con.execute('INSTALL httpfs');"

ENV DUCKDB_EXTENSIONS="httpfs"

COPY shared/storage.py /storage.py
COPY shared/entrypoint.py /entrypoint.py

ENTRYPOINT ["python", "/entrypoint.py"]
