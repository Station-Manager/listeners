package listeners

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Station-Manager/types"
)

const (
	defaultReadDeadlineMS = 500
)

func (s *Service) udpListener(run *runState, cfg types.ListenerConfig, ctx context.Context) {
	const op = "listeners.Service.udpListener"

	ip, err := getIP(cfg.Host)
	if err != nil {
		s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Str("host", cfg.Host).Msg("failed to resolve host")
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

	// Register connection for forceful shutdown
	connID := "udp:" + addr.String()
	run.registerConn(connID, conn)
	defer run.unregisterConn(connID)

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
		case <-run.shutdownChannel:
			s.Logger.InfoWith().Str("name", cfg.Name).Msg("UDP listener shutting down")
			return
		case <-ctx.Done():
			s.Logger.InfoWith().Str("name", cfg.Name).Msg("UDP listener context cancelled")
			return
		default:
			// Set a read deadline so we can check for shutdown periodically
			if err := conn.SetReadDeadline(time.Now().Add(defaultReadDeadlineMS * time.Millisecond)); err != nil {
				s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Msg("failed to set read deadline")
				continue
			}

			n, remoteAddr, err := conn.ReadFromUDP(buffer)
			if err != nil {
				// Check if it's a timeout (expected during normal operation)
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					continue
				}
				s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Msg("UDP read error")
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
	// Log a safe preview of the data (truncated, with indication if binary)
	preview := safeDataPreview(data, 64)

	s.Logger.DebugWith().
		Str("name", cfg.Name).
		Str("remote", remoteAddr.String()).
		Int("len", len(data)).
		Str("preview", preview).
		Msg("processing UDP packet")

	// TODO: Implement protocol-specific parsing (e.g., WSJT-X, N1MM, etc.)
	// Example: Parse ADIF, JSON, or other ham radio logging formats
}

func (s *Service) tcpListener(run *runState, cfg types.ListenerConfig, ctx context.Context) {
	const op = "listeners.Service.tcpListener"

	ip, err := getIP(cfg.Host)
	if err != nil {
		s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Str("host", cfg.Host).Msg("failed to resolve host")
		return
	}

	addr := &net.TCPAddr{
		IP:   ip,
		Port: cfg.Port,
	}

	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Str("address", addr.String()).Msg("failed to start TCP listener")
		return
	}

	// Register listener for forceful shutdown
	listenerID := "tcp-listener:" + addr.String()
	run.registerConn(listenerID, listener)
	defer run.unregisterConn(listenerID)

	defer func() {
		if cerr := listener.Close(); cerr != nil {
			s.Logger.ErrorWith().Err(cerr).Msg("failed to close TCP listener")
		}
	}()

	s.Logger.InfoWith().
		Str("name", cfg.Name).
		Str("address", addr.String()).
		Int("buffer_size", cfg.BufferSize).
		Msg("TCP listener started")

	// Track active connections for graceful shutdown
	var connWg sync.WaitGroup
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	// Connection counter for unique IDs
	var connCounter atomic.Uint64

	// Accept connections in a loop
	for {
		// Set accept deadline so we can check for shutdown periodically
		if err := listener.SetDeadline(time.Now().Add(defaultReadDeadlineMS * time.Millisecond)); err != nil {
			s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Msg("failed to set accept deadline")
			continue
		}

		select {
		case <-run.shutdownChannel:
			s.Logger.InfoWith().Str("name", cfg.Name).Msg("TCP listener shutting down, waiting for connections to close")
			connCancel()
			connWg.Wait()
			s.Logger.InfoWith().Str("name", cfg.Name).Msg("TCP listener shutdown complete")
			return
		case <-ctx.Done():
			s.Logger.InfoWith().Str("name", cfg.Name).Msg("TCP listener context cancelled")
			connCancel()
			connWg.Wait()
			return
		default:
			conn, err := listener.AcceptTCP()
			if err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					continue
				}
				// Check if we're shutting down (listener was closed)
				select {
				case <-run.shutdownChannel:
					return
				default:
				}
				s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Msg("TCP accept error")
				continue
			}

			// Register connection for forceful shutdown
			connNum := connCounter.Add(1)
			connID := fmt.Sprintf("tcp-conn:%s:%d", conn.RemoteAddr().String(), connNum)
			run.registerConn(connID, conn)

			connWg.Add(1)
			go func(c *net.TCPConn, id string) {
				defer connWg.Done()
				defer run.unregisterConn(id)
				s.handleTCPConnection(cfg, c, connCtx)
			}(conn, connID)
		}
	}
}

// handleTCPConnection handles a single TCP connection, reading data until the connection closes or context is cancelled.
func (s *Service) handleTCPConnection(cfg types.ListenerConfig, conn *net.TCPConn, ctx context.Context) {
	remoteAddr := conn.RemoteAddr().String()

	s.Logger.DebugWith().
		Str("name", cfg.Name).
		Str("remote", remoteAddr).
		Msg("TCP connection accepted")

	defer func() {
		if err := conn.Close(); err != nil {
			s.Logger.ErrorWith().Err(err).Str("remote", remoteAddr).Msg("failed to close TCP connection")
		}
		s.Logger.DebugWith().Str("name", cfg.Name).Str("remote", remoteAddr).Msg("TCP connection closed")
	}()

	buffer := make([]byte, cfg.BufferSize)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Set read deadline so we can check for context cancellation periodically
			if err := conn.SetReadDeadline(time.Now().Add(defaultReadDeadlineMS * time.Millisecond)); err != nil {
				s.Logger.ErrorWith().Err(err).Str("remote", remoteAddr).Msg("failed to set read deadline")
				return
			}

			n, err := conn.Read(buffer)
			if err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					continue
				}
				// Connection closed or other error
				if !errors.Is(err, net.ErrClosed) {
					s.Logger.DebugWith().Err(err).Str("remote", remoteAddr).Msg("TCP read ended")
				}
				return
			}

			if n == 0 {
				continue
			}

			// Copy the data to avoid buffer reuse issues
			data := make([]byte, n)
			copy(data, buffer[:n])

			s.Logger.DebugWith().
				Str("name", cfg.Name).
				Str("remote", remoteAddr).
				Int("bytes", n).
				Msg("TCP data received")

			// Process the received data
			s.handleTCPData(cfg, data, conn)
		}
	}
}

// handleTCPData processes incoming TCP data. Override or extend this for specific protocols.
func (s *Service) handleTCPData(cfg types.ListenerConfig, data []byte, conn *net.TCPConn) {
	remoteAddr := conn.RemoteAddr().String()

	// Log a safe preview of the data (truncated, with indication if binary)
	preview := safeDataPreview(data, 64)

	s.Logger.DebugWith().
		Str("name", cfg.Name).
		Str("remote", remoteAddr).
		Int("len", len(data)).
		Str("preview", preview).
		Msg("processing TCP data")

	// TODO: Implement protocol-specific parsing (e.g., N1MM, etc.)
	// Example: Parse ADIF, JSON, or other ham radio logging formats
}
