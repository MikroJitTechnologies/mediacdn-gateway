# API reference

All endpoints accept `GET` and `HEAD` only (anything else: `405 METHOD_NOT_ALLOWED`). Every response carries `Access-Control-Allow-Origin: *`. Error bodies are `text/plain;charset=utf-8`.

## `GET /t/{token}/image/{source}`

Resolves an image request to its canonical URL.

| Part | Format | Notes |
|---|---|---|
| `token` | 48 lowercase hex chars | Tenant token. Public — it identifies the site, it is not a secret. |
| `source` | base64url (padding optional) of the source URL | `https://host/path` or `s3://bucket/key`, must be on the tenant's allow-list. |
| `w` (query) | positive integer | Snapped **up** to the nearest allowed width (below). Omitted = 3840. |
| `q` (query) | 1–100 | Quality. Default 75. |

Allowed widths (Next.js `imageSizes` + `deviceSizes`):
`16 32 48 64 96 128 256 384 640 750 828 1080 1200 1920 2048 3840`

**Response:** `302 Found`

```
Location: /t/{token}/image/{source}/w{width}_q{quality}.{ext}
Cache-Control: private, max-age=86400
Vary: Accept
```

`ext` is chosen from the request's `Accept` header:

| Accept contains | Output |
|---|---|
| `image/avif` | `avif` |
| `image/webp` | `webp` |
| neither, source ends in `.png` `.gif` `.svg` `.webp` | `png` (keeps transparency) |
| neither, anything else | `jpg` |

The redirect is per-browser (`private`) because it depends on `Accept`; only the canonical URL is shared-cacheable.

## `GET /t/{token}/image/{source}/w{width}_q{quality}.{ext}`

Returns the processed image. `width` must be exactly one of the allowed widths and `ext` one of `avif`, `webp`, `jpg`, `png`, otherwise `404 NOT_FOUND`. Images are never upscaled: a width above the source's own width returns the source width.

**Response:** `200 OK`

```
Content-Type: image/avif
Cache-Control: public, s-maxage=604800, max-age=31536000, immutable
ETag: "<sha256 of the body>"
```

Send `If-None-Match` with the ETag to get `304 Not Modified`.

## `GET /healthz`

`200 OK` with body `OK`. Does not check imgproxy.

## Errors

| Status | Body | Cause |
|---|---|---|
| 400 | `INVALID_WIDTH` | `w` is not a positive integer |
| 400 | `INVALID_QUALITY` | `q` is outside 1–100 |
| 400 | `INVALID_SOURCE` | `source` is not valid base64url, not a URL, or not `http(s)`/`s3` |
| 403 | `Unknown or disabled tenant` | token not found, or tenant has `"enabled": false` |
| 403 | `Source domain not allowed: <host>` | `http(s)` source host not in the tenant's `domains` |
| 403 | `Source bucket not allowed: <bucket>` | `s3` bucket not in the tenant's `buckets` |
| 404 | `NOT_FOUND` | unknown path, width not in the list, or imgproxy could not find/decode the source |
| 405 | `METHOD_NOT_ALLOWED` | method other than GET/HEAD |
| 502 | `UPSTREAM_ERROR` | imgproxy unreachable, returned another error, or the image exceeds 32 MiB |

## Building a URL

```js
const b64url = (s) =>
  btoa(String.fromCharCode(...new TextEncoder().encode(s)))
    .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

const url = `https://media.example.com/t/${TOKEN}/image/${b64url("s3://example-public/photo.jpg")}?w=640&q=75`;
```

```sh
src=$(printf '%s' 'https://example.com/logo.png' | base64 | tr '+/' '-_' | tr -d '=\n')
curl -sI -H 'Accept: image/avif' "https://media.example.com/t/$TOKEN/image/$src?w=256"
```

For `<img srcset>` use widths from the list above so every descriptor matches what is served.
