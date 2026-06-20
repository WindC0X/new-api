# Creative Embed API Matrix

This matrix documents the embedded Opentu Creative surface served by `new-api`.
It is intentionally session-bound: API-token-only callers must use the normal relay APIs, not these browser-session routes.

## Runtime switches

| Variable | Default | Production note |
| --- | --- | --- |
| `CREATIVE_VIDEO_RELAY_ENABLED` | `false` | Enables `/creative/relay/v1/videos*`; keep disabled until provider/channel billing and polling are configured. |
| `CREATIVE_ASSET_SYNC_ENABLED` | `false` | Enables `/creative/api/assets*`; production must not set this to `true` without also setting explicit rollout mode, storage backend, and complete S3 config. |
| `CREATIVE_ASSET_ROLLOUT_MODE` | `local` | Production cloud sync requires `production`; missing/implicit rollout mode fails closed when sync is enabled. |
| `CREATIVE_ASSET_STORAGE` | `database` | Use `s3-compatible` in production; DB storage is local/canary only. Missing S3 config fails closed. |
| `CREATIVE_ASSET_S3_*` | empty | Endpoint, region, bucket, prefix and credentials for private object storage; production endpoint must be HTTPS. |
| `CREATIVE_PUBLIC_ORIGIN` | empty | Optional canonical browser origin such as `https://console.example.com` for HTTPS reverse-proxy/TLS-termination deployments. It replaces request-scheme/host in Creative same-origin checks; raw `X-Forwarded-*` remains untrusted. |
| `FRONTEND_BASE_URL` | empty | Controls non-Creative SPA fallback only; Creative API/relay routes are local and must not redirect to this host. |

## Route matrix

Nonce applies to unsafe methods only. Safe `GET` APIs remain session/owner scoped
and `private, no-store`; `/creative/api` safe reads allow originless navigation
or bootstrap requests but reject explicit cross-site `Origin`/`Referer` signals,
while `/creative/relay/v1` safe reads require same-origin/session.

| Route | Auth/session | Nonce | Response/cache contract |
| --- | --- | --- | --- |
| `GET /creative/api/bootstrap` | browser session | no | Capability/profile JSON, `private, no-store`. |
| `POST /creative/api/assets` | browser session | yes | Uploads one image/video/audio file; JSON returns only `{id,url,contentHash,mimeType,mediaType,sizeBytes}`. |
| `GET /creative/api/assets/:assetId` | browser session + owner | no | Owner-scoped metadata; no storage/provider internals. |
| `GET /creative/api/assets/:assetId/content` | browser session + owner | no | Streams bytes with Range support, `private, no-store`, `nosniff`. |
| `DELETE /creative/api/assets/:assetId` | browser session + owner | yes | Only unreferenced assets; two-phase `pending_delete` then storage delete then quota finalization. |
| `GET /creative/api/documents` | browser session | no | Owner-scoped document list wrapper. |
| `POST /creative/api/documents` | browser session | yes | Creates an owner-scoped cloud document; request must be sanitized and nonce-protected. |
| `GET /creative/api/documents/:id` | browser session + owner | no | Owner-scoped document fetch. |
| `PUT /creative/api/documents/:id` | browser session + owner | yes | Revision-aware document update; request must be sanitized and nonce-protected. |
| `DELETE /creative/api/documents/:id` | browser session + owner | yes | Owner-scoped document delete. |
| `POST /creative/relay/v1/chat/completions` | browser session | yes | Generic session-broker chat relay; upstream credential/routing material rejected. |
| `POST /creative/relay/v1/images/generations` | browser session | yes | Generic OpenAI-compatible image relay; not the Midjourney async task API. |
| `POST /creative/relay/v1/videos` | browser session | yes + stable idempotency key | Video submit relay; server-selected channel/model/key only. |
| `GET /creative/relay/v1/videos/:task_id` | browser session + owner | no | Owner-scoped sanitized status/fetch; same-origin/session still required. |
| `GET /creative/relay/v1/videos/:task_id/content` | browser session + owner | no | Owner-scoped video content proxy; no raw provider URL exposure; same-origin/session still required. |
| `POST /creative/relay/v1/suno/submit/:action` | browser session | yes + stable idempotency key | Suno submit for `music` or `lyrics`; server derives model/action and rejects callback/notify/model/key overrides. |
| `GET /creative/relay/v1/suno/fetch/:id` | browser session + owner | no | Owner-scoped single Suno task fetch; platform must be Suno; same-origin/session still required. |
| `POST /creative/relay/v1/suno/fetch` | browser session + owner | yes | Suno list fetch; IDs are capped/normalized, platform must be Suno. |
| `POST /creative/relay/v1/mj/submit/imagine` | browser session | yes + stable idempotency key | Midjourney imagine submit; model/channel/key/callback overrides rejected. |
| `GET /creative/relay/v1/mj/task/:task_id/fetch` | browser session + owner | no | Owner-scoped Midjourney task fetch; image URL is proxied and raw provider `videoUrl` is not exposed; same-origin/session still required. |
| `POST /creative/relay/v1/mj/task/list-by-condition` | browser session + owner | yes | Midjourney list fetch; IDs are capped/normalized, platform must be Midjourney. |
| `GET /creative/relay/v1/mj/image/:task_id` | browser session + owner | no | Owner-scoped Midjourney image proxy with SSRF/redirect validation; same-origin/session still required. |

## Security boundary

- Browser relay requests may not forward upstream credentials, channel/provider/model overrides, owner/user overrides, or notify/callback/webhook fields in headers, query, JSON, forms, or multipart file names.
- `pass_headers`, wildcard header passthrough, and `{client_header:*}` placeholders must drop sensitive browser headers such as `Cookie`, `Authorization`, `X-Creative-*`, API-key/secret variants, and upstream key variants.
- Public Creative DTOs must be owner-scoped and route-specific; do not expose `channel_id`, selected keys, storage object keys, signed URLs, S3 endpoints, or raw provider credentials.
- Provider/private URLs and response bodies must be redacted before logging.
