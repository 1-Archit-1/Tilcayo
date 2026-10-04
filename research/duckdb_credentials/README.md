# DuckDB credential research probes

Throwaway evidence for "Research: DuckDB S3 credential support across providers" (issue #10). Not production code.

Reproduce (DuckDB 1.5.6 at time of writing):

```bash
docker network create tilprobe
docker run -d --name tilprobe-s3 --network tilprobe \
  -e MINIO_ROOT_USER=probeadmin -e MINIO_ROOT_PASSWORD=probe-secret-123 \
  cgr.dev/chainguard/minio:latest server /data
for b in data evil; do docker run --rm --network tilprobe curlimages/curl -s -X PUT \
  --user probeadmin:probe-secret-123 --aws-sigv4 "aws:amz:us-east-1:s3" http://tilprobe-s3:9000/$b; done

cd workers && docker build -f general/Dockerfile -t tilcayo/worker-general:probe . \
  && docker build -f spatial/Dockerfile -t tilcayo/worker-spatial:probe . && cd ..
for m in leak create_secret breakout allowlist edges; do
  docker run --rm --network tilprobe -e AWS_ACCESS_KEY_ID=probeadmin -e AWS_SECRET_ACCESS_KEY=probe-secret-123 \
    -v "$PWD/research/duckdb_credentials/probe.py:/probe/probe.py:ro" --entrypoint python \
    tilcayo/worker-general:probe /probe/probe.py $m
done
docker run --rm --network tilprobe -e AWS_ACCESS_KEY_ID=probeadmin -e AWS_SECRET_ACCESS_KEY=probe-secret-123 \
  -v "$PWD/research/duckdb_credentials/probe.py:/probe/probe.py:ro" --entrypoint python \
  tilcayo/worker-spatial:probe /probe/probe.py spatial
```

Run `leak` first: it seeds `s3://data/in/probe.parquet`, which later modes read. Credentials above are throwaway test values for a local container.
