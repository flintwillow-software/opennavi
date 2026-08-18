---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - cloud-platform
priority: high
spec-refs: [090000, 090100, 090200, 090300]
---

# 09 Cloud Platform

> Cloud platform, marketplace, hosted runtime, team management, and billing for the OpenNavi ecosystem.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S090000 Marketplace](00-marketplace.md) | Marketplace | planned | Catalog, filtering, one-click deployment, preview |
| [S090100 Hosted Runtime](01-hosted-runtime.md) | Hosted Runtime | planned | Sandboxes, resource limits, backups, auto-updates |
| [S090200 Team Management](02-team-management.md) | Team Management | planned | Org hierarchy, RBAC, reporting, health monitoring |
| [S090300 Billing](03-billing.md) | Billing | planned | Subscription tiers, payouts, invoices, tax compliance |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Marketplace with searchable catalog, preview mode, and signature verification
- Hosted runtime with per-navi sandbox isolation and resource limits
- Team management with RBAC and cost attribution

## Related

- [Operations](../08-operations/README.md)
