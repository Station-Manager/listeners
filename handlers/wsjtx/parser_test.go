package wsjtx

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildQString creates a Qt QString in the binary format (length + UTF-16BE data).
func buildQString(s string) []byte {
	if s == "" {
		// Empty string: length = 0
		return []byte{0, 0, 0, 0}
	}

	// Convert to UTF-16BE
	utf16 := make([]byte, 0, len(s)*2)
	for _, r := range s {
		utf16 = append(utf16, byte(r>>8), byte(r))
	}

	// Prepend 4-byte length (in bytes)
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(utf16)))

	return append(length, utf16...)
}

// buildHeader creates a WSJT-X message header.
func buildHeader(msgType uint32, clientID string) []byte {
	buf := make([]byte, 12)
	binary.BigEndian.PutUint32(buf[0:4], magic)
	binary.BigEndian.PutUint32(buf[4:8], 2) // schema version
	binary.BigEndian.PutUint32(buf[8:12], msgType)
	return append(buf, buildQString(clientID)...)
}

func TestParser_Parse_InvalidMagic(t *testing.T) {
	p := NewParser()

	// Invalid magic number
	data := make([]byte, 20)
	binary.BigEndian.PutUint32(data[0:4], 0xDEADBEEF)

	_, err := p.Parse(data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid magic number")
}

func TestParser_Parse_TooShort(t *testing.T) {
	p := NewParser()

	_, err := p.Parse([]byte{0x01, 0x02, 0x03})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestParser_Parse_Heartbeat(t *testing.T) {
	p := NewParser()

	data := buildHeader(MsgHeartbeat, "WSJT-X")

	msg, err := p.Parse(data)
	require.NoError(t, err)

	header, ok := msg.(MessageHeader)
	require.True(t, ok)
	assert.Equal(t, magic, header.Magic)
	assert.Equal(t, uint32(2), header.Schema)
	assert.Equal(t, MsgHeartbeat, header.MsgType)
	assert.Equal(t, "WSJT-X", header.ID)
}

func TestParser_Parse_Decode(t *testing.T) {
	p := NewParser()

	// Build decode message
	header := buildHeader(MsgDecode, "WSJT-X")

	payload := []byte{
		1,                // New = true
		0, 0, 0x8C, 0xA0, // Time = 36000 ms (10:00:00)
	}

	// SNR = -10 (signed int32, stored as two's complement)
	snrVal := int32(-10)
	snr := make([]byte, 4)
	binary.BigEndian.PutUint32(snr, *(*uint32)(unsafe.Pointer(&snrVal)))
	payload = append(payload, snr...)

	// DeltaTime = 0.5 (float64)
	dt := make([]byte, 8)
	binary.BigEndian.PutUint64(dt, math.Float64bits(0.5))
	payload = append(payload, dt...)

	// DeltaFreq = 1500 Hz
	df := make([]byte, 4)
	binary.BigEndian.PutUint32(df, 1500)
	payload = append(payload, df...)

	// Mode = "FT8"
	payload = append(payload, buildQString("FT8")...)

	// Message = "CQ DX K1ABC FN42"
	payload = append(payload, buildQString("CQ DX K1ABC FN42")...)

	// LowConf = false
	payload = append(payload, 0)

	// OffAir = false
	payload = append(payload, 0)

	data := append(header, payload...)

	msg, err := p.Parse(data)
	require.NoError(t, err)

	decode, ok := msg.(*DecodeMessage)
	require.True(t, ok)
	assert.Equal(t, "WSJT-X", decode.Header.ID)
	assert.True(t, decode.New)
	assert.Equal(t, uint32(36000), decode.Time)
	assert.Equal(t, int32(-10), decode.SNR)
	assert.Equal(t, 0.5, decode.DeltaTime)
	assert.Equal(t, uint32(1500), decode.DeltaFreq)
	assert.Equal(t, "FT8", decode.Mode)
	assert.Equal(t, "CQ DX K1ABC FN42", decode.Message)
	assert.False(t, decode.LowConf)
	assert.False(t, decode.OffAir)
}

func TestParser_readQString_Empty(t *testing.T) {
	p := NewParser()

	// Empty string (length = 0)
	data := []byte{0, 0, 0, 0}
	r := bytes.NewReader(data)

	s, err := p.readQString(r)
	require.NoError(t, err)
	assert.Equal(t, "", s)
}

func TestParser_readQString_Null(t *testing.T) {
	p := NewParser()

	// Null string (length = -1)
	data := []byte{0xFF, 0xFF, 0xFF, 0xFF}
	r := bytes.NewReader(data)

	s, err := p.readQString(r)
	require.NoError(t, err)
	assert.Equal(t, "", s)
}

func TestParser_readQString_Simple(t *testing.T) {
	p := NewParser()

	data := buildQString("Hello")
	r := bytes.NewReader(data)

	s, err := p.readQString(r)
	require.NoError(t, err)
	assert.Equal(t, "Hello", s)
}

func TestDecodeUTF16BE(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "empty",
			input:    []byte{},
			expected: "",
		},
		{
			name:     "single byte",
			input:    []byte{0x00},
			expected: "",
		},
		{
			name:     "ASCII",
			input:    []byte{0x00, 'A', 0x00, 'B', 0x00, 'C'},
			expected: "ABC",
		},
		{
			name:     "callsign",
			input:    []byte{0x00, 'K', 0x00, '1', 0x00, 'A', 0x00, 'B', 0x00, 'C'},
			expected: "K1ABC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := decodeUTF16BE(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestJulianDayToTime(t *testing.T) {
	tests := []struct {
		name            string
		julianDay       int64
		msSinceMidnight uint32
		expected        time.Time
	}{
		{
			name:            "Unix epoch",
			julianDay:       2440588, // Jan 1, 1970
			msSinceMidnight: 0,
			expected:        time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:            "Y2K",
			julianDay:       2451545,  // Jan 1, 2000
			msSinceMidnight: 43200000, // 12:00:00
			expected:        time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		{
			name:            "With milliseconds",
			julianDay:       2451545,
			msSinceMidnight: 43200500, // 12:00:00.500
			expected:        time.Date(2000, 1, 1, 12, 0, 0, 500000000, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := julianDayToTime(tt.julianDay, tt.msSinceMidnight)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMsgTypeName(t *testing.T) {
	assert.Equal(t, "Heartbeat", msgTypeName(MsgHeartbeat))
	assert.Equal(t, "Status", msgTypeName(MsgStatus))
	assert.Equal(t, "Decode", msgTypeName(MsgDecode))
	assert.Equal(t, "QSO Logged", msgTypeName(MsgQSOLogged))
	assert.Equal(t, "Unknown(99)", msgTypeName(99))
}

func TestIsOptionalFieldAbsent(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "io.EOF is absent",
			err:      io.EOF,
			expected: true,
		},
		{
			name:     "io.ErrUnexpectedEOF is absent",
			err:      io.ErrUnexpectedEOF,
			expected: true,
		},
		{
			name:     "wrapped EOF is absent",
			err:      fmt.Errorf("read failed: %w", io.EOF),
			expected: true,
		},
		{
			name:     "other error is not absent",
			err:      fmt.Errorf("some I/O error"),
			expected: false,
		},
		{
			name:     "nil error is not absent",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isOptionalFieldAbsent(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
