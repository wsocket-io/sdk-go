# wSocket Go SDK

Official Go SDK for [wSocket](https://wsocket.io) — Realtime Pub/Sub over WebSockets.

[![Go Reference](https://pkg.go.dev/badge/github.com/wsocket-io/sdk-go.svg)](https://pkg.go.dev/github.com/wsocket-io/sdk-go)
[![Go Version](https://img.shields.io/github/v/tag/wsocket-io/sdk-go?label=version&sort=semver)](https://github.com/wsocket-io/sdk-go/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Installation

```bash
go get github.com/wsocket-io/sdk-go
```

## Quick Start

```go
package main

import (
    "fmt"
    wsocket "github.com/wsocket-io/sdk-go"
)

func main() {
    client := wsocket.NewClient("wss://node00.wsocket.online", "your-api-key")
    err := client.Connect()
    if err != nil {
        panic(err)
    }
    defer client.Disconnect()

    ch := client.Channel("chat:general")
    ch.Subscribe(func(data map[string]any, meta wsocket.MessageMeta) {
        fmt.Printf("[%s] %v\n", meta.Channel, data)
    })

    ch.Publish(map[string]any{"text": "Hello from Go!"})

    select {} // block forever
}
```

## Features

- **Pub/Sub** — Subscribe and publish to channels in real-time
- **Presence** — Track who is online in a channel
- **History** — Retrieve past messages
- **Connection Recovery** — Automatic reconnection with message replay
- **Thread-safe** — Safe for concurrent use

## Presence

```go
ch := client.Channel("chat:general")

ch.Presence().OnEnter(func(m wsocket.PresenceMember) {
    fmt.Println("joined:", m.ClientID)
})

ch.Presence().OnLeave(func(m wsocket.PresenceMember) {
    fmt.Println("left:", m.ClientID)
})

ch.Presence().Enter(map[string]any{"name": "Alice"})
members := ch.Presence().Get()
```

## History

```go
ch.OnHistory(func(result wsocket.HistoryResult) {
    for _, msg := range result.Messages {
        fmt.Printf("[%d] %v\n", msg.Timestamp, msg.Data)
    }
})

ch.History(wsocket.HistoryOptions{Limit: 50})
```

## Push Notifications

```go
push := wsocket.NewPushClient("https://your-server.com", "your-api-key", "your-app-id")

// Register device
push.RegisterFCM("device-token", "user-123")

// Send & broadcast
push.SendToMember("user-123", map[string]string{"title": "Hello"})
push.Broadcast(map[string]string{"title": "News"})

// Channel targeting
push.AddChannel("subscription-id", "alerts")
push.RemoveChannel("subscription-id", "alerts")

// VAPID key
vapidKey, _ := push.GetVapidKey()

// List subscriptions
subs, _ := push.ListSubscriptions("user-123")
```

## Requirements

- Go >= 1.21
- `github.com/gorilla/websocket`

## Development

```bash
go test ./...
```

## License

MIT
