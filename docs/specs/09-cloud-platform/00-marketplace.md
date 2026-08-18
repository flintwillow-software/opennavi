---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - cloud-platform
priority: high
spec-refs: [090000, 090001, 090002, 090003, 090004, 090005]
---

# 00 Marketplace

> Searchable catalog, filtering, preview, one-click deployment, seller publishing, and signature verification for the OpenNavi marketplace.

## 00 Catalog & Search

The marketplace provides a browsable, searchable catalog of navi agents and navi team bundles. Users discover navis by category, keyword, and various filter criteria.

### Requirement:

- The system SHALL provide a searchable marketplace catalog of navis and navi teams (FR-1.1).
- The system SHALL support filtering by category, price, rating, and popularity (FR-1.2).

### Scenario:

- GIVEN a user on the marketplace, WHEN they search "billing", THEN relevant navis appear in search results (TC-1).
- GIVEN 100+ listings in the marketplace, WHEN the user filters by category "finance" and price "<$50", THEN only matching listings return (TC-2).

### User Stories

- **US-001**: As Alex (Business Owner), I want to discover navis by category, so that I can find the right agent for my needs.

### Design Notes

The catalog is served by the Marketplace service (Catalogue + Commerce), backed by the Navi Registry. Listings include metadata such as name, description, seller, category, pricing, version, rating, and resource requirements. The API endpoint `GET /marketplace/navis` returns paginated results with items, total count, page, and page_size fields.

### Data Model

Marketplace listings follow this schema:
- `listing_id` — unique identifier for the listing
- `name` — human-readable name
- `description` — detailed description
- `seller_id` — reference to the seller account
- `category` — classification (e.g., finance, support, operations)
- `price` — pricing model (type, amount, currency, interval)
- `version` — semantic version string
- `binary_sha256` — binary hash for integrity verification
- `signature` — seller's cryptographic signature
- `verified` — whether the listing passed verification
- `rating` — aggregate user rating
- `total_purchases` — purchase count
- `interfaces` — supported navi interfaces (e.g., gateway, cron)
- `resource_requirements` — CPU, memory, storage requirements

Navi team bundles are multi-navi packages that group related agents together:
- `team_id` — unique identifier
- `navis` — list of member navis with version pins
- `price` — bundle pricing
- `p2p_config` — peer-to-peer discovery and shared board configuration

---

## 01 One-Click Deployment

Users deploy marketplace navis to the hosted runtime with a single action, without manual configuration or infrastructure setup.

### Requirement:

- The system SHALL support one-click deployment of marketplace navis to the hosted runtime (FR-1.3).

### Scenario:

- GIVEN a user views a marketplace navi, WHEN they click "Deploy", THEN the navi starts in the hosted runtime within 10 seconds (TC-3).

### User Stories

- **US-002**: As Alex, I want to deploy a marketplace navi in one click, so that I get value without setup effort.

### Design Notes

The deployment flow is:
1. User clicks "Deploy" on a marketplace listing
2. Platform calls `POST /runtime/deploy` with navi_id, version, resource_allocation, and config (including LLM provider choice and API key source)
3. Platform pulls the navi binary from the registry
4. Platform creates a sandboxed container with the allocated resources
5. Platform runs health checks
6. Navi is registered in the team manager
7. User sees "Navi is running" in the dashboard

Total cold-start time target: under 10 seconds.

---

## 02 Preview Mode

Users can evaluate a navi before purchasing through a time-limited, resource-constrained sandbox environment.

### Requirement:

- The system SHALL support navi preview mode (time-limited sandbox) before purchase (FR-1.4).

### Scenario:

- GIVEN a user previews a navi, WHEN 24 hours pass, THEN the preview sandbox is deleted and the user is prompted to purchase (TC-4).

### User Stories

- **US-009**: As Alex, I want to preview a navi before buying, so that I can evaluate fit.

### Design Notes

Preview mode provides:
- 24-hour sandbox with limited resources (256 MB RAM, 5 GB storage)
- Navi runs in a sandboxed environment with no access to external APIs
- User interacts with the navi via the dashboard chat interface
- All preview data is deleted after 24 hours
- Preview is free, limited to 3 previews per month (free tier) or unlimited (paid tiers)

---

## 03 Security & Verification

Every marketplace listing is cryptographically signed and verified before publication to ensure authenticity and integrity.

### Requirement:

- The system SHALL verify the signature of every marketplace navi before listing (FR-1.5).

### Scenario:

- GIVEN a navi with an invalid signature, WHEN the seller attempts to publish, THEN the listing is rejected with an error (TC-5).

### Design Notes

The listing verification workflow:
1. Seller submits navi binary + listing metadata
2. Platform verifies the binary signature against the seller's known public key
3. Platform performs static analysis scan for known malicious patterns and suspicious tool calls
4. Platform optionally runs a sandbox test for sensitive categories
5. Human review is triggered for flagged listings only
6. Listing is published

Edge case: marketplace navi fails signature verification — listing is rejected, seller notified.

---

## 04 Seller Publishing

Sellers can publish, version, and manage their navi listings with change notes, ratings, and reviews.

### Requirement:

- The system SHALL support seller publishing with versioning and change notes (FR-1.6).
- The system SHALL support ratings and reviews for navis and sellers (FR-1.7, Should).

### User Stories

- **US-003**: As Maya (Developer), I want to publish a navi with versioning, so that I can monetize my work and update it over time.
- **US-004**: As Maya, I want to see sales and usage analytics, so that I can improve my navis.

### Design Notes

Sellers manage their listings through the Seller Dashboard, which provides:
- Publish, version, and update navi binaries
- Change notes per version
- Sales and usage analytics
- Listing review workflow status

Ratings and reviews are aggregated across the platform, contributing to a seller reputation system.

---

## 05 Non-Functional Requirements

Cross-cutting performance, reliability, security, accessibility, and observability requirements for the cloud platform.

### Performance

- The system SHALL respond in < 300 ms (p95) for marketplace search (NFR-1).
- The system SHALL support 10,000 concurrent users (NFR-1).
- Navi cold-start SHALL be < 10 seconds (sandbox + binary launch) (NFR-1).

### Reliability

- Uptime target: 99.5% for hosted navis, 99.9% for platform API (NFR-2).
- Automatic backups every 24 hours with 30-day retention (NFR-2).
- Zero-data-loss on single-node failure (replicated storage) (NFR-2).

### Security

- All traffic SHALL be TLS 1.3 encrypted (NFR-3).
- Sandboxed navi execution with container isolation, no host access (NFR-3).
- Marketplace navis SHALL be scanned (static analysis + signature verification) before listing (NFR-3).
- Data at rest SHALL be encrypted (AES-256) (NFR-3).
- SOC 2 Type II compliance is a target (NFR-3).

### Accessibility

- WCAG 2.1 AA compliance for the dashboard UI (NFR-4).
- Keyboard navigation for all dashboard functions (NFR-4).

### Observability

- Full request logging with trace IDs (NFR-5).
- Prometheus metrics + Grafana dashboards (NFR-5).
- Error alerting with paging for platform API incidents (NFR-5).

### Scenario:

- GIVEN 10,000 concurrent users, WHEN they search the marketplace, THEN the p95 response time is less than 300 ms (TC-12, Should).

---

## Implementation Notes

### Phase 2: Marketplace — Catalog, Commerce, Preview

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Marketplace catalog (search, filter, category) | P1 | L | Navi registry |
| Seller dashboard (publish, version, analytics) | P1 | L | Marketplace catalog |
| Purchase flow (one-click, subscription add-on) | P1 | M | Billing engine |
| Preview sandbox (24h, limited resources) | P1 | M | Hosted runtime |
| Ratings and reviews | P2 | S | Marketplace catalog |
| Navi team bundles (multi-navi packages) | P2 | M | Marketplace catalog |

### Phase 5: Seller Marketplace — Payouts, Analytics, Trust

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Sales and usage analytics dashboard | P1 | M | Seller dashboard |
| Listing review workflow (auto + human) | P2 | M | Signature verification |
| Seller reputation system | P2 | S | Ratings and reviews |
| Marketplace API for third-party tools | P2 | L | Marketplace catalog |

### Integration Points

- **OpenNavi SDK** — navi binary format, signing, and verification
- **Stripe** — payment processing for marketplace purchases
- **PostgreSQL** — platform data (users, navis, listings)
- **S3-compatible storage** — navi binaries

### Order of Operations

- Phase 2 (Marketplace) and Phase 3 (Hosted Runtime) can be built concurrently
- Phase 5 (Seller Marketplace) requires Phase 2 commerce flow to be live

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from product-spec FR-1 Marketplace |