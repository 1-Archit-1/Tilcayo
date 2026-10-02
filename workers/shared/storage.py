import os
import duckdb


def configure_storage(con: duckdb.DuckDBPyConnection, extensions: list[str]):
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
    if 'httpfs' not in extensions:
        return

    endpoint  = os.environ.get('TILCAYO_S3_ENDPOINT')
    url_style = os.environ.get('TILCAYO_S3_URL_STYLE', 'vhost')
    use_ssl   = os.environ.get('TILCAYO_S3_USE_SSL', 'true').lower()

    if endpoint:
        con.execute(f"SET s3_endpoint='{endpoint}';")

    con.execute(f"SET s3_url_style='{url_style}';")

    if use_ssl in ('false', '0', 'no'):
        con.execute("SET s3_use_ssl=false;")
