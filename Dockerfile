FROM debian:13-slim

ENV XDG_DATA_HOME=/data

COPY caddy /usr/bin/caddy
