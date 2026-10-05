FROM python:3.12-slim

ENV DEBIAN_FRONTEND=noninteractive

# GDAL required for ST_Read() — GeoPackage, Shapefile, GeoJSON support.
# tini as PID 1: Python as PID 1 ignores SIGTERM, so cancel/stop would wait
# for the grace period and end in SIGKILL.
RUN apt-get update && apt-get install -y \
    libgdal-dev \
    gdal-bin \
    tini \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir duckdb

# Pre-install extensions at build time — no internet calls at runtime.
# Installed outside any home directory so a non-root worker can load them.
ENV TILCAYO_EXTENSION_DIR=/opt/duckdb/extensions
RUN python -c "import duckdb, os; con = duckdb.connect(config={'extension_directory': os.environ['TILCAYO_EXTENSION_DIR']}); con.execute('INSTALL spatial'); con.execute('INSTALL httpfs');"

ENV DUCKDB_EXTENSIONS="spatial,httpfs"

COPY shared/storage.py /storage.py
COPY shared/entrypoint.py /entrypoint.py

# Non-root by default; the control plane sets its own uid:gid at runtime.
USER 65532:65532

ENTRYPOINT ["tini", "--", "python", "/entrypoint.py"]
