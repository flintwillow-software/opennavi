---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - operations
priority: medium
spec-refs: [080500, 080501, 080502, 080503, 080504, 080505, 080506, 080507, 080508, 080509, 080510, 080511]
---

# Plan: Deployment and Operations

**Scope:** Service management, Docker deployment, update mechanism, health monitoring, and operational procedures.

---

## 00 Service Management

### 1.1 Daemon Architecture

The gateway (`opennavi-gateway`) is designed as a long-running daemon. It needs to:
- Start on boot
- Survive crashes (auto-restart)
- Handle graceful shutdown
- Log to a persistent location
- Be manageable via standard tooling

### 1.2 systemd Service (Linux)

```ini
# /etc/systemd/system/opennavi-gateway@.service
# Use: systemctl start opennavi-gateway@work
#       systemctl start opennavi-gateway@personal

[Unit]
Description=OpenNavi Gateway (%i)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%i
Group=%i
Environment=OPENNAVI_PROFILE=%i
Environment=HOME=/home/%i
ExecStart=/usr/local/bin/opennavi-gateway --profile %i --config /home/%i/.opennavi/profiles/%i/gateway.yaml
Restart=on-failure
RestartSec=5
StartLimitIntervalSec=300
StartLimitBurst=5

# Resource limits
MemoryMax=512M
CPUQuota=100%
TasksMax=100

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=opennavi-%i

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/home/%i/.opennavi
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```bash
# Enable and start
sudo systemctl daemon-reload
sudo systemctl enable opennavi-gateway@work
sudo systemctl start opennavi-gateway@work

# Check status
systemctl status opennavi-gateway@work
journalctl -u opennavi-gateway@work -f

# Restart
sudo systemctl restart opennavi-gateway@work
```

### 1.3 launchd Service (macOS)

```xml
<!-- ~/Library/LaunchAgents/com.opennavi.gateway.plist -->
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.opennavi.gateway.work</string>

    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/opennavi-gateway</string>
        <string>--profile</string>
        <string>work</string>
    </array>

    <key>EnvironmentVariables</key>
    <dict>
        <key>OPENNAVI_PROFILE</key>
        <string>work</string>
        <key>HOME</key>
        <string>/Users/$(whoami)</string>
    </dict>

    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>

    <key>StandardOutPath</key>
    <string>/Users/$(whoami)/.opennavi/logs/gateway.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/$(whoami)/.opennavi/logs/gateway-err.log</string>

    <key>SoftResourceLimits</key>
    <dict>
        <key>NumberOfFiles</key>
        <integer>1024</integer>
    </dict>
</dict>
</plist>
```

```bash
# Load
launchctl load ~/Library/LaunchAgents/com.opennavi.gateway.work.plist

# Unload
launchctl unload ~/Library/LaunchAgents/com.opennavi.gateway.work.plist

# Check
launchctl list | grep opennavi
```

### 1.4 Windows Service (Optional, Phase 3)

```powershell
# Using NSSM (Non-Sucking Service Manager)
nssm install opennavi-gateway C:\Program Files\opennavi\opennavi-gateway.exe
nssm set opennavi-gateway AppEnvironment OPENNAVI_PROFILE=work
nssm set opennavi-gateway AppDirectory C:\Users\<user>\.opennavi
nssm start opennavi-gateway
```

### Requirement: Daemon Service Architecture

The system SHALL run the gateway as a long-running daemon that starts on boot, auto-restarts on crashes, handles graceful shutdown, logs to a persistent location, and is manageable via standard system tooling.

#### Scenario: Auto-restart on crash
- **GIVEN** the gateway process crashes unexpectedly
- **WHEN** the service manager detects the failure
- **THEN** the system SHALL automatically restart the gateway with a 5-second delay

#### Scenario: Graceful shutdown
- **GIVEN** the gateway receives a termination signal
- **WHEN** the shutdown sequence begins
- **THEN** the system SHALL complete in-flight operations, close adapters, and persist session state before exiting

### Requirement: systemd Service for Linux

The system SHALL provide a systemd template service `opennavi-gateway@.service` for Linux with memory limit, CPU quota, logging to journald, and security hardening via `NoNewPrivileges`, `ProtectSystem=strict`, and `ProtectHome=read-only`.

#### Scenario: systemd service activation
- **GIVEN** the systemd service file is installed
- **WHEN** the user runs `systemctl enable opennavi-gateway@work`
- **THEN** the system SHALL configure the gateway to start on boot using the `work` profile

#### Scenario: Security hardening
- **GIVEN** the gateway is running under systemd
- **WHEN** the service starts
- **THEN** the system SHALL enforce `NoNewPrivileges=true`, `ProtectSystem=strict`, `ProtectHome=read-only`, and `PrivateTmp=true`

### Requirement: launchd Service for macOS

The system SHALL provide a launchd plist for macOS with `RunAtLoad` for boot-time start, `KeepAlive` for crash recovery, and file-based logging to `~/.opennavi/logs/`.

#### Scenario: launchd load on boot
- **GIVEN** the plist is installed in `~/Library/LaunchAgents/`
- **WHEN** the user loads the service with `launchctl load`
- **THEN** the system SHALL start the gateway at login and keep it alive on crash

---

## 01 Docker Deployment

### 2.1 Dockerfile

```dockerfile
# Build stage
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git ca-certificates
WORKDIR /app

# Copy go module files first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /opennavi-gateway \
    ./cmd/opennavi-gateway

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 opennavi && \
    adduser -u 1000 -G opennavi -s /bin/sh -D opennavi

WORKDIR /home/opennavi
RUN mkdir -p .opennavi/profiles/default/sessions .opennavi/logs .opennavi/plugins/bin

# Copy binary
COPY --from=builder /opennavi-gateway /usr/local/bin/opennavi-gateway
RUN chmod +x /usr/local/bin/opennavi-gateway

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/opennavi-gateway", "health"]

# Run as non-root
USER opennavi

# Default command
CMD ["opennavi-gateway", "--profile", "default"]
```

### 2.2 docker-compose.yml

```yaml
version: "3.8"

services:
  opennavi-gateway:
    build: .
    container_name: opennavi-gateway
    restart: unless-stopped
    profiles:
      - default
    environment:
      - OPENNAVI_PROFILE=work
      - TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
      - DISCORD_BOT_TOKEN=${DISCORD_BOT_TOKEN}
      - OPENROUTER_API_KEY=${OPENROUTER_API_KEY}
    volumes:
      - opennavi-data:/home/opennavi/.opennavi
      - ./gateway.yaml:/home/opennavi/.opennavi/profiles/work/gateway.yaml:ro
      - ./agents.yaml:/home/opennavi/.opennavi/profiles/work/agents.yaml:ro
      - ./persona.md:/home/opennavi/.opennavi/profiles/work/persona.md:ro
    ports:
      - "8080:8080"  # webhook port
    healthcheck:
      test: ["CMD", "/usr/local/bin/opennavi-gateway", "health"]
      interval: 30s
      timeout: 5s
      retries: 3
    deploy:
      resources:
        limits:
          memory: 512M
          cpus: "1.0"

  # Optional: separate instance for personal profile
  opennavi-gateway-personal:
    build: .
    container_name: opennavi-gateway-personal
    restart: unless-stopped
    profiles:
      - personal
    environment:
      - OPENNAVI_PROFILE=personal
      - TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
    volumes:
      - opennavi-personal-data:/home/opennavi/.opennavi
      - ./gateway-personal.yaml:/home/opennavi/.opennavi/profiles/personal/gateway.yaml:ro
    ports:
      - "8081:8080"
    depends_on:
      opennavi-gateway:
        condition: service_started

volumes:
  opennavi-data:
  opennavi-personal-data:
```

### 2.3 Docker Commands

```bash
# Build and run
docker compose --profile default up -d

# Check logs
docker compose --profile default logs -f opennavi-gateway

# Check health
docker compose --profile default ps

# Update
docker compose --profile default down
docker compose --profile default build --no-cache
docker compose --profile default up -d

# Exec into container
docker exec -it opennavi-gateway sh

# View sessions
docker exec opennavi-gateway ls -la /home/opennavi/.opennavi/profiles/default/sessions/
```

### 2.4 Docker Multi-Profile Setup

For running multiple profiles, use separate compose files or a single compose file with multiple services:

```bash
# Single profile
docker compose -f docker-compose-work.yml up -d

# Multiple profiles (separate compose files)
docker compose -f docker-compose-work.yml up -d
docker compose -f docker-compose-personal.yml up -d
```

### Requirement: Multi-Stage Docker Build

The system SHALL provide a multi-stage Dockerfile that builds the Go binary in a builder stage and runs it in a minimal Alpine runtime stage as a non-root user, with the binary statically compiled with stripped symbols.

#### Scenario: Non-root container execution
- **GIVEN** the Docker image is built
- **WHEN** the container starts
- **THEN** the system SHALL run as the `opennavi` user (UID 1000) with no root privileges

### Requirement: Docker Compose Profile Configuration

The system SHALL provide a docker-compose.yml supporting multiple compose profiles (default, personal) with environment variable substitution for secrets, read-only config volume mounts, resource limits, and a HEALTHCHECK.

#### Scenario: Profile-specific compose start
- **GIVEN** the compose file defines a `default` profile
- **WHEN** the user runs `docker compose --profile default up -d`
- **THEN** the system SHALL start the gateway with the `work` profile, mounted config files, and environment variables

#### Scenario: Read-only config mount
- **GIVEN** config files are mounted as volumes
- **WHEN** the container starts
- **THEN** the system SHALL mount `gateway.yaml`, `agents.yaml`, and `persona.md` as read-only (`:ro`)

### Requirement: Container Health Check

The system SHALL include a Docker HEALTHCHECK that runs the `opennavi-gateway health` command with a 30-second interval, 5-second timeout, 10-second start period, and 3 retries.

#### Scenario: Unhealthy container
- **GIVEN** the gateway process is running but an adapter is disconnected
- **WHEN** the health check runs
- **THEN** the system SHALL return a non-zero exit code, causing Docker to mark the container as unhealthy

### Requirement: Multi-Profile Docker Deployment

The system SHALL support running multiple profile instances simultaneously via separate compose files or a single compose file with multiple services, each with its own port mapping and data volume.

#### Scenario: Personal and work profiles
- **GIVEN** compose files for both `work` and `personal` profiles
- **WHEN** both compose files are deployed
- **THEN** the system SHALL run two gateway containers on different ports (8080 and 8081) with separate data volumes

---

## 02 Update Mechanism

### 3.1 `opennavi self-update` Command

```go
// cmd/opennavi/self_update.go

type SelfUpdateCmd struct {
    channel string // "stable" or "beta"
    force   bool
}

func (c *SelfUpdateCmd) Run() error {
    // 1. Check current version
    currentVersion, err := version.Current()
    if err != nil {
        return err
    }

    // 2. Check for updates
    latestVersion, err := version.Latest(c.channel)
    if err != nil {
        return err
    }

    if latestVersion <= currentVersion && !c.force {
        fmt.Println("opennavi is up to date")
        return nil
    }

    // 3. Download new binary
    downloadURL := fmt.Sprintf(
        "https://github.com/flintwillow-software/opennavi/releases/download/v%s/opennavi-%s-%s-%s",
        latestVersion, runtime.GOOS, runtime.GOARCH, "amd64",
    )

    tempFile, err := downloadBinary(downloadURL)
    if err != nil {
        return err
    }
    defer os.Remove(tempFile)

    // 4. Verify checksum
    checksumURL := downloadURL + ".sha256"
    expectedChecksum, err := downloadText(checksumURL)
    if err != nil {
        return err
    }

    actualChecksum, err := sha256File(tempFile)
    if err != nil {
        return err
    }

    if actualChecksum != expectedChecksum {
        return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedChecksum, actualChecksum)
    }

    // 5. Backup current binary
    currentBinary, _ := os.Executable()
    backupPath := currentBinary + ".bak"
    if err := copyFile(currentBinary, backupPath); err != nil {
        return err
    }

    // 6. Replace binary
    if err := copyFile(tempFile, currentBinary); err != nil {
        os.Rename(backupPath, currentBinary) // restore on failure
        return err
    }

    fmt.Printf("Updated opennavi from v%s to v%s\n", currentVersion, latestVersion)
    fmt.Println("Restart your gateway: systemctl restart opennavi-gateway@work")
    return nil
}
```

### 3.2 Version Management

```go
// internal/version/version.go

package version

import "runtime"

var (
    Version   = "dev"    // set by ldflags: -ldflags="-X internal/version.Version=1.0.0"
    Commit    = "unknown"
    BuiltAt   = "unknown"
    GoVersion = runtime.Version()
)

func Current() string {
    return Version
}

func Full() string {
    return fmt.Sprintf("opennavi v%s (commit %s, built %s, go %s)",
        Version, Commit, BuiltAt, GoVersion)
}

func Latest(channel string) (string, error) {
    // Fetch from GitHub releases API
    resp, err := http.Get("https://api.github.com/repos/flintwillow-software/opennavi/releases")
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    // Parse releases, filter by channel, return latest tag
    // ...
}
```

### 3.3 Update Config

```yaml
# ~/.opennavi/profiles/<name>/update.yaml
update:
  channel: "stable"   # stable or beta
  auto_check: true    # check for updates on startup
  auto_install: false # ask before installing
  notify: true        # notify user when update available
  notify_channel: "telegram"  # where to notify
```

### Requirement: Self-Update Command

The system SHALL provide an `opennavi self-update` command that checks the current version, fetches the latest release for the configured channel, downloads the new binary, verifies the SHA-256 checksum, backs up the current binary, and replaces it atomically.

#### Scenario: Update available
- **GIVEN** a newer version exists on the configured channel
- **WHEN** the user runs `opennavi self-update`
- **THEN** the system SHALL download the new binary, verify its checksum, back up the current binary, and replace it

#### Scenario: Already up to date
- **GIVEN** the current version is the latest on the configured channel
- **WHEN** the user runs `opennavi self-update` without `--force`
- **THEN** the system SHALL print "opennavi is up to date" and exit without making changes

### Requirement: Checksum Verification

The system SHALL download the SHA-256 checksum for the new binary from the release and verify it matches the downloaded file before replacing the current binary, returning an error on mismatch.

#### Scenario: Checksum mismatch
- **GIVEN** the downloaded binary SHA-256 does not match the published checksum
- **WHEN** the system verifies the checksum
- **THEN** the system SHALL return an error with the expected and actual checksum values and abort the update

### Requirement: Backup and Rollback on Update Failure

The system SHALL back up the current binary to `<binary>.bak` before replacing it and SHALL restore the backup if the replacement fails.

#### Scenario: Replacement failure rollback
- **GIVEN** the binary replacement fails (e.g., disk full, permission denied)
- **WHEN** the copy operation returns an error
- **THEN** the system SHALL restore the backup file to the original binary path

### Requirement: Update Configuration

The system SHALL support configurable update behavior via `update.yaml` including channel selection (stable or beta), auto-check on startup, auto-install, and notification preferences.

#### Scenario: Auto-check on startup
- **GIVEN** `auto_check: true` is set in update config
- **WHEN** the gateway starts
- **THEN** the system SHALL check for a newer version on the configured channel

#### Scenario: Notification on update available
- **GIVEN** `notify: true` is set in update config
- **WHEN** a newer version is found
- **THEN** the system SHALL notify the user on the configured `notify_channel` (e.g., Telegram)

---

## 03 Health and Status

### 4.1 `opennavi health` Command

```bash
$ opennavi health

Profile: work
Status: running
Uptime: 3d 14h 22m

Adapters:
  ✓ Telegram (connected, bot: @navi_bot)
  ✓ Discord (connected, guild: My Server)
  ✗ Slack (disconnected: token expired)
  ✓ Webhook (listening on :8080)

Sessions:
  Active: 12
  Idle: 3
  Total today: 47

Agents:
  default (primary): running
  coder (sub): idle
  researcher (sub): idle
  reviewer (sub): idle

Memory:
  Short-term: 2,340 / 4,096 chars
  Long-term: 1,234 entries
  User memory: 2 users

Provider Health:
  Anthropic: ✓ (last check: 2m ago)
  OpenRouter: ✓ (last check: 2m ago)
```

### 4.2 `opennavi status` Command

```bash
$ opennavi status

Service: opennavi-gateway@work
PID: 12345
Uptime: 3d 14h 22m
Memory: 128MB / 512MB limit
CPU: 12% / 100% limit

Version: opennavi v1.2.0 (commit abc1234, built 2026-05-29)
Update available: v1.3.0

Config: ~/.opennavi/profiles/work/gateway.yaml
Sessions: ~/.opennavi/profiles/work/sessions/
Logs: ~/.opennavi/logs/opennavi.log

Adapters:
  Telegram: connected
  Discord: connected
  Slack: disconnected
  Webhook: listening :8080

Profiles:
  work: running (PID 12345)
  personal: stopped
  default: stopped
```

### 4.3 Gateway Health Endpoint

For Docker/systemd health checks:

```go
// cmd/opennavi-gateway/health.go

func healthHandler(w http.ResponseWriter, r *http.Request) {
    status := GatewayHealth{}

    // Check each adapter
    for name, adapter := range gateway.Adapters() {
        if adapter.IsConnected() {
            status.Adapters[name] = "connected"
        } else {
            status.Adapters[name] = "disconnected"
            status.Healthy = false
        }
    }

    // Check session count
    status.SessionCount = gateway.SessionManager.Count()

    // Check memory usage
    status.MemoryUsage = getProcessMemory()

    // Check provider health
    for name, provider := range gateway.Providers() {
        if provider.IsHealthy() {
            status.Providers[name] = "healthy"
        } else {
            status.Providers[name] = "unhealthy"
            status.Healthy = false
        }
    }

    w.Header().Set("Content-Type", "application/json")
    if status.Healthy {
        w.WriteHeader(http.StatusOK)
    } else {
        w.WriteHeader(http.StatusServiceUnavailable)
    }
    json.NewEncoder(w).Encode(status)
}

type GatewayHealth struct {
    Healthy     bool              `json:"healthy"`
    Adapters    map[string]string // name → "connected" | "disconnected"
    Providers   map[string]string // name → "healthy" | "unhealthy"
    SessionCount int              `json:"session_count"`
    MemoryUsage uint64            `json:"memory_usage_bytes"`
    Version     string            `json:"version"`
    Uptime      string            `json:"uptime"`
}
```

### Requirement: Health Command

The system SHALL provide an `opennavi health` command that reports profile status, uptime, adapter connection states, active and idle session counts, agent states, short-term and long-term memory usage, and provider health with last-check timestamps.

#### Scenario: Health check with disconnected adapter
- **GIVEN** an adapter is disconnected (e.g., Slack token expired)
- **WHEN** the user runs `opennavi health`
- **THEN** the system SHALL display the adapter with a `✗` indicator and the disconnection reason

### Requirement: Status Command

The system SHALL provide an `opennavi status` command that reports service PID, uptime, memory and CPU usage relative to limits, version information, update availability, config and session directory paths, adapter connection status, and the status of all profiles.

#### Scenario: Full status display
- **GIVEN** the gateway is running with multiple profiles
- **WHEN** the user runs `opennavi status`
- **THEN** the system SHALL display the running profile (with PID), stopped profiles, and the current version with update availability

### Requirement: Health Endpoint for Monitoring

The system SHALL expose a health HTTP endpoint that returns JSON with adapter connectivity, session count, memory usage, provider health, version, and uptime, returning HTTP 200 when all components are healthy and HTTP 503 when any component is unhealthy.

#### Scenario: Health endpoint unhealthy
- **GIVEN** a provider is unreachable or an adapter is disconnected
- **WHEN** the health endpoint is called
- **THEN** the system SHALL set `healthy: false` in the response and return HTTP 503 Service Unavailable

#### Scenario: Health endpoint healthy
- **GIVEN** all adapters are connected and all providers are healthy
- **WHEN** the health endpoint is called
- **THEN** the system SHALL set `healthy: true` and return HTTP 200 OK

---

## 04 Logging and Rotation

### 5.1 Built-in Log Rotation

```go
// internal/logging/rotation.go

type LogRotator struct {
    path     string
    maxSize  int64    // max file size in bytes
    maxFiles int      // max number of rotated files
    compress bool     // gzip rotated files
}

func (r *LogRotator) Write(p []byte) (n int, err error) {
    // Check if rotation needed
    info, err := os.Stat(r.path)
    if err == nil && info.Size() >= r.maxSize {
        if err := r.rotate(); err != nil {
            return 0, err
        }
    }

    f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return 0, err
    }
    defer f.Close()

    return f.Write(p)
}

func (r *LogRotator) rotate() error {
    // Rotate: opennavi.log → opennavi.log.1 → opennavi.log.2 → ...
    for i := r.maxFiles - 1; i >= 1; i-- {
        src := fmt.Sprintf("%s.%d", r.path, i)
        dst := fmt.Sprintf("%s.%d", r.path, i+1)
        os.Rename(src, dst)
    }

    // Compress oldest if enabled
    if r.compress {
        oldest := fmt.Sprintf("%s.%d", r.path, r.maxFiles)
        if _, err := os.Stat(oldest); err == nil {
            compressFile(oldest)
        }
    }

    // Create new file
    return os.WriteFile(r.path, []byte{}, 0644)
}
```

### 5.2 Log Config

```yaml
# ~/.opennavi/profiles/<name>/logging.yaml
logging:
  level: "info"
  format: "json"

  outputs:
    - type: "stdout"
    - type: "file"
      path: "~/.opennavi/logs/opennavi.log"
      max_size: "100MB"
      max_files: 10
      compress: true

  component_levels:
    gateway: "info"
    agent: "debug"
    plugin: "warn"
    provider: "info"
    session: "info"
    cron: "info"
    auth: "warn"
```

### Requirement: Built-in Log Rotation

The system SHALL provide built-in log rotation with configurable max file size, max number of rotated files, and optional gzip compression of the oldest rotated file.

#### Scenario: Log rotation triggers
- **GIVEN** a log file reaches its configured max size
- **WHEN** the next log write occurs
- **THEN** the system SHALL rotate the log file (opennavi.log becomes opennavi.log.1, existing files shift up, and the oldest is compressed if configured)

### Requirement: Configurable Log Outputs

The system SHALL support configurable logging outputs — stdout and file — with configurable log level, format (including JSON), and per-component log level overrides.

#### Scenario: Component-specific log levels
- **GIVEN** a logging config with `component_levels` defined
- **WHEN** the system logs a message for a specific component
- **THEN** the system SHALL use the component's configured log level instead of the global level

#### Scenario: JSON formatted logging
- **GIVEN** `format: "json"` is set in logging config
- **WHEN** the system writes a log entry
- **THEN** the system SHALL output the log entry as a JSON object

---

## 05 Multi-Instance Management

### 6.1 Port Conflict Prevention

```go
// Each profile's gateway checks for port conflicts
func (g *GatewayRunner) checkPortConflict(port int) error {
    // Check if port is in use
    conn, err := net.Dial("tcp", fmt.Sprintf(":%d", port))
    if err == nil {
        conn.Close()
        return fmt.Errorf("port %d is already in use", port)
    }

    // Check if another opennavi-gateway is running on this port
    // by scanning /proc or using lsof
    return nil
}
```

### 6.2 Profile-Aware Port Assignment

```yaml
# ~/.opennavi/profiles/<name>/gateway.yaml
gateway:
  http_port: 8080  # default, override per profile

# Or use auto-port assignment
gateway:
  http_port: 0  # let OS assign a free port
  port_file: "~/.opennavi/profiles/<name>/gateway.port"  # write assigned port
```

### 6.3 Managing Multiple Gateways

```bash
# List all running gateways
opennavi gateway list

# Start a gateway for a profile
opennavi gateway start --profile work

# Stop a gateway for a profile
opennavi gateway stop --profile work

# Restart a gateway for a profile
opennavi gateway restart --profile work

# Check all gateway statuses
opennavi gateway status
```

### Requirement: Port Conflict Prevention

The system SHALL detect and prevent port conflicts when starting a gateway instance by checking whether the target port is already in use, returning an error if a conflict is detected.

#### Scenario: Port in use by another process
- **GIVEN** a gateway instance is configured to use port 8080
- **WHEN** another process is already listening on port 8080
- **THEN** the system SHALL return an error indicating the port is already in use and abort the gateway start

### Requirement: Profile-Aware Port Assignment

The system SHALL support profile-aware port assignment, including auto-assignment of a free port (port 0) with the assigned port written to a configurable port file.

#### Scenario: Auto port assignment
- **GIVEN** a profile's gateway config sets `http_port: 0`
- **WHEN** the gateway starts
- **THEN** the system SHALL let the OS assign a free port and write the assigned port number to the configured `port_file`

### Requirement: Multi-Gateway Management Commands

The system SHALL provide `opennavi gateway list`, `opennavi gateway start`, `opennavi gateway stop`, `opennavi gateway restart`, and `opennavi gateway status` commands for managing multiple gateway instances by profile name.

#### Scenario: List running gateways
- **GIVEN** multiple gateway instances are running for different profiles
- **WHEN** the user runs `opennavi gateway list`
- **THEN** the system SHALL display all running gateways with their profile names and PIDs

---

## 06 Resource Management

### 7.1 Memory Budget

```yaml
# ~/.opennavi/profiles/<name>/resources.yaml
resources:
  memory:
    max_rss: 512MB      # max resident set size
    warn_threshold: 0.8  # warn at 80% usage
  cpu:
    quota: 100%          # CPU usage limit
    priority: "normal"   # nice value: -20 to 19
  goroutines:
    max_per_component:
      agent_loop: 1
      tool_executor: 4
      adapter: 1
      cron: 1
      plugin: 1
  sessions:
    max_concurrent: 50
    max_per_user: 10
```

### 7.2 Memory Monitoring

```go
// Monitor memory usage and take action
type MemoryMonitor struct {
    config *MemoryConfig
    logger *Logger
}

func (m *MemoryMonitor) Run(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            mem := getProcessMemory()

            if mem > m.config.MaxRSS {
                m.logger.Warn("memory limit exceeded", "usage", mem, "limit", m.config.MaxRSS)
                // Trigger emergency cleanup
                m.triggerCleanup()
            } else if mem > float64(m.config.MaxRSS)*m.config.WarnThreshold {
                m.logger.Warn("memory approaching limit", "usage", mem, "limit", m.config.MaxRSS)
            }
        }
    }
}

func (m *MemoryMonitor) triggerCleanup() {
    // 1. Close idle sessions
    // 2. Prune long-term memory
    // 3. Force GC
    runtime.GC()
}
```

### Requirement: Memory Budget Configuration

The system SHALL support configurable memory budgets per profile including max RSS, a warning threshold as a fraction of max RSS, CPU quota, CPU priority, and per-component goroutine limits.

#### Scenario: Per-profile resource limits
- **GIVEN** a profile's `resources.yaml` configures `max_rss: 512MB` and `warn_threshold: 0.8`
- **WHEN** the gateway for that profile starts
- **THEN** the system SHALL enforce the memory limit and warn at 80% of max RSS

### Requirement: Memory Monitoring and Cleanup

The system SHALL monitor memory usage every 30 seconds, logging a warning when memory exceeds the warning threshold and triggering emergency cleanup — closing idle sessions, pruning long-term memory, and forcing garbage collection — when the memory limit is exceeded.

#### Scenario: Memory approaching limit
- **GIVEN** memory usage exceeds 80% of the configured max RSS
- **WHEN** the 30-second monitor tick fires
- **THEN** the system SHALL log a warning with current usage and limit

#### Scenario: Memory limit exceeded
- **GIVEN** memory usage exceeds the configured max RSS
- **WHEN** the 30-second monitor tick fires
- **THEN** the system SHALL log a warning and trigger emergency cleanup by closing idle sessions, pruning long-term memory, and forcing garbage collection

### Requirement: Goroutine and Session Limits

The system SHALL support configurable max goroutines per component type (agent loop, tool executor, adapter, cron, plugin) and configurable max concurrent sessions and max sessions per user.

#### Scenario: Session limit enforcement
- **GIVEN** `max_concurrent: 50` is configured
- **WHEN** a new session would exceed the limit
- **THEN** the system SHALL reject the new session with an appropriate error

---

## 07 Backup and Restore

### 8.1 `opennavi backup` Command

```bash
# Create a backup
opennavi backup create --output ~/opennavi-backup-2026-05-29.tar.gz

# List backups
opennavi backup list

# Restore from backup
opennavi backup restore --input ~/opennavi-backup-2026-05-29.tar.gz

# Restore to a different profile
opennavi backup restore --input ~/opennavi-backup.tar.gz --profile new-work
```

### 8.2 Backup Contents

```go
// What gets backed up
type Backup struct {
    Version     string    `json:"version"`
    CreatedAt   time.Time `json:"created_at"`
    ProfileName string    `json:"profile_name"`
    Contents    []BackupEntry `json:"contents"`
}

type BackupEntry struct {
    Type     string // "config", "session", "memory", "persona"
    Path     string // relative to profile dir
    Content  []byte // for small files
    Compressed bool  // for large files
}

// Backup includes:
// - settings.yaml
// - agents.yaml
// - gateway.yaml
// - persona.md
// - all sessions/
// - memory/ (user + long_term)
// - skills/
// - themes/
// - keybindings.yaml
// - models.yaml
// EXCLUDES: logs/, sessions/ (too large, use separate export)
```

### 8.3 Session Export/Import

```bash
# Export sessions to JSON
opennavi session export --output sessions.json

# Import sessions from JSON
opennavi session import --input sessions.json

# Export a single session
opennavi session export <session-id> --output session.html
```

### Requirement: Backup and Restore Commands

The system SHALL provide `opennavi backup create`, `opennavi backup list`, and `opennavi backup restore` commands for creating, listing, and restoring profile backups.

#### Scenario: Create a backup
- **GIVEN** a profile is configured with active sessions and memory
- **WHEN** the user runs `opennavi backup create --output <path>`
- **THEN** the system SHALL create a compressed archive of the profile's config, sessions, memory, persona, skills, themes, and keybindings

### Requirement: Backup Contents

The system SHALL include settings, agents config, gateway config, persona, all sessions, memory (user and long-term), skills, themes, keybindings, and models in backups, and SHALL exclude logs and large session exports.

#### Scenario: Backup excludes logs
- **GIVEN** a backup is created
- **WHEN** the system compiles the backup archive
- **THEN** the system SHALL exclude the `logs/` directory from the backup

### Requirement: Cross-Profile Restore

The system SHALL support restoring a backup to a different profile name via the `--profile` flag.

#### Scenario: Restore to new profile
- **GIVEN** a backup created for the `work` profile
- **WHEN** the user runs `opennavi backup restore --input <backup> --profile new-work`
- **THEN** the system SHALL restore the backup contents to the `new-work` profile directory

### Requirement: Session Export and Import

The system SHALL provide `opennavi session export` and `opennavi session import` commands for bulk JSON export and import of sessions, and `opennavi session export <id>` for single-session HTML export.

#### Scenario: Export all sessions
- **GIVEN** a profile has active and archived sessions
- **WHEN** the user runs `opennavi session export --output sessions.json`
- **THEN** the system SHALL export all sessions as a JSON file

---

## 08 Data Privacy and Deletion

### 9.1 `opennavi data` Commands

```bash
# View data storage usage
opennavi data usage

# Delete all sessions for a profile
opennavi data delete sessions --profile work

# Delete long-term memory
opennavi data delete memory --profile work

# Delete all data for a profile (destructive)
opennavi data delete all --profile work --force

# Export all data (GDPR compliance)
opennavi data export --output ~/opennavi-data-export.tar.gz

# Delete a specific user's memory
opennavi data delete user --user alice --profile work
```

### 9.2 Data Usage Report

```bash
$ opennavi data usage

Profile: work

Config:     12KB (settings, agents, gateway, keybindings)
Sessions:   4.2MB (47 sessions, 1,234 messages)
Memory:     890KB (short-term, long-term, user)
Persona:    2KB
Skills:     15KB
Themes:     8KB
Total:      4.5MB

Long-term memory: 1,234 entries
User memories: 2 users
Active sessions: 12
```

### 9.3 Data Retention Policy

```yaml
# ~/.opennavi/profiles/<name>/retention.yaml
retention:
  sessions:
    max_age: "90d"          # auto-delete sessions older than 90 days
    max_count: 500          # keep at most 500 sessions
    export_before_delete: true  # export to JSON before deleting

  memory:
    long_term_max_entries: 10000
    prune_low_score_threshold: 0.1
    low_score_max_age: "180d"

  logs:
    max_age: "30d"
    max_size: "100MB"

  sessions_export:
    format: "json"            # json or html
    compress: true
    path: "~/.opennavi/exports/"
```

### Requirement: Data Deletion Commands

The system SHALL provide `opennavi data delete` commands for deleting sessions, long-term memory, user-specific memories, or all data for a profile, with a `--force` flag required for destructive all-data deletion.

#### Scenario: Delete all profile data
- **GIVEN** a profile exists with sessions, memory, and config
- **WHEN** the user runs `opennavi data delete all --profile work --force`
- **THEN** the system SHALL delete all data for that profile including sessions, memory, and config

#### Scenario: Delete specific user memory
- **GIVEN** a profile has memory entries for user `alice`
- **WHEN** the user runs `opennavi data delete user --user alice --profile work`
- **THEN** the system SHALL delete only the memory entries associated with that user

### Requirement: Data Usage Report

The system SHALL provide an `opennavi data usage` command that reports storage usage by category — config, sessions, memory, persona, skills, themes — with entry counts for long-term memory, user memories, and active sessions.

#### Scenario: Storage usage display
- **GIVEN** a profile has stored data
- **WHEN** the user runs `opennavi data usage`
- **THEN** the system SHALL display the size and item count for each data category and the total storage used

### Requirement: GDPR Data Export

The system SHALL provide `opennavi data export` for GDPR-compliant export of all profile data to a compressed archive.

#### Scenario: Full data export
- **GIVEN** a profile is configured with sessions, memory, and config
- **WHEN** the user runs `opennavi data export --output <path>`
- **THEN** the system SHALL export all profile data including sessions, memory, config, persona, skills, and themes to a compressed archive

### Requirement: Configurable Data Retention Policy

The system SHALL support configurable data retention policies for sessions (max age, max count, optional export before delete), memory (max entries, prune score threshold, low-score max age), and logs (max age, max size).

#### Scenario: Auto-delete old sessions
- **GIVEN** a retention policy with `sessions.max_age: "90d"`
- **WHEN** the retention job runs
- **THEN** the system SHALL delete sessions older than 90 days, exporting them to JSON first if configured

---

## 09 Edge Case Handling

### 10.1 Provider Failures

```go
// Provider health check and failover
func (p *ProviderManager) Stream(ctx context.Context, messages []llm.Message, tools []llm.Tool) (<-chan llm.StreamEvent, error) {
    // Try primary provider
    events, err := p.primary.Stream(ctx, messages, tools)
    if err == nil {
        return events, nil
    }

    // Check if it's a retryable error
    if !isRetryable(err) {
        return nil, err
    }

    // Retry primary
    events, err = p.primary.Stream(ctx, messages, tools)
    if err == nil {
        return events, nil
    }

    // Fall back to secondary provider
    if p.fallback != nil {
        log.Printf("primary provider failed, falling back to %s", p.fallback.Name())
        return p.fallback.Stream(ctx, messages, tools)
    }

    return nil, err
}

// Provider health monitoring
type ProviderHealth struct {
    Name       string
    Healthy    bool
    LastCheck  time.Time
    Errors     int
    LastError  string
}

func (h *HealthMonitor) CheckProviders() {
    for name, provider := range h.providers {
        err := provider.Ping()
        if err != nil {
            h.providers[name] = ProviderHealth{
                Name:      name,
                Healthy:   false,
                LastCheck: time.Now(),
                Errors:    h.providers[name].Errors + 1,
                LastError: err.Error(),
            }
        } else {
            h.providers[name] = ProviderHealth{
                Name:      name,
                Healthy:   true,
                LastCheck: time.Now(),
                Errors:    0,
            }
        }
    }
}
```

### 10.2 Session Recovery

```go
// Handle corrupted session files
func (s *SessionManager) LoadSession(id string) (*ChatSession, error) {
    path := s.sessionPath(id)

    // Try loading normally first
    session, err := s.loadFromJSONL(path)
    if err == nil {
        return session, nil
    }

    // If corrupted, try backup
    backupPath := path + ".bak"
    if _, err := os.Stat(backupPath); err == nil {
        log.Printf("session %s corrupted, loading from backup", id)
        session, err = s.loadFromJSONL(backupPath)
        if err == nil {
            // Copy backup back to original
            data, _ := os.ReadFile(backupPath)
            os.WriteFile(path, data, 0644)
            return session, nil
        }
    }

    // If no backup, try last known good session
    lastGood := s.findLastGoodSession(id)
    if lastGood != nil {
        log.Printf("session %s corrupted, loading last known good", id)
        return lastGood, nil
    }

    return nil, fmt.Errorf("session %s is corrupted and no backup available", id)
}
```

---

## 10 Deployment Checklist

### 11.1 Initial Setup

```bash
# 1. Install opennavi
opennavi self-update

# 2. Create a profile
opennavi profiles create work "Work Profile"

# 3. Configure providers
# Edit ~/.opennavi/profiles/work/settings.yaml
# Add API keys via env vars or secret store

# 4. Configure gateway
# Edit ~/.opennavi/profiles/work/gateway.yaml
# Set up adapters (Telegram, Discord, etc.)

# 5. Set up persona
# Edit ~/.opennavi/profiles/work/persona.md

# 6. Configure agents
# Edit ~/.opennavi/profiles/work/agents.yaml

# 7. Install as service
# systemd:
sudo cp opennavi-gateway@.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable opennavi-gateway@work
sudo systemctl start opennavi-gateway@work

# docker:
docker compose --profile default up -d
```

### 11.2 Ongoing Maintenance

```bash
# Check status
opennavi status
opennavi health

# Update
opennavi self-update

# Backup
opennavi backup create --output ~/opennavi-backup-$(date +%Y-%m-%d).tar.gz

# Monitor logs
journalctl -u opennavi-gateway@work -f
# or
tail -f ~/.opennavi/logs/opennavi.log

# Data management
opennavi data usage
opennavi data delete sessions --profile work --older-than 90d
```

---

## 11 Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **systemd for Linux** | Standard, reliable, built-in restart |
| **launchd for macOS** | Native macOS service management |
| **Docker for containerized** | Isolation, easy setup, reproducible |
| **`opennavi self-update`** | Built-in update mechanism, checksum verification |
| **Profile-aware ports** | Multiple gateways on same machine without conflicts |
| **Resource limits per profile** | Prevent one profile from consuming all resources |
| **Health endpoint** | Docker/systemd health checks, monitoring |
| **Backup/restore** | Data safety, migration between machines |
| **Data deletion commands** | Privacy, GDPR compliance, storage management |
| **Configurable retention** | Auto-cleanup of old data |
| **Provider failover** | Availability when primary provider is down |
| **Session recovery** | Handle corrupted session files gracefully |


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S080500 | 0.1.0 | Migrated from docs/spec/ to spec-tree |