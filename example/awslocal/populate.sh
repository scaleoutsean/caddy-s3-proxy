#!/bin/bash
set -euo pipefail

export MC_CONFIG_DIR="$(mktemp -d)"
trap 'rm -rf "$MC_CONFIG_DIR"' EXIT

S3_ENDPOINT="${S3_ENDPOINT:-http://127.0.0.1:${VGW_HOST_PORT:-4566}}"
AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-dummy}"
AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-dummy}"

mc alias set vgw "$S3_ENDPOINT" "$AWS_ACCESS_KEY_ID" "$AWS_SECRET_ACCESS_KEY"

attempt=0
until mc ls vgw/ >/dev/null 2>&1; do
	attempt=$((attempt + 1))
	if [[ "$attempt" -ge 30 ]]; then
		echo "VGW did not become ready at $S3_ENDPOINT" >&2
		exit 1
	fi
	sleep 1
done

mc mb --ignore-existing vgw/my-bucket
printf '%s' "hello world" | mc pipe --attr "Content-Type=text/html" vgw/my-bucket/hello.txt
cat << 'EOF' | mc pipe --attr "Content-Type=text/html" vgw/my-bucket/index.html
<!DOCTYPE html><title>s3proxy Examples</title>
<h1>s3proxy Examples</h1>
<p>This is a silly sample site served by caddy and s3proxy.</p>
EOF

mc mb --ignore-existing vgw/test-results
printf '%s' "test reports" | mc pipe vgw/test-results/index.html
printf '%s' "results for test 1" | mc pipe vgw/test-results/1/report.txt
printf '%s' "results for test 2" | mc pipe vgw/test-results/2/report.txt
printf '%s' "results for test 57" | mc pipe vgw/test-results/57/report.txt
printf '%s' "results for test 784" | mc pipe vgw/test-results/784/report.txt

mc mb --ignore-existing vgw/bkt
printf '%s' "CAT" | mc pipe vgw/bkt/a/long/path/we/have/for/animals/cat.txt
printf '%s' "DOG" | mc pipe vgw/bkt/a/long/path/we/have/for/animals/dog.txt
printf '%s' "COW" | mc pipe vgw/bkt/a/long/path/we/have/for/animals/cow.txt
printf '%s' "BAT" | mc pipe vgw/bkt/a/long/path/we/have/for/animals/bat.txt

