package listeners

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Station-Manager/listeners/handlers"
	"github.com/Station-Manager/types"
)

const (
	// defaultReadDeadlineMS is the polling interval for shutdown checks.
	// 500ms balances responsiveness with CPU overhead.
	// TODO: Consider making this configurable via ListenerConfig.
	defaultReadDeadlineMS = 500
)

func (s *Service) runUDPListener(ctx context.Context, run *runState, rl resolvedListener, conn *net.UDPConn, handler handlers.PacketHandler) {
	cfg := rl.config
	addr := rl.udpAddr

	// Register connection for forceful shutdown
	connID := "udp:" + addr.String()
	run.registerConn(connID, conn)
	defer run.unregisterConn(connID)

	defer func() {
		if cerr := conn.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			s.Logger.ErrorWith().Err(cerr).Str("name", cfg.Name).Msg("failed to close UDP listener")
		}
	}()

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
			if err := conn.SetReadDeadline(time.Now().Add(defaultReadDeadlineMS * time.Millisecond)); err != nil {
				s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Msg("failed to set read deadline")
				continue
			}

			n, remoteAddr, err := conn.ReadFromUDP(buffer)
			if err != nil {
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

			s.handleUDPPacket(cfg, data, remoteAddr, conn.LocalAddr(), handler)
		}
	}
}

// handleUDPPacket processes incoming UDP data.
// UDP is message-oriented, so each read is a complete datagram.
func (s *Service) handleUDPPacket(cfg types.ListenerConfig, data []byte, remoteAddr *net.UDPAddr, localAddr net.Addr, handler handlers.PacketHandler) {
	// Always log safe metadata (length, source)
	logEntry := s.Logger.DebugWith().
		Str("name", cfg.Name).
		Str("remote", remoteAddr.String()).
		Int("len", len(data))

	// Only log payload preview when explicitly enabled
	if cfg.LogPayload {
		logEntry = logEntry.Str("preview", safeDataPreview(data, 32))
	}

	logEntry.Msg("processing UDP packet")

	// If no handler configured, just log the packet
	if handler == nil {
		s.Logger.DebugWith().Str("name", cfg.Name).Msg("no handler configured, packet logged only")
		return
	}

	// Build packet and dispatch to handler
	pkt := handlers.Packet{
		Data:         data,
		RemoteAddr:   remoteAddr,
		LocalAddr:    localAddr,
		Protocol:     "udp",
		ListenerName: cfg.Name,
	}

	if err := handler.Handle(pkt); err != nil {
		s.Logger.ErrorWith().Err(err).
			Str("name", cfg.Name).
			Str("handler", handler.Name()).
			Msg("handler error processing packet")
	}
}

func (s *Service) runTCPListener(ctx context.Context, run *runState, rl resolvedListener, listener *net.TCPListener, handler handlers.PacketHandler) {
	cfg := rl.config
	addr := rl.tcpAddr

	// Register listener for forceful shutdown
	listenerID := "tcp-listener:" + addr.String()
	run.registerConn(listenerID, listener)
	defer run.unregisterConn(listenerID)

	defer func() {
		if cerr := listener.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			s.Logger.ErrorWith().Err(cerr).Str("name", cfg.Name).Msg("failed to close TCP listener")
		}
	}()

	s.Logger.InfoWith().
		Str("name", cfg.Name).
		Str("address", addr.String()).
		Int("buffer_size", cfg.BufferSize).
		Msg("TCP listener started")

	var connWg sync.WaitGroup
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	var connCounter atomic.Uint64

	for {
		if err := listener.SetDeadline(time.Now().Add(defaultReadDeadlineMS * time.Millisecond)); err != nil {
			s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Msg("failed to set accept deadline")
			continue
		}

		select {
		case <-run.shutdownChannel:
			s.Logger.InfoWith().Str("name", cfg.Name).Msg("TCP listener shutting down, waiting for connections")
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
				select {
				case <-run.shutdownChannel:
					return
				default:
				}
				s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Msg("TCP accept error")
				continue
			}

			connNum := connCounter.Add(1)
			connID := fmt.Sprintf("tcp-conn:%s:%d", conn.RemoteAddr().String(), connNum)
			run.registerConn(connID, conn)

			connWg.Add(1)
			go func(c *net.TCPConn, id string) {
				defer connWg.Done()
				defer run.unregisterConn(id)
				s.handleTCPConnection(connCtx, cfg, c, handler)
			}(conn, connID)
		}
	}
}

// handleTCPConnection handles a single TCP connection.
// NOTE: TCP is a stream protocol. This implementation treats each read as independent data,
// which works for simple line-based protocols but may need a framing layer for protocols
// with message boundaries (length-prefixed, delimiter-based, etc.).
func (s *Service) handleTCPConnection(ctx context.Context, cfg types.ListenerConfig, conn *net.TCPConn, handler handlers.PacketHandler) {
	remoteAddr := conn.RemoteAddr().String()

	s.Logger.DebugWith().
		Str("name", cfg.Name).
		Str("remote", remoteAddr).
		Msg("TCP connection accepted")

	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Str("remote", remoteAddr).Msg("failed to close TCP connection")
		}
		s.Logger.DebugWith().Str("name", cfg.Name).Str("remote", remoteAddr).Msg("TCP connection closed")
	}()

	buffer := make([]byte, cfg.BufferSize)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			if err := conn.SetReadDeadline(time.Now().Add(defaultReadDeadlineMS * time.Millisecond)); err != nil {
				s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Str("remote", remoteAddr).Msg("failed to set read deadline")
				return
			}

			n, err := conn.Read(buffer)
			if err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					continue
				}
				if !errors.Is(err, net.ErrClosed) {
					s.Logger.DebugWith().Err(err).Str("name", cfg.Name).Str("remote", remoteAddr).Msg("TCP read ended")
				}
				return
			}

			if n == 0 {
				continue
			}

			data := make([]byte, n)
			copy(data, buffer[:n])

			s.Logger.DebugWith().
				Str("name", cfg.Name).
				Str("remote", remoteAddr).
				Int("bytes", n).
				Msg("TCP data received")

			s.handleTCPData(cfg, data, conn, handler)
		}
	}
}

// handleTCPData processes incoming TCP data.
// See handleTCPConnection for notes on TCP framing.
func (s *Service) handleTCPData(cfg types.ListenerConfig, data []byte, conn *net.TCPConn, handler handlers.PacketHandler) {
	// Always log safe metadata (length, source)
	logEntry := s.Logger.DebugWith().
		Str("name", cfg.Name).
		Str("remote", conn.RemoteAddr().String()).
		Int("len", len(data))

	// Only log payload preview when explicitly enabled
	if cfg.LogPayload {
		logEntry = logEntry.Str("preview", safeDataPreview(data, 32))
	}

	logEntry.Msg("processing TCP data")

	// If no handler configured, just log the data
	if handler == nil {
		s.Logger.DebugWith().Str("name", cfg.Name).Msg("no handler configured, data logged only")
		return
	}

	// Build packet and dispatch to handler
	pkt := handlers.Packet{
		Data:         data,
		RemoteAddr:   conn.RemoteAddr(),
		LocalAddr:    conn.LocalAddr(),
		Protocol:     "tcp",
		ListenerName: cfg.Name,
	}

	if err := handler.Handle(pkt); err != nil {
		s.Logger.ErrorWith().Err(err).
			Str("name", cfg.Name).
			Str("handler", handler.Name()).
			Msg("handler error processing data")
	}
}
