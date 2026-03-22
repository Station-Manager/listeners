# Packet Handlers

This package provides a pluggable packet handler system for network listeners.

## Overview

Packet handlers process incoming network data from UDP/TCP listeners. Each handler implements protocol-specific parsing (e.g., WSJT-X binary protocol, N1MM XML).

Handlers are registered at `init()` time via `Register()` and instantiated by the listeners service based on configuration.

## Architecture

```
listeners/
├── handlers/
│   ├── handler.go      # PacketHandler interface, Packet struct, registry
│   └── wsjtx/
│       ├── handler.go  # WSJT-X/JTDX handler implementation
│       └── parser.go   # Binary protocol parser
```

## Usage

### Configuration

Add a `handler` field to your listener configuration:

```json
{
  "listener_configs": [
    {
      "name": "WSJT-X UDP",
      "enabled": true,
      "host": "localhost",
      "port": 2237,
      "protocol": "UDP",
      "buffer_size": 1024,
      "handler": "wsjtx",
      "handler_config": {
        "auto_log": false,
        "log_decodes": true
      }
    }
  ]
}
```

### Available Handlers

| Handler | Description | Config Options |
|---------|-------------|----------------|
| `wsjtx` | WSJT-X and JTDX UDP protocol | `auto_log`, `log_decodes`, `message_types` |

### Handler Configuration Options

#### wsjtx

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `auto_log` | bool | false | Automatically log QSOs when received |
| `log_decodes` | bool | false | Log decode messages (for debugging) |
| `message_types` | []int | all | List of message type numbers to process |

#### Message Type Filtering

You can configure the handler to only process specific message types. This is useful when you only care about certain events (e.g., only logged QSOs):

```json
{
  "handler": "wsjtx",
  "handler_config": {
    "message_types": [12]
  }
}
```

Available message types:
| Type | Name | Description |
|------|------|-------------|
| 0 | Heartbeat | Keep-alive message |
| 1 | Status | Current application state |
| 2 | Decode | Decoded message (FT8, FT4, etc.) |
| 5 | QSO Logged | QSO was logged in the application |
| 12 | Logged ADIF | QSO logged with ADIF data |

For logging-app, you typically only need type 12 (Logged ADIF) since it contains the complete ADIF record.

## Implementing a New Handler

### 1. Create a Handler Package

```go
// handlers/myprotocol/handler.go
package myprotocol

import "github.com/Station-Manager/listeners/handlers"

func init() {
    handlers.Register("myprotocol", NewHandler)
}

type Handler struct {
    // handler state
}

func NewHandler(config map[string]any) (handlers.PacketHandler, error) {
    h := &Handler{}
    // parse config
    return h, nil
}

func (h *Handler) Name() string { return "myprotocol" }

func (h *Handler) Handle(pkt handlers.Packet) error {
    // parse pkt.Data and process
    return nil
}

func (h *Handler) Close() error {
    return nil
}
```

### 2. Import the Handler

To enable the handler, import it (typically in your main package or test):

```go
import _ "github.com/Station-Manager/listeners/handlers/myprotocol"
```

### 3. Configure in config.json

```json
{
  "handler": "myprotocol",
  "handler_config": {
    "some_option": "value"
  }
}
```

## WSJT-X Protocol

The WSJT-X handler parses the UDP broadcast protocol used by WSJT-X and JTDX. The protocol uses Qt's QDataStream format with big-endian byte ordering.

### Message Types

| Type | Name | Description |
|------|------|-------------|
| 0 | Heartbeat | Keep-alive message |
| 1 | Status | Current application state |
| 2 | Decode | Decoded message (FT8, FT4, etc.) |
| 5 | QSO Logged | QSO was logged in the application |
| 12 | Logged ADIF | QSO logged with ADIF data |

### References

- [WSJT-X Network Protocol](https://sourceforge.net/p/wsjt/wsjtx/ci/master/tree/Network/NetworkMessage.hpp)
- [WSJT-X User Guide](https://physics.princeton.edu/pulsar/k1jt/wsjtx-doc/wsjtx-main-2.6.1.html)

## Thread Safety

All handlers must be safe for concurrent use. The same handler instance may be called from multiple goroutines (e.g., multiple UDP packets arriving simultaneously).

