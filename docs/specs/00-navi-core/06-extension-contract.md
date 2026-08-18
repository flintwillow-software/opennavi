---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - navi-core
priority: medium
spec-refs: [000600, 000601, 000602, 000603, 000604, 000605, 000606, 000607, 000608, 000609, 000610, 000611]
---

# Extension Contract

**Status:** Draft  
**Last updated:** 2026-07-29

---

## 00 Overview

Extensions are **standalone compiled binaries** that augment a navi's functionality without requiring the navi to be rebuilt. They're loaded at runtime by the opennavi SDK, running in sandboxed processes with their own trust model (see `04-trust-signing-model.md`).

The extension contract defines:
1. **How extensions are discovered** — where the SDK looks for them
2. **How they communicate** — JSON-RPC over stdin/stdout
3. **What capabilities they can add** — tools, hooks, commands
4. **How they're verified** — signature checking before loading

### Requirement: Extension as Standalone Binary
The system SHALL load extensions as standalone compiled binaries at runtime, without requiring the navi to be rebuilt.

#### Scenario: Loading an extension at runtime
- **GIVEN** a navi is running
- **WHEN** the navi discovers an extension binary in the extension directory
- **THEN** the system SHALL load the extension as a standalone binary
- **AND** the navi SHALL NOT need to be rebuilt to include the extension

---

## 01 Extension Types

Extensions can augment a navi in three ways:

| Type | Description | Example |
|------|-------------|---------|
| **Tool extensions** | Add new tools the agent can execute | Monitoring query tool, custom API caller |
| **Hook extensions** | Register lifecycle hooks (message in/out, session events) | Alert classification hook, logging hook |
| **Command extensions** | Add new slash commands for interactive mode | `/triage`, `/status`, custom commands |

### Requirement: Tool Extensions
The system SHALL support tool extensions that add new tools the agent can execute.

#### Scenario: Adding a tool extension
- **GIVEN** an extension provides a monitoring query tool
- **WHEN** the extension is loaded
- **THEN** the agent SHALL be able to call the monitoring query tool

### Requirement: Hook Extensions
The system SHALL support hook extensions that register lifecycle hooks for events such as message in/out and session events.

#### Scenario: Adding a hook extension
- **GIVEN** an extension registers a hook for webhook_received
- **WHEN** a webhook event occurs
- **THEN** the system SHALL fire the hook to the extension

### Requirement: Command Extensions
The system SHALL support command extensions that add new slash commands for interactive mode.

#### Scenario: Adding a command extension
- **GIVEN** an extension defines a `/triage` command
- **WHEN** a user types `/triage` in interactive mode
- **THEN** the system SHALL dispatch the command to the extension for handling

---

## 02 Extension Discovery

Extensions are discovered from the extension directory:

```go
// Default location
~/.opennavi/extensions/
├── opennavi-monitoring/        # standplugin binary + metadata
│   ├── opennavi-monitoring     # compiled binary
│   └── manifest.json           # tool definitions, signature info
├── opennavi-alerting/          # another extension
│   ├── opennavi-alerting
│   └── manifest.json
```

The SDK scans this directory on startup:

```go
func discoverExtensions() ([]Extension, error) {
    dir := filepath.Join(getConfigDir(), "extensions")
    entries, err := os.ReadDir(dir)
    if err != nil {
        return nil, err
    }
    
    var extensions []Extension
    for _, entry := range entries {
        if !entry.IsDir() {
            continue
        }
        
        manifestPath := filepath.Join(dir, entry.Name(), "manifest.json")
        manifest, err := loadManifest(manifestPath)
        if err != nil {
            log.Warnf("Skipping extension %s: invalid manifest", entry.Name())
            continue
        }
        
        extensions = append(extensions, Extension{
            Name:    entry.Name(),
            Path:    filepath.Join(dir, entry.Name()),
            Manifest: *manifest,
        })
    }
    
    return extensions, nil
}
```

### Requirement: Extension Directory Scanning
The system SHALL scan the extension directory at `~/.opennavi/extensions/` on startup, reading each subdirectory's `manifest.json` to discover extensions.

#### Scenario: Discovering extensions on startup
- **GIVEN** an extension binary and manifest exist in `~/.opennavi/extensions/opennavi-monitoring/`
- **WHEN** the navi starts
- **THEN** the system SHALL read the manifest
- **AND** the system SHALL register the discovered extension

### Requirement: Invalid Manifest Handling
The system SHALL skip extension directories with invalid manifests and log a warning, continuing startup without that extension.

#### Scenario: Skipping an invalid extension
- **GIVEN** an extension directory exists but has an invalid manifest
- **WHEN** the navi starts
- **THEN** the system SHALL log a warning about the invalid manifest
- **AND** the system SHALL skip the extension
- **AND** the navi SHALL continue starting with other extensions

---

## 03 Extension Manifest

Each extension has a `manifest.json` file that declares its capabilities:

```json
{
  "name": "opennavi-monitoring",
  "version": "1.0.0",
  "author": "OnCall Bot Team",
  
  // Tools this extension provides
  "tools": [
    {
      "name": "query_grafana",
      "description": "Query Grafana for metrics and dashboards",
      "parameters": {
        "type": "object",
        "properties": {
          "query": {"type": "string", "description": "PromQL query"},
          "range": {"type": "string", "description": "Time range (e.g., '5m', '1h')"}
        }
      }
    },
    {
      "name": "check_service_health",
      "description": "Check health of monitored services",
      "parameters": {
        "type": "object",
        "properties": {
          "service_name": {"type": "string"}
        }
      }
    }
  ],
  
  // Hooks this extension provides (optional)
  "hooks": [
    {
      "name": "alert_classifier",
      "event": "webhook_received",
      "description": "Classify incoming alerts by severity"
    }
  ],
  
  // Commands this extension provides (optional)
  "commands": [
    {
      "name": "/health",
      "description": "Show current service health status"
    }
  ],
  
  // Signature information for verification
  "signature": {
    "algorithm": "gpg",  // or "codesign", "signtool"
    "key_id": "ABCDEF1234567890",
    "file": "opennavi-monitoring.asc"
  }
}
```

### Requirement: Manifest Declaration
The system SHALL require each extension to declare its capabilities in a `manifest.json` file, including tools, hooks, commands, and signature information.

#### Scenario: Declaring extension capabilities
- **GIVEN** an extension author creates a manifest.json
- **WHEN** the navi reads the manifest
- **THEN** the system SHALL parse the tools, hooks, and commands declared in the manifest
- **AND** the system SHALL validate the signature information

### Requirement: Optional Hooks and Commands
The system SHALL treat hooks and commands in the manifest as optional, with tools being the only required capability.

#### Scenario: Extension with only tools
- **GIVEN** an extension manifest declares tools but no hooks or commands
- **WHEN** the navi loads the extension
- **THEN** the system SHALL register the tools
- **AND** the system SHALL NOT report hooks or commands as missing

---

## 04 Extension Protocol

Extensions communicate with the host navi via **JSON-RPC over stdin/stdout**. This is the same protocol used for standplugins but adapted for the extension use case.

### Message Format

**Host → Extension:**
```json
{
  "jsonrpc": "2.0",
  "method": "callTool",
  "params": {
    "name": "query_grafana",
    "args": {"query": "rate(http_requests_total[5m])"}
  },
  "id": 1
}
```

**Extension → Host:**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "content": "{\"status\":\"ok\",\"data\":[...]}",
    "is_error": false
  },
  "id": 1
}
```

### Protocol Methods

| Method | Direction | Description |
|--------|-----------|-------------|
| `initialize` | Host → Extension | Sent when extension starts, with config |
| `listTools` | Extension → Host | Called by host to discover available tools |
| `callTool` | Host → Extension | Tool execution request |
| `listHooks` | Extension → Host | Called by host to discover available hooks |
| `fireHook` | Host → Extension | Hook event notification |
| `listCommands` | Extension → Host | Called by host to discover available commands |
| `handleCommand` | Host → Extension | Slash command execution request |
| `shutdown` | Host → Extension | Graceful shutdown signal |

### Example: Tool Execution Flow

```go
// 1. Host discovers tools via listTools
host.send(RPCRequest{Method: "listTools", ID: 1})
extension.respond(RPCResponse{ID: 1, Result: &ToolResult{Content: `[{...}]`}})

// 2. Host registers discovered tools with agent registry
tools := parseToolSchemas(response.Result.Content)
agentRegistry.register(tools...)

// 3. Agent calls a tool during execution
host.send(RPCRequest{Method: "callTool", Params: callToolParams, ID: 5})

// 4. Extension executes and responds
extension.respond(RPCResponse{ID: 5, Result: &ToolResult{Content: "query result...", IsError: false}})

// 5. Host returns result to agent
agent.processToolResult(response.Result.Content)
```

### Requirement: JSON-RPC Protocol
The system SHALL use JSON-RPC 2.0 over stdin/stdout for communication between the host navi and extensions.

#### Scenario: Calling a tool extension
- **GIVEN** an extension is loaded and its tools are registered
- **WHEN** the agent calls a tool provided by the extension
- **THEN** the host SHALL send a `callTool` JSON-RPC request to the extension via stdin
- **AND** the extension SHALL respond with a JSON-RPC response via stdout

### Requirement: Protocol Methods
The system SHALL support the defined protocol methods: `initialize`, `listTools`, `callTool`, `listHooks`, `fireHook`, `listCommands`, `handleCommand`, and `shutdown`.

#### Scenario: Initializing an extension
- **GIVEN** an extension process has been spawned
- **WHEN** the host sends an `initialize` RPC with config
- **THEN** the extension SHALL respond with initialization status

#### Scenario: Graceful shutdown
- **GIVEN** the navi is shutting down
- **WHEN** the host sends a `shutdown` RPC to all extensions
- **THEN** each extension SHALL perform cleanup and exit gracefully

---

## 05 Extension Lifecycle

### Startup

1. SDK discovers extensions in `~/.opennavi/extensions/`
2. For each extension, verify signature against allowed signers (see `04-trust-signing-model.md`)
3. If verified, spawn extension process with stdin/stdout pipes
4. Send `initialize` RPC with navi config
5. Extension responds with `listTools`, `listHooks`, `listCommands`
6. Host registers discovered capabilities

### Runtime

- Tools execute via `callTool` RPC on demand
- Hooks fire via `fireHook` RPC when events occur
- Commands execute via `handleCommand` RPC when user types slash commands

### Shutdown

1. Host sends `shutdown` RPC to all extensions
2. Extensions perform cleanup and exit gracefully
3. If extension doesn't respond within timeout, host sends SIGTERM

### Requirement: Extension Startup Sequence
The system SHALL follow a defined startup sequence: discover, verify signature, spawn, initialize, register capabilities.

#### Scenario: Starting an extension
- **GIVEN** an extension is discovered and verified
- **WHEN** the extension is started
- **THEN** the host SHALL spawn the extension process
- **AND** the host SHALL send an `initialize` RPC
- **AND** the extension SHALL report its capabilities
- **AND** the host SHALL register the capabilities

### Requirement: Extension Shutdown Sequence
The system SHALL follow a defined shutdown sequence: send `shutdown` RPC, wait for graceful exit, send SIGTERM on timeout.

#### Scenario: Shutting down an extension
- **GIVEN** the navi is shutting down
- **WHEN** the host sends `shutdown` to all extensions
- **THEN** each extension SHALL attempt graceful cleanup
- **AND** if an extension does not respond within the timeout, the host SHALL send SIGTERM

---

## 06 Sandbox Isolation

Extensions run in **sandboxed processes** with restricted access:

| Resource | Access Level | Notes |
|----------|-------------|-------|
| Filesystem | Read-only to project dir + `~/.opennavi/` | No write access to arbitrary paths |
| Network | Outbound only (HTTP, API calls) | No inbound listeners unless explicitly granted |
| Environment | Inherited from host + extension-specific vars | Sensitive keys must be passed via RPC config |
| Memory | Isolated process space | Can't read main navi's memory |
| Execution | Limited to tool/hook/command handlers | No arbitrary code execution outside protocol |

### Sandbox Enforcement

The opennavi SDK enforces sandbox boundaries:

```go
func spawnExtension(path string, manifest Manifest) (*ExtensionProcess, error) {
    cmd := exec.Command(path)
    
    // Restrict filesystem access
    cmd.SysProcAttr = &syscall.SysProcAttr{
        Chroot: getProjectDir(),  // chroot to project dir
    }
    
    // Set environment (no inherited sensitive vars)
    env := os.Environ()
    env = append(env, "OPENNAVI_EXTENSION=true")
    cmd.Env = env
    
    // Redirect stdin/stdout for RPC protocol
    stdin, _ := cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    
    return &ExtensionProcess{
        Cmd:      cmd,
        Stdin:    stdin,
        Stdout:   stdout,
        Manifest: manifest,
    }, nil
}
```

### Requirement: Sandboxed Extension Process
The system SHALL run extensions in sandboxed processes with restricted filesystem access (read-only to project dir and `~/.opennavi/`), outbound-only network access, isolated process memory, and no arbitrary code execution outside the protocol.

#### Scenario: Restricting filesystem access
- **GIVEN** an extension is spawned
- **WHEN** the extension attempts to write to an arbitrary filesystem path
- **THEN** the sandbox SHALL block the write
- **AND** the extension SHALL only have read access to the project directory and `~/.opennavi/`

#### Scenario: Restricting network access
- **GIVEN** an extension is spawned
- **WHEN** the extension attempts to start an inbound listener
- **THEN** the sandbox SHALL block the inbound listener
- **AND** the extension SHALL only have outbound network access

### Requirement: Process Isolation
The system SHALL run each extension in its own isolated process space, preventing access to the main navi's memory.

#### Scenario: Isolating extension memory
- **GIVEN** an extension is running in a sandboxed process
- **WHEN** the extension attempts to access the main navi's memory
- **THEN** the process isolation SHALL prevent the access

---

## 07 Extension Configuration

Extensions can have configuration passed via the `initialize` RPC:

```json
// Host → Extension (on startup)
{
  "jsonrpc": "2.0",
  "method": "initialize",
  "params": {
    "config": {
      "grafana_url": "https://grafana.example.com",
      "api_key": "${GRAFANA_API_KEY}",  // envvar expanded by host
      "default_range": "5m"
    },
    "navi_info": {
      "name": "oncall-bot",
      "version": "1.0.0"
    }
  },
  "id": 0
}
```

Extensions should not store configuration themselves — it's passed at runtime and can be changed without rebuilding the extension.

### Requirement: Runtime Configuration via Initialize
The system SHALL pass extension configuration via the `initialize` RPC, including extension-specific config, API keys, and navi info.

#### Scenario: Configuring an extension at startup
- **GIVEN** an extension requires configuration such as a Grafana URL and API key
- **WHEN** the host sends the `initialize` RPC
- **THEN** the host SHALL include the configuration in the RPC params
- **AND** the host SHALL expand environment variable references in the config values

### Requirement: Config Change Without Rebuild
The system SHALL pass configuration at runtime via `initialize`, allowing configuration changes without rebuilding the extension.

#### Scenario: Changing extension configuration
- **GIVEN** an extension is configured via the `initialize` RPC
- **WHEN** a user changes the extension's configuration
- **THEN** the extension SHALL receive the new configuration on next startup
- **AND** the extension SHALL NOT need to be rebuilt

---

## 08 Error Handling

### Extension Crashes

If an extension crashes (panic, segfault, etc.):

1. SDK detects process exit
2. Log warning with extension name and exit code
3. Mark extension as unavailable for current session
4. Navi continues without that extension's capabilities
5. Optionally retry spawn after delay (configurable)

```go
func monitorExtension(proc *ExtensionProcess, name string) {
    err := proc.Cmd.Wait()
    if err != nil {
        log.Warnf("Extension %s exited with error: %v", name, err)
        
        // Don't crash the main navi — just disable this extension
        extensionRegistry.disable(name)
    }
}
```

### RPC Errors

Extensions can return errors via the `error` field in responses:

```json
{
  "jsonrpc": "2.0",
  "error": {
    "code": -32601,
    "message": "Tool not found"
  },
  "id": 5
}
```

Standard JSON-RPC error codes apply:
- `-32700`: Parse error
- `-32600`: Invalid request
- `-32601`: Method not found
- `-32602`: Invalid params
- `-32603`: Internal error

### Requirement: Crash Resilience
The system SHALL handle extension crashes gracefully: log a warning, mark the extension as unavailable, and continue the navi without that extension's capabilities.

#### Scenario: Extension crashes
- **GIVEN** an extension process crashes
- **WHEN** the SDK detects the process exit
- **THEN** the system SHALL log a warning with the extension name and exit code
- **AND** the system SHALL mark the extension as unavailable for the current session
- **AND** the navi SHALL continue without the extension's capabilities

### Requirement: RPC Error Handling
The system SHALL support standard JSON-RPC error codes for extension communication, including parse error, invalid request, method not found, invalid params, and internal error.

#### Scenario: Extension returns an RPC error
- **GIVEN** an extension receives a request for an unknown method
- **WHEN** the extension responds
- **THEN** the extension SHALL return a JSON-RPC error with code `-32601` (Method not found)
- **AND** the host SHALL handle the error accordingly

---

## 09 Building an Extension

Extension authors write their standplugin in Go (or any language that can read/write JSON over stdin/stdout):

```go
// cmd/opennavi-monitoring/main.go
package main

import (
    "bufio"
    "encoding/json"
    "fmt"
    "os"
)

func main() {
    scanner := bufio.NewScanner(os.Stdin)
    for scanner.Scan() {
        var req RPCRequest
        if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
            continue
        }
        
        resp := handleRequest(req)
        output, _ := json.Marshal(resp)
        fmt.Fprintln(os.Stdout, string(output))
    }
}

func handleRequest(req RPCRequest) RPCResponse {
    switch req.Method {
    case "initialize":
        return RPCResponse{JSONRPC: "2.0", Result: &ToolResult{Content: "ok"}, ID: req.ID}
    
    case "listTools":
        tools := []ToolSchema{
            {
                Name:        "query_grafana",
                Description: "Query Grafana for metrics",
                Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
            },
        }
        result, _ := json.Marshal(tools)
        return RPCResponse{JSONRPC: "2.0", Result: &ToolResult{Content: string(result)}, ID: req.ID}
    
    case "callTool":
        // Execute tool logic
        output, err := executeGrafanaQuery(...)
        if err != nil {
            return RPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: -1, Message: err.Error()}, ID: req.ID}
        }
        return RPCResponse{JSONRPC: "2.0", Result: &ToolResult{Content: output}, ID: req.ID}
    
    case "shutdown":
        os.Exit(0)
        return RPCResponse{}
    
    default:
        return RPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: -32601, Message: "Method not found"}, ID: req.ID}
    }
}
```

Build and install:

```bash
# Build the extension
go build -o opennavi-monitoring ./cmd/opennavi-monitoring/

# Create extension directory
mkdir -p ~/.opennavi/extensions/opennavi-monitoring

# Move binary + create manifest.json
mv opennavi-monitoring ~/.opennavi/extensions/opennavi-monitoring/
cat > ~/.opennavi/extensions/opennavi-monitoring/manifest.json << 'EOF'
{
  "name": "opennavi-monitoring",
  "version": "1.0.0",
  "author": "OnCall Bot Team",
  "tools": [...],
  "signature": {...}
}
EOF

# Sign the extension (platform-specific)
gpg --detach-sign --armor ~/.opennavi/extensions/opennavi-monitoring/opennavi-monitoring
```

### Requirement: Language-Agnostic Protocol
The system SHALL support extensions written in Go or any language capable of reading and writing JSON over stdin/stdout.

#### Scenario: Writing an extension in Go
- **GIVEN** an extension author writes a standplugin in Go
- **WHEN** the extension is built and installed
- **THEN** the extension SHALL read JSON-RPC requests from stdin and write responses to stdout

### Requirement: Extension Installation Process
The system SHALL support extension installation by placing the binary and manifest in a subdirectory of `~/.opennavi/extensions/`.

#### Scenario: Installing an extension
- **GIVEN** an extension author has built the extension binary and created a manifest.json
- **WHEN** the author places them in `~/.opennavi/extensions/opennavi-monitoring/`
- **THEN** the navi SHALL discover the extension on next startup
- **AND** the navi SHALL register its capabilities after signature verification

---

## 10 Security Considerations

### What Extensions Can Do

- Execute tool logic (query APIs, run commands in sandboxed env)
- Register hooks for lifecycle events
- Add slash commands for interactive mode
- Make outbound network requests (within sandbox)

### What Extensions Cannot Do

- Access main navi's memory space
- Write to arbitrary filesystem paths (sandboxed to project dir + `~/.opennavi/`)
- Execute code outside of tool/hook/command handlers
- Modify the navi binary or other extensions
- Bypass signature verification

### Developer Responsibilities

1. **Sign your extension** — use platform-specific signing tools
2. **Publish public keys** — on project website, keyserver, README
3. **Document capabilities** — clearly state what the extension does and why it needs access
4. **Minimize permissions** — only request filesystem/network access you actually need

### User Responsibilities

1. **Verify signatures before loading extensions**
2. **Only load extensions from trusted sources** (mentioned in `AllowedSigners`)
3. **Review extension manifests** — understand what tools/hooks/commands are being added
4. **Disable unused extensions** — less attack surface

### Requirement: Extension Capability Boundaries
The system SHALL allow extensions to execute tool logic, register hooks, add slash commands, and make outbound network requests, while preventing access to main navi memory, arbitrary filesystem writes, code execution outside handlers, modification of the navi binary, and bypassing signature verification.

#### Scenario: Extension attempts memory access
- **GIVEN** an extension is running in a sandboxed process
- **WHEN** the extension attempts to access the main navi's memory space
- **THEN** the sandbox SHALL prevent the access

#### Scenario: Extension attempts filesystem write
- **GIVEN** an extension is running in a sandboxed process
- **WHEN** the extension attempts to write to an arbitrary filesystem path
- **THEN** the sandbox SHALL deny the write

### Requirement: Extension Developer Responsibilities
The system SHALL require extension developers to sign their extensions, publish public keys, document capabilities, and minimize permissions.

#### Scenario: Publishing a signed extension
- **GIVEN** an extension developer has built an extension
- **WHEN** the developer releases the extension
- **THEN** the developer SHALL sign the extension binary
- **AND** the developer SHALL publish the public key and document what the extension does

---

## 11 Next Steps

- **[01-navi-definition.md](./01-navi-definition.md)** — How extensions fit into the navi definition
- **[04-trust-signing-model.md](./04-trust-signing-model.md)** — Signature verification flow for extensions
- **[02-agent-config/00-skills-system.md](../02-agent-config/00-skills-system.md)** — Skills are baked in at build time, not runtime loaded


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S000600 | 0.1.0 | Migrated from docs/spec/ to spec-tree |