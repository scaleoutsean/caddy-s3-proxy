# Versity S3 Gateway example

This example runs the Caddy S3 proxy against Versity S3 Gateway (VGW), using the POSIX backend and the same dummy root credentials as the CI test workflow. VGW stores objects in `example/data`.

## Run the demo

Prerequisites: Docker Compose, `mc` (MinIO Client), and a built Caddy image with the S3 proxy. From the repository root:

```sh
make docker
docker compose -f example/docker-compose.yml up -d
./example/awslocal/populate.sh
```

The population script uses your local `mc` executable and connects to `http://127.0.0.1:4566`. Override `VGW_HOST_PORT` and `CADDY_HOST_PORT` if those host ports are already in use; the seed script uses `VGW_HOST_PORT` automatically. You can also override `S3_ENDPOINT`, `AWS_ACCESS_KEY_ID`, or `AWS_SECRET_ACCESS_KEY` for different VGW settings.

Try the examples at `http://localhost`:

```sh
curl http://localhost/hello.txt
curl http://localhost/dev/testing/coverage-reports/2/report.txt
curl http://localhost/animals/dog.txt
```

The home page is served from `my-bucket/index.html`. The coverage route strips its URL prefix before looking up an object in `test-results`; the animals route uses the configured root prefix and enables directory browsing.

## Run the Go tests against VGW

Start only the gateway, then run the test suite from the repository root:

```sh
make vgw
make test
```

The Makefile points the AWS SDK at `http://localhost:4566` with the dummy credentials used by VGW in CI.

With the current VGW image, `TestProxy/returns_200_If-Modified-Since` fails because VGW returns `200` with the object body instead of the expected `304`. The full suite also has Caddyfile parser diagnostic-string assertions that mismatch the current Caddy version; those are unrelated to VGW.

## Configuration

The Caddyfile uses the `vgw` Compose service as the S3 endpoint and enables path-style requests. Path-style addressing is needed for this local endpoint; normal AWS S3 deployments can omit the endpoint and use the default addressing behavior.