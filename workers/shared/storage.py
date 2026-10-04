import os
import duckdb


def configure_storage(con: duckdb.DuckDBPyConnection, extensions: list[str]):
    """
    Configure S3-compatible storage via DuckDB's secrets manager.

    Replaces the legacy SET s3_* approach, which stored credentials as plain
    DuckDB config values readable via current_setting(). CREATE SECRET keeps
    credentials in a redacted in-memory object instead.

    Required env vars (all providers):
        AWS_ACCESS_KEY_ID
        AWS_SECRET_ACCESS_KEY

    Optional env vars:
        TILCAYO_S3_ENDPOINT    e.g. minio.example.com:9000 (omit for AWS S3)
        TILCAYO_S3_REGION      e.g. us-east-1 (default); use "auto" for R2
        TILCAYO_S3_URL_STYLE   "path" or "vhost" (default: vhost)
        TILCAYO_S3_USE_SSL     "true" or "false" (default: true)

    After the secret is created, lock_configuration = true is set so user SQL
    cannot override credentials or inject new secrets.
    """
    if 'httpfs' not in extensions:
        return

    key_id     = os.environ.get('AWS_ACCESS_KEY_ID', '')
    secret_key = os.environ.get('AWS_SECRET_ACCESS_KEY', '')
    endpoint   = os.environ.get('TILCAYO_S3_ENDPOINT', '')
    region     = os.environ.get('TILCAYO_S3_REGION', 'us-east-1')
    url_style  = os.environ.get('TILCAYO_S3_URL_STYLE', 'vhost')
    use_ssl    = os.environ.get('TILCAYO_S3_USE_SSL', 'true').lower() not in ('false', '0', 'no')

    # Build the secret parameter list. Endpoint is optional — omitting it
    # means DuckDB routes to AWS S3 by default.
    params = [
        f"TYPE S3",
        f"PROVIDER config",
        f"KEY_ID '{key_id}'",
        f"SECRET '{secret_key}'",
        f"REGION '{region}'",
        f"URL_STYLE '{url_style}'",
        f"USE_SSL {str(use_ssl).lower()}",
    ]
    if endpoint:
        params.append(f"ENDPOINT '{endpoint}'")

    params_sql = ",\n    ".join(params)
    con.execute(f"CREATE OR REPLACE SECRET tilcayo_s3 (\n    {params_sql}\n);")

    # Lock the connection so user SQL cannot change settings or create secrets.
    con.execute("SET lock_configuration = true;")
