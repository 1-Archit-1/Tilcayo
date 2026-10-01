import os
import sys
import duckdb

EXTENSIONS = [e.strip() for e in os.environ.get('DUCKDB_EXTENSIONS', '').split(',') if e.strip()]

def configure_storage(con: duckdb.DuckDBPyConnection):
    """
    Configure httpfs for S3-compatible storage providers.

    AWS and compatible providers (R2, GCS HMAC) pick up standard env vars
    automatically: AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION.

    Non-AWS providers (MinIO, R2 custom domain, Backblaze) additionally need
    endpoint and style configuration, which have no standard env var convention.
    We define our own:

        TILCAYO_S3_ENDPOINT    e.g. minio.example.com:9000
        TILCAYO_S3_URL_STYLE   "path" or "vhost" (default: vhost)
        TILCAYO_S3_USE_SSL     "true" or "false" (default: true)
    """
    if 'httpfs' not in EXTENSIONS:
        return

    endpoint  = os.environ.get('TILCAYO_S3_ENDPOINT')
    url_style = os.environ.get('TILCAYO_S3_URL_STYLE', 'vhost')
    use_ssl   = os.environ.get('TILCAYO_S3_USE_SSL', 'true').lower()

    if endpoint:
        con.execute(f"SET s3_endpoint='{endpoint}';")

    con.execute(f"SET s3_url_style='{url_style}';")

    if use_ssl in ('false', '0', 'no'):
        con.execute("SET s3_use_ssl=false;")

def main():
    query = os.environ.get('QUERY')
    if not query:
        print("Error: QUERY environment variable is required.", file=sys.stderr)
        sys.exit(1)

    output_path = os.environ.get('OUTPUT_PATH')

    con = duckdb.connect()

    for ext in EXTENSIONS:
        con.execute(f"LOAD {ext};")

    configure_storage(con)

    try:
        if output_path:
            con.execute(f"COPY ({query}) TO '{output_path}'")
        else:
            con.sql(query).show()
    except Exception as e:
        print(f"Error executing query:\n{e}", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    main()
