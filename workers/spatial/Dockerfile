FROM python:3.12-slim

ENV DEBIAN_FRONTEND=noninteractive

# GDAL required for ST_Read() — GeoPackage, Shapefile, GeoJSON support
RUN apt-get update && apt-get install -y \
    libgdal-dev \
    gdal-bin \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir duckdb==1.1.1

# Pre-install extensions at build time — no internet calls at runtime
RUN python -c "import duckdb; con = duckdb.connect(); con.execute('INSTALL spatial'); con.execute('INSTALL httpfs');"

ENV DUCKDB_EXTENSIONS="spatial,httpfs"

COPY shared/storage.py /storage.py
COPY shared/entrypoint.py /entrypoint.py

ENTRYPOINT ["python", "/entrypoint.py"]
