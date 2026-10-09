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

# DuckDB spills here. With a read-only root filesystem, /tmp must be a
# disk-backed mount (K8s emptyDir, Docker volume): tmpfs counts against the
# container's memory limit, so spilling to it can cause the OOM kill it avoids.
TEMP_DIR = '/tmp/duckdb'

CGROUP_MEMORY_MAX = '/sys/fs/cgroup/memory.max'
DEFAULT_MEMORY_FRACTION = 0.5


ERROR_SENTINEL = 'tilcayo-error: '
ERROR_SENTINEL_MAX = 1024


def fail(message: str):
    """
    Print an error to stderr and exit 1 (every worker failure exits 1).

    The full message comes first, then one final line
    `tilcayo-error: <message>` with '\\' escaped as '\\\\', newlines as '\\n'
    and carriage returns as '\\r', truncated to 1024 characters. The control plane parses only that
    last line of the log tail, so it must be the last thing printed.
    """
    # Buffered stdout is flushed at exit, after stderr; flush it now so it
    # cannot land below the sentinel in the combined log.
    sys.stdout.flush()
    print(message, file=sys.stderr)
    one_line = (message.replace('\\', '\\\\').replace('\n', '\\n')
                .replace('\r', '\\r'))[:ERROR_SENTINEL_MAX]
    print(ERROR_SENTINEL + one_line, file=sys.stderr, flush=True)
    sys.exit(1)


def _fail_on_uncaught(exc_type, exc, tb):
    """Print the usual traceback, then exit through fail()."""
    sys.__excepthook__(exc_type, exc, tb)
    fail(f"Error: unexpected worker failure: {exc_type.__name__}: {exc}")


# Every worker failure must end with the sentinel line, including bugs that
# raise outside the try blocks below; importing storage installs this hook.
sys.excepthook = _fail_on_uncaught


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
      - the directory of OUTPUT_PATH (with trailing '/'), if set: '/out/' or
        's3://<bucket>/[path/]'
      - each comma-separated prefix in TILCAYO_ALLOWED_INPUTS: exactly
        '/data/', or 's3://<bucket>/[path/]' ending in '/'. Empty by default.

    Anything else (e.g. '/', '/proc/', '/tmp/') raises ValueError: a local
    directory covering /proc would expose /proc/self/environ, which still
    holds the storage keys. The worker checks this itself rather than trusting
    whoever composed its environment.
    """
    dirs = []
    if output_path:
        head, sep, _ = output_path.rpartition('/')
        if not sep or not head:
            raise ValueError(f"OUTPUT_PATH must include a directory: {output_path!r}")
        out_dir = head + '/'
        if out_dir != '/out/' and not _is_s3_prefix(out_dir):
            raise ValueError("OUTPUT_PATH must be under '/out/' or "
                             f"'s3://<bucket>/': {output_path!r}")
        dirs.append(out_dir)

    for prefix in os.environ.get('TILCAYO_ALLOWED_INPUTS', '').split(','):
        prefix = prefix.strip()
        if not prefix:
            continue
        if prefix != '/data/' and not _is_s3_prefix(prefix):
            raise ValueError("TILCAYO_ALLOWED_INPUTS entries must be '/data/' or "
                             f"'s3://<bucket>/...' ending in '/': {prefix!r}")
        dirs.append(prefix)
    return dirs


def _is_s3_prefix(prefix: str) -> bool:
    """'s3://<non-empty bucket>/...' ending in '/'."""
    if not prefix.startswith('s3://') or not prefix.endswith('/'):
        return False
    return bool(prefix[len('s3://'):].split('/', 1)[0])


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


def memory_limit_bytes() -> int | None:
    """
    TILCAYO_MEMORY_FRACTION (default 0.5) of the container's cgroup v2 memory
    limit, or None when the container has no limit (DuckDB's default applies).

    DuckDB's memory_limit caps only its buffer manager. On large sorts the
    process used about 1.6x the limit, so DuckDB's own default (80% of the
    cgroup limit) got the container OOM-killed instead of failing cleanly.
    """
    raw = os.environ.get('TILCAYO_MEMORY_FRACTION', '')
    fraction = float(raw) if raw else DEFAULT_MEMORY_FRACTION
    if not 0 < fraction <= 1:
        raise ValueError(f"TILCAYO_MEMORY_FRACTION must be in (0, 1]: {raw!r}")
    try:
        with open(CGROUP_MEMORY_MAX) as f:
            limit = f.read().strip()
    except FileNotFoundError:
        # cgroup v1 host (or not in a container): the limit can't be read.
        print("Warning: no cgroup v2 memory limit found; DuckDB uses its own "
              "memory_limit default (80% of RAM), which may get a capped "
              "container OOM-killed.", file=sys.stderr)
        return None
    if limit == 'max':
        return None
    return int(int(limit) * fraction)


def configure_resources(con: duckdb.DuckDBPyConnection):
    """Extension path, spill directory and memory limit. Before LOAD and lock."""
    extension_dir = os.environ.get('TILCAYO_EXTENSION_DIR')
    if extension_dir:
        con.execute(f"SET extension_directory = {_sql_str(extension_dir)};")
    con.execute(f"SET temp_directory = {_sql_str(TEMP_DIR)};")
    limit = memory_limit_bytes()
    if limit is not None:
        con.execute(f"SET memory_limit = '{limit}B';")


def open_connection(extensions: list[str], output_path: str | None) -> duckdb.DuckDBPyConnection:
    """
    Create a hardened DuckDB connection. Order matters:
      scrub env -> connect -> no persistent secrets -> resources -> LOAD
      -> CREATE SECRET -> allowed_directories -> enable_external_access=false
      -> lock.
    Any failure exits 1.
    """
    creds = take_credentials()
    try:
        con = duckdb.connect()
        con.execute("SET allow_persistent_secrets = false;")
        configure_resources(con)
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
