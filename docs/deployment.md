# Deployment

A typical single-host setup: imgproxy on `127.0.0.1:8080`, the gateway on `127.0.0.1:8090`, nginx terminating TLS and caching in front of both.

## 1. imgproxy

Install imgproxy (Docker image or a native build) and configure signing. Example `/etc/imgproxy.env`:

```sh
IMGPROXY_BIND=127.0.0.1:8080
IMGPROXY_KEY=<openssl rand -hex 32>
IMGPROXY_SALT=<openssl rand -hex 32>

# s3:// sources
IMGPROXY_USE_S3=true
IMGPROXY_S3_ENDPOINT=https://s3.example.com
IMGPROXY_S3_ENDPOINT_USE_PATH_STYLE=true
IMGPROXY_S3_REGION=us-east-1
AWS_ACCESS_KEY_ID=<read-only key>
AWS_SECRET_ACCESS_KEY=<secret>

# Every bucket/scheme any tenant uses. The gateway enforces per-tenant rules on top.
IMGPROXY_ALLOWED_SOURCES=s3://example-public/,https://
# Don't let https:// sources resolve to internal addresses (loopback/link-local are already refused).
IMGPROXY_ALLOW_PRIVATE_SOURCE_ADDRESSES=false

IMGPROXY_TTL=31536000
IMGPROXY_AUTO_WEBP=true
IMGPROXY_AUTO_AVIF=true
```

Give imgproxy a **read-only** S3 key limited to the buckets tenants use.

## 2. Gateway

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o mediacdn-gateway .
install -m 755 mediacdn-gateway /usr/local/bin/
install -d /etc/mediacdn
install -m 640 -g imgproxy gateway.example.json /etc/mediacdn/gateway.json   # then edit
install -m 644 deploy/mediacdn-gateway.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now mediacdn-gateway
curl -s http://127.0.0.1:8090/healthz   # OK
```

The unit reads `IMGPROXY_KEY` / `IMGPROXY_SALT` from the same env file as imgproxy, so the two always agree. Set `"listen": "127.0.0.1:8090"` in `gateway.json` if nginx runs on the same host.

## 3. nginx

Start from [`deploy/nginx.conf`](../deploy/nginx.conf):

- Route `/t/` to the gateway with `proxy_cache`; the cache key must include the query string (`$uri$is_args$args`) because `w`/`q` live there on the redirect step.
- Do **not** cache by `Vary: Accept` on the canonical URLs — the format is already in the path.
- Optionally route everything else to imgproxy if older, directly signed imgproxy URLs must keep working.
- Size `max_size` for your disk; `inactive=30d` drops variants nobody requests.

## 4. DNS / CDN in front

The gateway works behind a CDN (e.g. Cloudflare proxy) or directly. Canonical responses are `public, s-maxage=604800, max-age=31536000, immutable`, so any shared cache can keep them; the `302` is `private`.

## Monitoring

- `GET /healthz` on the gateway (process up).
- A real canonical image URL through the public hostname (end-to-end: nginx → gateway → imgproxy → storage).
- imgproxy's own `/health`.

## Upgrading

Build, replace `/usr/local/bin/mediacdn-gateway`, `systemctl restart mediacdn-gateway`. URLs are stable across versions; cached variants stay valid.
