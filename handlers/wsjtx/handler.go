package wsjtx

import (
	"fmt"
	"strings"

	"github.com/Station-Manager/listeners/handlers"
)

func init() {
	handlers.Register("wsjtx", NewHandler)
}

// QSOLogger is the interface for logging QSOs.
// This allows the handler to work with any service that implements QSO logging
// (e.g., the facade service in logging-app).
type QSOLogger interface {
	// LogQso logs a QSO to the database.
	// The handler will call this when a QSO Logged message is received.
	LogQso(qso any) error
}

// LogFunc is a function type for logging messages.
type LogFunc func(format string, args ...any)

// Handler processes WSJT-X and JTDX UDP messages.
type Handler struct {
	parser    *Parser
	qsoLogger QSOLogger
	logDebug  LogFunc
	logInfo   LogFunc
	logError  LogFunc

	// Configuration options
	autoLog      bool            // Automatically log QSOs when received
	logDecodes   bool            // Log decode messages (for debugging)
	messageTypes map[uint32]bool // Message types to process (nil = all)
}

// Config keys for handler configuration.
const (
	ConfigAutoLog      = "auto_log"      // bool: automatically log QSOs
	ConfigLogDecodes   = "log_decodes"   // bool: log decode messages
	ConfigMessageTypes = "message_types" // []any: list of message type numbers to process
	ConfigQSOLogger    = "qso_logger"    // QSOLogger: service for logging QSOs
	ConfigLogDebug     = "log_debug"     // LogFunc: debug logging function
	ConfigLogInfo      = "log_info"      // LogFunc: info logging function
	ConfigLogError     = "log_error"     // LogFunc: error logging function
)

// NewHandler creates a new WSJT-X handler.
// Configuration options:
//   - auto_log (bool): Automatically log QSOs when received (default: false)
//   - log_decodes (bool): Log decode messages for debugging (default: false)
//   - message_types ([]any): List of message type numbers to process (default: all)
//     Example: [5, 12] to only process QSO Logged and Logged ADIF messages
//   - qso_logger (QSOLogger): Service for logging QSOs (required for auto_log)
//   - log_debug/log_info/log_error (LogFunc): Logging functions
func NewHandler(config map[string]any) (handlers.PacketHandler, error) {
	h := &Handler{
		parser:       NewParser(),
		autoLog:      false,
		logDecodes:   false,
		messageTypes: nil, // nil means process all message types
	}

	// Parse configuration
	if v, ok := config[ConfigAutoLog].(bool); ok {
		h.autoLog = v
	}
	if v, ok := config[ConfigLogDecodes].(bool); ok {
		h.logDecodes = v
	}
	if v, ok := config[ConfigMessageTypes].([]any); ok && len(v) > 0 {
		h.messageTypes = make(map[uint32]bool, len(v))
		for _, mt := range v {
			switch t := mt.(type) {
			case float64: // JSON numbers are float64
				h.messageTypes[uint32(t)] = true
			case int:
				h.messageTypes[uint32(t)] = true
			case uint32:
				h.messageTypes[t] = true
			}
		}
	}
	if v, ok := config[ConfigQSOLogger].(QSOLogger); ok {
		h.qsoLogger = v
	}
	if v, ok := config[ConfigLogDebug].(LogFunc); ok {
		h.logDebug = v
	}
	if v, ok := config[ConfigLogInfo].(LogFunc); ok {
		h.logInfo = v
	}
	if v, ok := config[ConfigLogError].(LogFunc); ok {
		h.logError = v
	}

	// Validate configuration
	if h.autoLog && h.qsoLogger == nil {
		return nil, fmt.Errorf("wsjtx: auto_log requires qso_logger to be configured")
	}

	return h, nil
}

// Name returns the handler identifier.
func (h *Handler) Name() string {
	return "wsjtx"
}

// shouldProcess returns true if the given message type should be processed.
func (h *Handler) shouldProcess(msgType uint32) bool {
	if h.messageTypes == nil {
		return true // Process all if not configured
	}
	return h.messageTypes[msgType]
}

// Handle processes a WSJT-X/JTDX UDP packet.
func (h *Handler) Handle(pkt handlers.Packet) error {
	msg, err := h.parser.Parse(pkt.Data)
	if err != nil {
		h.debugf("failed to parse packet from %s: %v", pkt.RemoteAddr, err)
		return nil // Don't propagate parse errors - just log and continue
	}

	// Get message type from the parsed message
	var msgType uint32
	switch m := msg.(type) {
	case MessageHeader:
		msgType = m.MsgType
	case *StatusMessage:
		msgType = m.Header.MsgType
	case *DecodeMessage:
		msgType = m.Header.MsgType
	case *QSOLoggedMessage:
		msgType = m.Header.MsgType
	case *LoggedADIFMessage:
		msgType = m.Header.MsgType
	default:
		return nil
	}

	// Check if we should process this message type
	if !h.shouldProcess(msgType) {
		h.debugf("skipping message type %d (%s) - not in configured types", msgType, msgTypeName(msgType))
		return nil
	}

	// Process the message
	switch m := msg.(type) {
	case MessageHeader:
		h.handleHeader(m)
	case *StatusMessage:
		h.handleStatus(m)
	case *DecodeMessage:
		h.handleDecode(m)
	case *QSOLoggedMessage:
		return h.handleQSOLogged(m)
	case *LoggedADIFMessage:
		return h.handleLoggedADIF(m)
	}

	return nil
}

// Close releases any resources held by the handler.
func (h *Handler) Close() error {
	return nil
}

func (h *Handler) handleHeader(header MessageHeader) {
	h.debugf("received %s message from %s (schema %d)",
		msgTypeName(header.MsgType), header.ID, header.Schema)
}

func (h *Handler) handleStatus(msg *StatusMessage) {
	h.debugf("status from %s: freq=%d mode=%s dx=%s",
		msg.Header.ID, msg.DialFrequency, msg.Mode, msg.DXCall)
}

func (h *Handler) handleDecode(msg *DecodeMessage) {
	if h.logDecodes {
		h.infof("decode from %s: [%s] SNR=%d dt=%.1f freq=%d msg=%q",
			msg.Header.ID, msg.Mode, msg.SNR, msg.DeltaTime, msg.DeltaFreq, msg.Message)
	}
}

func (h *Handler) handleQSOLogged(msg *QSOLoggedMessage) error {
	h.infof("QSO logged from %s: %s -> %s (%s) @ %d Hz, mode=%s, sent=%s rcvd=%s",
		msg.Header.ID, msg.MyCall, msg.DXCall, msg.DXGrid,
		msg.TxFrequency, msg.Mode, msg.ReportSent, msg.ReportReceived)

	if h.autoLog && h.qsoLogger != nil {
		// TODO: Convert QSOLoggedMessage to types.Qso and call qsoLogger.LogQso
		// For now, just log that we would log
		h.infof("would auto-log QSO: %s with %s", msg.MyCall, msg.DXCall)
	}

	return nil
}

func (h *Handler) handleLoggedADIF(msg *LoggedADIFMessage) error {
	h.infof("ADIF logged from %s: %s", msg.Header.ID, truncate(msg.ADIF, 100))

	if h.autoLog && h.qsoLogger != nil {
		// TODO: Parse ADIF and call qsoLogger.LogQso
		h.infof("would auto-log QSO from ADIF")
	}

	return nil
}

// Logging helpers
func (h *Handler) debugf(format string, args ...any) {
	if h.logDebug != nil {
		h.logDebug(format, args...)
	}
}

func (h *Handler) infof(format string, args ...any) {
	if h.logInfo != nil {
		h.logInfo(format, args...)
	}
}

func (h *Handler) errorf(format string, args ...any) {
	if h.logError != nil {
		h.logError(format, args...)
	}
}

func msgTypeName(msgType uint32) string {
	names := map[uint32]string{
		MsgHeartbeat:     "Heartbeat",
		MsgStatus:        "Status",
		MsgDecode:        "Decode",
		MsgClear:         "Clear",
		MsgReply:         "Reply",
		MsgQSOLogged:     "QSO Logged",
		MsgClose:         "Close",
		MsgReplay:        "Replay",
		MsgHaltTx:        "Halt TX",
		MsgFreeText:      "Free Text",
		MsgWSPRDecode:    "WSPR Decode",
		MsgLocation:      "Location",
		MsgLoggedADIF:    "Logged ADIF",
		MsgHighlightCall: "Highlight Call",
		MsgSwitchConfig:  "Switch Config",
		MsgConfigure:     "Configure",
	}
	if name, ok := names[msgType]; ok {
		return name
	}
	return fmt.Sprintf("Unknown(%d)", msgType)
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
