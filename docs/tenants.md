# Managing tenants

Tenants live in `gateway.json`:

```json
{
  "listen": ":8090",
  "imgproxy": "http://127.0.0.1:8080",
  "tenants": [
    {
      "id": "example",
      "token": "0123456789abcdef0123456789abcdef0123456789abcdef",
      "enabled": true,
      "domains": ["example.com", "*.example.com"],
      "buckets": ["example-public"]
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `id` | Your own label, not used in URLs. |
| `token` | 48 lowercase hex chars, goes into every URL of this tenant. |
| `enabled` | `false` makes every URL of the tenant return `403 Unknown or disabled tenant`. |
| `domains` | Hosts allowed for `https://` / `http://` sources. Exact match, or `*.example.com` for any subdomain (not the apex — list it separately). |
| `buckets` | Buckets allowed for `s3://bucket/key` sources. imgproxy's S3 key must be able to read them. |

## Add a tenant

```sh
openssl rand -hex 24          # new token
$EDITOR /etc/mediacdn/gateway.json
systemctl reload mediacdn-gateway
```

If the tenant uses a new bucket, also add it to imgproxy's `IMGPROXY_ALLOWED_SOURCES` and to the read-only S3 key's policy, then restart imgproxy.

Reload is `SIGHUP`: the new file is parsed first, and if it is invalid the gateway logs `reload failed, keeping old config` and keeps serving with the previous tenants.

Hand the site its token and the URL format from [api.md](api.md). For Next.js, the custom loader in the README is all it needs.

## Disable or rotate

- **Disable:** set `"enabled": false`, reload. Cached canonical images in nginx/CDN stay until they expire or are purged.
- **Rotate a token:** replace `token`, reload, ship the site with the new token. Old URLs stop working immediately at the gateway (cached copies excepted).

## Check a tenant

```sh
TOKEN=...; src=$(printf '%s' 's3://example-public/photo.jpg' | base64 | tr '+/' '-_' | tr -d '=\n')
curl -sI -H 'Accept: image/avif' "https://media.example.com/t/$TOKEN/image/$src?w=640"   # 302 → …/w640_q75.avif
```

A `403 Source domain not allowed: <host>` / `Source bucket not allowed: <bucket>` means the allow-list needs that entry.
