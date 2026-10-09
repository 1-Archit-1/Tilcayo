#!/usr/bin/env bash
# =============================================================================
# Tilcayo worker attack checks
# Run from anywhere; uses the :test images built by test_workers.sh
# (set BUILD=1 to build them here).
#
# Starts a throwaway S3-compatible store on a private docker network, seeds
# s3://data/in/, then runs each payload THROUGH THE REAL ENTRYPOINT
# (QUERY / OUTPUT_PATH env) of the general, spatial and ml images. Prints a
# PASS/FAIL table and exits non-zero on any FAIL. INFO rows record store
# behaviour that is observed rather than asserted. Credentials below are
# throwaway values for the local container only.
#
#   STORE=minio|garage   store under test (default minio)
#   URL_STYLE=path|vhost how workers address buckets (default path)
#   BUILD=1              build the :test images first
#   IMAGES="general ml"  images to attack (default: general spatial ml). Use a
#                        subset while iterating; run all images on both stores
#                        before shipping worker changes.
#
# The test key can read and write BOTH buckets (data and evil), like the MinIO
# root user, so that "foreign bucket" probes are blocked by the worker and not
# by a store-side permission.
# =============================================================================

set -u
cd "$(dirname "$0")"

STORE=${STORE:-minio}
URL_STYLE=${URL_STYLE:-path}
NET=tilsec-net
S3=tilsec-s3
CURL=curlimages/curl:latest
CURL_BOX=tilsec-curl   # one long-lived curl container; `docker exec` is much cheaper than `docker run`
read -r -a IMAGES <<< "${IMAGES:-general spatial ml}"
for i in "${IMAGES[@]}"; do
  case "$i" in general|spatial|ml) ;; *) echo "unknown image in IMAGES: $i" >&2; exit 2 ;; esac
done

case "$STORE" in
  minio)
    STORE_IMAGE=cgr.dev/chainguard/minio:latest
    PORT=9000
    REGION=us-east-1
    ALT_REGION=garage
    ADMIN=secadmin
    SECRET=sec-secret-7f3a9c
    ;;
  garage)
    STORE_IMAGE=dxflrs/garage:v2.4.1
    PORT=3900
    REGION=garage
    ALT_REGION=us-east-1
    ADMIN=GK0123456789abcdef01234567   # Garage key ids are GK + 24 hex
    SECRET=0a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef0a1b2c3d4e5f6071
    ;;
  *) echo "STORE must be minio or garage" >&2; exit 2 ;;
esac
case "$URL_STYLE" in
  path|vhost) ;;
  *) echo "URL_STYLE must be path or vhost" >&2; exit 2 ;;
esac

# vhost addressing needs bucket.<domain> to resolve: give the store container
# the domain and one alias per bucket on the private network.
HOST="$S3"
ALIAS_ARGS=()
DOMAIN=""
if [ "$URL_STYLE" = vhost ]; then
  DOMAIN=s3.tilsec
  HOST="$DOMAIN"
  ALIAS_ARGS=(--network-alias "$DOMAIN" --network-alias "data.$DOMAIN" --network-alias "evil.$DOMAIN")
fi
EP="$HOST:$PORT"
SIGV4="aws:amz:$REGION:s3"

cleanup() {
  docker rm -f "$S3" "$CURL_BOX" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
}
WORK_DIR=$(mktemp -d)
chmod 755 "$WORK_DIR"  # the curl image runs as a non-root user
trap 'cleanup; rm -rf "$WORK_DIR"' EXIT
cleanup

if [ "${BUILD:-0}" = 1 ]; then
  for i in "${IMAGES[@]}"; do
    docker build -q -f "$i/Dockerfile" -t "tilcayo/worker-$i:test" . >/dev/null || exit 1
  done
fi

# ── Store setup ───────────────────────────────────────────────────────────────

docker network create "$NET" >/dev/null || exit 1

# $WORK_DIR is mounted at /seed so seed_put can upload files written there later.
docker run -d --name "$CURL_BOX" --network "$NET" -v "$WORK_DIR:/seed:ro" \
  --entrypoint sleep "$CURL" infinity >/dev/null || exit 1

s3curl() {  # s3curl <curl args...>  (admin creds, inside the network)
  docker exec "$CURL_BOX" curl -s --user "$ADMIN:$SECRET" --aws-sigv4 "$SIGV4" "$@"
}

start_minio() {
  docker run -d --name "$S3" --network "$NET" "${ALIAS_ARGS[@]}" \
    -e MINIO_ROOT_USER="$ADMIN" -e MINIO_ROOT_PASSWORD="$SECRET" \
    ${DOMAIN:+-e MINIO_DOMAIN="$DOMAIN"} \
    "$STORE_IMAGE" server /data >/dev/null || exit 1
  for _ in $(seq 1 30); do
    s3curl -o /dev/null -w '%{http_code}' "http://$EP/minio/health/live" | grep -q 200 && break
    sleep 1
  done
  for b in data evil; do s3curl -X PUT "http://$EP/$b" >/dev/null; done
}

# Garage: single node, layout assigned by hand, buckets and the key created
# through its CLI (there is no bucket-create over S3 for a key without the
# create-bucket flag, and the key must be granted on each bucket).
start_garage() {
  local ROOT_DOMAIN_LINE=""
  [ -n "$DOMAIN" ] && ROOT_DOMAIN_LINE="root_domain = \".$DOMAIN\""
  cat > "$WORK_DIR/garage.toml" <<EOF
metadata_dir = "/tmp/meta"
data_dir = "/tmp/data"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "0.0.0.0:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = "1799bccfd7411eddcf9ebd316bc1f5287ad12a68094e1c6ac6abde7e6feae1ec"

[s3_api]
s3_region = "$REGION"
api_bind_addr = "0.0.0.0:$PORT"
$ROOT_DOMAIN_LINE
EOF
  docker run -d --name "$S3" --network "$NET" "${ALIAS_ARGS[@]}" \
    -v "$WORK_DIR/garage.toml:/etc/garage.toml:ro" \
    "$STORE_IMAGE" >/dev/null || exit 1
  garage() { docker exec "$S3" /garage "$@" 2>/dev/null; }
  local node=""
  for _ in $(seq 1 30); do
    node=$(garage node id -q | cut -d@ -f1)
    [ -n "$node" ] && break
    sleep 1
  done
  garage layout assign -z dc1 -c 1G "$node" >/dev/null
  garage layout apply --version 1 >/dev/null
  for b in data evil; do garage bucket create "$b" >/dev/null; done
  garage key import --yes -n tilsec "$ADMIN" "$SECRET" >/dev/null
  for b in data evil; do garage bucket allow --read --write --owner "$b" --key tilsec >/dev/null; done
  for _ in $(seq 1 30); do
    [ "$(s3curl -o /dev/null -w '%{http_code}' "http://$EP/data?list-type=2")" = 200 ] && break
    sleep 1
  done
}

"start_$STORE"

# Seed inputs: a parquet in data/in/, and decoy objects in evil/ so foreign-bucket
# reads would succeed if not blocked. Generated by a worker image with the
# entrypoint bypassed (setup only, not under test).
SEED_DIR="$WORK_DIR"
docker run --rm --user "$(id -u):$(id -g)" -v "$SEED_DIR:/out" --entrypoint python tilcayo/worker-spatial:test -c "
import duckdb, os
con = duckdb.connect(config={'extension_directory': os.environ['TILCAYO_EXTENSION_DIR']}); con.execute('LOAD spatial')
con.execute(\"COPY (SELECT 42 AS x, 'probe' AS s) TO '/out/probe.parquet'\")
con.execute(\"COPY (SELECT 1 AS id, ST_Point(1,2) AS geom) TO '/out/pts.geojson' (FORMAT GDAL, DRIVER 'GeoJSON')\")
" || exit 1
seed_put() {  # seed_put <local file> <bucket/key>
  s3curl -o /dev/null -w '%{http_code}' -T "/seed/$1" "http://$EP/$2"
}
seed_put probe.parquet data/in/probe.parquet >/dev/null
seed_put pts.geojson   data/in/pts.geojson   >/dev/null
seed_put probe.parquet data/private/x.parquet >/dev/null  # same bucket, outside in/
seed_put probe.parquet evil/x.parquet        >/dev/null
seed_put pts.geojson   evil/pts.geojson      >/dev/null

list_bucket() { s3curl "http://$EP/$1?list-type=2"; }
object_exists() {  # object_exists <bucket/key>
  [ "$(s3curl -o /dev/null -w '%{http_code}' -I "http://$EP/$1")" = 200 ]
}

if ! object_exists data/in/probe.parquet; then
  echo "Setup failed: could not seed s3://data/in/probe.parquet" >&2
  exit 1
fi
EVIL_BASELINE=$(list_bucket evil | grep -o '<Key>[^<]*</Key>' | sort)

# ── Runner ────────────────────────────────────────────────────────────────────

RESULTS=()
FAILS=0
PASSES=0
INFOS=0
JOB=0
OUT=""      # combined stdout+stderr of the last run
RC=0        # exit code of the last run
OUTKEY=""   # bucket/key of the last run's OUTPUT_PATH

# Workers run the way the control plane starts them: as the operator's
# uid:gid, read-only root filesystem with a disk-backed /tmp (DuckDB spills
# there), no capabilities, no privilege escalation.
HARDEN=(--user "$(id -u):$(id -g)" --read-only -v /tmp
        --cap-drop ALL --security-opt no-new-privileges)

# Local data root, mounted read-only at /data (as TILCAYO_DATA_SOURCE would
# be). Symlinks point outside the root; /data2 is a sibling that is mounted
# but not allow-listed.
LOCAL_DATA="$WORK_DIR/data"
mkdir -p "$LOCAL_DATA/sub" "$WORK_DIR/data2" "$WORK_DIR/out"
printf 'x\n42\n' > "$LOCAL_DATA/x.csv"
printf 'x\n7\n'  > "$WORK_DIR/data2/x.csv"
cp "$SEED_DIR/pts.geojson" "$LOCAL_DATA/pts.geojson"
cp "$SEED_DIR/pts.geojson" "$WORK_DIR/data2/pts.geojson"
ln -s /etc/passwd "$LOCAL_DATA/link_file"
ln -s /etc        "$LOCAL_DATA/link_dir"

# run_worker <image> <query>  — real entrypoint, fresh job output path.
# Optional overrides: ALLOWED (allow-list), WREGION (TILCAYO_S3_REGION),
# STDOUT_ONLY=1 (no OUTPUT_PATH, result is printed), STRAY_AWS=1 (also inject
# the keys as AWS_* vars, as a misconfigured host might), LOCAL=1 (store-free
# job: /data read-only, own /out dir, OUTPUT_PATH=/out/result.parquet),
# WOUT (override OUTPUT_PATH), EXTRA (extra docker run args, word-split).
run_worker() {
  JOB=$((JOB + 1))
  OUTKEY="data/out/job$JOB/result.parquet"
  OUTFILE=""
  local out_args=(-e OUTPUT_PATH="${WOUT:-s3://$OUTKEY}")
  local allowed=${ALLOWED:-s3://data/in/}
  local local_args=()
  if [ "${LOCAL:-0}" = 1 ]; then
    mkdir -p "$WORK_DIR/out/job$JOB"
    OUTFILE="$WORK_DIR/out/job$JOB/result.parquet"
    out_args=(-e OUTPUT_PATH="${WOUT:-/out/result.parquet}")
    allowed=${ALLOWED:-s3://data/in/,/data/}
    local_args=(-v "$LOCAL_DATA:/data:ro" -v "$WORK_DIR/data2:/data2:ro" -v "$WORK_DIR/out/job$JOB:/out")
  fi
  [ "${STDOUT_ONLY:-0}" = 1 ] && out_args=()
  local stray_args=()
  [ "${STRAY_AWS:-0}" = 1 ] && stray_args=(-e AWS_ACCESS_KEY_ID="$ADMIN" -e AWS_SECRET_ACCESS_KEY="$SECRET")
  # shellcheck disable=SC2086  # EXTRA is deliberately word-split
  OUT=$(docker run --rm --network "$NET" "${HARDEN[@]}" ${EXTRA:-} \
    ${local_args[@]+"${local_args[@]}"} \
    -e TILCAYO_S3_KEY_ID="$ADMIN" -e TILCAYO_S3_SECRET="$SECRET" \
    ${stray_args[@]+"${stray_args[@]}"} \
    -e TILCAYO_S3_ENDPOINT="$EP" -e TILCAYO_S3_URL_STYLE="$URL_STYLE" -e TILCAYO_S3_USE_SSL=false \
    -e TILCAYO_S3_REGION="${WREGION:-$REGION}" \
    -e TILCAYO_ALLOWED_INPUTS="$allowed" \
    ${out_args[@]+"${out_args[@]}"} \
    -e QUERY="$2" \
    "tilcayo/worker-$1:test" 2>&1)
  RC=$?
}

# Did the last run write its output (S3 object, or the local result file)?
output_written() {
  if [ -n "$OUTFILE" ]; then [ -e "$OUTFILE" ]; else object_exists "$OUTKEY"; fi
}

# expect_ok <image> <case>: the last run must exit 0 and write its output.
expect_ok() {
  if [ "$RC" -eq 0 ] && output_written; then
    record "$1" "$2" PASS "exit 0, output written"
  else
    record "$1" "$2" FAIL "exit $RC: $(first_err)"
  fi
}

record() {  # record <image> <case> <PASS|FAIL|INFO> <detail>
  RESULTS+=("$(printf '%-8s %-34s %-4s %s' "$1" "$2" "$3" "$4")")
  case "$3" in
    PASS) PASSES=$((PASSES + 1)) ;;
    INFO) INFOS=$((INFOS + 1)) ;;
    *)    FAILS=$((FAILS + 1)) ;;
  esac
}

# Prefer the line that names the error over DuckDB's trailing caret marker;
# on a worker failure that is the `tilcayo-error: ` sentinel, printed last.
first_err() {
  local l
  l=$(printf '%s' "$OUT" | grep -E '^tilcayo-error: |Error|HTTP|denied|forbidden' | tail -n 1 | cut -c1-90)
  [ -n "$l" ] || l=$(printf '%s' "$OUT" | grep -v '^\s*$' | tail -n 1 | cut -c1-90)
  printf '%s' "$l"
}

# expect_blocked <image> <case> <query>: must exit non-zero, write no output
# object, and must not print the secret.
expect_blocked() {
  run_worker "$1" "$3"
  if [ "$RC" -eq 0 ]; then
    record "$1" "$2" FAIL "exit 0: $(first_err)"
  elif printf '%s' "$OUT" | grep -qF "$SECRET"; then
    record "$1" "$2" FAIL "secret in output"
  elif output_written; then
    record "$1" "$2" FAIL "output written"
  else
    record "$1" "$2" PASS "exit $RC: $(first_err)"
  fi
}

check_evil_unchanged() {  # check_evil_unchanged <image> <case>
  local now
  now=$(list_bucket evil | grep -o '<Key>[^<]*</Key>' | sort)
  if [ "$now" = "$EVIL_BASELINE" ]; then
    record "$1" "$2" PASS "evil bucket unchanged"
  else
    record "$1" "$2" FAIL "evil bucket changed: $(echo "$now" | tr '\n' ' ')"
  fi
}

# Secret must not appear in stdout/stderr or in the output object.
check_leak() {  # check_leak <image>
  run_worker "$1" "SELECT current_setting('s3_secret_access_key') AS v"
  local obj=""
  if object_exists "$OUTKEY"; then obj=$(s3curl "http://$EP/$OUTKEY" | tr -d '\0'); fi
  if printf '%s%s' "$OUT" "$obj" | grep -qF "$SECRET"; then
    record "$1" "leak current_setting(secret)" FAIL "secret leaked (exit $RC)"
  else
    record "$1" "leak current_setting(secret)" PASS "exit $RC, secret absent"
  fi
}

EVIL_SECRET="CREATE SECRET evil (TYPE S3, KEY_ID '$ADMIN', SECRET '$SECRET', ENDPOINT '$EP', URL_STYLE 'path', USE_SSL false, SCOPE 's3://evil')"
SHADOW_SECRET="CREATE SECRET shadow (TYPE S3, KEY_ID 'attacker', SECRET 'attacker', ENDPOINT 'attacker.example:9000', URL_STYLE 'path', USE_SSL false, SCOPE 's3://data/out/')"

for img in "${IMAGES[@]}"; do
  # Positive control first: proves the store and allow-list work at all.
  run_worker "$img" "SELECT x * 2 AS y FROM read_parquet('s3://data/in/probe.parquet')"
  if [ "$RC" -eq 0 ] && object_exists "$OUTKEY"; then
    record "$img" "positive control (read in/, write out)" PASS "exit 0, object exists"
  else
    record "$img" "positive control (read in/, write out)" FAIL "exit $RC: $(first_err)"
  fi
  SIBLING_KEY="$OUTKEY"  # another job's result, for the sibling-read probe
  # A trailing '--' comment must not swallow the COPY's closing paren.
  run_worker "$img" "SELECT x FROM read_parquet('s3://data/in/probe.parquet') -- trailing comment"
  if [ "$RC" -eq 0 ] && object_exists "$OUTKEY"; then
    record "$img" "positive: trailing -- comment" PASS "exit 0, object exists"
  else
    record "$img" "positive: trailing -- comment" FAIL "exit $RC: $(first_err)"
  fi

  check_leak "$img"

  # The keys stay in the process's initial environment even after the scrub;
  # only the allow-list keeps user SQL from reading it.
  expect_blocked "$img" "read /proc/self/environ" "SELECT content FROM read_text('/proc/self/environ')"
  expect_blocked "$img" "read_blob /proc/self/environ" "SELECT content FROM read_blob('/proc/self/environ')"

  # Stray AWS_* vars: dropped with a warning naming them, never readable via
  # current_setting(), and the job still runs on the TILCAYO_S3_* keys.
  STRAY_AWS=1 run_worker "$img" "SELECT current_setting('s3_secret_access_key') AS v, current_setting('s3_access_key_id') AS k"
  STRAY_OBJ=""
  if object_exists "$OUTKEY"; then STRAY_OBJ=$(s3curl "http://$EP/$OUTKEY" | tr -d '\0'); fi
  if printf '%s%s' "$OUT" "$STRAY_OBJ" | grep -qF "$SECRET"; then
    record "$img" "stray AWS_*: no leak" FAIL "secret leaked (exit $RC)"
  else
    record "$img" "stray AWS_*: no leak" PASS "exit $RC, secret absent"
  fi
  if printf '%s' "$OUT" | grep -q 'Warning: ignoring and removing AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY'; then
    record "$img" "stray AWS_*: warning logged" PASS "names logged, values not"
  else
    record "$img" "stray AWS_*: warning logged" FAIL "no warning: $(first_err)"
  fi
  STRAY_AWS=1 run_worker "$img" "SELECT x FROM read_parquet('s3://data/in/probe.parquet')"
  if [ "$RC" -eq 0 ] && object_exists "$OUTKEY"; then
    record "$img" "stray AWS_*: job still runs" PASS "exit 0, object exists"
  else
    record "$img" "stray AWS_*: job still runs" FAIL "exit $RC: $(first_err)"
  fi

  expect_blocked "$img" "break-out multi-statement" \
    "SELECT 1 AS a) TO 's3://data/out/decoy/x.parquet'; $EVIL_SECRET; COPY (SELECT * FROM 's3://data/in/probe.parquet') TO 's3://evil/breakout_$img.parquet'; COPY (SELECT 1 AS a"
  check_evil_unchanged "$img" "break-out: nothing in evil"

  expect_blocked "$img" "standalone CREATE SECRET"      "$EVIL_SECRET"
  expect_blocked "$img" "SCOPE-shadowing CREATE SECRET" "$SHADOW_SECRET"
  expect_blocked "$img" "read foreign bucket"           "SELECT * FROM read_parquet('s3://evil/x.parquet')"
  expect_blocked "$img" "concat path to foreign bucket" "SELECT * FROM read_parquet('s3://' || 'evil/x.parquet')"
  expect_blocked "$img" "local file outside allow-list" "SELECT content FROM read_text('/etc/passwd')"
  expect_blocked "$img" "http:// URL"                   "SELECT * FROM read_text('http://example.com/')"
  expect_blocked "$img" "ATTACH"                        "ATTACH '/tmp/x.db'"
  expect_blocked "$img" "ATTACH inside SELECT stream"   "SELECT 1; ATTACH '/tmp/x.db'"
  expect_blocked "$img" "INSTALL"                       "INSTALL spatial"

  # '..' passes DuckDB's prefix check and is sent to the store, so the store
  # decides. Reading anything outside in/ is a failure, however it is refused.
  expect_blocked "$img" "traversal in/../private/"      "SELECT * FROM read_parquet('s3://data/in/../private/x.parquet')"
  expect_blocked "$img" "traversal in/../../evil/"      "SELECT * FROM read_parquet('s3://data/in/../../evil/x.parquet')"
  expect_blocked "$img" "traversal in/%2E%2E/private/"  "SELECT * FROM read_parquet('s3://data/in/%2E%2E/private/x.parquet')"

  # DuckDB's own spill directory is not readable by user SQL.
  expect_blocked "$img" "glob spill dir /tmp/**"        "SELECT * FROM glob('/tmp/**')"

  # Only a job's own output directory is allow-listed, not the output bucket:
  # another job's result (written by the positive control) is out of reach.
  if object_exists "$SIBLING_KEY"; then
    expect_blocked "$img" "read sibling job's result" "SELECT * FROM read_parquet('s3://$SIBLING_KEY')"
  else
    record "$img" "read sibling job's result" FAIL "setup: s3://$SIBLING_KEY missing"
  fi

  # The worker refuses any local allow-list entry other than /data/, so a
  # misconfigured prefix can never expose /proc/self/environ (the keys). A
  # harmless query, so only the refusal at setup can make the job fail.
  for bad in / /proc/ /tmp/; do
    ALLOWED="$bad" expect_blocked "$img" "allowed inputs '$bad' refused" "SELECT 1 AS a"
  done
  # Same for OUTPUT_PATH, whose directory is allow-listed too: only /out/ or
  # an s3:// prefix.
  for bad in /proc/self/x.parquet /tmp/x.parquet; do
    WOUT="$bad" expect_blocked "$img" "OUTPUT_PATH '${bad%/*}/' refused" "SELECT 1 AS a"
  done

  if [ "$img" = ml ]; then
    # The single-SELECT guard parses the SQL after embed() is resolved.
    expect_blocked "$img" "embed(): second statement"   "SELECT embed('x'); DROP TABLE t"
    expect_blocked "$img" "embed(): COPY to foreign"    "SELECT embed('x'); COPY (SELECT 1 AS a) TO 's3://evil/embed_$img.parquet'"
    expect_blocked "$img" "embed(): smuggled in string" "SELECT embed('a'') ; ATTACH ''x.db'' --')"
  fi

  # ── Local files: store-free job, /data read-only, its own /out dir ──
  # Unlike S3, '..' and symlinks on local paths are checked by DuckDB itself.
  LOCAL=1 run_worker "$img" "SELECT x FROM read_csv('/data/x.csv')"
  expect_ok "$img" "local: read /data/x.csv"
  LOCAL=1 run_worker "$img" "SELECT x FROM read_csv('/data/sub/../x.csv')"
  expect_ok "$img" "local: /data/sub/../x.csv"
  LOCAL=1 run_worker "$img" "SELECT x FROM read_csv('/data/**/*.csv')"
  expect_ok "$img" "local: glob /data/**/*.csv"
  LOCAL=1 expect_blocked "$img" "local: /data/../etc/passwd"    "SELECT content FROM read_text('/data/../etc/passwd')"
  LOCAL=1 expect_blocked "$img" "local: /data/sub/../../etc/"   "SELECT content FROM read_text('/data/sub/../../etc/passwd')"
  LOCAL=1 expect_blocked "$img" "local: glob /data/../*"        "SELECT * FROM glob('/data/../*')"
  LOCAL=1 expect_blocked "$img" "local: symlink file outside"   "SELECT content FROM read_text('/data/link_file')"
  LOCAL=1 expect_blocked "$img" "local: symlink dir outside"    "SELECT content FROM read_text('/data/link_dir/passwd')"
  LOCAL=1 expect_blocked "$img" "local: sibling /data2/"        "SELECT x FROM read_csv('/data2/x.csv')"
  LOCAL=1 expect_blocked "$img" "local: file:///etc/passwd"     "SELECT content FROM read_text('file:///etc/passwd')"
  LOCAL=1 expect_blocked "$img" "local: glob /*"                "SELECT * FROM glob('/*')"
  LOCAL=1 expect_blocked "$img" "local: other job via /out/.."  "SELECT * FROM read_parquet('/out/../job1/result.parquet')"
  if [ "$img" = spatial ]; then
    # A real GeoJSON outside the root, so a refusal can't be a parse error.
    LOCAL=1 run_worker "$img" "SELECT id FROM ST_Read('/data/pts.geojson')"
    expect_ok "$img" "local: ST_Read /data/pts.geojson"
    LOCAL=1 expect_blocked "$img" "local: ST_Read /data/../data2/" "SELECT id FROM ST_Read('/data/../data2/pts.geojson')"
    LOCAL=1 expect_blocked "$img" "local: ST_Read sibling /data2/" "SELECT id FROM ST_Read('/data2/pts.geojson')"
  fi
  # The data root is read-only even if OUTPUT_PATH pointed into it.
  LOCAL=1 WOUT=/data/evil.parquet run_worker "$img" "SELECT 1 AS a"
  if [ "$RC" -ne 0 ] && [ ! -e "$LOCAL_DATA/evil.parquet" ]; then
    record "$img" "local: write into /data refused" PASS "exit $RC: $(first_err)"
  else
    record "$img" "local: write into /data refused" FAIL "exit $RC, file exists: $([ -e "$LOCAL_DATA/evil.parquet" ] && echo yes || echo no)"
  fi

  # ── Container hardening ──
  DEFAULT_UID=$(docker run --rm --entrypoint id "tilcayo/worker-$img:test" -u)
  if [ -n "$DEFAULT_UID" ] && [ "$DEFAULT_UID" != 0 ]; then
    record "$img" "image default user is non-root" PASS "uid $DEFAULT_UID"
  else
    record "$img" "image default user is non-root" FAIL "uid '$DEFAULT_UID'"
  fi
  # tini is PID 1, so SIGTERM (cancel) ends the worker at once with 143.
  CID=$(docker run -d --network "$NET" "${HARDEN[@]}" \
    -e QUERY="SELECT sum(hash(range)) FROM range(0, 1000000000000)" "tilcayo/worker-$img:test")
  sleep 3
  T0=$(date +%s); docker stop -t 10 "$CID" >/dev/null; TOOK=$(( $(date +%s) - T0 ))
  STOP_RC=$(docker inspect -f '{{.State.ExitCode}}' "$CID"); docker rm -v "$CID" >/dev/null
  if [ "$STOP_RC" = 143 ] && [ "$TOOK" -lt 5 ]; then
    record "$img" "SIGTERM exits 143 promptly" PASS "exit $STOP_RC after ${TOOK}s"
  else
    record "$img" "SIGTERM exits 143 promptly" FAIL "exit $STOP_RC after ${TOOK}s"
  fi

  # ListObjectsV2 under the allow-listed prefix (glob) must keep working.
  run_worker "$img" "SELECT x FROM read_parquet('s3://data/in/*.parquet')"
  if [ "$RC" -eq 0 ] && object_exists "$OUTKEY"; then
    record "$img" "positive: glob in/*.parquet" PASS "exit 0, object exists"
  else
    record "$img" "positive: glob in/*.parquet" FAIL "exit $RC: $(first_err)"
  fi

  if [ "$img" = spatial ]; then
    run_worker "$img" "SELECT id FROM ST_Read('s3://data/in/pts.geojson')"
    if [ "$RC" -eq 0 ] && object_exists "$OUTKEY"; then
      record "$img" "positive: ST_Read s3://data/in/" PASS "exit 0, object exists"
    else
      record "$img" "positive: ST_Read s3://data/in/" FAIL "exit $RC: $(first_err)"
    fi
    expect_blocked "$img" "ST_Read /vsis3/"   "SELECT * FROM ST_Read('/vsis3/evil/pts.geojson')"
    expect_blocked "$img" "ST_Read /vsicurl/" "SELECT * FROM ST_Read('/vsicurl/http://example.com/x.geojson')"
    expect_blocked "$img" "ST_Read foreign s3" "SELECT * FROM ST_Read('s3://evil/pts.geojson')"
  fi
  check_evil_unchanged "$img" "evil bucket unchanged (end)"
done

# ── Store compatibility (once, on the general image) ──────────────────────────

# Multipart write: COPY ... TO a result big enough that DuckDB splits the upload.
# An ETag with a "-<parts>" suffix shows the store completed a multipart upload.
# The rows are read back by a second job to prove the object is intact.
MP_ROWS=1200000
run_worker general "SELECT i, md5(i::VARCHAR) AS a, md5((i*7)::VARCHAR) AS b, md5((i*13)::VARCHAR) AS c FROM range($MP_ROWS) t(i)"
MP_KEY="$OUTKEY"
if [ "$RC" -ne 0 ] || ! object_exists "$MP_KEY"; then
  record general "multipart write (COPY TO)" FAIL "exit $RC: $(first_err)"
else
  MP_ETAG=$(s3curl -I "http://$EP/$MP_KEY" | tr -d '\r' | grep -i '^etag:' | cut -d' ' -f2)
  MP_SIZE=$(s3curl -I "http://$EP/$MP_KEY" | tr -d '\r' | grep -i '^content-length:' | cut -d' ' -f2)
  ALLOWED="s3://${MP_KEY%/*}/" STDOUT_ONLY=1 run_worker general "SELECT count(*) AS n FROM read_parquet('s3://$MP_KEY')"
  if [ "$RC" -eq 0 ] && printf '%s' "$OUT" | grep -q "$MP_ROWS"; then
    record general "multipart write (COPY TO)" PASS "$MP_ROWS rows read back, $MP_SIZE bytes, etag $MP_ETAG"
  else
    record general "multipart write (COPY TO)" FAIL "read-back failed, etag $MP_ETAG: $(first_err)"
  fi
fi

# ── Resources: memory_limit from the cgroup, spilling on a read-only root ────

# memory_limit = TILCAYO_MEMORY_FRACTION (default 0.5) of the container limit.
STDOUT_ONLY=1 EXTRA="--memory 1g" run_worker general "SELECT current_setting('memory_limit') AS m"
if [ "$RC" -eq 0 ] && printf '%s' "$OUT" | grep -q '512.0 MiB'; then
  record general "memory_limit = 0.5 x 1g (default)" PASS "512.0 MiB"
else
  record general "memory_limit = 0.5 x 1g (default)" FAIL "exit $RC: $(printf '%s' "$OUT" | grep -o '[0-9.]* [MG]iB' | head -1)"
fi
STDOUT_ONLY=1 EXTRA="--memory 1g -e TILCAYO_MEMORY_FRACTION=0.25" run_worker general "SELECT current_setting('memory_limit') AS m"
if [ "$RC" -eq 0 ] && printf '%s' "$OUT" | grep -q '256.0 MiB'; then
  record general "memory_limit = 0.25 x 1g (configured)" PASS "256.0 MiB"
else
  record general "memory_limit = 0.25 x 1g (configured)" FAIL "exit $RC: $(printf '%s' "$OUT" | grep -o '[0-9.]* [MG]iB' | head -1)"
fi
STDOUT_ONLY=1 EXTRA="-e TILCAYO_MEMORY_FRACTION=2" run_worker general "SELECT 1 AS a"
if [ "$RC" -eq 1 ] && printf '%s' "$OUT" | grep -q 'TILCAYO_MEMORY_FRACTION must be in'; then
  record general "bad TILCAYO_MEMORY_FRACTION refused" PASS "exit 1"
else
  record general "bad TILCAYO_MEMORY_FRACTION refused" FAIL "exit $RC: $(first_err)"
fi

# A GROUP BY bigger than a ~100 MB memory_limit spills to /tmp/duckdb and
# succeeds. Without a writable /tmp the same job fails creating the spill
# dir, which shows the spill really happened.
SPILL_Q="SELECT md5(range::VARCHAR) AS m, count(*) AS c FROM range(0, 5000000) GROUP BY m"
LOCAL=1 EXTRA="--cpus 1 --memory 1g -e TILCAYO_MEMORY_FRACTION=0.1" run_worker general "$SPILL_Q"
expect_ok general "spill to /tmp on read-only root"
SPILL_OUT=$(docker run --rm --user "$(id -u):$(id -g)" --read-only --tmpfs "/out:uid=$(id -u)" \
  --cpus 1 --memory 1g -e TILCAYO_MEMORY_FRACTION=0.1 -e OUTPUT_PATH=/out/result.parquet \
  -e QUERY="$SPILL_Q" tilcayo/worker-general:test 2>&1)
if printf '%s' "$SPILL_OUT" | grep -q 'Failed to create directory "/tmp/duckdb"'; then
  record general "spill needs writable /tmp (control)" PASS "fails without /tmp"
else
  record general "spill needs writable /tmp (control)" FAIL "$(printf '%s' "$SPILL_OUT" | grep Error | tail -1 | cut -c1-80)"
fi

# Presigned GET of a worker result. Signed with stdlib SigV4 (query-string
# auth), path-style, host = $EP, the way the control plane will sign it.
run_worker general "SELECT x FROM read_parquet('s3://data/in/probe.parquet')"
PRESIGN_PY=$(cat <<'PY'
import sys, hmac, hashlib, datetime, urllib.parse as u
host, region, key_id, secret, path = sys.argv[1:6]
now = datetime.datetime.now(datetime.timezone.utc)
amz, day = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d")
scope = f"{day}/{region}/s3/aws4_request"
q = {"X-Amz-Algorithm": "AWS4-HMAC-SHA256", "X-Amz-Credential": f"{key_id}/{scope}",
     "X-Amz-Date": amz, "X-Amz-Expires": "300", "X-Amz-SignedHeaders": "host"}
qs = "&".join(u.quote(k, safe="") + "=" + u.quote(v, safe="") for k, v in sorted(q.items()))
canon = "\n".join(["GET", u.quote(path, safe="/"), qs, f"host:{host}\n", "host", "UNSIGNED-PAYLOAD"])
sts = "\n".join(["AWS4-HMAC-SHA256", amz, scope, hashlib.sha256(canon.encode()).hexdigest()])
k = ("AWS4" + secret).encode()
for part in (day, region, "s3", "aws4_request"):
    k = hmac.new(k, part.encode(), hashlib.sha256).digest()
sig = hmac.new(k, sts.encode(), hashlib.sha256).hexdigest()
print(f"http://{host}{path}?{qs}&X-Amz-Signature={sig}")
PY
)
PRESIGNED=$(docker run --rm --entrypoint python tilcayo/worker-general:test -c "$PRESIGN_PY" \
  "$EP" "$REGION" "$ADMIN" "$SECRET" "/$OUTKEY")
curl_net() { docker exec "$CURL_BOX" curl -s "$@"; }
PS_CODE=$(curl_net -o /dev/null -w '%{http_code}' "$PRESIGNED")
PS_MAGIC=$(curl_net "$PRESIGNED" | head -c 4)
LAST="${PRESIGNED: -1}"
[ "$LAST" = a ] && FLIP=b || FLIP=a
BAD_CODE=$(curl_net -o /dev/null -w '%{http_code}' "${PRESIGNED%?}$FLIP")
# Also a signature for a different key (same URL, other object) must not work.
BAD2_CODE=$(curl_net -o /dev/null -w '%{http_code}' "${PRESIGNED/\/data\/out\//\/data\/in\/}")
if [ "$PS_CODE" = 200 ] && [ "$PS_MAGIC" = PAR1 ]; then
  record general "presigned GET of result" PASS "200, parquet magic PAR1"
else
  record general "presigned GET of result" FAIL "status $PS_CODE, magic '$PS_MAGIC'"
fi
if [ "$BAD_CODE" != 200 ] && [ "$BAD2_CODE" != 200 ]; then
  record general "presigned GET: tampering refused" PASS "bad sig $BAD_CODE, other key $BAD2_CODE"
else
  record general "presigned GET: tampering refused" FAIL "bad sig $BAD_CODE, other key $BAD2_CODE"
fi

# Region handling: observed, not asserted. $REGION must work (the whole suite
# ran on it); report what happens with the other store's default region and
# with the region unset (Tilcayo's default is us-east-1).
WREGION="$ALT_REGION" run_worker general "SELECT x FROM read_parquet('s3://data/in/probe.parquet')"
record general "region $ALT_REGION (not configured)" INFO "exit $RC: $(first_err)"


# ── Report ────────────────────────────────────────────────────────────────────

echo ""
echo "store=$STORE image=$STORE_IMAGE url_style=$URL_STYLE region=$REGION"
printf '%-8s %-34s %-4s %s\n' IMAGE CASE RES DETAIL
printf '%s\n' "${RESULTS[@]}"
echo ""
echo "$PASSES passed, $FAILS failed, $INFOS informational"
[ "$FAILS" -eq 0 ]
