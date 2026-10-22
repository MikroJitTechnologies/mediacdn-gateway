# mediacdn-gateway

Tenant-scoped image URLs in front of [imgproxy](https://imgproxy.net). Each site gets a public tenant token and an allow-list of source domains / S3 buckets; the gateway signs imgproxy requests itself, so sites never hold the imgproxy key.

```
GET /t/<token>/image/<base64url(source)>?w=640&q=75
  -> 302 /t/<token>/image/<base64url(source)>/w640_q75.avif   (avif | webp | png | jpg, picked from Accept)
GET /t/<token>/image/<base64url(source)>/w640_q75.avif
  -> image, Cache-Control: public, s-maxage=604800, max-age=31536000, immutable + ETag
```

- `source` is `https://<allowed domain>/path` or `s3://<allowed bucket>/key`, checked per tenant (`*.example.com` wildcards allowed).
- `w` snaps up to the Next.js image sizes (16…3840), `q` is 1–100 (default 75). No upscaling.
- Fallback format for browsers without AVIF/WebP: PNG for png/gif/svg/webp sources (keeps alpha), JPEG otherwise.
- Errors are plain text: `Unknown or disabled tenant`, `Source domain not allowed: <host>`, `Source bucket not allowed: <bucket>`, `NOT_FOUND`.
- The 302 is `private, max-age=86400` + `Vary: Accept`; only the canonical, format-specific URL is cached publicly.

## Run

Requires imgproxy with signing enabled (and `IMGPROXY_USE_S3=true` for `s3://` sources). The gateway reads the same key/salt:

```sh
go test ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o mediacdn-gateway .

IMGPROXY_KEY=<hex> IMGPROXY_SALT=<hex> ./mediacdn-gateway gateway.json
```

`gateway.json` (see [`gateway.example.json`](gateway.example.json)):

```json
{
  "listen": ":8090",
  "imgproxy": "http://127.0.0.1:8080",
  "tenants": [
    { "id": "example", "token": "<48 hex chars>", "enabled": true,
      "domains": ["example.com", "*.example.com"], "buckets": ["example-public"] }
  ]
}
```

Generate a token with `openssl rand -hex 24`. Send `SIGHUP` (`systemctl reload`) to reload tenants without a restart.
imgproxy's `IMGPROXY_ALLOWED_SOURCES` must include the schemes/buckets tenants use (e.g. `s3://example-public/,https://`); the gateway enforces the per-tenant allow-list before signing.

Deployment examples: [`deploy/mediacdn-gateway.service`](deploy/mediacdn-gateway.service) (systemd) and [`deploy/nginx.conf`](deploy/nginx.conf) (TLS + proxy cache in front).

## Next.js loader

```ts
// lib/image-loader.ts  (next.config: images: { loader: "custom", loaderFile: "./lib/image-loader.ts" })
const CDN = "https://media.example.com";
const TOKEN = process.env.NEXT_PUBLIC_MEDIACDN_TOKEN!; // public by design

const b64url = (s: string) =>
  btoa(String.fromCharCode(...new TextEncoder().encode(s))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

export default function mediacdnLoader({ src, width, quality }: { src: string; width: number; quality?: number }) {
  const source = src.startsWith("/") ? `https://example.com${src}` : src; // or "s3://<bucket>/<key>"
  return `${CDN}/t/${TOKEN}/image/${b64url(source)}?w=${width}&q=${quality ?? 75}`;
}
```
