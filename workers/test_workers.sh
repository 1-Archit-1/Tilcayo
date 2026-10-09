#!/usr/bin/env bash
# =============================================================================
# Tilcayo Worker Validation
# Run from: tilcayo/workers/
#
# All tests use local files only — no S3, no credentials needed.
# The script generates its own test data so you don't need anything pre-made.
#
# Usage:
#   cd workers/
#   chmod +x test_workers.sh
#   ./test_workers.sh
#
# Or run individual sections by copying the commands below.
# =============================================================================

set -e
cd "$(dirname "$0")"  # always run from workers/

# Workers run the way the control plane will start them: as the caller's
# uid:gid, read-only root filesystem with a disk-backed /tmp (DuckDB spills
# there), no capabilities, no privilege escalation.
RUN=(docker run --rm --user "$(id -u):$(id -g)" --read-only -v /tmp
     --cap-drop ALL --security-opt no-new-privileges)

# Results go to a per-run /out dir, as in store-free mode: the worker refuses
# any other local OUTPUT_PATH directory.
OUT_DIR=$(mktemp -d)
trap 'rm -rf "$OUT_DIR"' EXIT

# expect_error <name> <command...>: the command must exit 1 and its last
# stderr line must be the `tilcayo-error: ` sentinel. Stops the script if not.
expect_error() {
  local name=$1 rc=0 err last
  shift
  err=$("$@" 2>&1 >/dev/null) || rc=$?
  last=$(printf '%s\n' "$err" | tail -n 1)
  if [ "$rc" -eq 1 ] && [[ "$last" == "tilcayo-error: "* ]]; then
    echo "PASS: $name: ${last:0:120}"
  else
    echo "FAIL: $name: exit $rc, last stderr line: ${last:0:120}"
    exit 1
  fi
}

# ── Build ─────────────────────────────────────────────────────────────────────

echo ""
echo "==> Building images (context: workers/ root)"
echo ""

docker build -f general/Dockerfile  -t tilcayo/worker-general:test  .
docker build -f spatial/Dockerfile  -t tilcayo/worker-spatial:test  .
docker build -f ml/Dockerfile       -t tilcayo/worker-ml:test       .

# ── Generate test data ────────────────────────────────────────────────────────

echo ""
echo "==> Generating test data in workers/testdata/"
echo ""

mkdir -p testdata

# Small CSV
cat > testdata/users.csv << 'EOF'
id,name,city,age
1,Alice,New York,31
2,Bob,London,25
3,Carol,Berlin,40
4,Dave,New York,28
5,Eve,London,35
EOF

# Second CSV for join test
cat > testdata/orders.csv << 'EOF'
user_id,product,amount
1,Widget,99.99
2,Gadget,149.50
1,Doohickey,24.99
3,Widget,99.99
5,Gadget,149.50
EOF

# Small GeoJSON (3 points — New York, London, Berlin)
cat > testdata/cities.geojson << 'EOF'
{
  "type": "FeatureCollection",
  "features": [
    {"type":"Feature","properties":{"name":"New York","pop":8336817},"geometry":{"type":"Point","coordinates":[-74.006,40.7128]}},
    {"type":"Feature","properties":{"name":"London","pop":8982000},"geometry":{"type":"Point","coordinates":[-0.1276,51.5074]}},
    {"type":"Feature","properties":{"name":"Berlin","pop":3645000},"geometry":{"type":"Point","coordinates":[13.405,52.52]}}
  ]
}
EOF

# A GeoJSON bounding box polygon (rough Europe envelope)
cat > testdata/europe_bbox.geojson << 'EOF'
{
  "type": "FeatureCollection",
  "features": [
    {"type":"Feature","properties":{"region":"Europe"},"geometry":{"type":"Polygon","coordinates":[[[-25,34],[45,34],[45,72],[-25,72],[-25,34]]]}}
  ]
}
EOF

# Pre-computed embeddings parquet (generated inline with Python)
python3 - << 'PYEOF'
import struct, os

# Minimal hand-crafted parquet with 3 rows: id, text, embedding (FLOAT[4])
# Using duckdb itself to generate the file so we don't need pyarrow here
import duckdb
con = duckdb.connect()
con.execute("""
    COPY (
        SELECT
            id,
            text,
            embedding
        FROM (VALUES
            (1, 'climate change policy', [0.1, 0.2, 0.3, 0.4]::FLOAT[4]),
            (2, 'machine learning algorithms', [0.9, 0.8, 0.1, 0.05]::FLOAT[4]),
            (3, 'renewable energy sources', [0.15, 0.25, 0.35, 0.38]::FLOAT[4])
        ) t(id, text, embedding)
    ) TO 'testdata/embeddings.parquet'
""")
print("Generated testdata/embeddings.parquet")
PYEOF

echo "Test data ready."

# =============================================================================
# GENERAL WORKER
# =============================================================================

echo ""
echo "══════════════════════════════════════════"
echo " GENERAL WORKER"
echo "══════════════════════════════════════════"

# ── Test 1: query a CSV, print to stdout
echo ""
echo "── Test 1: SELECT from CSV (stdout)"
"${RUN[@]}" \
  -v "$(pwd)/testdata:/data" \
  -e TILCAYO_ALLOWED_INPUTS=/data/ \
  -e QUERY="SELECT * FROM read_csv('/data/users.csv') WHERE city = 'New York'" \
  tilcayo/worker-general:test

# ── Test 2: join two CSVs, write output to parquet
echo ""
echo "── Test 2: JOIN two CSVs → output.parquet"
"${RUN[@]}" \
  -v "$(pwd)/testdata:/data" \
  -e TILCAYO_ALLOWED_INPUTS=/data/ \
  -e QUERY="SELECT u.name, o.product, o.amount FROM read_csv('/data/users.csv') u JOIN read_csv('/data/orders.csv') o ON u.id = o.user_id ORDER BY u.name" \
  -v "$OUT_DIR:/out" \
  -e OUTPUT_PATH="/out/output_join.parquet" \
  tilcayo/worker-general:test

echo "Wrote output_join.parquet — reading it back:"
"${RUN[@]}" \
  -v "$OUT_DIR:/data:ro" \
  -e TILCAYO_ALLOWED_INPUTS=/data/ \
  -e QUERY="SELECT * FROM read_parquet('/data/output_join.parquet')" \
  tilcayo/worker-general:test

# ── Test 3: bad query exits 1
echo ""
echo "── Test 3: Bad query should exit 1"
expect_error "unknown table" "${RUN[@]}" \
  -v "$(pwd)/testdata:/data" \
  -e QUERY="SELECT * FROM nonexistent_table" \
  tilcayo/worker-general:test

# ── Test 4: missing QUERY env var exits 1
echo ""
echo "── Test 4: Missing QUERY env var should exit 1"
expect_error "missing QUERY" "${RUN[@]}" \
  tilcayo/worker-general:test

# =============================================================================
# SPATIAL WORKER
# =============================================================================

echo ""
echo "══════════════════════════════════════════"
echo " SPATIAL WORKER"
echo "══════════════════════════════════════════"

# ── Test 5: read GeoJSON, print to stdout
echo ""
echo "── Test 5: ST_Read GeoJSON → stdout"
"${RUN[@]}" \
  -v "$(pwd)/testdata:/data" \
  -e TILCAYO_ALLOWED_INPUTS=/data/ \
  -e QUERY="SELECT name, pop, ST_AsText(geom) AS wkt FROM ST_Read('/data/cities.geojson')" \
  tilcayo/worker-spatial:test

# ── Test 6: spatial filter — cities within Europe bbox
echo ""
echo "── Test 6: Spatial filter — cities within Europe bbox"
"${RUN[@]}" \
  -v "$(pwd)/testdata:/data" \
  -e TILCAYO_ALLOWED_INPUTS=/data/ \
  -e QUERY="SELECT c.name FROM ST_Read('/data/cities.geojson') c, ST_Read('/data/europe_bbox.geojson') e WHERE ST_Within(c.geom, e.geom)" \
  tilcayo/worker-spatial:test

# ── Test 7: spatial query → output Parquet (outputs are always .parquet)
echo ""
echo "── Test 7: ST_Read → output Parquet"
"${RUN[@]}" \
  -v "$(pwd)/testdata:/data" \
  -e TILCAYO_ALLOWED_INPUTS=/data/ \
  -e QUERY="SELECT name, pop, geom FROM ST_Read('/data/cities.geojson') WHERE pop > 5000000" \
  -v "$OUT_DIR:/out" \
  -e OUTPUT_PATH="/out/output_big_cities.parquet" \
  tilcayo/worker-spatial:test

echo "Wrote output_big_cities.parquet"

# ── Test 8: spatial join with CSV (mix formats)
echo ""
echo "── Test 8: Mix CSV + GeoJSON — match users to cities by name"
"${RUN[@]}" \
  -v "$(pwd)/testdata:/data" \
  -e TILCAYO_ALLOWED_INPUTS=/data/ \
  -e QUERY="SELECT u.name AS user_name, c.name AS city_name, c.pop FROM read_csv('/data/users.csv') u JOIN ST_Read('/data/cities.geojson') c ON u.city = c.name" \
  tilcayo/worker-spatial:test

# =============================================================================
# ML WORKER
# =============================================================================

echo ""
echo "══════════════════════════════════════════"
echo " ML WORKER"
echo "══════════════════════════════════════════"

# ── Test 9: raw vector similarity (no embed macro)
echo ""
echo "── Test 9: array_distance — raw vector query"
"${RUN[@]}" \
  -v "$(pwd)/testdata:/data" \
  -e TILCAYO_ALLOWED_INPUTS=/data/ \
  -e QUERY="SELECT id, text, array_distance(embedding::FLOAT[4], [0.1, 0.2, 0.3, 0.4]::FLOAT[4]) AS dist FROM read_parquet('/data/embeddings.parquet') ORDER BY dist LIMIT 3" \
  tilcayo/worker-ml:test

# ── Test 10: embed() macro — vector generated at query time
echo ""
echo "── Test 10: embed() macro — sentence-transformers generates the vector"
echo "(Note: embed() produces 384-dim vectors; test data uses 4-dim. This test"
echo " uses a query that only exercises the macro substitution itself.)"
"${RUN[@]}" \
  -e QUERY="SELECT array_length(embed('climate change policy')) AS embedding_dimensions" \
  tilcayo/worker-ml:test

# ── Test 11: full embed() similarity search
# Requires embeddings.parquet to have 384-dim vectors — regenerate with real model
echo ""
echo "── Test 11: Full embed() similarity search (regenerates embeddings with real model)"
"${RUN[@]}" \
  -v "$(pwd)/testdata:/data" \
  -e QUERY="
    WITH docs AS (
        -- embed() is resolved before DuckDB runs, so it only accepts string literals
        SELECT 1 AS id, 'climate change policy' AS text, embed('climate change policy') AS e
        UNION ALL SELECT 2, 'machine learning algorithms', embed('machine learning algorithms')
        UNION ALL SELECT 3, 'renewable energy sources', embed('renewable energy sources')
    )
    SELECT id, text,
           array_distance(e, embed('clean energy and climate')) AS dist
    FROM docs
    ORDER BY dist
  " \
  tilcayo/worker-ml:test

# =============================================================================
# FAILURE SENTINEL — every failure exits 1, last stderr line `tilcayo-error: `
# =============================================================================

echo ""
echo "══════════════════════════════════════════"
echo " FAILURE SENTINEL"
echo "══════════════════════════════════════════"
echo ""

# ── Test 12: one case per exit path
for img in spatial ml; do
  expect_error "$img: missing QUERY" "${RUN[@]}" tilcayo/worker-$img:test
done
expect_error "unparsable SQL" "${RUN[@]}" \
  -e QUERY="SELEC 1 FROM" \
  tilcayo/worker-general:test
expect_error "non-SELECT" "${RUN[@]}" \
  -e QUERY="CREATE TABLE t(i INT)" \
  tilcayo/worker-general:test
expect_error "execution error (missing /data file)" "${RUN[@]}" \
  -v "$(pwd)/testdata:/data" \
  -e TILCAYO_ALLOWED_INPUTS=/data/ \
  -e QUERY="SELECT * FROM read_csv('/data/does_not_exist.csv')" \
  tilcayo/worker-general:test
expect_error "bad TILCAYO_ALLOWED_INPUTS (/)" "${RUN[@]}" \
  -e TILCAYO_ALLOWED_INPUTS=/ \
  -e QUERY="SELECT 1" \
  tilcayo/worker-general:test
# Offline and not baked in, so loading the model fails inside embed().
expect_error "ml: embed() error (unknown model)" "${RUN[@]}" \
  -e EMBEDDING_MODEL=no-such-model \
  -e QUERY="SELECT embed('x') AS e" \
  tilcayo/worker-ml:test
for img in general spatial ml; do
  expect_error "$img: uncaught exception" docker run --rm --entrypoint python \
    tilcayo/worker-$img:test -c "import storage; raise RuntimeError('boom')"
done

echo ""
echo "==> All tests complete."
