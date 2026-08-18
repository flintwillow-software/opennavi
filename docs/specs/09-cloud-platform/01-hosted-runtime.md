---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - cloud-platform
priority: high
spec-refs: [090100, 090101, 090102, 090103, 090104, 090105]
---

# 01 Hosted Runtime

> Sandboxed navi execution environment with resource limits, automatic backups, auto-updates, LLM API key management, and a web-based configuration dashboard.

## 00 Sandbox Isolation

Each navi runs in its own isolated container with strict network policies and no access to other navis or the host system.

### Requirement:

- The system SHALL run navi binaries in isolated sandboxes (container per navi) (FR-2.1).

### Scenario:

- GIVEN two navis running in the same account, WHEN one navi attempts to access another's data, THEN access is denied (TC-6).

### Design Notes

Sandbox architecture:
- Each navi runs in its own container with dedicated CPU/memory allocation
- Ephemeral storage for runtime; persisted data in platform-managed volumes
- Network access limited to LLM provider APIs only — no arbitrary internet access
- No access to other navi containers or the host system
- Resource limits enforced via cgroups
- Network policies enforced via eBPF

Data persistence model:
- Session data in platform-managed PostgreSQL (per-navi schema)
- Memory data in platform-managed vector DB (per-navi index)
- File storage in platform-managed object store (S3-compatible)

### Cold Start Flow

```
1. User deploys navi from marketplace
2. Platform pulls navi binary from registry
3. Platform creates container with resource allocation
4. Platform mounts per-navi data volumes
5. Platform configures network (LLM provider access + webhook endpoints)
6. Platform starts navi binary
7. Navi health check returns 200 OK
8. Navi registered in team manager
9. User sees "Navi is running" in dashboard
Total: < 10 seconds
```

---

## 01 Resource Limits

The platform enforces per-navi CPU, memory, and storage limits configurable per subscription tier.

### Requirement:

- The system SHALL enforce per-navi resource limits (CPU, memory, storage) (FR-2.2).

### Design Notes

Resource allocations are defined per subscription tier and per marketplace listing. The Navi Sandbox component uses cgroups for enforcement. Resource requirements are specified in marketplace listing metadata (e.g., "1 core CPU, 512 MB memory, 1 GB storage").

Edge case: user exceeds tier limits — platform shows upgrade prompt and throttles on overage (TC-10).

---

## 02 Backups & Data Persistence

Navi state, session data, and memory are automatically backed up with configurable retention.

### Requirement:

- The system SHALL provide automatic backups of navi state (sessions, memory) (FR-2.3).

### Scenario:

- GIVEN a navi's session data, WHEN the backup runs, THEN the data is stored with 30-day retention (TC-7).

### Design Notes

- Backups run every 24 hours
- 30-day retention period
- Daily snapshots stored in platform-managed object store
- Zero-data-loss on single-node failure through replicated storage
- Backups cover session data, memory/index data, and file storage

---

## 03 Auto-Updates & Rollback

The platform supports automatic navi updates with rollback on failure, ensuring continuous operation.

### Requirement:

- The system SHALL support automatic navi updates with rollback on failure (FR-2.4, Should).

### Design Notes

- When a seller publishes a new version, the platform can auto-update deployed navis
- Health checks run after update; if the navi fails to start, the platform rolls back to the previous version
- Users are notified of version changes and can manually trigger rollbacks
- Edge case: navi crashes in hosted runtime — auto-restart (up to 3 times), then alert user

---

## 04 LLM API Key Management

Users can bring their own LLM API keys or use platform-managed keys with transparent pricing.

### Requirement:

- The system SHALL support BYO LLM API keys or platform-managed keys (FR-2.5).

### Scenario:

- GIVEN a user with a BYO API key, WHEN the key expires, THEN the navi pauses and the user is notified (TC-8).

### Design Notes

- Users choose between BYO (bring your own) and platform-managed at deployment time
- BYO: users configure their own LLM provider keys; navi pauses if key expires
- Platform-managed: platform includes LLM costs in subscription/marketplace pricing with a markup
- API key source is specified in the deployment config: `"api_key_source": "byo"` or `"api_key_source": "platform"`
- Design decision: supporting both gives users flexibility — they pay for LLM themselves (BYO) or via platform markup

---

## 05 Configuration Dashboard

Users configure their navis through a web-based dashboard, eliminating the need for YAML editing and CLI tools.

### Requirement:

- The system SHALL provide a web-based configuration dashboard for navis (FR-2.6).

### User Stories

- **US-008**: As Alex, I want to configure a navi through a dashboard, so that I don't need to edit YAML.

### Design Notes

The dashboard provides:
- Navi list with status indicators and logs
- Configuration editor (LLM provider, model selection, API key source)
- Deployment management (start, stop, restart, update)
- Resource allocation viewer
- Integration with the team management dashboard for team-level views

---

## Implementation Notes

### Phase 3: Hosted Runtime — Sandbox, Deployment, Monitoring

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Container orchestration (Kubernetes) | P1 | L | Phase 1 |
| Navi sandbox (container isolation, resource limits) | P1 | L | Container orchestration |
| Deployment pipeline (pull binary, create container, health check) | P1 | M | Navi sandbox |
| Data persistence (session, memory, file storage) | P1 | M | Container orchestration |
| Network policies (LLM-only access, webhook endpoints) | P1 | M | Navi sandbox |
| Health monitoring and auto-restart | P1 | M | Deployment pipeline |
| One-click deploy from marketplace | P1 | S | Marketplace + Hosted runtime |

### Integration Points

- **Kubernetes** — container orchestration for navi sandbox management
- **PostgreSQL** — per-navi session data schemas
- **Vector DB** — per-navi memory/index persistence
- **S3-compatible storage** — file storage and backups
- **eBPF** — network policy enforcement

### Order of Operations

- Phase 3 (Hosted Runtime) can be built in parallel with Phase 2 (Marketplace)
- Phase 4 (Team Management) depends on Phase 3 completion

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from product-spec FR-2 Hosted Runtime |