# Leaflow OKData API (v1)

Public social, creator, advertising, and e-commerce data across 30 platforms for Leaflow projects, with resource-oriented reads, structured searches, project authentication, and idempotent usage reporting.

## Structure

- `openapi.yaml` — Entry point (`https://okdata.leaflow.cloud`), management/discovery/request-history operations, and `$ref` index of all 30 platforms.
- `common.yaml` — Shared management resources, `ResultMetadata`, `DataCursorPagination`, `View`, `Fields`, `IdempotencyKey`, `MissingItem`, and platform `Error` envelope.
- `models/*.yaml` — Shared canonical domain schemas across all platforms (`social`, `commerce`, `ads`, `live`, `creator`).
- `platforms/*.yaml` — Platform-specific resource paths, typed `GET` parameters, and `QUERY` request schemas.
- `catalogue.json` — Synchronized machine-readable operation index (without supplier costs).

## Conventions

- **Enablement**: Enable OKData once per project in Console (`POST /api/v1/enablement`) before invoking metered queries (`okdata:query.invoke`). Authenticate with a project IAM scoped token.
- **Free discovery & history**: `GET /api/v1/capabilities`, `GET /api/v1/capabilities/{capabilityId}`, `GET /api/v1/requests`, `GET /api/v1/requests/{requestId}`, and `GET /api/v1/requests/{requestId}/result` do not execute paid data queries or incur charges. Discovery uses a shared 60-second cache of the free source status feed; unconfirmed availability is `unknown`, not `available`.
- **Generated links**: `POST /api/v1/wechat-channels/videos/{video_id}/share-links` creates a share link. Replaying its idempotency key returns the same saved link without another source call.
- **Idempotency**: Every metered query requires an `Idempotency-Key` header (valid 24 hours per project). Matching retries return the saved response (`meta.replayed: true`) with no additional charge. Use a fresh key for each new page.
- **Pagination**: Paginated queries return `pagination` (`returned`, `has_more`, `next_cursor`, `cursor_expires_at`). Pass `next_cursor` back unchanged as `cursor` with the same query parameters (`view` and `fields` may vary). Cursor TTL is 24 hours (10 minutes for `zhihu.questions.answers.list`).
- **Batch reads**: `GET /api/v1/x/users:batchGet` (up to 100), `GET /api/v1/douyin/xingtu/creators:batchGet` (up to 50), and `GET /api/v1/douyin/users:batchGetHistory` (up to 5) accept repeated `ids` query parameters and return `{ items, missing, meta }`.
- **Projection**: All data queries accept optional `view` (`brief`, `compact`, `standard`, `full`) and `fields` without changing billing.
- **Billing**: Retail prices, currency, taxes, and invoices are owned by Leaflow Billing (`requests` meter for per-call queries; `items` meter with per-request min/max bounds for per-item queries). Supplier account billing details are never exposed.
