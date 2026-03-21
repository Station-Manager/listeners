package listeners

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/Station-Manager/types"
)

const (
	defaultReadDeadlineMS = 500
)

func (s *Service) udpListener(cfg types.ListenerConfig, ctx context.Context, shutdown <-chan struct{}) {
	const op = "listeners.Service.udpListener"

	ip, err := getIP(cfg.Host)
	if err != nil {
		s.Logger.ErrorWith().Err(err).Str("host", cfg.Host).Msg("failed to resolve host")
		return
	}

	addr := &net.UDPAddr{
		IP:   ip,
		Port: cfg.Port,
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		s.Logger.ErrorWith().Err(err).Str("address", addr.String()).Msg("failed to start UDP listener")
		return
	}
	defer func(conn *net.UDPConn) {
		cerr := conn.Close()
		if cerr != nil {
			s.Logger.ErrorWith().Err(cerr).Msg("failed to close UDP listener")
		}
	}(conn)

	s.Logger.InfoWith().
		Str("name", cfg.Name).
		Str("address", addr.String()).
		Int("buffer_size", cfg.BufferSize).
		Msg("UDP listener started")

	buffer := make([]byte, cfg.BufferSize)

	for {
		select {
		case <-shutdown:
			s.Logger.InfoWith().Str("name", cfg.Name).Msg("UDP listener shutting down")
			return
		case <-ctx.Done():
			s.Logger.InfoWith().Str("name", cfg.Name).Msg("UDP listener context cancelled")
			return
		default:
			// Set a read deadline so we can check for shutdown periodically
			if err := conn.SetReadDeadline(time.Now().Add(defaultReadDeadlineMS * time.Millisecond)); err != nil {
				s.Logger.ErrorWith().Err(err).Msg("failed to set read deadline")
				continue
			}

			n, remoteAddr, err := conn.ReadFromUDP(buffer)
			if err != nil {
				// Check if it's a timeout (expected during normal operation)
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					continue
				}
				s.Logger.ErrorWith().Err(err).Msg("UDP read error")
				continue
			}

			if n == 0 {
				continue
			}

			// Copy the data to avoid buffer reuse issues
			data := make([]byte, n)
			copy(data, buffer[:n])

			s.Logger.DebugWith().
				Str("name", cfg.Name).
				Str("remote", remoteAddr.String()).
				Int("bytes", n).
				Msg("UDP packet received")

			// Process the received data
			s.handleUDPPacket(cfg, data, remoteAddr)
		}
	}
}

// handleUDPPacket processes incoming UDP data. Override or extend this for specific protocols.
func (s *Service) handleUDPPacket(cfg types.ListenerConfig, data []byte, remoteAddr *net.UDPAddr) {
	// Log the raw data for debugging
	s.Logger.DebugWith().
		Str("name", cfg.Name).
		Str("remote", remoteAddr.String()).
		Str("data", string(data)).
		Msg("processing UDP packet")

	// TODO: Implement protocol-specific parsing (e.g., WSJT-X, N1MM, etc.)
	// Example: Parse ADIF, JSON, or other ham radio logging formats
}

func (s *Service) tcpListener(cfg types.ListenerConfig, ctx context.Context, shutdown <-chan struct{}) {
	return
}
