// Package wsjtx implements a packet handler for the WSJT-X and JTDX UDP protocol.
//
// WSJT-X and JTDX broadcast UDP datagrams containing decoded messages, status updates,
// and logged QSO information. The protocol uses Qt's QDataStream format with big-endian
// byte ordering.
//
// # Protocol Overview
//
// Each message starts with a header:
//   - Magic number: 4 bytes (0xADBCCBDA)
//   - Schema version: 4 bytes (uint32, currently 2 or 3)
//   - Message type: 4 bytes (uint32)
//   - Client ID: QString (length-prefixed UTF-16)
//
// # Message Types
//
//	0 = Heartbeat
//	1 = Status
//	2 = Decode
//	3 = Clear
//	4 = Reply (to WSJT-X)
//	5 = QSO Logged
//	6 = Close
//	7 = Replay (request decodes)
//	8 = Halt TX
//	9 = Free Text
//	10 = WSPR Decode
//	11 = Location
//	12 = Logged ADIF
//	13 = Highlight Callsign
//	14 = Switch Configuration
//	15 = Configure
//
// This handler focuses on message types 2 (Decode) and 5 (QSO Logged) for logging contacts.
//
// # References
//
//   - https://sourceforge.net/p/wsjt/wsjtx/ci/master/tree/Network/NetworkMessage.hpp
//   - https://physics.princeton.edu/pulsar/k1jt/wsjtx-doc/wsjtx-main-2.6.1.html
package wsjtx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// Magic number identifying WSJT-X protocol messages.
const magic uint32 = 0xADBCCBDA

// Message type constants.
const (
	MsgHeartbeat     uint32 = 0
	MsgStatus        uint32 = 1
	MsgDecode        uint32 = 2
	MsgClear         uint32 = 3
	MsgReply         uint32 = 4
	MsgQSOLogged     uint32 = 5
	MsgClose         uint32 = 6
	MsgReplay        uint32 = 7
	MsgHaltTx        uint32 = 8
	MsgFreeText      uint32 = 9
	MsgWSPRDecode    uint32 = 10
	MsgLocation      uint32 = 11
	MsgLoggedADIF    uint32 = 12
	MsgHighlightCall uint32 = 13
	MsgSwitchConfig  uint32 = 14
	MsgConfigure     uint32 = 15
)

// MessageHeader contains the common header fields for all WSJT-X messages.
type MessageHeader struct {
	Magic   uint32
	Schema  uint32
	MsgType uint32
	ID      string // Client identifier (e.g., "WSJT-X", "JTDX")
}

// StatusMessage represents a Status (type 1) message.
type StatusMessage struct {
	Header        MessageHeader
	DialFrequency uint64 // Hz
	Mode          string
	DXCall        string
	Report        string
	TxMode        string
	TxEnabled     bool
	Transmitting  bool
	Decoding      bool
	RxDF          uint32 // Hz offset
	TxDF          uint32 // Hz offset
	DECall        string
	DEGrid        string
	DXGrid        string
	TxWatchdog    bool
	SubMode       string
	FastMode      bool
	SpecialOpMode uint8
	FrequencyTol  uint32
	TRPeriod      uint32
	ConfigName    string
	TxMessage     string
}

// DecodeMessage represents a Decode (type 2) message.
type DecodeMessage struct {
	Header    MessageHeader
	New       bool
	Time      uint32 // ms since midnight UTC
	SNR       int32  // dB
	DeltaTime float64
	DeltaFreq uint32 // Hz
	Mode      string
	Message   string
	LowConf   bool
	OffAir    bool
}

// QSOLoggedMessage represents a QSO Logged (type 5) message.
type QSOLoggedMessage struct {
	Header           MessageHeader
	DateTimeOff      time.Time
	DXCall           string
	DXGrid           string
	TxFrequency      uint64 // Hz
	Mode             string
	ReportSent       string
	ReportReceived   string
	TxPower          string
	Comments         string
	Name             string
	DateTimeOn       time.Time
	OperatorCall     string
	MyCall           string
	MyGrid           string
	ExchangeSent     string
	ExchangeReceived string
	ADIFPropMode     string
}

// LoggedADIFMessage represents a Logged ADIF (type 12) message.
type LoggedADIFMessage struct {
	Header MessageHeader
	ADIF   string
}

// Parser handles decoding of WSJT-X binary protocol messages.
type Parser struct{}

// NewParser creates a new WSJT-X protocol parser.
func NewParser() *Parser {
	return &Parser{}
}

// Parse decodes a WSJT-X binary message and returns the parsed message.
// Returns an error if the message is malformed or uses an unsupported format.
func (p *Parser) Parse(data []byte) (any, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("message too short: %d bytes", len(data))
	}

	r := bytes.NewReader(data)

	// Read header
	header, err := p.readHeader(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read header: %w", err)
	}

	if header.Magic != magic {
		return nil, fmt.Errorf("invalid magic number: 0x%X (expected 0x%X)", header.Magic, magic)
	}

	// Parse message based on type
	switch header.MsgType {
	case MsgHeartbeat:
		return header, nil
	case MsgStatus:
		return p.parseStatus(r, header)
	case MsgDecode:
		return p.parseDecode(r, header)
	case MsgQSOLogged:
		return p.parseQSOLogged(r, header)
	case MsgLoggedADIF:
		return p.parseLoggedADIF(r, header)
	case MsgClear, MsgClose:
		return header, nil
	default:
		// Return header for unhandled message types
		return header, nil
	}
}

func (p *Parser) readHeader(r *bytes.Reader) (MessageHeader, error) {
	var h MessageHeader

	if err := binary.Read(r, binary.BigEndian, &h.Magic); err != nil {
		return h, err
	}
	if err := binary.Read(r, binary.BigEndian, &h.Schema); err != nil {
		return h, err
	}
	if err := binary.Read(r, binary.BigEndian, &h.MsgType); err != nil {
		return h, err
	}

	var err error
	h.ID, err = p.readQString(r)
	if err != nil {
		return h, fmt.Errorf("failed to read client ID: %w", err)
	}

	return h, nil
}

// readQString reads a Qt QString from the stream.
// QString is encoded as: 4-byte length (in bytes, -1 for null), followed by UTF-16BE data.
func (p *Parser) readQString(r *bytes.Reader) (string, error) {
	var length int32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return "", err
	}

	// -1 indicates null string
	if length == -1 || length == 0 {
		return "", nil
	}

	if length < 0 || length > 65535 {
		return "", fmt.Errorf("invalid QString length: %d", length)
	}

	// Length is in bytes, UTF-16 uses 2 bytes per code unit
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return "", err
	}

	// Convert UTF-16BE to string
	return decodeUTF16BE(data), nil
}

// readQBool reads a Qt bool from the stream (1 byte).
func (p *Parser) readQBool(r *bytes.Reader) (bool, error) {
	b, err := r.ReadByte()
	if err != nil {
		return false, err
	}
	return b != 0, nil
}

// isOptionalFieldAbsent returns true if the error indicates an optional field
// is absent (EOF or unexpected EOF), which is expected for older schema versions.
// Returns false for other errors like I/O failures or data corruption.
func isOptionalFieldAbsent(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// readQDateTime reads a Qt QDateTime from the stream.
// QDateTime is encoded as: Julian day (int64), ms since midnight (uint32), timespec (uint8).
func (p *Parser) readQDateTime(r *bytes.Reader) (time.Time, error) {
	var julianDay int64
	if err := binary.Read(r, binary.BigEndian, &julianDay); err != nil {
		return time.Time{}, err
	}

	var msecsSinceMidnight uint32
	if err := binary.Read(r, binary.BigEndian, &msecsSinceMidnight); err != nil {
		return time.Time{}, err
	}

	var timeSpec uint8
	if err := binary.Read(r, binary.BigEndian, &timeSpec); err != nil {
		return time.Time{}, err
	}

	// Convert Julian day to Gregorian date
	// Julian day 0 = November 24, 4714 BC in the proleptic Gregorian calendar
	// We use the algorithm from Qt's QDate implementation
	if julianDay < 0 {
		return time.Time{}, nil // Invalid date
	}

	return julianDayToTime(julianDay, msecsSinceMidnight), nil
}

func (p *Parser) parseStatus(r *bytes.Reader, header MessageHeader) (*StatusMessage, error) {
	msg := &StatusMessage{Header: header}
	var err error

	if err = binary.Read(r, binary.BigEndian, &msg.DialFrequency); err != nil {
		return nil, err
	}
	if msg.Mode, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.DXCall, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.Report, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.TxMode, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.TxEnabled, err = p.readQBool(r); err != nil {
		return nil, err
	}
	if msg.Transmitting, err = p.readQBool(r); err != nil {
		return nil, err
	}
	if msg.Decoding, err = p.readQBool(r); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.BigEndian, &msg.RxDF); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.BigEndian, &msg.TxDF); err != nil {
		return nil, err
	}
	if msg.DECall, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.DEGrid, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.DXGrid, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.TxWatchdog, err = p.readQBool(r); err != nil {
		return nil, err
	}
	if msg.SubMode, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.FastMode, err = p.readQBool(r); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.BigEndian, &msg.SpecialOpMode); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.BigEndian, &msg.FrequencyTol); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.BigEndian, &msg.TRPeriod); err != nil {
		return nil, err
	}
	if msg.ConfigName, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.TxMessage, err = p.readQString(r); err != nil {
		// This field may not exist in older schema versions
		if !isOptionalFieldAbsent(err) {
			return nil, err
		}
		msg.TxMessage = ""
	}

	return msg, nil
}

func (p *Parser) parseDecode(r *bytes.Reader, header MessageHeader) (*DecodeMessage, error) {
	msg := &DecodeMessage{Header: header}
	var err error

	if msg.New, err = p.readQBool(r); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.BigEndian, &msg.Time); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.BigEndian, &msg.SNR); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.BigEndian, &msg.DeltaTime); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.BigEndian, &msg.DeltaFreq); err != nil {
		return nil, err
	}
	if msg.Mode, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.Message, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.LowConf, err = p.readQBool(r); err != nil {
		return nil, err
	}
	if msg.OffAir, err = p.readQBool(r); err != nil {
		// This field may not exist in older schema versions
		if !isOptionalFieldAbsent(err) {
			return nil, err
		}
		msg.OffAir = false
	}

	return msg, nil
}

func (p *Parser) parseQSOLogged(r *bytes.Reader, header MessageHeader) (*QSOLoggedMessage, error) {
	msg := &QSOLoggedMessage{Header: header}
	var err error

	if msg.DateTimeOff, err = p.readQDateTime(r); err != nil {
		return nil, err
	}
	if msg.DXCall, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.DXGrid, err = p.readQString(r); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.BigEndian, &msg.TxFrequency); err != nil {
		return nil, err
	}
	if msg.Mode, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.ReportSent, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.ReportReceived, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.TxPower, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.Comments, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.Name, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.DateTimeOn, err = p.readQDateTime(r); err != nil {
		return nil, err
	}
	if msg.OperatorCall, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.MyCall, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.MyGrid, err = p.readQString(r); err != nil {
		return nil, err
	}
	if msg.ExchangeSent, err = p.readQString(r); err != nil {
		// These fields may not exist in older schema versions
		if !isOptionalFieldAbsent(err) {
			return nil, err
		}
		msg.ExchangeSent = ""
	}
	if msg.ExchangeReceived, err = p.readQString(r); err != nil {
		if !isOptionalFieldAbsent(err) {
			return nil, err
		}
		msg.ExchangeReceived = ""
	}
	if msg.ADIFPropMode, err = p.readQString(r); err != nil {
		if !isOptionalFieldAbsent(err) {
			return nil, err
		}
		msg.ADIFPropMode = ""
	}

	return msg, nil
}

func (p *Parser) parseLoggedADIF(r *bytes.Reader, header MessageHeader) (*LoggedADIFMessage, error) {
	msg := &LoggedADIFMessage{Header: header}
	var err error

	if msg.ADIF, err = p.readQString(r); err != nil {
		return nil, err
	}

	return msg, nil
}

// decodeUTF16BE converts UTF-16BE bytes to a Go string.
func decodeUTF16BE(data []byte) string {
	if len(data) < 2 {
		return ""
	}

	runes := make([]rune, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		r := rune(data[i])<<8 | rune(data[i+1])

		// Handle surrogate pairs
		if r >= 0xD800 && r <= 0xDBFF && i+3 < len(data) {
			low := rune(data[i+2])<<8 | rune(data[i+3])
			if low >= 0xDC00 && low <= 0xDFFF {
				r = 0x10000 + ((r - 0xD800) << 10) + (low - 0xDC00)
				i += 2
			}
		}
		runes = append(runes, r)
	}
	return string(runes)
}

// julianDayToTime converts a Julian day number and milliseconds to a time.Time.
func julianDayToTime(julianDay int64, msSinceMidnight uint32) time.Time {
	// Algorithm from Qt's QDate::fromJulianDay
	// Based on the algorithm in "Calendrical Calculations" by Reingold & Dershowitz
	a := julianDay + 32044
	b := (4*a + 3) / 146097
	c := a - (146097*b)/4
	d := (4*c + 3) / 1461
	e := c - (1461*d)/4
	m := (5*e + 2) / 153

	day := int(e - (153*m+2)/5 + 1)
	month := int(m + 3 - 12*(m/10))
	year := int(100*b + d - 4800 + m/10)

	hours := int(msSinceMidnight / 3600000)
	msSinceMidnight %= 3600000
	minutes := int(msSinceMidnight / 60000)
	msSinceMidnight %= 60000
	seconds := int(msSinceMidnight / 1000)
	ms := int(msSinceMidnight % 1000)

	return time.Date(year, time.Month(month), day, hours, minutes, seconds, ms*1e6, time.UTC)
}
