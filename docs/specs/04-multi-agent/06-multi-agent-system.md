---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - multi-agent
priority: medium
spec-refs: [040600, 040601, 040602, 040603, 040604, 040605, 040606]
---

# Plan: Multi-Agent System

**Scope:** Universal agent configuration, sub-agent delegation, council mode via ChatSession, and inter-navi communication. Sub-agents are navis with \`type: sub\` in agents.yaml.

**Inspiration:** OpenCode's flat agent config with `mode: primary|subagent|all`, pi-mono's agent runtime, Hermes' hook system.

> **See also:** [P2P Chat & Presence](./00-p2p-chat-presence.md) for the P2P chat model that replaces the `council_start` mechanism for agent-to-agent communication. The `chat_send` tool and presence detection in that spec are the new primitives for inter-navi messaging. The `ChatSession` and `CouncilState` defined here are now one implementation detail of the broader P2P model — sessions are replicated across participants, not managed by a central orchestrator.

---

## 00 Agent Configuration

### 1.1 Universal Agent Config

All agents — primary or sub — share the same config structure. The `type` field determines behavior:

```go
type AgentConfig struct {
    // Identity
    Name        string // display name
    ID          string // unique identifier
    Type        AgentType // "primary" or "sub"
    Description string // what this agent does

    // LLM
    Provider      string
    Model         string
    Temperature   float64
    MaxTokens     int
    Thinking      ThinkingConfig
    Endpoint      string // optional custom endpoint (for providers that support it)

    // Environment
    EnvVars map[string]string // envvar expansion for keys
    // EnvVar format: "${ENV_VAR_NAME}" in config values gets expanded at runtime

    // System prompt
    PersonaPath   string // path to persona.md or persona/ directory
    SystemPrompt  string // base system prompt
    ContextFiles  []ContextFile

    // Tools
    Tools         []AgentTool      // allowed tools
    ToolPlugins   []string         // plugin names to load

    // Session
    SessionMode   SessionMode      // "new", "inherit", "fork"
    MemoryStack   *MemoryStack     // memory configuration

    // Permissions (sub-agents only)
    CanReadFiles  bool
    CanWriteFiles bool
    CanExecShell  bool
    CanAccessWeb  bool
    CanModifyOwn  bool   // can modify this agent's config
    CanModifySubs bool   // can modify sub-agent configs
}

type AgentType string
const (
    AgentPrimary AgentType = "primary"
    AgentSub     AgentType = "sub"  // Used as delegated agent (council, one-off)
)

type SessionMode string
const (
    SessionNew    SessionMode = "new"     // create new session
    SessionInherit SessionMode = "inherit" // inherit parent session
    SessionFork   SessionMode = "fork"    // fork parent session
)
```

### 1.2 Config File Structure

```yaml
# ~/.opennavi/profiles/<name>/agents.yaml
agents:
  default:
    type: primary
    name: "Navi"
    provider: "anthropic"
    model: "claude-sonnet-4-20250514"
    persona: "persona.md"
    tools: ["read", "write", "edit", "bash", "grep", "find"]
    temperature: 0.7

  coder:
    type: sub
    name: "Coder"
    provider: "anthropic"
    model: "claude-sonnet-4-20250514"
    persona: "persona/coder.md"
    tools: ["read", "write", "edit", "bash", "grep", "find"]
    # Specialized for implementation work
    can_modify_own: true
    can_modify_subs: true

  researcher:
    type: sub
    name: "Researcher"
    provider: "openrouter"
    model: "o3-pro"
    endpoint: "${OPENROUTER_ENDPOINT}"  # envvar expansion
    persona: "persona/researcher.md"
    tools: ["web_search", "fetch_content", "code_search"]
    can_access_web: true
    temperature: 0.3

  local_coder:
    type: sub
    name: "Local Coder"
    provider: "lmstudio"
    model: "qwen2.5-coder:32b"
    endpoint: "http://localhost:1234/v1"  # custom endpoint
    persona: "persona/coder.md"
    tools: ["read", "write", "edit", "bash"]
    temperature: 0.5

  ollama_reviewer:
    type: sub
    name: "Reviewer"
    provider: "ollama"
    model: "llama3.1:70b"
    endpoint: "http://localhost:11434/v1"
    persona: "persona/reviewer.md"
    tools: ["grep", "find", "read", "ls"]
    temperature: 0.3
```

### 1.3 EnvVar Expansion

Config values support `${VAR_NAME}` expansion at load time:

```go
func expandEnvVars(input string) string {
    return os.Expand(input, func(key string) string {
        return os.Getenv(key)
    })
}
```

Applied to: `Endpoint`, `Model` (for provider-specific model names), any string field.

### 1.4 Provider Support

| Provider | Custom Endpoint | EnvVar Keys | Local Models |
|----------|----------------|-------------|--------------|
| Anthropic | No | `ANTHROPIC_API_KEY` | No |
| OpenRouter | Yes | `OPENROUTER_API_KEY`, `OPENROUTER_ENDPOINT` | No |
| LM Studio | Yes (always localhost) | `LM_STUDIO_ENDPOINT` | Yes |
| Ollama | Yes (always localhost) | `OLLAMA_HOST` | Yes |
| OpenAI | Yes | `OPENAI_API_KEY`, `OPENAI_ENDPOINT` | No |
| Google | Yes | `GOOGLE_API_KEY`, `GOOGLE_ENDPOINT` | No |

### Requirement: Universal Agent Config Structure
The system SHALL support a universal agent configuration structure shared by primary and sub agents, differentiated by a type field.

#### Scenario: Configuring a primary agent
- **GIVEN** a user defines an agent in agents.yaml
- **WHEN** the agent config is loaded
- **THEN** the system SHALL parse the type field to determine whether the agent is primary or sub
- **AND** the system SHALL load all other fields identically regardless of type

### Requirement: EnvVar Expansion in Config
The system SHALL expand `${VAR_NAME}` environment variable references in all string config fields at load time.

#### Scenario: Expanding an environment variable in endpoint
- **GIVEN** an agent config contains `${OPENROUTER_ENDPOINT}` in the endpoint field
- **WHEN** the config is loaded
- **THEN** the system SHALL replace the placeholder with the value of the OPENROUTER_ENDPOINT environment variable

### Requirement: Provider Support with Custom Endpoints
The system SHALL support multiple LLM providers including Anthropic, OpenRouter, LM Studio, Ollama, OpenAI, and Google, with custom endpoints for providers that support them.

#### Scenario: Configuring a local LM Studio endpoint
- **GIVEN** a user configures an agent with provider: lmstudio and a custom endpoint
- **WHEN** the agent is initialized
- **THEN** the system SHALL use the endpoint for all API calls to that provider
- **AND** the system SHALL load the API key from the configured environment variable

---

## 01 ChatSession — Multi-Agent Coordination

### 2.1 ChatSession Struct

Every session is a conversation with participants. The behavior is determined by who's in the session, not by an enum.

```go
type ChatSession struct {
    ID        string
    Name      string       // display name (optional, for group sessions)
    AgentID   string       // the primary agent for this session
    Mode      SessionMode  // "chat" or "council"
    Messages  []Message
    History   []Message    // full history (may be trimmed for context)
    Memory    *MemoryStack // agent's memory stack

    // Participants — determines behavior
    // A session with 1 user + 1 agent = 1:1 chat
    // A session with 2+ users + 1+ agents = group chat
    // A session with council mode = council session
    Participants []Participant

    // Council-specific (only active in council mode)
    CouncilState CouncilState

    // Lifecycle
    Status       SessionStatus
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type Participant struct {
    ID        string       // user ID or agent ID
    Role      ParticipantRole // "user" or "agent"
    Name      string       // display name
    Platform  string       // platform (for users)
    AgentID   string       // agent ID (for agents)
    IsBot     bool
}

type ParticipantRole string
const (
    ParticipantUser ParticipantRole = "user"
    ParticipantAgent ParticipantRole = "agent"
)

type SessionMode string
const (
    SessionChat SessionMode = "chat"    // normal conversation
    SessionCouncil SessionMode = "council" // multi-agent coordination
)

type CouncilState struct {
    Mode         CouncilMode
    Round        int
    Responses    map[string]string  // agentID -> response
    Complete     bool
    Question     string             // the current council question
}

type CouncilMode string
const (
    CouncilDelegation CouncilMode = "delegation"  // sequential, task-oriented
    CouncilDiscussion CouncilMode = "discussion"   // concurrent, collaborative
)
```

### 2.2 Session Behavior — Determined by Participants

A session's behavior is determined by its participants:

```go
func (s *ChatSession) Behavior() SessionBehavior {
    users := 0
    agents := 0
    for _, p := range s.Participants {
        switch p.Role {
        case ParticipantUser:
            users++
        case ParticipantAgent:
            agents++
        }
    }

    if s.Mode == SessionCouncil {
        return SessionBehaviorCouncil
    }
    if users > 1 || agents > 1 {
        return SessionBehaviorGroup
    }
    return SessionBehaviorOneToOne
}

type SessionBehavior int
const (
    SessionBehaviorOneToOne SessionBehavior = iota
    SessionBehaviorGroup
    SessionBehaviorCouncil
)
```

| Participants | Behavior | Description |
|-------------|----------|-------------|
| 1 user + 1 agent | 1:1 chat | Normal conversational session |
| 2+ users + 1+ agents | Group chat | Multi-user conversation with agent(s) |
| Any + council mode | Council | Multi-agent coordination session |

### 2.3 Council Mode Flow (via ChatSession)

Council mode uses ChatSession to coordinate multi-agent conversation. Council members are configured as sub-agents in agents.yaml with \`type: sub\`, each with their own persona, tools, and model — this is what gives each council member its specialty.

```
1. Main navi creates ChatSession with council members as participants
2. Main navi sends question to each member within the session
3. Each member responds (sequentially or in parallel)
4. Main navi synthesizes responses
5. Cycle repeats until resolved
```

**Why ChatSession for council?**
- All participants share one session context
- Clean "round" model (round 1, round 2, ...)
- No serialization overhead between navis
- Easy to see the full conversation history
- Explicit coordination via CouncilState

**Delegation mode** (sequential):
```
Main: "@coder, implement feature X. @researcher, find benchmarks."

@coder: [implements feature X]
@researcher: [finds benchmarks]

Main: "Results collected. Synthesis: ..."
```

**Discussion mode** (collaborative):
```
Main: "I need to implement feature X. Council, what's the best approach?"

@coder: "I'd suggest using pattern Y because..."
@researcher: "Actually, benchmark Z shows approach W is better."
@reviewer: "I'd add error handling for edge case V."

Main: "Good points. Let's combine them: approach W with V's safeguards."
```

### 2.4 Session Creation and Participant Management

```go
// Create a new session with a single user
func (m *SessionManager) CreateSession(agentID string, user Participant) *ChatSession {
    session := &ChatSession{
        ID:       uuid.New().String(),
        AgentID:  agentID,
        Mode:     SessionChat,
        Participants: []Participant{user},
        CreatedAt: time.Now(),
    }
    return session
}

// Add a participant to an existing session (group chat)
func (s *ChatSession) AddParticipant(p Participant) error {
    for _, existing := range s.Participants {
        if existing.ID == p.ID {
            return fmt.Errorf("participant %s already in session", p.ID)
        }
    }
    s.Participants = append(s.Participants, p)
    return nil
}

// Remove a participant from a session
func (s *ChatSession) RemoveParticipant(participantID string) error {
    for i, p := range s.Participants {
        if p.ID == participantID {
            s.Participants = append(s.Participants[:i], s.Participants[i+1:]...)
            return nil
        }
    }
    return fmt.Errorf("participant %s not found in session", participantID)
}

// Start council mode on an existing session
func (s *ChatSession) StartCouncil(agents []*Agent, question string) error {
    if s.Mode == SessionCouncil {
        return fmt.Errorf("session is already in council mode")
    }

    s.Mode = SessionCouncil
    s.CouncilState = CouncilState{
        Mode:     CouncilDiscussion,
        Round:    1,
        Question: question,
        Responses: make(map[string]string),
    }

    // Add agents as participants
    for _, agent := range agents {
        s.Participants = append(s.Participants, Participant{
            ID:      agent.ID,
            Role:    ParticipantAgent,
            Name:    agent.Name,
            AgentID: agent.ID,
        })
    }

    return nil
}

// End council mode, return to chat
func (s *ChatSession) EndCouncil() {
    s.Mode = SessionChat
    s.CouncilState = CouncilState{}
}

func (s *ChatSession) IsCouncilComplete() bool {
    if s.Mode != SessionCouncil {
        return true
    }
    agentParticipants := 0
    for _, p := range s.Participants {
        if p.Role == ParticipantAgent {
            agentParticipants++
        }
    }
    return len(s.CouncilState.Responses) >= agentParticipants
}
```
        Mode:     CouncilDiscussion,
        Round:    1,
        Responses: make(map[string]string),
    }
    return nil
}

func (s *ChatSession) MemberResponse(agentID, response string) error {
    s.CouncilState.Responses[agentID] = response
    s.Messages = append(s.Messages, Message{
        From:    agentID,
        Content: response,
        IsCouncil: true,
    })
    return nil
}

func (s *ChatSession) IsCouncilComplete() bool {
    if s.Mode != SessionCouncil {
        return true
    }
    agentParticipants := 0
    for _, p := range s.Participants {
        if p.Role == ParticipantAgent {
            agentParticipants++
        }
    }
    return len(s.CouncilState.Responses) >= agentParticipants
}
```

### 2.4 Delegated Sub-Agent Execution

```go
type SubAgentTask struct {
    ID          string
    Agent       *Agent
    Prompt      string
    SessionMode SessionMode  // new, inherit, fork
    Tools       []AgentTool
    Timeout     time.Duration
    WorkDir     string
}

type SubAgentResult struct {
    TaskID   string
    AgentID  string
    Success  bool
    Output   string
    Error    error
    Duration time.Duration
}

type SubAgentManager struct {
    tasks   map[string]SubAgentTask
    results map[string]SubAgentResult
    mu      sync.RWMutex
}

func (m *SubAgentManager) Execute(ctx context.Context, task SubAgentTask) (*SubAgentResult, error) {
    // 1. Create or inherit session for sub-agent
    session, err := m.createSubSession(task)
    if err != nil {
        return nil, err
    }

    // 2. Create sub-agent with its config
    agent := agent.NewAgent(task.Agent.BuildConfig(session))

    // 3. Run agent loop
    resultCh := make(chan *SubAgentResult)
    go func() {
        output, err := agent.Prompt(ctx, task.Prompt)
        resultCh <- &SubAgentResult{
            TaskID:   task.ID,
            AgentID:  task.Agent.ID,
            Success:  err == nil,
            Output:   output,
            Error:    err,
            Duration: time.Since(startTime),
        }
    }()

    // 4. Wait with timeout
    select {
    case result := <-resultCh:
        m.mu.Lock()
        m.results[task.ID] = *result
        m.mu.Unlock()
        return result, nil
    case <-time.After(task.Timeout):
        agent.Abort()
        return &SubAgentResult{TaskID: task.ID, Success: false, Error: context.DeadlineExceeded}, nil
    }
}
```

### Requirement: ChatSession with Participant-Based Behavior
The system SHALL determine session behavior based on participants and mode, supporting 1:1 chat, group chat, and council modes.

#### Scenario: Creating a 1:1 chat session
- **GIVEN** a session has one user and one agent participant
- **WHEN** the session is created
- **THEN** the system SHALL set behavior to 1:1 chat
- **AND** the system SHALL NOT activate council features

#### Scenario: Starting a council session
- **GIVEN** a session has multiple agent participants
- **WHEN** the session mode is set to council
- **THEN** the system SHALL initialize council state with round tracking
- **AND** the system SHALL coordinate multi-agent responses within the session

### Requirement: Council Mode Flow
The system SHALL support council mode within ChatSession where the main navi sends questions to each member, collects responses, and synthesizes results in sequential or collaborative mode.

#### Scenario: Running a council discussion
- **GIVEN** a council session with multiple sub-agents
- **WHEN** the main agent sends a question
- **THEN** the system SHALL deliver the question to each member agent
- **AND** each member SHALL respond within the session context
- **AND** the main agent SHALL synthesize the responses

### Requirement: Delegated Sub-Agent Execution
The system SHALL support delegated sub-agent execution where a sub-agent runs with its own config, session mode, tools, and timeout, returning results to the calling agent.

#### Scenario: Executing a sub-agent task with timeout
- **GIVEN** a sub-agent task is defined with a prompt and timeout
- **WHEN** the sub-agent manager executes the task
- **THEN** the system SHALL create a session for the sub-agent
- **AND** run the sub-agent with its own config and tools
- **AND** return the result or timeout error within the configured timeout

---

## 02 Self-Improvement

### 3.1 Config Modification

Primary agents can modify their own config and sub-agent configs. This is done via a tool call:

```go
type ModifyConfigTool struct {
    Name        string = "modify_config"
    Description string = "Modify this agent's or a sub-agent's configuration"
    Parameters  json.RawMessage
}

type ModifyConfigInput struct {
    TargetAgent string          // "self" or sub-agent ID
    Changes     map[string]any  // fields to modify
    Reason      string          // why the change is being made
}
```

**Behavior:**
1. Agent calls `modify_config` with target and changes
2. OpenNavi applies the changes to the config file
3. OpenNavi sends a **notification** to the user (TUI toast / gateway message)
4. Changes take effect immediately (for running agents) or on next session start
5. User can revert via CLI if desired

**Notification format:**
```
🔄 Navi is modifying its config:
   - Changed: temperature 0.7 → 0.3 (reason: "task requires more precision")
   - Added: tool "web_search" (reason: "need to research API docs")

Revert: opennavi config revert <timestamp>
```

### 3.2 What Can Be Modified

| Field | Self | Sub-Agents | Notes |
|-------|------|------------|-------|
| `temperature` | ✅ | ✅ | Runtime change |
| `max_tokens` | ✅ | ✅ | Runtime change |
| `persona` | ✅ | ✅ | Next session only |
| `context_files` | ✅ | ✅ | Next session only |
| `tools` | ✅ | ✅ | Next session only |
| `model` | ✅ | ✅ | Next session only |
| `provider` | ❌ | ❌ | Requires restart |
| `type` | ❌ | ❌ | Immutable |

### 3.3 Self-Improvement Triggers

The agent should modify its config when:
- A tool call consistently fails → modify tool list or add error-handling tools
- A persona doesn't fit the task → switch persona
- Context is too large → adjust context files or add compression
- A sub-agent's behavior was ineffective → modify its persona or tools
- User feedback indicates a pattern → proactively adjust

### Requirement: Config Modification via Tool
The system SHALL provide a modify_config tool that allows agents to modify their own or sub-agent configurations, with user notification and revert capability.

#### Scenario: Modifying temperature via tool
- **GIVEN** an agent calls modify_config with target self and a temperature change
- **WHEN** the system processes the modification
- **THEN** the system SHALL apply the change to the config file
- **AND** the system SHALL send a notification to the user detailing the change and reason
- **AND** the user SHALL be able to revert the change via CLI

### Requirement: Self-Improvement Triggers
The system SHALL trigger config modifications when a tool call consistently fails, a persona doesn't fit the task, context is too large, a sub-agent is ineffective, or user feedback indicates a pattern.

#### Scenario: Adjusting tool list after repeated failures
- **GIVEN** a tool call consistently fails
- **WHEN** the agent detects the failure pattern
- **THEN** the system SHALL modify the tool list or add error-handling tools
- **AND** the system SHALL notify the user of the change

---

## 03 Agent Lifecycle

### 4.1 Agent Registry

```go
type AgentRegistry struct {
    agents  map[string]*Agent
    default string // default primary agent
    mu      sync.RWMutex
}

func (r *AgentRegistry) Load(configPath string) error
func (r *AgentRegistry) Get(id string) (*Agent, error)
func (r *AgentRegistry) List() []string
func (r *AgentRegistry) GetDefault() *Agent
```

### 4.2 Agent Creation Flow

```
1. Load agents.yaml from profile
2. Expand env vars in all string fields
3. Resolve persona paths (persona.md or persona/)
4. Validate each agent's config
5. Create Agent instances from config
6. Register in AgentRegistry
```

### Requirement: Agent Registry Loading
The system SHALL load agent configurations from agents.yaml, expand environment variables, resolve persona paths, validate each config, and register agents in the AgentRegistry.

#### Scenario: Loading agents from config
- **GIVEN** an agents.yaml file with valid configurations
- **WHEN** the system loads the registry
- **THEN** the system SHALL expand env vars in all string fields
- **AND** resolve persona paths to files or directories
- **AND** validate each agent's configuration
- **AND** register each agent in the AgentRegistry

### Requirement: Agent Creation and Retrieval
The system SHALL provide an AgentRegistry that supports creating agents from config, retrieving by ID, listing all agents, and getting the default primary agent.

#### Scenario: Retrieving a sub-agent by ID
- **GIVEN** an AgentRegistry with registered agents
- **WHEN** a caller requests an agent by ID
- **THEN** the system SHALL return the agent instance
- **AND** the system SHALL return an error if the agent is not found

---

## 04 File Structure

```
internal/agent/
├── agent.go           # Agent struct, NewAgent, public API
├── state.go           # AgentState, copy-on-write mutations
├── events.go          # Event types, EventBus, AgentEndBarrier
├── loop.go            # Core execution loop
├── tools.go           # ToolExecutor, before/after hooks
├── queue.go           # SteeringQueue, FollowUpQueue
├── transform.go       # Context transformation pipeline
├── types.go           # AgentMessage, AgentEvent, AgentTool, AgentConfig
├── registry.go        # AgentRegistry
├── session.go         # ChatSession, Participant, SessionMode
├── subagent.go        # SubAgentManager, SubAgentTask, SubAgentResult
├── config.go          # Agent config loading and validation
└── modify.go          # modify_config tool implementation
```

### Requirement: Internal Agent Package Structure
The system SHALL organize the agent package into files for the Agent struct, state management, events, execution loop, tools, queue, context transformation, types, registry, session, sub-agent management, config loading, and config modification.

#### Scenario: Organizing agent source files
- **GIVEN** a developer is navigating the agent package
- **WHEN** looking for a specific component
- **THEN** the system SHALL place the Agent struct in agent.go, session logic in session.go, sub-agent management in subagent.go, and config loading in config.go

### Requirement: Separation of Core Execution Files
The system SHALL separate core execution concerns across agent.go, state.go, events.go, and loop.go, so that the Agent struct, copy-on-write state mutations, event types and barriers, and the core execution loop each live in their own file.

#### Scenario: Locating the execution loop implementation
- **GIVEN** a developer needs to modify the core execution loop
- **WHEN** the developer locates the relevant file
- **THEN** the system SHALL place the core execution loop in loop.go
- **AND** the system SHALL place copy-on-write state mutations in state.go

---

## 05 Implementation Order

### Phase 1: Core Multi-Agent
1. **AgentRegistry** — load agents.yaml, resolve env vars, validate
2. **AgentConfig** — universal config struct with envvar expansion
3. **ChatSession** — single-agent session (existing, extended)
4. **SubAgentManager** — spawn, wait, collect results
5. **modify_config tool** — agent can modify its own config

### Phase 2: Group Chat and Council
6. **Participant management** — AddParticipant, RemoveParticipant
7. **Behavior detection** — determine session behavior from participants
8. **Council mode** — StartCouncil, EndCouncil, council coordination
9. **@mentions in TUI** — user can invoke council mode
10. **Council in gateway** — council messages delivered via platform

### Phase 3: Self-Improvement
11. **Config modification rules** — what can be changed, when
12. **Notification system** — toast/gateway message on config change
13. **Self-improvement triggers** — when the agent decides to modify

### Requirement: Phased Implementation Order
The system SHALL implement multi-agent features in three phases: Phase 1 (Core Multi-Agent) including AgentRegistry, AgentConfig, ChatSession, and SubAgentManager; Phase 2 (Group Chat and Council) including participant management, behavior detection, and council mode; Phase 3 (Self-Improvement) including config modification rules, notification system, and self-improvement triggers.

#### Scenario: Implementing core multi-agent features first
- **GIVEN** the implementation plan specifies phases
- **WHEN** Phase 1 is complete
- **THEN** the system SHALL support AgentRegistry, AgentConfig, ChatSession, and SubAgentManager
- **AND** the system SHALL provide the modify_config tool

### Requirement: Sequential Phase Dependency
The system SHALL sequence the implementation phases so that each phase builds on the previous one, with Phase 2 enabling group chat and council through participant management and @mentions, and Phase 3 enabling self-improvement through config modification rules and notifications.

#### Scenario: Enabling council mode after group chat
- **GIVEN** Phase 1 core multi-agent features are complete
- **WHEN** Phase 2 is implemented
- **THEN** the system SHALL support participant management, behavior detection, and council mode coordination
- **AND** the system SHALL deliver council messages via platform gateway

---

## 06 Key Design Decisions

1. **Universal config** — No separate primary/sub config. One `type` field. Simple, extensible.
2. **Envvar expansion** — `${VAR}` syntax for all string fields. Secure key management.
3. **Custom endpoints** — Every provider that supports it gets an `endpoint` field.
4. **Unified session model** — Every session is a conversation with participants. Behavior is determined by who's in it, not by a type enum. Council is a session mode, not a separate type.
5. **Config modification via tool** — Transparent, auditable, user-notified.
6. **LM Studio + Ollama** — First-class support for local open-weight models with custom endpoints.


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S040600 | 0.1.0 | Migrated from docs/spec/ to spec-tree |