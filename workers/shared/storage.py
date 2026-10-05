import os
import sys
import duckdb

# Storage keys are delivered under Tilcayo's own names, which httpfs never reads.
CREDENTIAL_ENV_VARS = ('TILCAYO_S3_KEY_ID', 'TILCAYO_S3_SECRET')

# httpfs copies AWS_* env vars into its legacy s3_* settings at connect/load
# time, where user SQL can read them with current_setting(). Any AWS_* var is
# therefore dropped from os.environ before connect(). This only hides them from
# httpfs: the values stay in the process's initial environment
# (/proc/self/environ), which the path allow-list keeps user SQL away from.
STRAY_ENV_PREFIX = 'AWS_'


def fail(message: str):
    """Print an error to stderr and exit 1 (every worker failure exits 1)."""
    print(message, file=sys.stderr)
    sys.exit(1)


def _sql_str(value: str) -> str:
    """Quote a value as a SQL string literal."""
    return "'" + value.replace("'", "''") + "'"


def take_credentials() -> dict:
    """
    Read storage credentials into a local dict and remove them from os.environ.
    Also drop any AWS_* var, with a warning naming it (never its value): the
    worker does not use them, and httpfs would expose them to user SQL.
    Must run BEFORE duckdb.connect().
    """
    creds = {name: os.environ.pop(name, '') for name in CREDENTIAL_ENV_VARS}
    stray = sorted(name for name in os.environ if name.startswith(STRAY_ENV_PREFIX))
    for name in stray:
        os.environ.pop(name)
    if stray:
        print(f"Warning: ignoring and removing {', '.join(stray)}; "
              "storage keys are read from TILCAYO_S3_KEY_ID / TILCAYO_S3_SECRET only.",
              file=sys.stderr)
    return creds


def configure_storage(con: duckdb.DuckDBPyConnection, extensions: list[str], creds: dict):
    """
    Configure S3-compatible storage via DuckDB's secrets manager.

    Replaces the legacy SET s3_* approach, which stored credentials as plain
    DuckDB config values readable via current_setting(). CREATE SECRET keeps
    credentials in a redacted in-memory object instead.

    Credentials come from take_credentials() (TILCAYO_S3_KEY_ID,
    TILCAYO_S3_SECRET). Static keys only; temporary credentials are not
    supported in v1.

    Optional env vars:
        TILCAYO_S3_ENDPOINT    e.g. minio.example.com:9000 (omit for AWS S3)
        TILCAYO_S3_REGION      e.g. us-east-1 (default); use "auto" for R2
        TILCAYO_S3_URL_STYLE   "path" or "vhost" (default: vhost)
        TILCAYO_S3_USE_SSL     "true" or "false" (default: true)

    Does not lock the connection; harden_connection() does that afterwards.
    """
    if 'httpfs' not in extensions:
        return

    endpoint  = os.environ.get('TILCAYO_S3_ENDPOINT', '')
    region    = os.environ.get('TILCAYO_S3_REGION', 'us-east-1')
    url_style = os.environ.get('TILCAYO_S3_URL_STYLE', 'vhost')
    use_ssl   = os.environ.get('TILCAYO_S3_USE_SSL', 'true').lower() not in ('false', '0', 'no')

    # Build the secret parameter list. Endpoint is optional — omitting it
    # means DuckDB routes to AWS S3 by default.
    params = [
        "TYPE S3",
        "PROVIDER config",
        f"KEY_ID {_sql_str(creds.get('TILCAYO_S3_KEY_ID', ''))}",
        f"SECRET {_sql_str(creds.get('TILCAYO_S3_SECRET', ''))}",
        f"REGION {_sql_str(region)}",
        f"URL_STYLE {_sql_str(url_style)}",
        f"USE_SSL {str(use_ssl).lower()}",
    ]
    if endpoint:
        params.append(f"ENDPOINT {_sql_str(endpoint)}")

    params_sql = ",\n    ".join(params)
    con.execute(f"CREATE OR REPLACE SECRET tilcayo_s3 (\n    {params_sql}\n);")


def allowed_directories(output_path: str | None) -> list[str]:
    """
    Paths user SQL may touch once external access is disabled:
      - the directory of OUTPUT_PATH (with trailing '/'), if set
      - each comma-separated prefix in TILCAYO_ALLOWED_INPUTS (provisional
        name); each must end in '/'. Empty by default.
    """
    dirs = []
    if output_path:
        head, sep, _ = output_path.rpartition('/')
        if not sep or not head:
            raise ValueError(f"OUTPUT_PATH must include a directory: {output_path!r}")
        dirs.append(head + '/')

    for prefix in os.environ.get('TILCAYO_ALLOWED_INPUTS', '').split(','):
        prefix = prefix.strip()
        if not prefix:
            continue
        if not prefix.endswith('/'):
            raise ValueError(f"TILCAYO_ALLOWED_INPUTS entries must end in '/': {prefix!r}")
        dirs.append(prefix)
    return dirs


def harden_connection(con: duckdb.DuckDBPyConnection, output_path: str | None):
    """
    Restrict file/network access to the allow-list, then lock configuration.
    Runs for EVERY engine, with or without httpfs, after extensions are loaded
    and the storage secret exists.
    """
    dirs = allowed_directories(output_path)
    if dirs:
        con.execute(f"SET allowed_directories = [{', '.join(_sql_str(d) for d in dirs)}];")
    con.execute("SET enable_external_access = false;")
    con.execute("SET lock_configuration = true;")


def open_connection(extensions: list[str], output_path: str | None) -> duckdb.DuckDBPyConnection:
    """
    Create a hardened DuckDB connection. Order matters:
      scrub env -> connect -> no persistent secrets -> LOAD -> CREATE SECRET
      -> allowed_directories -> enable_external_access=false -> lock.
    Any failure exits 1.
    """
    creds = take_credentials()
    try:
        con = duckdb.connect()
        con.execute("SET allow_persistent_secrets = false;")
        for ext in extensions:
            con.execute(f"LOAD {ext};")
        configure_storage(con, extensions, creds)
        harden_connection(con, output_path)
    except Exception as e:
        fail(f"Error setting up engine:\n{e}")
    return con


def validate_single_select(con: duckdb.DuckDBPyConnection, query: str) -> str:
    """
    Require the query to be exactly one SELECT statement and return its text.
    Exits 1 otherwise.
    """
    try:
        statements = con.extract_statements(query)
    except Exception as e:
        fail(f"Error parsing query:\n{e}")
    if len(statements) != 1 or statements[0].type != duckdb.StatementType.SELECT:
        fail("Error: QUERY must be exactly one SELECT statement.")
    # The statement text keeps any trailing ';' — drop it so it can be wrapped.
    return statements[0].query.rstrip(' \t\r\n;')


def run_query(con: duckdb.DuckDBPyConnection, query: str, output_path: str | None):
    """Validate the user query, then write it to OUTPUT_PATH or print it."""
    sql = validate_single_select(con, query)
    try:
        if output_path:
            # Newline before ')' so a trailing '--' comment cannot swallow it.
            copy_sql = f"COPY (\n{sql}\n) TO {_sql_str(output_path)}"
            wrapped = con.extract_statements(copy_sql)
            if len(wrapped) != 1 or wrapped[0].type != duckdb.StatementType.COPY:
                fail("Error: QUERY must be exactly one SELECT statement.")
            con.execute(copy_sql)
        else:
            con.sql(sql).show()
    except Exception as e:
        fail(f"Error executing query:\n{e}")
