package wsjtx

import (
	"testing"

	"github.com/Station-Manager/listeners/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandler_MessageTypeFiltering(t *testing.T) {
	tests := []struct {
		name         string
		configTypes  []any
		msgType      uint32
		shouldHandle bool
	}{
		{
			name:         "nil config processes all",
			configTypes:  nil,
			msgType:      MsgDecode,
			shouldHandle: true,
		},
		{
			name:         "empty config processes all",
			configTypes:  []any{},
			msgType:      MsgDecode,
			shouldHandle: true,
		},
		{
			name:         "filter allows matching type",
			configTypes:  []any{float64(MsgLoggedADIF)},
			msgType:      MsgLoggedADIF,
			shouldHandle: true,
		},
		{
			name:         "filter blocks non-matching type",
			configTypes:  []any{float64(MsgLoggedADIF)},
			msgType:      MsgDecode,
			shouldHandle: false,
		},
		{
			name:         "multiple types allowed",
			configTypes:  []any{float64(MsgQSOLogged), float64(MsgLoggedADIF)},
			msgType:      MsgQSOLogged,
			shouldHandle: true,
		},
		{
			name:         "int type works",
			configTypes:  []any{int(MsgLoggedADIF)},
			msgType:      MsgLoggedADIF,
			shouldHandle: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]any{}
			if tt.configTypes != nil {
				config[ConfigMessageTypes] = tt.configTypes
			}

			h, err := NewHandler(config)
			require.NoError(t, err)

			handler := h.(*Handler)
			assert.Equal(t, tt.shouldHandle, handler.shouldProcess(tt.msgType))
		})
	}
}

func TestHandler_OnlyProcessesConfiguredTypes(t *testing.T) {
	// Track which message types were processed
	var processedTypes []uint32

	config := map[string]any{
		ConfigMessageTypes: []any{float64(MsgLoggedADIF)}, // Only process type 12
		ConfigLogInfo: LogFunc(func(format string, args ...any) {
			// Capture when messages are processed
			processedTypes = append(processedTypes, MsgLoggedADIF)
		}),
	}

	h, err := NewHandler(config)
	require.NoError(t, err)

	// Build a heartbeat message (type 0) - should be skipped
	heartbeat := buildHeader(MsgHeartbeat, "WSJT-X")
	err = h.Handle(handlers.Packet{Data: heartbeat})
	assert.NoError(t, err)

	// Build a decode message (type 2) - should be skipped
	decode := buildDecodeMessage()
	err = h.Handle(handlers.Packet{Data: decode})
	assert.NoError(t, err)

	// Build a logged ADIF message (type 12) - should be processed
	adif := buildLoggedADIFMessage()
	err = h.Handle(handlers.Packet{Data: adif})
	assert.NoError(t, err)

	// Only the ADIF message should have been processed
	assert.Len(t, processedTypes, 1)
}

func buildDecodeMessage() []byte {
	header := buildHeader(MsgDecode, "WSJT-X")
	payload := []byte{
		1,                // New = true
		0, 0, 0x8C, 0xA0, // Time = 36000 ms
	}
	// SNR (int32)
	payload = append(payload, 0, 0, 0, 10)
	// DeltaTime (float64)
	payload = append(payload, 0, 0, 0, 0, 0, 0, 0, 0)
	// DeltaFreq (uint32)
	payload = append(payload, 0, 0, 0x05, 0xDC)
	// Mode
	payload = append(payload, buildQString("FT8")...)
	// Message
	payload = append(payload, buildQString("CQ TEST")...)
	// LowConf
	payload = append(payload, 0)
	// OffAir
	payload = append(payload, 0)

	return append(header, payload...)
}

func buildLoggedADIFMessage() []byte {
	header := buildHeader(MsgLoggedADIF, "WSJT-X")
	adif := "<call:5>K1ABC<eor>"
	payload := buildQString(adif)
	return append(header, payload...)
}
