FROM ubuntu:22.04

# Avoid prompts from apt
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    python3 \
    python3-pip \
    && rm -rf /var/lib/apt/lists/*

# Install DuckDB Python package
RUN pip3 install --no-cache-dir duckdb==1.1.1

# Pre-install extensions via Python so they are baked into the Docker image
# This avoids needing to download them at runtime.
RUN python3 -c "import duckdb; con = duckdb.connect(); con.execute('INSTALL spatial; INSTALL vss; INSTALL httpfs; INSTALL aws;')"

COPY entrypoint.py /entrypoint.py
RUN chmod +x /entrypoint.py

ENTRYPOINT ["python3", "/entrypoint.py"]
