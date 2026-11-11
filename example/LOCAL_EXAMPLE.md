# Local Root CA Example

This example is for Caddyfile-local, not related to the stuff in `example/awslocal/`.

See `Caddyfile-local` for details. Key points:
- DNS resolution uses short host names
- Caddyfile uses local Root CA-issued TLS certificate and key for Caddy host

Modify the file as required for your needs.

Run with custom caddy executable that includes required extension:

```sh
caddy run --config Caddyfile-local
```
