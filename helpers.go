package listeners

import (
	"encoding/hex"
	"net"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Station-Manager/errors"
)

// getIP parses an address string and returns a net.IP.
// Currently only supports IPv4 addresses. IPv6 is not supported.
// Special case: "localhost" is mapped to 127.0.0.1.
func getIP(address string) (net.IP, error) {
	const op = errors.Op("listeners.getIP")
	host := strings.TrimSpace(strings.ToLower(address))

	if host == "localhost" {
		return net.ParseIP("127.0.0.1"), nil
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return nil, errors.New(op).Msgf("invalid IP address: %s (expected IPv4 address or 'localhost')", address)
	}
	if ip.To4() == nil {
		return nil, errors.New(op).Msgf("IPv6 not supported: %s (use IPv4 address or 'localhost')", address)
	}
	return ip, nil
}

// safeDataPreview returns a safe, truncated preview of data for logging.
// If data is printable ASCII/UTF-8, returns the string (truncated if needed).
// If data contains binary/non-printable bytes, returns a hex preview.
func safeDataPreview(data []byte, maxLen int) string {
	if len(data) == 0 {
		return "<empty>"
	}

	// Check if data is printable
	if isPrintable(data) {
		if len(data) <= maxLen {
			return string(data)
		}
		return string(data[:maxLen]) + "..."
	}

	// Binary data: show hex preview
	previewLen := maxLen / 2 // hex encoding doubles length
	if len(data) > previewLen {
		return hex.EncodeToString(data[:previewLen]) + "... (binary)"
	}
	return hex.EncodeToString(data) + " (binary)"
}

// isPrintable checks if data consists of printable UTF-8 characters.
func isPrintable(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, r := range string(data) {
		if !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
