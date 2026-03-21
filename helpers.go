package listeners

import (
	"encoding/hex"
	"net"
	"strings"

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
// Uses ASCII-only for network protocol data to avoid confusing Unicode output.
// If data contains non-printable ASCII, returns a hex preview.
func safeDataPreview(data []byte, maxLen int) string {
	if len(data) == 0 {
		return "<empty>"
	}

	// Check if data is printable ASCII
	if isPrintableASCII(data) {
		if len(data) <= maxLen {
			return string(data)
		}
		return string(data[:maxLen]) + "..."
	}

	// Binary data: show hex preview
	previewLen := maxLen / 2 // hex encoding doubles length
	if len(data) > previewLen {
		return hex.EncodeToString(data[:previewLen]) + "...(bin)"
	}
	return hex.EncodeToString(data) + "(bin)"
}

// isPrintableASCII checks if data consists only of printable ASCII characters.
// This is more restrictive than isPrintable but safer for network protocol logging.
func isPrintableASCII(data []byte) bool {
	for _, b := range data {
		// Printable ASCII range: 0x20 (space) to 0x7E (~), plus common whitespace
		if b < 0x20 || b > 0x7E {
			// Allow tab, newline, carriage return
			if b != '\t' && b != '\n' && b != '\r' {
				return false
			}
		}
	}
	return true
}
