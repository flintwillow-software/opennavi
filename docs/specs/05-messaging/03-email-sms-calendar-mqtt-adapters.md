---
status: draft
created: '2026-08-12'
updated: '2026-08-12'
tags:
  - spec
  - messaging
  - adapters
priority: medium
spec-refs: [050300, 050301, 050302, 050303, 050304]
---

# Plan: Extended Platform Adapters — Email, SMS, Calendar, MQTT, Zendesk

**Scope:** Additional platform adapters identified in [Gap 6](../../gaps/GAPS.md#gap-6-platform-adapters--email-zendesk-mqtt-sms-calendar): Email (IMAP/SMTP), Zendesk (REST/webhooks), MQTT (IoT), SMS (Twilio/SNS), and Calendar (CalDAV/Google Calendar).

> **Part of:** [Messaging Gateway](./README.md)

## 00 Email Adapter (IMAP/SMTP)

### 1.1 Protocol Details

- **Inbound:** IMAP with IDLE extension for real-time push (or polling fallback every 30s). Connects to the INBOX, watches for new messages via `EXAMINE` + `IDLE`. Uses TLS on port 993.
- **Outbound:** SMTP with STARTTLS (port 587) or implicit TLS (port 465). Supports auth via LOGIN/PLAIN/CRAM-MD5.
- **Auth:** Application-specific password or OAuth 2.0 (XOAUTH2) for Gmail/Outlook.
- **Real-time vs polling:** IDLE gives push-like behavior; if unavailable, fall back to polling every 30--60s with `SEARCH UNSEEN`.

### 1.2 Message Format Normalization

```go
type EmailParser struct{}

func (p *EmailParser) Parse(payload []byte) (*ParsedMessage, error) {
    msg := &mail.Message{}
    if err := mail.ReadMessage(bytes.NewReader(payload)); err != nil {
        return nil, err
    }

    subject, _ := msg.Header.Get("Subject")
    from, _ := msg.Header.Get("From")
    body, _ := io.ReadAll(msg.Body)

    // Parse MIME multipart (text/plain + text/html + attachments)
    mediaType, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
    var textBody string
    if strings.HasPrefix(mediaType, "multipart/") {
        textBody = extractTextFromMIME(msg.Body, params["boundary"])
    } else {
        textBody = string(body)
    }

    return &ParsedMessage{
        UserID:    from,            // "user@example.com"
        Text:      textBody,
        ChannelID: subject,         // thread identifier
        Metadata: map[string]any{
            "subject": subject,
            "to":      msg.Header.Get("To"),
            "date":    msg.Header.Get("Date"),
            "message_id": msg.Header.Get("Message-ID"),
            "in_reply_to": msg.Header.Get("In-Reply-To"),
        },
    }, nil
}

// Outbound: Message → SMTP email
func (d *EmailDelivery) SendText(key SessionKey, text string) error {
    m := gomail.NewMessage()
    m.SetHeader("From", d.fromAddr)
    m.SetHeader("To", key.UserID)
    m.SetHeader("Subject", formatSubject(key))
    m.SetBody("text/plain", text)

    return d.dialer.DialAndSend(m)
}
```

### 1.3 Error Handling & Retry

- **IMAP connection drops:** Reconnect with exponential backoff (1s, 2s, 4s, 8s, max 60s). Re-EXAMINE the INBOX after reconnect.
- **SMTP transient failures** (4xx): Retry up to 3 times with backoff. Persistent 5xx: return error, log, and notify admin.
- **Rate limiting:** SMTP providers enforce per-hour send limits. Track send count and throttle to 80% of provider limit.
- **MIME parsing errors:** Return partial parse with available fields; log the raw payload for debugging.

### 1.4 Configuration

```yaml
# gateway.yaml snippet
adapters:
  email:
    enabled: true
    imap:
      host: "imap.gmail.com"
      port: 993
      tls: true
      username: "navi@example.com"
      password: "${EMAIL_APP_PASSWORD}"
      poll_interval: 30s       # fallback polling interval
      idle: true               # use IDLE extension
    smtp:
      host: "smtp.gmail.com"
      port: 587
      starttls: true
      username: "navi@example.com"
      password: "${EMAIL_APP_PASSWORD}"
      from_addr: "navi@example.com"
    folders:
      - "INBOX"
      - "INBOX/Automations"    # watch additional folders
```

### 1.5 Implementation Notes

- Use `github.com/emersion/go-imap` for IMAP client and `github.com/emersion/go-imap-idle` for the IDLE extension.
- Use `github.com/go-gomail/gomail` for SMTP. It handles MIME, multipart, and attachments.
- MIME parsing: `mime/multipart` for boundary splitting, `net/textproto` and `golang.org/x/net/html` for HTML-to-text fallback.
- Attachments: extract `Content-Disposition: attachment` parts and populate `ImageRef` / `FileRef` on the `MessageContent`.
- Track `Message-ID` / `In-Reply-To` headers to maintain thread continuity across the gateway.

### Requirement: IMAP Inbound with IDLE

The system SHALL receive email via IMAP using the IDLE extension for real-time push notification, falling back to polling every 30-60 seconds when IDLE is unavailable.

#### Scenario: IMAP connected with IDLE
- GIVEN an email adapter configured with `idle: true`
- WHEN the adapter starts
- THEN the system SHALL connect to the IMAP server on port 993 with TLS
- AND use the IDLE extension to watch the INBOX for new messages in real time

#### Scenario: IDLE unavailable, polling fallback
- GIVEN the IMAP server does not support the IDLE extension
- WHEN the adapter starts
- THEN the system SHALL fall back to polling with `SEARCH UNSEEN` every 30-60 seconds

### Requirement: SMTP Outbound Delivery

The system SHALL send outgoing email via SMTP with STARTTLS on port 587 or implicit TLS on port 465, supporting application-specific password or OAuth 2.0 authentication.

#### Scenario: SMTP sends email
- GIVEN a message targets email delivery
- WHEN the email delivery adapter sends it
- THEN the system SHALL connect to the SMTP server
- AND send a formatted email with the message content as the body

#### Scenario: SMTP authentication
- GIVEN the SMTP server requires authentication
- WHEN the adapter connects
- THEN the system SHALL authenticate using the configured username and password or OAuth 2.0 token

### Requirement: Email Message Normalization

The system SHALL parse inbound email into a `ParsedMessage` with the sender's email address as the user ID, the message body as text, the subject as the channel ID, and headers as metadata.

#### Scenario: Email parsed into ParsedMessage
- GIVEN an email message is received
- WHEN the `EmailParser` parses it
- THEN the system SHALL extract the From header as the user ID, the subject as the channel ID, and the body as text
- AND populate metadata with subject, to, date, message-id, and in-reply-to headers

#### Scenario: MIME multipart parsed
- GIVEN an email with a MIME multipart content type
- WHEN the `EmailParser` parses it
- THEN the system SHALL extract the text/plain part as the body
- AND extract attachments as `FileRef` entries on the message content

### Requirement: Outbound Email Composition

The system SHALL compose outbound email via `EmailDelivery.SendText`, setting the From address, the recipient as the target user, a formatted subject, and the message text as the plain-text body.

#### Scenario: Email composed with formatted subject
- GIVEN a message targets email delivery
- WHEN the email delivery adapter composes the message
- THEN the system SHALL set the From header to the configured sender address
- AND set the To header to the target user's email address
- AND set the Subject header to the formatted subject
- AND set the message text as the body

#### Scenario: Plain text body sent
- GIVEN an outbound email is being sent
- WHEN `SendText` runs
- THEN the system SHALL send the email with a text/plain body via the SMTP dialer

### Requirement: Email Connection Resilience

The system SHALL handle IMAP connection drops by reconnecting with exponential backoff (1s, 2s, 4s, 8s, max 60s), re-examining the INBOX after each reconnect.

#### Scenario: IMAP disconnect reconnects
- GIVEN the IMAP connection drops
- WHEN the adapter detects the disconnect
- THEN the system SHALL reconnect with exponential backoff starting at 1 second
- AND re-examine the INBOX after reconnecting

#### Scenario: Backoff capped at 60 seconds
- GIVEN repeated IMAP connection failures
- WHEN the adapter retries
- THEN the backoff interval SHALL be capped at a maximum of 60 seconds

### Requirement: SMTP Retry and Rate Limiting

The system SHALL retry SMTP transient failures (4xx) up to 3 times with backoff, log persistent failures (5xx), and throttle outbound sends to 80% of the provider's per-hour send limit.

#### Scenario: SMTP transient failure retried
- GIVEN an SMTP send returns a 4xx transient error
- WHEN the delivery adapter sends
- THEN the system SHALL retry up to 3 times with exponential backoff

#### Scenario: SMTP persistent failure logged
- GIVEN an SMTP send returns a 5xx persistent error
- WHEN the delivery adapter sends
- THEN the system SHALL log the error and notify an admin

### Requirement: Email Adapter Configuration

The system SHALL configure the email adapter with IMAP server settings (host, port, TLS, username, password, poll interval, IDLE flag), SMTP settings (host, port, STARTTLS, username, password, from address), and watched folders.

#### Scenario: Configuration loaded from gateway.yaml
- GIVEN an email adapter configuration exists in gateway.yaml
- WHEN the adapter initializes
- THEN the system SHALL load the IMAP and SMTP connection settings
- AND watch the configured folders for new messages

#### Scenario: Envvar expansion for secrets
- GIVEN the email password is specified as `${EMAIL_APP_PASSWORD}`
- WHEN the adapter initializes
- THEN the system SHALL expand the environment variable and use the resolved value

### Requirement: Multi-Folder Monitoring

The system SHALL monitor every folder listed in the email configuration, watching the INBOX and any additional folders for new messages.

#### Scenario: INBOX monitored
- GIVEN the email adapter is configured with the INBOX folder
- WHEN the adapter runs
- THEN the system SHALL watch the INBOX for new messages

#### Scenario: Additional folders monitored
- GIVEN the configuration includes an additional folder such as `INBOX/Automations`
- WHEN the adapter runs
- THEN the system SHALL watch that folder for new messages as well

### Requirement: Email Thread Continuity

The system SHALL track the `Message-ID` and `In-Reply-To` headers of inbound and outbound email to maintain thread continuity across the gateway.

#### Scenario: Inbound reply correlated to thread
- GIVEN an inbound email has an `In-Reply-To` header
- WHEN the `EmailParser` parses it
- THEN the system SHALL record the referenced message ID in metadata for thread correlation

#### Scenario: Outbound replies reference parent
- GIVEN the gateway replies to an email thread
- WHEN the email delivery adapter sends the reply
- THEN the system SHALL set the `In-Reply-To` header to the parent message's ID

### Requirement: Email Attachment Extraction

The system SHALL extract MIME attachments from inbound email and populate `ImageRef` and `FileRef` entries on the message content.

#### Scenario: Attachment extracted as FileRef
- GIVEN an inbound email has a part with `Content-Disposition: attachment`
- WHEN the `EmailParser` parses it
- THEN the system SHALL extract the attachment
- AND populate a `FileRef` entry with its name, data, and MIME type

#### Scenario: Image attachment extracted as ImageRef
- GIVEN an inbound email has an image attachment
- WHEN the `EmailParser` parses it
- THEN the system SHALL populate an `ImageRef` entry with the image data and MIME type

---

## 01 Zendesk Adapter

### 2.1 Protocol Details

- **Inbound:** Webhook receiver (Zendesk triggers → POST to `/gateway/zendesk`). Also supports polling via the Search API for ticket updates.
- **Outbound:** REST API at `https://{subdomain}.zendesk.com/api/v2/`. Uses basic auth (email + API token) or OAuth 2.0.
- **Real-time vs polling:** Webhooks are real-time; polling is fallback every 60s for tickets updated since last check.
- **Auth:** API token passed as `{email}/token:{api_token}` in the `Authorization` header (Basic auth).

### 2.2 Message Format Normalization

```go
type ZendeskParser struct{}

func (p *ZendeskParser) Parse(payload []byte) (*ParsedMessage, error) {
    var event struct {
        TicketID    int64  `json:"ticket_id"`
        TicketTitle string `json:"ticket_title"`
        Comment     string `json:"comment"`
        Requester   string `json:"requester_email"`
        Assignee    string `json:"assignee_email"`
        Status      string `json:"ticket_status"`
        Priority    string `json:"ticket_priority"`
        Url         string `json:"ticket_url"`
    }

    if err := json.Unmarshal(payload, &event); err != nil {
        return nil, err
    }

    return &ParsedMessage{
        UserID:    event.Requester,
        Text:      fmt.Sprintf("[%s] %s\n\n%s", event.Status, event.TicketTitle, event.Comment),
        ChannelID: fmt.Sprintf("ticket-%d", event.TicketID),
        Metadata: map[string]any{
            "ticket_id":    event.TicketID,
            "ticket_url":   event.Url,
            "status":       event.Status,
            "priority":     event.Priority,
            "assignee":     event.Assignee,
            "platform":     "zendesk",
        },
    }, nil
}

// Outbound: Message → Zendesk ticket comment
func (d *ZendeskDelivery) SendText(key SessionKey, text string) error {
    ticketID := extractTicketID(key.ChannelID)
    req := ZendeskRequest{
        Method: "PUT",
        URL:    fmt.Sprintf("%s/api/v2/tickets/%d.json", d.baseURL, ticketID),
        Body: map[string]any{
            "ticket": map[string]any{
                "comment": map[string]any{
                    "body": text,
                    "author_id": d.agentID,
                },
            },
        },
    }
    return d.do(req)
}
```

### 2.3 Error Handling & Retry

- **Rate limiting:** Zendesk returns `429 Too Many Requests` with a `Retry-After` header. Respect and retry after the specified delay.
- **Transient errors** (5xx): Retry up to 3 times with exponential backoff (1s, 4s, 16s).
- **Ticket not found** (404): Log warning and skip; do not retry.
- **Webhook signature verification:** Validate `X-Zendesk-Webhook-Signature` HMAC with the shared secret.

### 2.4 Configuration

```yaml
adapters:
  zendesk:
    enabled: true
    subdomain: "mycompany"
    email: "navi@mycompany.com"
    api_token: "${ZENDESK_API_TOKEN}"
    webhook_secret: "${ZENDESK_WEBHOOK_SECRET}"
    webhook_path: "/gateway/zendesk"
    poll_interval: 60s
    filters:
      status: ["open", "pending"]
      priority: ["urgent", "high"]
    agent_id: 12345678  # Zendesk agent ID for comment authorship
```

### 2.5 Implementation Notes

- Use `net/http` directly — no SDK needed. The REST API is straightforward.
- Webhook receiver: mount on the gateway's HTTP server (same as the webhook adapter) at `/gateway/zendesk`.
- Ticket threading: map `ChannelID = "ticket-{id}"` to preserve session continuity per ticket.
- For initial setup, register a Zendesk trigger: "Notify by HTTP" → POST ticket data to the gateway webhook URL.
- Extend the `ZendeskDelivery` to support status transitions (`ticket.status = "solved"`) via `SendNotification`.

### Requirement: Zendesk Webhook Inbound

The system SHALL receive Zendesk ticket updates via webhook POST requests to `/gateway/zendesk`, with polling fallback every 60 seconds via the Search API.

#### Scenario: Zendesk webhook received
- GIVEN a Zendesk trigger sends a POST to `/gateway/zendesk`
- WHEN the Zendesk adapter processes it
- THEN the system SHALL parse the ticket ID, title, comment, requester, assignee, status, and priority

#### Scenario: Polling fallback for missed webhooks
- GIVEN webhook delivery is unreliable
- WHEN the adapter polls every 60 seconds
- THEN the system SHALL query the Search API for tickets updated since the last check

### Requirement: Zendesk REST Outbound

The system SHALL send ticket comments and status updates via the Zendesk REST API at `https://{subdomain}.zendesk.com/api/v2/`, authenticating with an API token passed as Basic auth.

#### Scenario: Ticket comment added via API
- GIVEN a message targets a Zendesk ticket
- WHEN the Zendesk delivery adapter sends it
- THEN the system SHALL post the message as a comment on the ticket
- AND set the author ID to the configured agent ID

#### Scenario: REST API authentication
- GIVEN the Zendesk adapter is configured with an API token
- WHEN it makes an API request
- THEN the system SHALL authenticate using Basic auth with `{email}/token:{api_token}`

### Requirement: Zendesk Ticket Normalization

The system SHALL normalize Zendesk ticket webhook payloads into `ParsedMessage` with the requester email as the user ID, a formatted ticket summary as the text, and the ticket ID as the channel ID.

#### Scenario: Ticket update normalized
- GIVEN a Zendesk webhook payload with ticket data
- WHEN the `ZendeskParser` parses it
- THEN the system SHALL set the user ID to the requester email and the channel ID to `ticket-{id}`
- AND populate metadata with ticket ID, URL, status, priority, and assignee

#### Scenario: Ticket comment sent as reply
- GIVEN an agent generates a response to a ticket
- WHEN the Zendesk delivery adapter sends it
- THEN the system SHALL extract the ticket ID from the channel ID
- AND post the response as a comment on the matching ticket

### Requirement: Zendesk Ticket Text Format

The system SHALL format normalized Zendesk ticket messages with a bracketed status, the ticket title, and the comment body, and populate ticket metadata.

#### Scenario: Text formatted with ticket context
- GIVEN a Zendesk webhook payload with status, title, and comment
- WHEN the `ZendeskParser` parses it
- THEN the system SHALL format the text as `[status] title` followed by the comment

#### Scenario: Ticket metadata populated
- GIVEN a Zendesk webhook payload
- WHEN the `ZendeskParser` parses it
- THEN the system SHALL populate metadata with the ticket ID, ticket URL, status, priority, assignee, and platform

### Requirement: Zendesk Rate Limit and Retry

The system SHALL respect Zendesk's rate limit by honoring the `Retry-After` header on 429 responses, retry transient 5xx errors up to 3 times with exponential backoff, and log 404 ticket-not-found errors without retry.

#### Scenario: Rate limited and retried
- GIVEN Zendesk returns a 429 with a Retry-After header
- WHEN the adapter receives the response
- THEN the system SHALL wait for the specified delay before retrying

#### Scenario: Transient error retried
- GIVEN Zendesk returns a 5xx server error
- WHEN the adapter receives the response
- THEN the system SHALL retry up to 3 times with exponential backoff (1s, 4s, 16s)

#### Scenario: Ticket not found logged
- GIVEN Zendesk returns a 404 for a ticket
- WHEN the adapter receives the response
- THEN the system SHALL log a warning and skip the message without retrying

### Requirement: Zendesk Webhook Signature Verification

The system SHALL verify the authenticity of incoming Zendesk webhooks by validating the `X-Zendesk-Webhook-Signature` HMAC against the shared secret.

#### Scenario: Valid signature passes
- GIVEN a webhook request with a valid HMAC signature
- WHEN the adapter processes the request
- THEN the system SHALL verify the signature and process the payload

#### Scenario: Invalid signature rejected
- GIVEN a webhook request with an invalid or missing signature
- WHEN the adapter processes the request
- THEN the system SHALL reject the request

### Requirement: Zendesk Adapter Configuration

The system SHALL configure the Zendesk adapter with subdomain, email, API token, webhook secret, webhook path, poll interval, status and priority filters, and agent ID for comment authorship.

#### Scenario: Configuration loaded from gateway.yaml
- GIVEN a Zendesk adapter configuration exists in gateway.yaml
- WHEN the adapter initializes
- THEN the system SHALL load the subdomain, credentials, and filtering settings

#### Scenario: Status and priority filters
- GIVEN filters are configured for status `["open", "pending"]` and priority `["urgent", "high"]`
- WHEN the adapter polls for updates
- THEN the system SHALL only process tickets matching the filters

### Requirement: Zendesk Webhook Path Mounting

The system SHALL mount the Zendesk webhook receiver on the gateway's HTTP server at the configured webhook path (default `/gateway/zendesk`).

#### Scenario: Receiver mounted at configured path
- GIVEN the gateway HTTP server is running
- WHEN the Zendesk adapter initializes
- THEN the system SHALL mount the webhook receiver at the configured webhook path

#### Scenario: Webhook POST handled at path
- GIVEN a Zendesk trigger sends a POST to the webhook path
- WHEN the request arrives
- THEN the system SHALL parse the ticket payload and forward it to the handler

### Requirement: Ticket Threading via Channel ID

The system SHALL map each Zendesk ticket to a channel ID of `ticket-{id}` to preserve session continuity per ticket.

#### Scenario: Ticket session continuity
- GIVEN a ticket update arrives for a known ticket
- WHEN the `ZendeskParser` parses it
- THEN the system SHALL use `ticket-{id}` as the channel ID so follow-ups join the same session

#### Scenario: Comments routed to correct ticket
- GIVEN an agent responds to a ticket thread
- WHEN the Zendesk delivery adapter sends it
- THEN the system SHALL derive the ticket ID from the channel ID to post the comment

### Requirement: Zendesk Status Transitions

The system SHALL support ticket status transitions (e.g., setting a ticket to `solved`) via the notification delivery path, extending ticket delivery beyond comments.

#### Scenario: Ticket status updated via SendNotification
- GIVEN a notification is targeted at a Zendesk ticket
- WHEN `SendNotification` runs
- THEN the system SHALL update the ticket status (e.g., to `solved`) via the REST API

---

## 02 MQTT Adapter

### 3.1 Protocol Details

- **Inbound:** Subscribe to MQTT topics (e.g., `home/+/status`, `devices/#`). Receives messages from brokers (Mosquitto, HiveMQ, AWS IoT Core).
- **Outbound:** Publish to MQTT topics for device commands (e.g., `home/livingroom/light/set`).
- **Auth:** Username/password, TLS client certificates, or AWS IoT SigV4.
- **Real-time vs polling:** MQTT is inherently real-time via persistent TCP/WebSocket connection with QoS levels.
- **QoS levels:** QoS 0 (fire-and-forget) for telemetry, QoS 1 (at-least-once) for commands, QoS 2 (exactly-once) for critical messages.

### 3.2 Message Format Normalization

```go
type MQTTParser struct{}

func (p *MQTTParser) Parse(payload []byte) (*ParsedMessage, error) {
    // Payload is the raw MQTT message body (JSON or plain text)
    var msg struct {
        Device  string `json:"device"`
        Sensor  string `json:"sensor"`
        Value   any    `json:"value"`
        Unit    string `json:"unit"`
        Command string `json:"command"`
    }

    // Try JSON; fall back to plain text
    var text string
    if err := json.Unmarshal(payload, &msg); err != nil {
        text = string(payload)
    } else {
        text = fmt.Sprintf("Device %s sensor %s = %v %s", msg.Device, msg.Sensor, msg.Value, msg.Unit)
    }

    return &ParsedMessage{
        UserID:    "mqtt-broker",       // system identity
        Text:      text,
        ChannelID: "mqtt",              // single channel for all MQTT events
        IsBot:     true,                // device messages are machine-generated
        Metadata: map[string]any{
            "topic":      msg.Device,
            "sensor":     msg.Sensor,
            "value":      msg.Value,
            "raw_payload": string(payload),
        },
    }, nil
}

// Outbound: Message → MQTT publish
func (d *MQTTDelivery) SendText(key SessionKey, text string) error {
    topic := key.ChannelID // e.g., "home/livingroom/light/set"
    token := d.client.Publish(topic, d.qos, false, []byte(text))
    return token.WaitTimeout(5 * time.Second)
}
```

### 3.3 Error Handling & Retry

- **Broker disconnection:** The Paho client's connection handler automatically reconnects. Register `OnConnectionLost` and `OnConnect` callbacks to re-subscribe topics.
- **Publish failures:** Retry QoS 1/2 publishes up to 3 times. QoS 0 failures are silently dropped (fire-and-forget).
- **Will message:** Set a Last Will Testament (LWT) so the broker publishes a "device offline" message on unexpected disconnect.
- **Topic validation:** Reject topic strings with wildcards (`+`, `#`) in outgoing publishes; only allow concrete topic paths.

### 3.4 Configuration

```yaml
adapters:
  mqtt:
    enabled: true
    broker: "tcp://localhost:1883"
    client_id: "opennavi-gateway"
    username: "${MQTT_USERNAME}"
    password: "${MQTT_PASSWORD}"
    tls:
      enabled: false
      ca_cert: "/etc/opennavi/mqtt-ca.pem"
    qos: 1                         # default publish QoS
    subscribe_topics:
      - "home/+/status"
      - "home/+/telemetry"
      - "devices/#"
    publish_prefix: "home"         # prefix for outgoing command topics
    will:
      topic: "home/gateway/status"
      message: "offline"
      qos: 1
```

### 3.5 Implementation Notes

- Use `github.com/eclipse/paho.mqtt.golang` — the de facto Go MQTT client.
- Topic routing: map MQTT topic patterns to `ChannelID` for session routing. E.g., `home/livingroom/temp` → `ChannelID: "home/livingroom"`.
- For Home Assistant integration, parse Home Assistant MQTT discovery messages (`homeassistant/status`, `homeassistant/+/+/config`) to auto-register devices.
- Incoming device telemetry can be routed to a dedicated "IoT" session or a specific automation agent based on topic prefix.
- Consider MQTT 5.0 features: session expiry, message expiry, user properties for richer metadata.

### Requirement: MQTT Topic Subscription and Publish

The system SHALL subscribe to configured MQTT topics for inbound messages and publish to derived topics for outbound device commands, using a persistent TCP or WebSocket connection.

#### Scenario: Subscribed topics receive messages
- GIVEN an MQTT adapter is configured with subscribe topics
- WHEN the adapter starts
- THEN the system SHALL subscribe to each configured topic pattern
- AND forward each received message to the handler as a `RawMessage`

#### Scenario: Outbound publish to topic
- GIVEN a message targets an MQTT topic as the channel ID
- WHEN the MQTT delivery adapter sends it
- THEN the system SHALL publish the message content to the topic

### Requirement: MQTT QoS Levels

The system SHALL support MQTT QoS levels: QoS 0 for fire-and-forget telemetry, QoS 1 for at-least-once commands, and QoS 2 for exactly-once critical messages, with a configurable default publish QoS.

#### Scenario: QoS level configured per adapter
- GIVEN the adapter has a default QoS configured
- WHEN the adapter publishes a message
- THEN the system SHALL use the configured QoS level

#### Scenario: QoS 1 publishes retried on failure
- GIVEN a QoS 1 or 2 publish fails
- WHEN the adapter detects the failure
- THEN the system SHALL retry the publish up to 3 times

### Requirement: MQTT Payload Normalization

The system SHALL normalize MQTT message payloads into `ParsedMessage` with a system identity, the parsed text, and metadata including the topic, sensor, and raw payload.

#### Scenario: JSON payload parsed
- GIVEN an MQTT message with a JSON payload containing device, sensor, and value fields
- WHEN the `MQTTParser` parses it
- THEN the system SHALL build a formatted text string from the fields
- AND populate metadata with the topic, sensor, value, and raw payload

#### Scenario: Plain text payload fallback
- GIVEN an MQTT message with a non-JSON payload
- WHEN the `MQTTParser` parses it
- THEN the system SHALL use the raw payload as the text

#### Scenario: Machine origin flagged
- GIVEN an MQTT message is received
- WHEN the `MQTTParser` parses it
- THEN the system SHALL set `IsBot` to true

### Requirement: MQTT Topic-to-Channel Routing

The system SHALL map MQTT topic patterns to channel IDs for session routing, e.g., `home/livingroom/temp` maps to `ChannelID: "home/livingroom"`.

#### Scenario: Topic mapped to channel ID
- GIVEN an MQTT message arrives on a topic such as `home/livingroom/temp`
- WHEN the adapter processes it
- THEN the system SHALL derive the channel ID from the topic path
- AND route the message to the matching session

#### Scenario: Telemetry routed by topic prefix
- GIVEN device telemetry arrives on a topic with a recognized prefix
- WHEN the adapter processes it
- THEN the system SHALL route the telemetry to the IoT session or automation agent matching the prefix

### Requirement: MQTT Reconnection and Resubscribe

The system SHALL handle broker disconnection by automatically reconnecting via the Paho client's built-in handler, re-subscribing to all topics on reconnect, and setting a Last Will Testament for offline notification.

#### Scenario: Auto-reconnect on disconnect
- GIVEN the MQTT broker connection drops
- WHEN the Paho client detects the disconnect
- THEN the system SHALL automatically reconnect
- AND re-subscribe to all configured topics via the `OnConnect` callback

#### Scenario: Last Will Testament published
- GIVEN the adapter is configured with a will topic and message
- WHEN the adapter disconnects unexpectedly
- THEN the broker SHALL publish the will message to the configured topic

### Requirement: MQTT Topic Validation

The system SHALL validate outgoing publish topics by rejecting topic strings containing wildcard characters (`+`, `#`), allowing only concrete topic paths.

#### Scenario: Concrete topic published
- GIVEN an outgoing publish topic is a concrete path like `home/livingroom/light/set`
- WHEN the adapter validates the topic
- THEN the system SHALL allow the publish

#### Scenario: Wildcard topic rejected
- GIVEN an outgoing publish topic contains a wildcard (`+` or `#`)
- WHEN the adapter validates the topic
- THEN the system SHALL reject the publish and return an error

### Requirement: MQTT Adapter Configuration

The system SHALL configure the MQTT adapter with broker URL, client ID, credentials, TLS settings, default QoS, subscribe topics, publish prefix, and Last Will Testament settings.

#### Scenario: Configuration loaded from gateway.yaml
- GIVEN an MQTT adapter configuration exists in gateway.yaml
- WHEN the adapter initializes
- THEN the system SHALL load the broker URL, client ID, and credentials
- AND subscribe to the configured topics

#### Scenario: Publish prefix prepended
- GIVEN a publish prefix of `"home"` is configured
- WHEN the adapter publishes a command
- THEN the system SHALL prepend the prefix to the outgoing topic path

### Requirement: MQTT Authentication and TLS

The system SHALL authenticate MQTT connections with username/password credentials, TLS client certificates, or AWS IoT SigV4 signing, and apply TLS settings including CA certificates.

#### Scenario: Username and password auth
- GIVEN the MQTT configuration includes a username and password
- WHEN the adapter connects to the broker
- THEN the system SHALL authenticate with the configured credentials

#### Scenario: TLS with CA certificate
- GIVEN the MQTT configuration enables TLS with a CA certificate path
- WHEN the adapter connects to the broker
- THEN the system SHALL establish a TLS connection using the configured CA certificate

### Requirement: Home Assistant Discovery Integration

The system SHALL parse Home Assistant MQTT discovery messages (`homeassistant/status`, `homeassistant/+/+/config`) to auto-register devices.

#### Scenario: Discovery message registers device
- GIVEN a Home Assistant discovery message arrives on a `homeassistant/+/+/config` topic
- WHEN the adapter processes it
- THEN the system SHALL auto-register the discovered device

#### Scenario: Status message processed
- GIVEN a message arrives on `homeassistant/status`
- WHEN the adapter processes it
- THEN the system SHALL handle the Home Assistant status change

### Requirement: MQTT Device Telemetry Routing

The system SHALL route incoming device telemetry from MQTT topics to a dedicated IoT session or a specific automation agent based on the topic prefix.

#### Scenario: Telemetry routed to IoT session
- GIVEN device telemetry arrives on a topic with a recognized prefix
- WHEN the adapter routes the message
- THEN the system SHALL route the telemetry to the IoT session matching the prefix

#### Scenario: Telemetry routed to automation agent
- GIVEN device telemetry arrives on a topic prefix associated with an automation agent
- WHEN the adapter routes the message
- THEN the system SHALL route the telemetry to that automation agent

---

## 03 SMS Adapter (Twilio / AWS SNS)

### 4.1 Protocol Details

- **Inbound:** Twilio webhook (POST to `/gateway/sms`) or AWS SNS HTTP endpoint. Twilio sends `From`, `Body`, `To`, `MessageSid`.
- **Outbound:** Twilio REST API (`POST /2010-04-01/Accounts/{sid}/Messages.json`) or AWS SNS Publish API.
- **Auth:** Twilio Account SID + Auth Token (Basic auth). AWS SNS uses IAM credentials (SigV4).
- **Real-time vs polling:** Webhook-driven (real-time). No polling needed.
- **Delivery receipts:** Twilio sends status callbacks (`delivered`, `failed`, `undelivered`). Track via `MessageSid`.

### 4.2 Message Format Normalization

```go
type SMSParser struct{}

func (p *SMSParser) Parse(payload []byte) (*ParsedMessage, error) {
    // Twilio webhook format
    var twilioMsg struct {
        From        string `json:"From"`
        To          string `json:"To"`
        Body        string `json:"Body"`
        MessageSid  string `json:"MessageSid"`
        AccountSid  string `json:"AccountSid"`
        NumMedia    string `json:"NumMedia"`
        MediaUrl0   string `json:"MediaUrl0"`
    }

    if err := json.Unmarshal(payload, &twilioMsg); err != nil {
        // Try AWS SNS format
        var snsMsg struct {
            Message      string `json:"Message"`
            TopicArn     string `json:"TopicArn"`
            MessageId    string `json:"MessageId"`
            Type         string `json:"Type"`
        }
        if err2 := json.Unmarshal(payload, &snsMsg); err2 != nil {
            return nil, err
        }
        // AWS SNS subscription confirmation
        if snsMsg.Type == "SubscriptionConfirmation" {
            return nil, fmt.Errorf("SNS subscription confirmation required")
        }
        return &ParsedMessage{
            UserID:    snsMsg.TopicArn,
            Text:      snsMsg.Message,
            ChannelID: "sms-alerts",
            IsBot:     true,
            Metadata:  map[string]any{"platform": "aws-sns", "message_id": snsMsg.MessageId},
        }, nil
    }

    var images []ImageRef
    if twilioMsg.NumMedia == "1" && twilioMsg.MediaUrl0 != "" {
        images = append(images, ImageRef{URL: twilioMsg.MediaUrl0})
    }

    return &ParsedMessage{
        UserID:    twilioMsg.From,         // "+1234567890"
        Text:      twilioMsg.Body,
        ChannelID: twilioMsg.From,         // SMS sessions are per-phone-number
        Metadata:  map[string]any{"message_sid": twilioMsg.MessageSid, "platform": "twilio"},
        Images:    images,
    }, nil
}

// Outbound: Message → SMS
func (d *SMSDelivery) SendText(key SessionKey, text string) error {
    // Split messages > 160 characters into segments
    segments := splitSMSSegments(text, 160)
    for _, segment := range segments {
        _, err := d.twilioClient.Api.CreateMessage(d.ctx, &twilioApi.CreateMessageParams{
            To:   twilio.String(key.UserID),
            From: twilio.String(d.fromNumber),
            Body: twilio.String(segment),
        })
        if err != nil {
            return err
        }
    }
    return nil
}
```

### 4.3 Error Handling & Retry

- **Twilio API errors:** Distinguish between 400 (invalid number, unsubscribed) and 500 (transient). Retry 500s up to 3 times; log 400s and skip.
- **Delivery failures:** Monitor status callbacks. If `status=failed`, log and optionally alert.
- **Rate limits:** Twilio allows 1 message per second per phone number. Queue outbound messages and enforce a 1s inter-message delay.
- **SMS segmentation:** Messages > 160 characters (GSM-7) or > 70 characters (UCS-2) are split into segments. Each segment costs separately — prefer concise messages.

### 4.4 Configuration

```yaml
adapters:
  sms:
    enabled: true
    provider: "twilio"              # or "aws-sns"
    twilio:
      account_sid: "${TWILIO_ACCOUNT_SID}"
      auth_token: "${TWILIO_AUTH_TOKEN}"
      from_number: "+15551234567"
      status_callback_url: "https://example.com/gateway/sms-status"
    aws_sns:
      region: "us-east-1"
      topic_arn: "arn:aws:sns:us-east-1:123456789012:navi-alerts"
      access_key_id: "${AWS_ACCESS_KEY_ID}"
      secret_access_key: "${AWS_SECRET_ACCESS_KEY}"
    webhook_path: "/gateway/sms"
    max_segments: 5                 # max SMS segments per message
    rate_limit_per_second: 1
```

### 4.5 Implementation Notes

- Use `github.com/twilio/twilio-go` for Twilio REST API. The `twilio-go` SDK handles auth and request signing.
- For AWS SNS, use `github.com/aws/aws-sdk-go-v2/service/sns` with IAM credentials.
- Twilio webhook verification: validate the `X-Twilio-Signature` header using the auth token to prevent forgery.
- SMS is best suited for **notifications and alerts** (short, urgent messages). For conversational interactions, prefer Telegram or email.
- Store `MessageSid` in message metadata for delivery receipt correlation.

### Requirement: SMS Webhook Inbound

The system SHALL receive SMS messages via webhook at `/gateway/sms` from Twilio or AWS SNS, parsing the sender's phone number, message body, and media attachments.

#### Scenario: Twilio webhook received
- GIVEN a Twilio webhook POST arrives at `/gateway/sms`
- WHEN the `SMSParser` parses it
- THEN the system SHALL extract the From number as the user ID, the Body as text, and the MessageSid as metadata
- AND include any media URLs as image references

#### Scenario: AWS SNS message received
- GIVEN an AWS SNS HTTP POST arrives
- WHEN the `SMSParser` parses it
- THEN the system SHALL extract the message body and topic ARN
- AND set the channel ID to `sms-alerts`

#### Scenario: SNS subscription confirmation
- GIVEN an AWS SNS SubscriptionConfirmation message is received
- WHEN the `SMSParser` parses it
- THEN the system SHALL return an error indicating confirmation is required

### Requirement: SMS Outbound Delivery

The system SHALL send SMS messages via the Twilio REST API or AWS SNS Publish API, splitting messages longer than 160 characters into GSM-7 segments.

#### Scenario: SMS sent via Twilio
- GIVEN a message targets SMS delivery via Twilio
- WHEN the SMS delivery adapter sends it
- THEN the system SHALL post the message to the Twilio Messages API

#### Scenario: SMS segmented by length
- GIVEN an SMS message exceeds 160 characters
- WHEN the SMS delivery adapter sends it
- THEN the system SHALL split the message into segments of 160 characters each
- AND send each segment as a separate SMS

### Requirement: Twilio and SNS Payload Normalization

The system SHALL normalize Twilio and AWS SNS payloads into `ParsedMessage` with the sender's phone number as the user ID, the message body as text, and the phone number as the channel ID for Twilio, or the topic ARN as the user ID for SNS.

#### Scenario: Twilio payload normalized
- GIVEN a Twilio webhook payload
- WHEN the `SMSParser` parses it
- THEN the system SHALL set the user ID to the From number and the channel ID to the same phone number
- AND include any media URL as an image reference

#### Scenario: AWS SNS payload normalized
- GIVEN an AWS SNS payload
- WHEN the `SMSParser` parses it
- THEN the system SHALL set the user ID to the topic ARN
- AND set the channel ID to `sms-alerts`
- AND set `IsBot` to true

### Requirement: SMS Delivery Receipt Correlation

The system SHALL store the Twilio `MessageSid` in message metadata so delivery status callbacks (`delivered`, `failed`, `undelivered`) can be correlated with the original message.

#### Scenario: MessageSid stored in metadata
- GIVEN a Twilio webhook payload is parsed
- WHEN the `SMSParser` parses it
- THEN the system SHALL store the `MessageSid` in message metadata

#### Scenario: Status callback correlated
- GIVEN a Twilio delivery status callback arrives
- WHEN the adapter processes it
- THEN the system SHALL correlate the callback with the message by `MessageSid`
- AND log the delivery outcome

### Requirement: SMS Delivery Failure Handling

The system SHALL monitor Twilio delivery status callbacks, retry transient 500 errors up to 3 times, and log 400 errors (invalid number, unsubscribed) without retry.

#### Scenario: Transient error retried
- GIVEN Twilio returns a 500 server error
- WHEN the SMS delivery adapter sends
- THEN the system SHALL retry up to 3 times

#### Scenario: Invalid number logged
- GIVEN Twilio returns a 400 error for an invalid number
- WHEN the SMS delivery adapter sends
- THEN the system SHALL log the error and skip the message without retrying

#### Scenario: Delivery failure alert
- GIVEN a Twilio status callback reports `status=failed`
- WHEN the adapter receives the callback
- THEN the system SHALL log the failure and optionally alert an admin

### Requirement: SMS Rate Limiting

The system SHALL enforce a one-message-per-second rate limit per phone number by queuing outbound SMS messages and inserting a 1-second delay between them.

#### Scenario: Outbound messages queued
- GIVEN multiple SMS messages are queued for the same destination number
- WHEN the adapter sends them
- THEN the system SHALL enforce a 1-second inter-message delay

#### Scenario: Max segments enforced
- GIVEN an SMS message would exceed the configured max segments
- WHEN the delivery adapter sends it
- THEN the system SHALL truncate the message to the maximum allowed segments

### Requirement: SMS Adapter Configuration

The system SHALL configure the SMS adapter with provider (Twilio or AWS SNS), provider credentials, sending phone number, webhook path, max segments, and rate limit settings.

#### Scenario: Twilio configuration loaded
- GIVEN a Twilio SMS adapter configuration exists in gateway.yaml
- WHEN the adapter initializes
- THEN the system SHALL load the account SID, auth token, and from number

#### Scenario: AWS SNS configuration loaded
- GIVEN an AWS SNS adapter configuration exists in gateway.yaml
- WHEN the adapter initializes
- THEN the system SHALL load the region, topic ARN, and IAM credentials

### Requirement: SMS Provider Selection

The system SHALL select the outbound SMS provider (Twilio or AWS SNS) based on the configured provider field and route messages through the matching API.

#### Scenario: Twilio provider used for outbound
- GIVEN the SMS provider is configured as `twilio`
- WHEN the SMS delivery adapter sends a message
- THEN the system SHALL send it via the Twilio REST API using the account SID, auth token, and from number

#### Scenario: AWS SNS provider used for outbound
- GIVEN the SMS provider is configured as `aws-sns`
- WHEN the SMS delivery adapter sends a message
- THEN the system SHALL publish it via the AWS SNS API in the configured region

### Requirement: Twilio Webhook Signature Verification

The system SHALL validate the `X-Twilio-Signature` header of incoming Twilio webhooks using the auth token to prevent forgery.

#### Scenario: Valid signature processed
- GIVEN a Twilio webhook with a valid `X-Twilio-Signature`
- WHEN the adapter processes the request
- THEN the system SHALL verify the signature and process the payload

#### Scenario: Invalid signature rejected
- GIVEN a Twilio webhook with an invalid or missing signature
- WHEN the adapter processes the request
- THEN the system SHALL reject the request

### Requirement: SMS Segment Encoding Compliance

The system SHALL split SMS messages into segments based on the GSM-7 character set (160 characters per segment) or UCS-2 (70 characters per segment) depending on the content encoding.

#### Scenario: GSM-7 message segmented
- GIVEN an SMS message uses GSM-7 encoding
- WHEN the delivery adapter sends it
- THEN the system SHALL split the message into segments of at most 160 characters each

#### Scenario: UCS-2 message segmented
- GIVEN an SMS message uses UCS-2 encoding
- WHEN the delivery adapter sends it
- THEN the system SHALL split the message into segments of at most 70 characters each

---

## 04 Calendar Adapter (CalDAV / Google Calendar)

### 5.1 Protocol Details

- **Inbound:** Polling-based (calendar providers don't offer real-time webhooks broadly). Poll Google Calendar API (push notifications via `watch` channels) or CalDAV sync.
- **Outbound:** Create/update/delete events via Google Calendar API or CalDAV `MKCOL` / `PUT` requests.
- **Auth:** OAuth 2.0 (Google Calendar) or Basic/Digest auth (CalDAV).
- **Real-time vs polling:** Google Calendar supports push notifications via `Channels.watch()` — set up a webhook receiver. CalDAV uses `CTAG` / `sync-collection` for efficient polling.
- **Primary use case:** Read-only detection of upcoming events to trigger automations. Event creation is secondary.

### 5.2 Message Format Normalization

```go
type CalendarParser struct{}

func (p *CalendarParser) Parse(payload []byte) (*ParsedMessage, error) {
    // Google Calendar watch notification payload
    var notif struct {
        ResourceState string `json:"resourceState"`
        ResourceID    string `json:"resourceId"`
        ResourceURI   string `json:"resourceUri"`
        ChannelID     string `json:"channelId"`
        Expiration    string `json:"expiration"`
    }

    if err := json.Unmarshal(payload, &notif); err != nil {
        // Try iCal format (CalDAV)
        return p.parseICal(string(payload))
    }

    return &ParsedMessage{
        UserID:    "calendar",
        Text:      fmt.Sprintf("Calendar change: %s", notif.ResourceState),
        ChannelID: notif.ChannelID,
        IsBot:     true,
        Metadata:  map[string]any{
            "resource_id": notif.ResourceID,
            "resource_uri": notif.ResourceURI,
            "state":       notif.ResourceState,
            "platform":    "google-calendar",
        },
    }, nil
}

func (p *CalendarParser) parseICal(raw string) (*ParsedMessage, error) {
    // Parse VCALENDAR / VEVENT components
    // Extract SUMMARY, DTSTART, DTEND, LOCATION, DESCRIPTION
    return &ParsedMessage{
        UserID: "calendar",
        Text:   fmt.Sprintf("Event: %s at %s", summary, dtstart),
        ChannelID: "calendar",
        Metadata: map[string]any{
            "event_summary": summary,
            "start_time":    dtstart,
            "end_time":      dtend,
            "location":      location,
            "platform":      "caldav",
        },
    }, nil
}

// Outbound: Create event via Google Calendar API
func (d *CalendarDelivery) SendNotification(key SessionKey, notif NotificationData) error {
    calendarID := key.ChannelID // "primary" or specific calendar ID
    event := &calendar.Event{
        Summary: notif.Title,
        Description: notif.Body,
        Start: &calendar.EventDateTime{
            DateTime: time.Now().Add(1 * time.Hour).Format(time.RFC3339),
            TimeZone: "UTC",
        },
        End: &calendar.EventDateTime{
            DateTime: time.Now().Add(2 * time.Hour).Format(time.RFC3339),
            TimeZone: "UTC",
        },
    }
    _, err := d.service.Events.Insert(calendarID, event).Do()
    return err
}
```

### 5.3 Error Handling & Retry

- **OAuth token expiry:** Automatically refresh tokens using the refresh token. Store the new token in the profile config.
- **API rate limits:** Google Calendar: 1,000,000 queries per day. CalDAV: server-dependent. Track usage and throttle when approaching limits.
- **Polling failures:** On error, back off (30s, 60s, 120s, max 300s) and retry. Resume normal interval on success.
- **Watch channel expiry:** Google Calendar watch channels expire after 7 days. Renew the channel before expiry (track via `expiration` field).

### 5.4 Configuration

```yaml
adapters:
  calendar:
    enabled: true
    provider: "google"                # or "caldav"
    google:
      client_id: "${GOOGLE_CLIENT_ID}"
      client_secret: "${GOOGLE_CLIENT_SECRET}"
      refresh_token: "${GOOGLE_REFRESH_TOKEN}"
      calendar_id: "primary"          # or specific calendar
      watch:
        enabled: true
        webhook_url: "https://example.com/gateway/calendar"
        channel_id: "opennavi-gateway"
        ttl: "604800s"                # 7 days
    caldav:
      url: "https://caldav.example.com"
      username: "${CALDAV_USERNAME}"
      password: "${CALDAV_PASSWORD}"
      calendar_path: "/calendars/user/default/"
    poll_interval: 300s               # polling fallback
    upcoming_window: "24h"            # check for events in the next 24h
```

### 5.5 Implementation Notes

- Google Calendar: use `google.golang.org/api/calendar/v3` with OAuth 2.0. Create a `calendar.Service` from an authenticated HTTP client.
- CalDAV: use `github.com/emersion/go-calendar` (or `github.com/emersion/go-webdav/caldav`) for PROPFIND, REPORT, and sync-collection requests.
- For event detection, poll the API every 5 minutes (Google) or use `sync-collection` REPORT (CalDAV) for incremental sync.
- Map upcoming events to `MessageTypeNotification` so they can be delivered as alerts or prompts to the agent.
- Calendar adapter is primarily **read-only** for automation triggers. Event creation is a secondary capability exposed via `SendNotification`.
- For Home Assistant integration, bridge calendar events into MQTT topics (`homeassistant/calendar/opennavi/next`).

### Requirement: Calendar Change Detection

The system SHALL detect calendar events by polling the Google Calendar API (with push notification via `Channels.watch` as an optimization) or using CalDAV sync-collection for incremental sync, with a 5-minute polling interval for Google Calendar.

#### Scenario: CalDAV poll detects events
- GIVEN the calendar adapter is configured with CalDAV
- WHEN the adapter polls
- THEN the system SHALL use `sync-collection` REPORT for incremental sync
- AND detect new, updated, and deleted events

#### Scenario: Google Calendar push notification
- GIVEN the calendar adapter is configured with Google Calendar and watch enabled
- WHEN the adapter starts
- THEN the system SHALL set up a watch channel with a webhook URL
- AND receive push notifications for calendar changes

### Requirement: Calendar Event Creation

The system SHALL create calendar events via the Google Calendar API or CalDAV MKCOL/PUT requests, using the notification's title, body, and configured start/end times.

#### Scenario: Event created via Google Calendar API
- GIVEN a notification with a title and body
- WHEN the `CalendarDelivery.SendNotification` runs
- THEN the system SHALL create an event with the title, description, start time, and end time

#### Scenario: Event created via CalDAV
- GIVEN the calendar adapter is configured with CalDAV
- WHEN the adapter creates an event
- THEN the system SHALL send a PUT request with iCal data to the CalDAV calendar path

### Requirement: Calendar Notification Normalization

The system SHALL normalize Google Calendar watch notifications and iCal events into `ParsedMessage` with a system identity, the event summary as text, and metadata including event details.

#### Scenario: Google Calendar notification normalized
- GIVEN a Google Calendar watch notification payload
- WHEN the `CalendarParser` parses it
- THEN the system SHALL extract the resource state, resource ID, and channel ID
- AND populate metadata with the resource URI and state

#### Scenario: iCal event parsed
- GIVEN an iCal VCALENDAR payload with VEVENT components
- WHEN the `CalendarParser` parses it
- THEN the system SHALL extract the event summary, start time, end time, and location
- AND populate the text with a formatted event string

### Requirement: Calendar Payload Format Detection

The system SHALL detect the incoming calendar payload format, parsing Google Calendar watch notification JSON and falling back to iCal parsing when the payload is not JSON.

#### Scenario: Google watch notification detected
- GIVEN a payload is valid Google Calendar watch notification JSON
- WHEN the `CalendarParser` parses it
- THEN the system SHALL parse the notification fields
- AND populate metadata with the resource ID, resource URI, and state

#### Scenario: iCal fallback
- GIVEN a payload cannot be decoded as JSON
- WHEN the `CalendarParser` parses it
- THEN the system SHALL fall back to parsing the payload as iCal format

### Requirement: OAuth Token Refresh

The system SHALL automatically refresh expired OAuth 2.0 tokens for Google Calendar using the stored refresh token, persisting the new token to the profile configuration.

#### Scenario: Token refreshed on expiry
- GIVEN the Google Calendar OAuth token expires
- WHEN the adapter makes a request
- THEN the system SHALL use the refresh token to obtain a new access token
- AND store the new token in the profile config

#### Scenario: Token refresh failure
- GIVEN the refresh token is invalid or expired
- WHEN the adapter attempts to refresh
- THEN the system SHALL return an error and log the failure

### Requirement: Watch Channel Renewal

The system SHALL renew Google Calendar watch channels before they expire (after 7 days), tracking the expiration time and recreating the channel with a new expiration.

#### Scenario: Watch channel renewed before expiry
- GIVEN a Google Calendar watch channel is approaching its 7-day expiry
- WHEN the adapter detects the near-expiry
- THEN the system SHALL renew the channel with a new expiration

#### Scenario: Watch channel expiry notification
- GIVEN a watch channel expires
- WHEN notifications stop arriving
- THEN the system SHALL detect the missed notifications on the next polling cycle

### Requirement: Calendar Adapter Configuration

The system SHALL configure the calendar adapter with provider (Google or CalDAV), provider-specific credentials (OAuth client ID/secret, refresh token or CalDAV username/password), calendar ID, watch settings, poll interval, and upcoming event window.

#### Scenario: Google Calendar configuration loaded
- GIVEN a Google Calendar adapter configuration exists in gateway.yaml
- WHEN the adapter initializes
- THEN the system SHALL load the OAuth credentials and calendar ID

#### Scenario: Upcoming window defines detection scope
- GIVEN an upcoming window of `"24h"` is configured
- WHEN the adapter polls for events
- THEN the system SHALL check for events in the next 24 hours

### Requirement: Calendar Provider Selection

The system SHALL operate in either Google Calendar or CalDAV mode based on the configured provider field, loading the matching credentials and API settings.

#### Scenario: Google Calendar mode
- GIVEN the calendar provider is configured as `google`
- WHEN the adapter initializes
- THEN the system SHALL load the OAuth client ID, client secret, refresh token, and calendar ID
- AND use the Google Calendar API for polling and event operations

#### Scenario: CalDAV mode
- GIVEN the calendar provider is configured as `caldav`
- WHEN the adapter initializes
- THEN the system SHALL load the CalDAV URL, username, password, and calendar path
- AND use CalDAV sync for event operations

### Requirement: Calendar Automation Integration

The system SHALL map upcoming calendar events to `MessageTypeNotification` so they can be delivered as alerts or prompts to the agent, and support bridging events into Home Assistant MQTT topics.

#### Scenario: Upcoming event becomes notification
- GIVEN an upcoming event is detected within the configured window
- WHEN the adapter processes the event
- THEN the system SHALL emit a `MessageTypeNotification` for the event

#### Scenario: Event bridged to Home Assistant
- GIVEN Home Assistant integration is enabled
- WHEN the next event is detected
- THEN the system SHALL publish the event to the Home Assistant MQTT topic `homeassistant/calendar/opennavi/next`

### Requirement: Calendar Read-Only Automation Mode

The system SHALL default to read-only mode for the calendar adapter, primarily detecting upcoming events to trigger automations, with event creation as a secondary capability.

#### Scenario: Read-only detection of upcoming events
- GIVEN the calendar adapter is running in its default mode
- WHEN a new event is detected within the upcoming window
- THEN the system SHALL emit a notification for the event
- AND SHALL NOT automatically create or modify calendar events

#### Scenario: Event creation as secondary capability
- GIVEN the calendar adapter is explicitly invoked to create an event
- WHEN `SendNotification` is called with event data
- THEN the system SHALL create the event via the configured provider API

---

## Related

- [Message Protocol & Gateway](./00-message-protocol-gateway.md) — core `Message` type and adapter interface
- [Platform Adapters & Routing](./01-platform-adapters-routing.md) — existing Telegram, Discord, Slack adapter patterns
- [Hooks, Cron & Implementation](./02-hooks-cron-impl.md) — hook system, cron scheduling, gateway config
- [Gap 6: Platform Adapters](../../gaps/GAPS.md#gap-6-platform-adapters--email-zendesk-mqtt-sms-calendar) — gap analysis that motivated this spec

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S050300 | 0.1.0 | Migrated from docs/spec/ to spec-tree |