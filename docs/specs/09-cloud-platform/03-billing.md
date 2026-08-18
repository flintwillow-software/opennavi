---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - cloud-platform
priority: high
spec-refs: [090300, 090301, 090302, 090303]
---

# 03 Billing

> Subscription tiers, marketplace purchases, seller payouts, and tax-compliant invoicing for the OpenNavi cloud platform.

## 00 Subscription Tiers

The platform offers tiered subscription plans (Free, Pro, Business, Enterprise) with graduated feature access and resource limits.

### Requirement:

- The system SHALL support subscription tiers (Free, Pro, Business, Enterprise) (FR-4.1).

### Scenario:

- GIVEN a user on the Free tier with 1 navi, WHEN they try to deploy a 2nd navi, THEN an upgrade prompt is shown (TC-10).

### Design Notes

Subscription tier model:

```json
{
  "tier_id": "pro",
  "name": "Pro",
  "price": 49.00,
  "currency": "USD",
  "interval": "monthly",
  "features": {
    "max_navis": 5,
    "max_teams": 2,
    "runtime_hours": 720,
    "storage_gb": 10,
    "support": "email",
    "marketplace_commission": 0.20,
    "sla": "99.5%"
  }
}
```

Tier structure:
- **Free** — 1 navi, limited features, time-limited or usage-capped (TBD)
- **Pro** ($49/month) — 5 navis, 2 teams, marketplace access, email support
- **Business** — custom limits, priority support, negotiated commission
- **Enterprise** — custom pricing, org-wide deployment, dedicated support, negotiated SLA

Edge case: user exceeds tier limits — upgrade prompt displayed, throttling on overage.

### User Stories

- **US-010**: As Priya (Platform Admin), I want to manage subscriptions and billing, so that the platform generates revenue.

---

## 01 Marketplace Purchases

The platform supports one-time purchases and recurring subscription licenses for marketplace navis and navi team bundles.

### Requirement:

- The system SHALL support one-time purchases and recurring licenses for marketplace navis (FR-4.2).

### Design Notes

Pricing models for marketplace listings:

| Model | Description | Example | Platform Commission |
|-------|-------------|---------|-------------------|
| **Free** | No cost, open-source/complimentary navis | Basic tools, templates | 0% |
| **One-time purchase** | Pay once, use forever | Specialized analysis tools | 20% |
| **Subscription** | Monthly/yearly recurring | Billing agent, support team | 20% |
| **Usage-based** | Pay per transaction or per hour | High-volume processing | 15% |
| **Enterprise license** | Custom pricing for org-wide deployment | Custom teams | Negotiated |

Purchases are processed through Stripe. Marketplace navi costs are added to the user's subscription as add-ons.

---

## 02 Seller Payouts

The platform calculates and remits seller payouts based on the platform commission model, with automated monthly settlement.

### Requirement:

- The system SHALL calculate and remit seller payouts (platform commission model) (FR-4.3).

### Scenario:

- GIVEN a seller with $23.20 in earnings, WHEN the monthly payout runs, THEN the seller receives the payout (after 20% commission) (TC-11).

### Design Notes

Commission flow:

```
User pays $29/month for marketplace navi
  -> Platform: $5.80 (20% commission)
  -> Seller: $23.20 (80% after commission)
  -> Payout: monthly, minimum $50 threshold
```

Subscription lifecycle:
1. User signs up on Free tier
2. User upgrades to Pro ($49/month, 5 navis, marketplace access)
3. User purchases marketplace navi ($29/month add-on)
4. Monthly billing cycle calculates: base tier + marketplace navis
5. Platform calculates seller payout: marketplace navi cost x 80%
6. Platform charges user's card (Stripe)
7. Platform remits seller payout (minus commission)
8. Platform generates invoice

Edge case: seller account inactive > 90 days — listings paused, users notified of seller inactivity.

### User Stories

- **US-004**: As Maya (Developer), I want to see sales and usage analytics, so that I can improve my navis.

---

## 03 Invoicing & Tax Compliance

The platform generates invoices and tax-compliant receipts for all purchases and subscriptions.

### Requirement:

- The system SHALL generate invoices and tax-compliant receipts (FR-4.4).

### Design Notes

- Invoices are generated at each billing cycle
- Tax-compliant receipts include VAT, GST, or sales tax as applicable
- Invoices are accessible from the dashboard
- Edge case: payment failure — 7-day grace period, then navi suspension

---

## Implementation Notes

### Phase 1: Foundation — Auth, Billing, Navi Registry

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Subscription tiers (Free, Pro, Business) | P1 | M | Billing engine |
| Billing engine (Stripe integration) | P1 | L | User management |

### Phase 2: Marketplace — Catalog, Commerce, Preview

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Purchase flow (one-click, subscription add-on) | P1 | M | Billing engine |

### Phase 5: Seller Marketplace — Payouts, Analytics, Trust

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Seller payout automation | P1 | M | Billing engine |

### Integration Points

- **Stripe** — subscriptions, one-time payments, seller payouts
- **PostgreSQL** — billing records, subscription data, transaction history

### Order of Operations

- Phase 1 (Auth + Billing) is the prerequisite for all other phases
- Billing engine must be operational before marketplace purchases and seller payouts can work

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from product-spec FR-4 Billing |