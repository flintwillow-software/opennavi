---
status: draft
created: '2026-08-11'
updated: '2026-08-11'
tags:
  - gaps
  - analysis
  - roadmap
priority: high
---

# Gap Analysis: Case Studies → Spec Coverage

> **Status:** Draft · **Priority:** High  
> **Purpose:** Document features demanded by case studies that are not yet covered by specs.  
> **Action:** Specs are marked incomplete. Cover gaps as use cases are implemented.

## Coverage Summary

| Spec Group | Status | Notes |
|---|---|---|
| **navi-core** | ✅ Draft | Definition, build, trust, interfaces, extensions |
| **agent-runtime** | ✅ Draft | Agent loop, thought queue, tool execution, error recovery |
| **agent-config** | ⚠️ Partial | Skills system solid. **Persona needs runtime configurability (Gap 3)** |
| **session-context** | ✅ Draft | Sessions, context window, webhook schemas |
| **multi-agent** | ✅ Draft | P2P chat, presence, IAM, idle scheduling done. Team-of-teams orchestration spec'd (05-team-of-teams-orchestration.md). No Kanban system (Gap 1 → sruth), no human handoff protocol (Gap 8 → sruth) |
| **messaging** | ✅ Draft | Telegram, Discord, Slack, Email, Zendesk, MQTT, SMS, Calendar adapters all spec'd (04-email-sms-calendar-mqtt-adapters.md) |
| **memory** | ✅ Draft | Four-tier memory done. Vector knowledge base spec'd (15-memory-system.md §5b) |
| **llm-provider** | ✅ Draft | Provider abstraction, streaming, token counting |
| **operations** | ✅ Draft | Daemon, cron, webhooks, health, API versioning, incident response, credential vault all spec'd |
| **cloud-platform** | 📋 Planned | Marketplace, hosted runtime, billing. Team registry, observability, cost attribution, governance spec'd in multi-agent |
| **integrations** | ✅ Draft | External API integrations framework spec'd (Gap 4) |
| **creative-pipeline** | ✅ Draft | Asset pipeline, creative review, preview server, playtesting spec'd (Gap 9) |
| **avatar** | ✅ Draft | 3D avatar system, VRM/glTF support, hardware device spec'd (Gap 10) |

---

## Gap 1: Kanban Board System

**Demanded by:** SaaS product lifecycle, corporate IT, e-commerce, MMORPG, home assistant, film production

**Status:** Spec'd in [sruth](../sruth/docs/specs/README.md) — the project management tool with a full Kanban engine (columns, WIP enforcement, classes of service, SLA, stale detection, pull discovery). Navi agents interact via sruth's MCP interface; board work discovery is spec'd in [S040200 board-based discovery](../opennavi/docs/specs/04-multi-agent/02-iam-idle-scheduling.md) and the role definitions in [Agent Roles](../opennavi/docs/specs/14-agent-roles/README.md).

---

## Gap 2: Incident Response Protocol

**Demanded by:** SaaS product lifecycle, corporate IT, home assistant

**Status:** Spec'd — [S080800 Incident Response](../opennavi/docs/specs/08-operations/08-incident-response.md) defines the reusable 8-step protocol (DETECT → TRIAGE → INVESTIGATE → FIX → REVIEW → VERIFY → DEPLOY → POST-MORTEM) with escalation rules and a runbook system. The SRE role leads incident command ([S140300](../opennavi/docs/specs/14-agent-roles/03-sre-engineer.md)).

## Gap 3: Configurable Personality System

**Demanded by:** Personal assistant navi

**Status:** Start simple — static `persona.md` with configurable frontmatter. The frontmatter dimensions are interpreted as system prompt instructions injected at runtime.

**Persona frontmatter spec:**

```yaml
---
name: "Butler"                   # character name
style: casual-professional        # casual | professional | playful | formal | warm | direct
humor: low                        # none | low | medium | high
verbosity: concise                # concise | balanced | detailed
empathy: medium                   # low | medium | high
directness: medium                # low | medium | high
creativity: low                   # low | medium | high
adaptation: mirror               # mirror (match user) | consistent | context-aware
---
```

**How it works:**
- Frontmatter fields are parsed at persona load time
- Each dimension maps to a system prompt instruction block
- The `style` field selects a preset that sets defaults for all dimensions
- Individual dimensions override the preset
- Example: `style: butler` → formal, low humor, concise, high empathy, high directness, low creativity
- The persona body (`persona.md` content after frontmatter) is the free-form personality description

**Examples of preset mappings:**

| Preset | Formality | Humor | Verbosity | Empathy | Directness | Creativity |
|--------|-----------|-------|-----------|---------|------------|------------|
| butler | formal | none | concise | high | high | low |
| au-pair | warm | low | balanced | high | medium | low |
| coach | casual | medium | balanced | medium | high | medium |
| companion | warm | high | detailed | high | low | medium |
| minimalist | direct | none | concise | low | high | low |
| default | casual-professional | low | balanced | medium | medium | medium |

**Implementation:**
- Extend `13-persona-system.md` to support frontmatter parsing
- Add `PersonaConfig` struct with dimension fields
- Generate system prompt instructions from dimensions at persona load time
- Keep `.md` format — frontmatter + free-form body, no new file format

---

## Gap 4: Integrations Framework

**Demanded by:** Finance, e-commerce, corporate IT, film production, home assistant, personal assistant

**Status:** Rough plan, fine detail deferred.

**Integration types needed:**

| Type | Examples | Case Studies |
|------|----------|--------------|
| **Banking/Financial** | Plaid, Open Banking, Stripe | Finance, e-commerce |
| **Accounting** | QuickBooks, Xero | Finance |
| **SIEM/Security** | Splunk, Sentinel, Wazuh | Corporate IT |
| **Ticketing** | ServiceNow, Jira, Zendesk | Corporate IT, customer support |
| **Identity** | AD/LDAP, Okta, Auth0 | Corporate IT |
| **MDM** | Jamf, Intune, Workspace ONE | Corporate IT |
| **Payment** | Stripe, PayPal, Square | E-commerce |
| **Shipping** | FedEx, UPS, ShipStation | E-commerce |
| **Creative** | ShotGrid, Frame.io, Blender | Film production, MMORPG |
| **Calendar** | CalDAV, Google Calendar, Outlook | Film production, home assistant, personal |
| **IoT** | MQTT, Home Assistant API, Hue | Home assistant |
| **Communication** | Email (IMAP/SMTP), SMS (Twilio) | Customer support, home assistant |

**Common integration pattern:**

```
┌─────────────────────────────────────────────────┐
│                Integration Adapter                │
│                                                   │
│  ┌───────────────────────────────────────────┐   │
│  │         Auth Layer                         │   │
│  │  OAuth 2.0 │ API Key │ Basic │ mTLS        │   │
│  └───────────────────────────────────────────┘   │
│                                                   │
│  ┌───────────────────────────────────────────┐   │
│  │         Rate Limiter                       │   │
│  │  Per-endpoint │ Per-credential │ Global     │   │
│  └───────────────────────────────────────────┘   │
│                                                   │
│  ┌───────────────────────────────────────────┐   │
│  │         Retry & Circuit Breaker            │   │
│  │  Exponential backoff │ Health check       │   │
│  └───────────────────────────────────────────┘   │
│                                                   │
│  ┌───────────────────────────────────────────┐   │
│  │         Credential Resolver                │   │
│  │  Env var │ File │ Vault │ Encrypted store  │   │
│  └───────────────────────────────────────────┘   │
│                                                   │
│  ┌───────────────────────────────────────────┐   │
│  │         Webhook Listener (optional)        │   │
│  │  Per-service webhook → normalized event    │   │
│  └───────────────────────────────────────────┘   │
└─────────────────────────────────────────────────┘
```

**Implementation approach:**
- Define a generic `IntegrationAdapter` interface
- Each adapter: auth, rate limiting, retry, credential resolution, webhook subscription
- Integrations are compiled into the navi binary (like tools), not runtime-loaded
- Credentials resolved at runtime from vault/env/file (see Gap 11)

---

## Gap 5: Vector Knowledge Base

**Demanded by:** Customer support, e-commerce

**Status:** Yes, this is where RAG fits. Several options available.

**RAG landscape (Go-native or embeddable):**

| Library | Type | Backend | Notes |
|---------|------|---------|-------|
| **[raggo](https://github.com/teilomillet/raggo)** | Go lib | OpenAI, Milvus, chromem | Simple RAG, Contextual RAG, Memory Context. Chunking, embeddings, hybrid search. |
| **[gorag](https://github.com/StacklokLabs/gorag)** | Go lib | Ollama, OpenAI, pgvector, Qdrant | Multi-vector DB support. Backend abstraction. |
| **[goragkit](https://pkg.go.dev/github.com/njchilds90/goragkit)** | Go lib | Multiple backends | LangChain-level capability, Go-native. |
| **[sqlite-vec](https://github.com/asg017/sqlite-vec)** | C ext | SQLite (embedded) | Pure C, no deps. Runs anywhere SQLite runs. **Best fit for local navis** — no external service needed. Bindings for Go. |
| **[chromem-go](https://github.com/philippgille/chromem-go)** | Go lib | In-memory + persist | Embedded vector DB for Go. No external server. |
| **WeKnora** (Tencent) | Go app | Multiple backends | Full RAG pipeline, but heavy (14k stars, complex). |

**Recommendation:** Start with **sqlite-vec** — it's a pure C SQLite extension, same SQLite engine navis already use for sessions. No external service, no network dependency. The navi already ships with SQLite for session storage; sqlite-vec adds vector search as a virtual table.

```sql
-- Create a vector index for knowledge base entries
CREATE VIRTUAL TABLE kb_embeddings USING vec0(
    embedding float[768]
);

-- Query: find similar resolved tickets
SELECT rowid, distance
FROM kb_embeddings
WHERE embedding MATCH ?
  AND k = 5;
```

**What to spec:**
- Extend `15-memory-system.md` with a 5th tier: **knowledge base** (vector-based)
- Knowledge base is populated from resolved tickets, documentation, runbooks, FAQs
- Queried via semantic search (not keyword matching)
- Supports auto-population (agent writes to KB after resolving an issue)
- Can be local-only (sqlite-vec) or external (Qdrant, pgvector) for larger deployments

---

## Gap 6: Platform Adapters — Email, Zendesk, MQTT, SMS, Calendar

**Demanded by:** Customer support, e-commerce, home assistant, film production, personal assistant

**Status:** Rough plan, implement as needed.

| Adapter | Protocol | Libraries | Case Studies |
|---------|----------|-----------|--------------|
| **Email** | IMAP (inbound), SMTP (outbound) | `go-imap`, `gomail`, `smtp` | Customer support, e-commerce |
| **Zendesk** | REST API + webhooks | HTTP client | Customer support |
| **MQTT** | MQTT 3.1.1/5.0 | `eclipse/paho.mqtt.golang` | Home assistant (IoT) |
| **SMS** | Twilio API, AWS SNS | `twilio-go`, HTTP client | Home assistant (critical alerts) |
| **Calendar** | CalDAV, Google Calendar API | `go-calendar`, `google.golang.org/api/calendar/v3` | Film production, home assistant, personal |

**Implementation notes:**
- Each adapter follows the same `Adapter` interface from `02-platform-adapters-routing.md`
- Email requires the most work: IMAP IDLE for real-time, SMTP for sending, MIME parsing for attachments
- MQTT bridge is critical for home assistant: device discovery, state updates, command publishing
- Calendar integration is primarily read-only (detect events → trigger automations) but can create events

---

## Gap 7: Team-of-Teams Orchestration

**Demanded by:** Team-of-teams enterprise

**Status:** Leverage P2P communication + IAM with team-level principals (similar to AWS IAM).

**Approach:**

```
IAM hierarchy:
  AWS IAM-style: Team → Team Lead → Agents

  Team: "corporate-it" (principal)
    ├── Team Lead: "it-leader" (IAM role) — manages the team
    │   ├── Agent: "user-lifecycle-agent"
    │   ├── Agent: "patch-agent"
    │   └── Agent: "backup-agent"
    │
    └── talks to other Team Leads via P2P
        └── "product-lifecycle-team"
            └── Team Lead: "pl-leader"
```

**How it works:**
- Team has a principal identity (signed keypair)
- Team Lead is a navi agent with the `team-lead` role in IAM
- Team Manager is a higher-level navi that communicates with Team Leads via P2P
- IAM permissions define which teams can talk to which other teams
- `team-of-teams` orchestrator is a navi that subscribes to team registries

**Required for spec:**
- Extend IAM (`03-iam-idle-scheduling.md`) with team-level principals and roles
- Team registry: a P2P-accessible directory of teams, their leads, and capabilities
- Observability: teams publish health metrics to a shared channel (P2P broadcast)
- Cost attribution: teams report usage metrics, aggregated by team-of-teams orchestrator
- Governance: policy enforcement via IAM (team-scoped policies)

---

## Gap 8: Human-Agent Handoff Protocol

**Demanded by:** MMORPG, film production, SaaS product lifecycle

**Status:** Spec'd in [sruth S060100 Human-Agent Handoff](../sruth/docs/specs/06-agent-mcp/01-human-agent-handoff.md) — structured handoff payloads, reserved state labels, assignment-driven routing, and notifications via the shared webhook engine. The issue IS the handoff document.

## Gap 9: Creative/Media Pipeline

**Demanded by:** MMORPG, film production

**Status:** Flush this out. Art is for artists.

**The core principle:** Agents never create art, narrative, or game design. They provide tooling, validation, integration, and feedback. Humans remain the sole creative authors.

**Pipeline stages:**

```
┌─────────────────────────────────────────────────────┐
│                  HUMAN CREATIVE SPACE                │
│                                                     │
│  Artist works in: Blender, Maya, Substance, Pro Tools│
│  Designer specifies: markdown specs, JSON tables     │
│                                                     │
│  Submission:                                         │
│  opennavi asset submit <path> --metadata <file>      │
│  opennavi audio submit <path> --metadata <file>      │
│  opennavi spec submit <path>                         │
└───────────────────────┬─────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────┐
│              AGENT TECHNICAL SPACE                    │
│                                                      │
│  1. VALIDATE                                         │
│     - Format check, poly count, texture resolution   │
│     - Skeleton match, naming convention              │
│     - Budget check (GPU, memory, disk)               │
│                                                      │
│  2. INTEGRATE                                        │
│     - LOD generation, collision mesh                 │
│     - Texture compression, material instantiation    │
│     - Animation retargeting, SoundBank build         │
│                                                      │
│  3. PREVIEW                                          │
│     - Build preview scene (fast, for early review)  │
│     - Or full staging build (for final review)       │
│                                                      │
│  4. HANDOFF → Creative Review (human)               │
│     - Preview link, validation report, notes         │
└───────────────────────┬─────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────┐
│              HUMAN CREATIVE REVIEW                   │
│                                                      │
│  Approve → agent proceeds to next stage              │
│  Request Changes → agent returns to artist with     │
│                    specific feedback                  │
│  Reject → agent returns to triage with rationale    │
└─────────────────────────────────────────────────────┘
```

**Key components to spec:**
- **Asset validation rules** — format, size, naming, skeleton, texture, animation
- **Automated integration** — LOD generation, collision mesh, texture compression, animation retargeting
- **Preview server** — lightweight web-based staging viewer for in-context review
- **Creative review queue** — managed by Game Director agent, tracks human reviewer availability
- **Playtesting portal** — structured build assignment, session tracking, feedback forms

---

## Gap 10: Avatar System & Hardware Device

**Demanded by:** Personal assistant navi

**Status:** Research done. Architecture sketch below.

**Avatar technology landscape:**

| Tech | Type | Description |
|------|------|-------------|
| **[VRM](https://vrm.dev/) (pixiv/three-vrm)** | 3D avatar format | Industry standard for humanoid avatars. Blendshapes, bones, spring physics, MToon shader. JavaScript/TypeScript for rendering. |
| **glTF 2.0** | 3D model format | Khronos standard. VRM is built on top of glTF. |
| **three-vrm** | Renderer | `@pixiv/three-vrm` — Three.js-based VRM renderer. Expression, lip-sync, spring bones. |
| **[vrm-transform](https://github.com/urth-inc/vrm-transform)** | Go lib | Transform GLB/VRM files — resize textures, convert to KTX2. |
| **[three-avatar](https://github.com/VerseEngine/three-avatar)** | Avatar system | Three.js avatar with locomotion, IK, VR support. |
| **[Morphix](https://github.com/grep-many/morphix)** | Motion capture | Browser-based real-time motion capture for VRM avatars. MediaPipe + Kalidokit. |
| **[pocket-character](https://github.com/pocket-stack/pocket-character)** | Desktop widget | Native process rendering VRM character with idle animation, blink, physics. |
| **[CharacterStudio](https://github.com/m3-org/CharacterStudio)** | Avatar creation | Web-based VRM avatar creation platform (React, Three.js). |

**Architecture for the personal assistant:**

```
┌─────────────────────────────────────────────────────────┐
│                   Navi Binary (Go)                       │
│                                                         │
│  ┌─────────────────────────────────────────────────┐   │
│  │  Personality Engine (Gap 3)                      │   │
│  │  persona.md → system prompt instructions          │   │
│  └───────────────┬─────────────────────────────────┘   │
│                   │ state (emotion, mood, context)      │
│                   ▼                                     │
│  ┌─────────────────────────────────────────────────┐   │
│  │  Avatar State Manager                            │   │
│  │  - Current emotion (happy, sad, thinking, etc.)  │   │
│  │  - Gesture triggers (greeting, emphasis, etc.)   │   │
│  │  - Speech state (text → phoneme timing)          │   │
│  │  - Animation queue (idle, active, listening)     │   │
│  └───────────────┬─────────────────────────────────┘   │
│                   │ event stream (JSON over WebSocket)  │
└───────────────────┼─────────────────────────────────────┘
                    │
                    ▼
┌─────────────────────────────────────────────────────────┐
│              Avatar Renderer (Web/Embedded)              │
│                                                         │
│  Option A: Web UI (Three.js in browser)                 │
│  - Navi serves WebSocket + HTTP                         │
│  - Browser renders VRM avatar via three-vrm             │
│  - Lip-sync from phoneme timing data                    │
│  - No GPU dependency on navi hardware                   │
│                                                         │
│  Option B: Native renderer (pocket-character style)     │
│  - Go → CGO → OpenGL/WebGPU                             │
│  - Transparent window overlay for desktop               │
│  - Much more complex to implement                       │
└─────────────────────────────────────────────────────────┘
```

**Avatar state machine:**
```
States:
  IDLE      → idle animation, subtle breathing, periodic blink
  LISTENING → slight head tilt, attentive posture
  THINKING  → gaze shift, subtle gesture
  SPEAKING  → lip-sync, gesture for emphasis
  EMOTING   → expression change (happy, concerned, etc.)
  SLEEPING  → eyes closed, reduced animation rate

Transitions driven by personality state + conversation context
```

**Hardware device (PiSugar Whisplay HAT):**
- Raspberry Pi Zero 2W + PiSugar Whisplay HAT (1.69" LCD, WM8960 codec, dual MEMS mics, speaker, RGB LEDs)
- 3D-printed flip case (~$77 BOM)
- Runs a lightweight navi binary with avatar in Web UI mode
- Wake word detection, lid-open activation
- The avatar renders on the tiny LCD as a simplified face (not full body)

**Implementation order:**
1. Start with personality system (Gap 3) — no avatar needed
2. Add avatar state manager as a data model (emotion, gesture, speech state)
3. Web UI avatar rendering (Option A) — three-vrm in the browser
4. Hardware device support — full navi binary on Pi Zero 2W

---

## Gap 11: Credential/Secret Vault

**Demanded by:** Corporate IT, e-commerce, finance, home assistant

**Status:** Start with three methods: local file encryption/decryption, environment variables, and HashiCorp Vault. Add more as needed.

**Vault resolution chain:**

```go
func resolveCredential(name string) (string, error) {
    // 1. Try environment variable first (highest priority, good for CI)
    if val := os.Getenv(name); val != "" {
        return val, nil
    }

    // 2. Try HashiCorp Vault (if configured)
    if vaultAddr := os.Getenv("VAULT_ADDR"); vaultAddr != "" {
        client, err := api.NewClient(&api.Config{
            Address: vaultAddr,
        })
        if err == nil {
            secret, err := client.Logical().Read("secret/data/" + name)
            if err == nil && secret != nil {
                if data, ok := secret.Data["data"].(map[string]interface{}); ok {
                    if val, ok := data[name]; ok {
                        return fmt.Sprint(val), nil
                    }
                }
            }
        }
    }

    // 3. Try local encrypted file (~/.config/navi/vault/...)
    path := filepath.Join(configDir, "vault", name+".enc")
    if data, err := os.ReadFile(path); err == nil {
        // Decrypt with master key (from env or keychain)
        decrypted, err := decrypt(data, masterKey)
        if err == nil {
            return string(decrypted), nil
        }
    }

    return "", fmt.Errorf("credential %s not found in any source", name)
}
```

**Local file encryption:**
- Each credential stored as a separate encrypted file (`~/.config/navi/vault/CRED_NAME.enc`)
- AES-256-GCM encryption with a master key from environment variable
- Master key can be set via `NAVI_VAULT_KEY` env var or system keychain
- CLI commands: `opennavi vault set`, `opennavi vault get`, `opennavi vault list`

**HashiCorp Vault integration:**
- Support KV v2 secrets engine (most common)
- Auth methods: token, AppRole, Kubernetes
- Auto-refresh for dynamic secrets (database credentials, AWS IAM)
- Connection configured via standard Vault environment variables

**Future additions (not spec'd yet):**
- macOS Keychain integration
- systemd credential support
- 1Password CLI integration
- AWS Secrets Manager / GCP Secret Manager

---

## Gap 12: Canary Deployments & Release Management

**Demanded by:** SaaS product lifecycle

**Status:** Handle in CI/CD (GitHub Actions). Deployment gating uses GitHub Actions environments and variables. This will be iterated on when the use case is tested.

**Approach (deferred):**
- Canary stages defined as GitHub Actions environments: `canary-10%`, `canary-50%`, `production`
- Each environment has approval gates, required checks, and variables
- Navi creates PRs, CI/CD handles the deployment pipeline
- The navi monitors deployment health via webhooks (CI/CD status → agent)
- Agent decides on promotion or rollback based on health metrics

---

## Appendix: Complete Gap Inventory

| # | Gap | Scope | Priority | Action |
|---|---|---|---|---|
| 1 | Kanban board system | Spec'd in sruth `01-kanban/` + opennavi S040200 board discovery | Done | Agent board tools in 14-agent-roles |
| 2 | Incident response protocol | Spec'd in opennavi `08-operations/08-incident-response.md` | Done | SRE role leads incident command |
| 3 | Configurable personality | Extend `13-persona-system.md` | P1 | Add frontmatter dimensions |
| 4 | Integrations framework | New spec group: `integrations/` | P0 | ✅ Spec'd — 5 files (README, REQUIREMENTS, DESIGN, IMPLEMENTATION, TESTING) |
| 5 | Vector knowledge base | Extend `15-memory-system.md` | P1 | ✅ Spec'd — §5b with sqlite-vec |
| 6 | Platform adapters | Extend messaging specs | P1 | ✅ Spec'd — `04-email-sms-calendar-mqtt-adapters.md` |
| 7 | Team-of-teams | Extend IAM spec + new team/ group | P0 | ✅ Spec'd — `05-team-of-teams-orchestration.md` |
| 8 | Human-agent handoff | sruth S060100 | Done | ✅ Spec'd — sruth `06-agent-mcp/01-human-agent-handoff.md` |
| 9 | Creative pipeline | New spec group: `creative-pipeline/` | P2 | ✅ Spec'd — 5 files (asset pipeline, review queue, playtesting) |
| 10 | Avatar system + hardware | New spec group: `avatar/` | P2 | ✅ Spec'd — 5 files (state machine, VRM, PiSugar HAT) |
| 11 | Credential vault | Extend trust model spec | P1 | ✅ Spec'd — `25-credential-vault.md` |
| 12 | Canary deployments | CI/CD (GitHub Actions) | P2 | Deferred, iterate when tested |

## Related

- [Spec Index](../specs/README.md) — the specs these gaps reference
- [Case Study: Autonomous SaaS Product Lifecycle](../../../../docs/case-study/autonomous-saas-product-lifecycle/README.md)
- [Case Study: Corporate IT SysAdmin Team](../../../../docs/case-study/corporate-it-sysadmin-team/corporate-it-sysadmin-team.md)
- [Case Study: Customer Support Team](../../../../docs/case-study/customer-support-team/customer-support-team.md)
- [Case Study: Developer Tooling Navi](../../../../docs/case-study/developer-tooling-navi/README.md)
- [Case Study: E-Commerce Marketplace Team](../../../../docs/case-study/ecommerce-marketplace-team/ecommerce-marketplace-team.md)
- [Case Study: Film Production Studio](../../../../docs/case-study/film-production-studio/README.md)
- [Case Study: Finance & Accounting Navi](../../../../docs/case-study/finance-accounting-navi/README.md)
- [Case Study: Team-of-Teams Enterprise](../../../../docs/case-study/team-of-teams-enterprise/team-of-teams-enterprise.md)
- [Case Study: Home Assistant Navi](../../../../docs/case-study/home-assistant-navi/README.md)
- [Case Study: Personal Assistant Navi](../../../../docs/case-study/personal-assistant-navi/README.md)
- [Case Study: KARVOE Engine](../../../../docs/case-study/karvoe-engine/README.md)