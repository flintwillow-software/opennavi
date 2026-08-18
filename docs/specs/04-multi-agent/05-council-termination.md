---
status: draft
created: '2026-08-02'
updated: '2026-08-02'
tags:
  - spec
  - multi-agent
priority: medium
spec-refs: [040500, 040501, 040502, 040503]
---

# Research: Multi-Agent Council Termination Conditions

**Date:** 2026-08-02  
**Purpose:** Inform updates to the multi-agent council system (14-multi-agent-system.md) with patterns from production systems and research frameworks.

> **See also:** [P2P Chat & Presence](./00-p2p-chat-presence.md) for the P2P chat model. Council sessions in the new model are multi-participant P2P chat sessions — the termination conditions defined here (max rounds, consensus, echo detection, timeout) still apply as the session lifecycle rules within a P2P chat.

---

## 00 Patterns Found

### 1.1 Hermes Agent — Multi-Agent Coordination

**Source:** Based on documented behavior of Hermes-style autonomous agents (GitHub: hermes-agent, similar implementations)

**Termination patterns:**
- **Round-based with max rounds**: Council discussions cap at N rounds (typically 3-5). After N rounds, the main agent synthesizes regardless of completeness.
- **Consensus detection**: If all members agree on a response (content similarity > threshold), council ends early — no need for more rounds.
- **Timeout per round**: Each member has a hard timeout (e.g., 10 minutes). If they don't respond, their slot is marked "no response" and the round continues.
- **Manual override**: User can send `/end_council` or `/stop` at any time to terminate early.

**Key insight:** Hermes uses a hybrid approach — automatic termination after max rounds OR consensus, plus manual override. This prevents infinite loops while still allowing productive discussions to conclude naturally when members agree.

---

### 1.2 OpenAI Swarm / Codex Patterns

**Source:** OpenAI's Swarm paper and Codex implementation patterns (published research + documentation)

**Termination patterns:**
- **Handoff-based termination**: In handoff mode, a sub-agent can explicitly transfer control back to the main agent by returning a special "handoff" signal. When all agents have returned control, council ends.
- **Task completion detection**: Each sub-agent evaluates whether its assigned task is complete before responding. If complete, it signals `task_complete` in its response metadata.
- **Max iterations hard cap**: Production systems often enforce a max iteration count (e.g., 20 rounds) regardless of other termination conditions — purely as a safety net against runaway loops.

**Key insight:** Explicit completion signaling from sub-agents is more reliable than implicit "I'm done" detection. The main agent should check `task_complete` flags before deciding to continue or synthesize.

---

### 1.3 LangGraph / CrewAI Patterns

**Source:** LangGraph documentation (langchain-ai/langgraph), CrewAI framework patterns

**Termination patterns:**
- **State-based termination**: Each agent maintains a shared state object. When the state reaches a terminal condition (e.g., all tasks complete, error encountered), the graph ends.
- **Conditional edges**: After each agent responds, conditional logic determines whether to continue the council or route to synthesis/termination node.
- **Human-in-the-loop break points**: Systems often insert manual approval steps between council rounds — user must explicitly approve continuing before next round starts.

**Key insight:** State-based termination is more robust than time-based because it handles variable-speed agents (some respond instantly, others take minutes). Conditional edges allow flexible routing based on actual progress rather than fixed schedules.

---

### 1.4 AutoGen / Multi-Agent Research Patterns

**Source:** Microsoft's AutoGen framework documentation and research papers on multi-agent LLM systems

**Termination patterns:**
- **Echo detection**: If a sub-agent's response is too similar to its previous response (content similarity > 0.9), the system assumes it's stuck in an echo loop and terminates that agent's participation.
- **Progress tracking**: Main agent tracks whether each round produces new information vs. repeating old points. If no new info for N consecutive rounds, council ends.
- **Confidence scoring**: Each member's response includes a confidence score. If all members have low confidence (< 0.5), the main agent synthesizes based on available information rather than waiting for better responses.

**Key insight:** Echo detection and progress tracking are critical for preventing "talking in circles" — a common failure mode in multi-agent councils where agents repeat or paraphrase previous responses without adding value.

---

### 1.5 Production System Patterns (GitHub, GitLab, CI/CD)

**Source:** Observations from production incident response and code review automation systems

**Termination patterns:**
- **Priority-based escalation**: If council can't resolve within N rounds for a P0 issue, escalate to human. For lower priority, auto-synthesize after max rounds.
- **Timeout with partial results**: When timeout hits, synthesize based on whatever responses were received (not all members need to respond). Empty slots are treated as "no response" rather than errors.
- **Failure counting**: Track consecutive failures per agent. After 3 consecutive timeouts or errors for an agent, mark it as unavailable and continue with remaining agents.

**Key insight:** Production systems prioritize getting a usable result over waiting for perfect participation. Partial results + timeout = synthesis based on available data, not error states.

### Requirement: Round-Based Termination with Max Rounds
The system SHALL cap council discussions at a configurable maximum number of rounds, after which the main agent synthesizes a result regardless of completeness.

#### Scenario: Terminating at max rounds
- **GIVEN** a council session is running
- **WHEN** the council reaches max_rounds without explicit termination
- **THEN** the system SHALL synthesize the results and end the council

### Requirement: Consensus Detection
The system SHALL detect consensus when all council member responses are content-similar above a configured threshold, terminating the council early.

#### Scenario: Ending council early on consensus
- **GIVEN** all council members have responded in a round
- **WHEN** the pairwise similarity of all responses exceeds the consensus threshold
- **THEN** the system SHALL synthesize a consensus summary and end the council without further rounds

### Requirement: Per-Round Member Timeout
The system SHALL enforce a hard timeout per council member per round, marking non-responsive members as no response rather than treating it as an error.

#### Scenario: Member times out in a round
- **GIVEN** a council round is in progress
- **WHEN** a member does not respond within the round timeout
- **THEN** the system SHALL mark the member as no response for that round
- **AND** the round SHALL continue with the remaining members

### Requirement: Echo Detection and Progress Tracking
The system SHALL detect echo loops when a member's response is too similar to its previous response and terminate the council when no new information appears for consecutive rounds.

#### Scenario: Detecting an echo loop
- **GIVEN** a council member responds with content highly similar to its previous response
- **WHEN** the similarity exceeds the echo threshold
- **THEN** the system SHALL mark the agent as echoing
- **AND** the system SHALL end the council if no new information appears in two consecutive rounds

### Requirement: Manual Termination Override
The system SHALL allow a user to terminate an active council at any time via a manual command.

#### Scenario: User ends the council manually
- **GIVEN** a council session is active
- **WHEN** the user sends the end council command from the chat platform
- **THEN** the system SHALL immediately synthesize the available responses and end the council

### Requirement: Timeout with Partial Results
The system SHALL synthesize results from whatever responses were received when a timeout occurs, treating empty slots as no response rather than errors.

#### Scenario: Synthesizing with partial responses
- **GIVEN** a council times out before all members respond
- **WHEN** the system synthesizes
- **THEN** the system SHALL base the synthesis on the responses received
- **AND** the system SHALL treat non-responding members as no response

---

## 01 Recommended Approach for OpenNavi

Based on the patterns above, here's a synthesis of termination conditions for OpenNavi council mode:

### 2.1 Termination Conditions (in priority order)

| Condition | When to Trigger | Action |
|-----------|----------------|--------|
| **All members responded** | Every participant has provided a response in the current round | Proceed to synthesis or next round based on max_rounds config |
| **Max rounds reached** | Council has run for `max_rounds` (default: 5) without explicit termination | Synthesize and end council |
| **Consensus detected** | All member responses are content-similar (>0.8 cosine similarity) | Synthesize consensus summary and end council |
| **Echo detected** | Any member's response is too similar to their previous response (similarity > 0.9) | Mark that agent as "echoing", continue with remaining agents, end if no new info in 2 rounds |
| **No progress for N rounds** | Council has run N consecutive rounds without producing new information (tracked by main agent) | Synthesize based on available data and end council |
| **Manual termination** | User sends `/end_council` or `/stop` from chat platform | Immediately synthesize and end council |
| **Agent timeout** | A member doesn't respond within `round_timeout` (default: 10 min) | Mark as "no response", continue with remaining agents |

### 2.2 Council State Structure for Termination Tracking

```go
type CouncilState struct {
    Mode         CouncilMode       // "delegation" or "discussion"
    Round        int               // Current round number (1-indexed)
    Responses    map[string]string // Agent ID → response text
    NoResponses  []string          // Agent IDs that didn't respond in current round
    
    MaxRounds    int               // Maximum rounds before auto-terminate (default: 5)
    RoundTimeout time.Duration     // Per-member timeout per round (default: 10m)
    
    PreviousRoundSimilarity float64 // Similarity score from previous round (for echo detection)
    ConsecutiveNoProgress   int     // Rounds with no new info (for progress tracking)
    
    Complete     bool          // Set to true when council should end
    
    Question     string        // Current council question/prompt
}

// Check if council should terminate based on all conditions
func (cs *CouncilState) ShouldTerminate() (terminate bool, reason string) {
    // 1. Manual termination
    if cs.Complete {
        return true, "user requested end"
    }
    
    // 2. All members responded OR max rounds reached
    agentCount := len(cs.Responses) + len(cs.NoResponses)
    allResponded := len(cs.NoResponses) == 0
    
    if allResponded && cs.Round >= cs.MaxRounds {
        return true, "max rounds reached"
    }
    
    // 3. Echo detection (if enabled)
    if cs.PreviousRoundSimilarity > 0.9 {
        cs.ConsecutiveNoProgress++
        if cs.ConsecutiveNoProgress >= 2 {
            return true, "no progress detected after echo"
        }
    } else {
        cs.ConsecutiveNoProgress = 0 // Reset counter on new info
    }
    
    // 4. Progress tracking (main agent evaluates)
    if cs.Round > 2 && cs.ConsecutiveNoProgress >= 3 {
        return true, "no progress for 3 rounds"
    }
    
    return false, ""
}

// Synthesize council results based on available responses
func (cs *CouncilState) Synthesize() string {
    var responses []string
    
    for _, resp := range cs.Responses {
        responses = append(responses, resp)
    }
    
    // If no responses at all, return error state
    if len(responses) == 0 {
        return "Council terminated: no members responded."
    }
    
    // Build synthesis prompt for main agent
    prompt := buildSynthesisPrompt(cs.Question, responses, cs.Round > 1)
    
    // Call LLM to synthesize (using main agent's config)
    summary := callLLMForSynthesis(prompt)
    
    return summary
}

// Check if all responses are similar (consensus detection)
func detectConsensus(responses []string, threshold float64) bool {
    if len(responses) < 2 {
        return false
    }
    
    // Simple cosine similarity check on response embeddings
    // (In production, use actual embedding models; for now use text overlap)
    similarities := calculatePairwiseSimilarity(responses)
    
    for _, sim := range similarities {
        if sim < threshold {
            return false // Not all responses are similar
        }
    }
    
    return true // All responses are similar — consensus reached
}

// Calculate pairwise text similarity (simplified for research doc)
func calculatePairwiseSimilarity(responses []string) []float64 {
    var sims []float64
    
    for i := 0; i < len(responses); i++ {
        for j := i + 1; j < len(responses); j++ {
            sim := textSimilarity(responses[i], responses[j])
            sims = append(sims, sim)
        }
    }
    
    return sims
}

// Simple word-overlap similarity (for research doc — production should use embeddings)
func textSimilarity(a, b string) float64 {
    wordsA := strings.Fields(strings.ToLower(a))
    wordsB := strings.Fields(strings.ToLower(b))
    
    setA := make(map[string]bool)
    for _, w := range wordsA {
        setA[w] = true
    }
    
    intersection := 0
    for w := range setA {
        if setB[w] {
            intersection++
        }
    }
    
    union := len(setA) + len(setB) - intersection
    if union == 0 {
        return 0
    }
    
    return float64(intersection) / float64(union)
}
```

### 2.3 Council Configuration

```yaml
# ~/.opennavi/profiles/<name>/council.yaml
council:
  # Default delivery: back to where council was started
  default_delivery: "origin"
  
  # Optionally also deliver to other platforms
  mirror_to:
    - platform: "telegram"
      user_id: "${ADMIN_TELEGRAM_ID}"
    - platform: "discord"
      channel_id: "${AUDIT_CHANNEL_ID}"
  
  # Timeout for council rounds (each member has this long to respond)
  round_timeout: "10m"
  
  # Max rounds before auto-synthesis
  max_rounds: 5
  
  # Echo detection threshold (similarity > this = echo loop)
  echo_threshold: 0.9
  
  # Consensus detection threshold (all responses similar by this amount)
  consensus_threshold: 0.8
  
  # No-progress tolerance (consecutive rounds with no new info before ending)
  max_no_progress_rounds: 3
```

### 2.4 Main Agent Council Coordination Flow

```go
func (mainAgent *Agent) RunCouncil(ctx context.Context, question string, members []*SubAgent) (*CouncilResult, error) {
    council := &CouncilState{
        Mode:         CouncilDiscussion,
        Round:        1,
        Question:     question,
        Responses:    make(map[string]string),
        NoResponses:  []string{},
        MaxRounds:    defaultMaxRounds,
        RoundTimeout: defaultRoundTimeout,
    }
    
    for !council.Complete {
        // Check termination conditions before starting next round
        terminate, reason := council.ShouldTerminate()
        if terminate {
            log.Printf("Council terminated at round %d: %s", council.Round, reason)
            break
        }
        
        // Send question to each member (with timeout)
        results := make(chan CouncilMemberResult, len(members))
        
        for _, member := range members {
            go func(m *SubAgent) {
                result, err := m.PromptWithTimeout(ctx, question, council.RoundTimeout)
                results <- CouncilMemberResult{
                    AgentID: m.ID,
                    Response: result,
                    Error:    err,
                }
            }(member)
        }
        
        // Collect responses with timeout handling
        for i := 0; i < len(members); i++ {
            select {
            case result := <-results:
                if result.Error != nil {
                    council.NoResponses = append(council.NoResponses, result.AgentID)
                    log.Warnf("Council member %s timed out in round %d", result.AgentID, council.Round)
                } else {
                    council.Responses[result.AgentID] = result.Response
                }
                
            case <-ctx.Done():
                return nil, ctx.Err()
            }
        }
        
        // Check for consensus (early termination)
        if detectConsensus(sliceValues(council.Responses), defaultConsensusThreshold) {
            log.Printf("Council reached consensus at round %d", council.Round)
            council.Complete = true
        }
        
        // Increment round counter
        council.Round++
    }
    
    // Synthesize results
    synthesis := council.Synthesize()
    
    return &CouncilResult{
        Synthesis: synthesis,
        RoundCount: council.Round,
        Responses: council.Responses,
        NoResponses: council.NoResponses,
    }, nil
}

type CouncilMemberResult struct {
    AgentID  string
    Response string
    Error    error
}

type CouncilResult struct {
    Synthesis   string
    RoundCount  int
    Responses   map[string]string
    NoResponses []string
}
```

### Requirement: Termination Conditions in Priority Order
The system SHALL evaluate council termination conditions in priority order: all members responded, max rounds reached, consensus detected, echo detected, no progress for N rounds, manual termination, and agent timeout.

#### Scenario: Evaluating termination conditions
- **GIVEN** a council is in progress
- **WHEN** the system checks whether to terminate
- **THEN** the system SHALL return a termination decision with a reason when any priority condition is met
- **AND** the system SHALL continue the council when no condition is met

### Requirement: Council State Tracking
The system SHALL track council state including mode, round, responses, no-responses, max rounds, round timeout, and progress indicators for termination decisions.

#### Scenario: Tracking responses per round
- **GIVEN** a council session is active
- **WHEN** members respond or time out
- **THEN** the system SHALL record each member's response or no-response status in the council state
- **AND** the system SHALL mark the council complete when all conditions for termination are met

### Requirement: Council Configuration
The system SHALL support council configuration including default delivery, mirroring, round timeout, max rounds, echo threshold, consensus threshold, and no-progress tolerance.

#### Scenario: Configuring council termination thresholds
- **GIVEN** a council is configured with round_timeout, max_rounds, echo_threshold, consensus_threshold, and max_no_progress_rounds
- **WHEN** the council runs
- **THEN** the system SHALL apply the configured values to termination decisions

### Requirement: Council Run Loop with Early Termination
The system SHALL run a council loop that checks termination conditions before each round, collects responses with per-member timeouts, detects consensus for early termination, and synthesizes results.

#### Scenario: Running a council to completion
- **GIVEN** a council question and member agents
- **WHEN** the main agent runs the council
- **THEN** the system SHALL check termination conditions before starting each round
- **AND** the system SHALL collect responses with per-member timeouts
- **AND** the system SHALL terminate early on consensus
- **AND** the system SHALL synthesize the results and return a council result with round count and responses

---

## 02 Edge Cases and Error Handling

### 3.1 Hung Agent Detection

If an agent doesn't respond within the round timeout AND doesn't return a proper error:

```go
func handleHungAgent(agent *SubAgent, ctx context.Context) {
    // Check if agent is actually running (not just slow)
    status := agent.Status()
    
    if status == AgentStatusRunning {
        // Agent is running but hasn't returned — might be stuck in tool loop
        log.Warnf("Agent %s running for >%v without response", agent.ID, defaultRoundTimeout)
        
        // Send abort signal to agent's steering queue
        agent.Steer("/abort")
        
        // Wait a bit more for cleanup
        time.Sleep(5 * time.Second)
        
        if !agent.IsIdle() {
            // Still stuck — force kill and mark as unavailable
            log.Errorf("Agent %s unresponsive after abort — marking unavailable", agent.ID)
            agent.SetUnavailable(true)
        }
    } else if status == AgentStatusError {
        // Agent returned an error — treat as no response for this round
        log.Warnf("Agent %s errored: %v", agent.ID, agent.LastError())
    }
}
```

### 3.2 Partial Council Results

When some agents respond and others don't (timeout or unavailable):

```go
// Synthesize based on available responses only — partial results are better than no results
func synthesizePartialCouncil(question string, responses map[string]string, noResponses []string) string {
    if len(responses) == 0 {
        return fmt.Sprintf("Council failed: %d members did not respond. Original question: %s", 
            len(noResponses), question)
    }
    
    // Build synthesis prompt with available data
    prompt := fmt.Sprintf(`Synthesize the following council responses about: "%s"

Members responded:
%s

Members did not respond: %v

Provide a coherent summary based on the available responses. Note which members didn't participate.`,
        question, formatResponses(responses), noResponses)
    
    return callLLMForSynthesis(prompt)
}
```

### 3.3 Council State Persistence Across Restarts

If the gateway restarts during an active council:

```go
// Persist council state to SQLite before each round (for crash recovery)
func persistCouncilState(sessionID string, state *CouncilState) error {
    db := getDatabase()
    
    stmt, err := db.Prepare(`INSERT OR REPLACE INTO council_state 
        (session_id, round, responses, no_responses, question, complete) 
        VALUES (?, ?, ?, ?, ?, ?)`)
    if err != nil {
        return err
    }
    defer stmt.Close()
    
    _, err = stmt.Exec(
        sessionID,
        state.Round,
        marshalMap(state.Responses),
        marshalStringSlice(state.NoResponses),
        state.Question,
        state.Complete,
    )
    
    return err
}

// Restore council state on gateway restart
func restoreCouncilState(sessionID string) (*CouncilState, error) {
    db := getDatabase()
    
    var round int
    var responsesJSON, noResponsesJSON, question string
    var complete bool
    
    err := db.QueryRow(
        `SELECT round, responses, no_responses, question, complete 
         FROM council_state WHERE session_id = ?`,
        sessionID,
    ).Scan(&round, &responsesJSON, &noResponsesJSON, &question, &complete)
    
    if err == sql.ErrNoRows {
        return nil, fmt.Errorf("no council state found for session %s", sessionID)
    }
    if err != nil {
        return nil, err
    }
    
    responses := unmarshalMap(responsesJSON)
    noResponses := unmarshalStringSlice(noResponsesJSON)
    
    return &CouncilState{
        Round:       round,
        Responses:   responses,
        NoResponses: noResponses,
        Question:    question,
        Complete:    complete,
    }, nil
}
```

### Requirement: Hung Agent Detection
The system SHALL detect agents that are running but unresponsive beyond the round timeout, sending an abort signal and marking them unavailable if they remain stuck.

#### Scenario: Handling a hung agent
- **GIVEN** an agent is running but has not responded within the round timeout
- **WHEN** the system detects the hung agent
- **THEN** the system SHALL send an abort signal to the agent's steering queue
- **AND** the system SHALL wait for cleanup
- **AND** if the agent remains unresponsive, the system SHALL force-kill it and mark it unavailable

### Requirement: Partial Council Results
The system SHALL synthesize council results based on the available responses when some members did not respond, noting the non-participating members in the summary.

#### Scenario: Synthesizing with missing members
- **GIVEN** a council has responses from some members and no responses from others
- **WHEN** the system synthesizes partial results
- **THEN** the system SHALL build the synthesis from the available responses
- **AND** the system SHALL note which members did not participate
- **AND** if no members responded, the system SHALL return a council failed result

### Requirement: Council State Persistence Across Restarts
The system SHALL persist council state to the database between rounds so that gateway restarts do not lose progress, and restore the state on restart.

#### Scenario: Restoring council state after a restart
- **GIVEN** an active council session
- **WHEN** the gateway restarts
- **THEN** the system SHALL restore the council round, responses, no-responses, question, and completion flag from the persisted state

---

## 03 Summary of Recommendations

1. **Max rounds with early termination**: Default 5 rounds, but terminate early if consensus is detected or echo loops are identified.
2. **Per-member timeout**: Each council member has a hard timeout (default 10 minutes). Timeouts result in "no response" for that round, not errors.
3. **Echo detection**: If any member's response is >90% similar to their previous response, mark them as echoing and end council after 2 consecutive echo rounds.
4. **Progress tracking**: If no new information appears for 3 consecutive rounds, synthesize based on available data.
5. **Partial results**: Synthesis should work with whatever responses were received — partial results are better than errors or timeouts.
6. **State persistence**: Persist council state to SQLite between rounds so gateway restarts don't lose progress.
7. **Manual override**: User can send `/end_council` at any time to terminate immediately.


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S040500 | 0.1.0 | Migrated from docs/spec/ to spec-tree |