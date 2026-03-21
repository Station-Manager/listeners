package listeners

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Station-Manager/config"
	"github.com/Station-Manager/errors"
	"github.com/Station-Manager/logging"
	"github.com/Station-Manager/types"
)

const (
	ServiceName        = "udp_listener"
	stopTimeoutSeconds = 5
)

// closer is any type that can be closed (net.Conn, net.Listener, etc.)
type closer interface {
	Close() error
}

type runState struct {
	shutdownChannel chan struct{}
	wg              sync.WaitGroup

	// Track active listeners/connections for forceful shutdown
	connsMu     sync.Mutex
	activeConns map[string]closer // key: unique identifier
}

type Service struct {
	ConfigService   *config.Service  `di.inject:"configservice"`
	Logger          *logging.Service `di.inject:"loggingservice"`
	ListenerConfigs []types.ListenerConfig

	initialized atomic.Bool
	started     atomic.Bool
	initOnce    sync.Once
	mu          sync.Mutex

	currentRun *runState
}

// resolvedListener holds validated config and resolved address for a listener.
type resolvedListener struct {
	config  types.ListenerConfig
	proto   string
	udpAddr *net.UDPAddr
	tcpAddr *net.TCPAddr
}

func (s *Service) Initialize() error {
	const op errors.Op = "listeners.Service.Initialize"

	var initErr error
	s.initOnce.Do(func() {
		if s.ConfigService == nil {
			initErr = errors.New(op).Msg(ErrNilConfigService)
			return
		}
		if s.Logger == nil {
			initErr = errors.New(op).Msg(ErrNilLoggerService)
			return
		}

		cfgs, err := s.ConfigService.ListenerConfigs()
		if err != nil {
			initErr = errors.New(op).Err(err).Msg("failed to load listener configurations")
			return
		}

		var skippedCount int
		for _, cfg := range cfgs {
			if err = validateConfig(&cfg); err != nil {
				s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Msgf("Invalid listener configuration: %v", cfg)
				skippedCount++
				continue
			}
			if cfg.Enabled {
				s.ListenerConfigs = append(s.ListenerConfigs, cfg)
			}
		}

		if skippedCount > 0 {
			s.Logger.WarnWith().Int("skipped", skippedCount).Msg("Some listener configurations were invalid and skipped")
		}

		s.initialized.Store(true)
	})

	return initErr
}

func (s *Service) Start(ctx context.Context) error {
	const op errors.Op = "listeners.Service.Start"
	if !s.initialized.Load() {
		return errors.New(op).Msg(ErrServiceNotInitialized)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started.Load() {
		s.Logger.InfoWith().Msg("Listeners already started, skipping start")
		return nil
	}

	if len(s.ListenerConfigs) < 1 {
		s.Logger.InfoWith().Msg("No enabled listeners configured, skipping start")
		return nil
	}

	// Phase 1: Validate all configs and resolve all addresses before launching anything
	resolved := make([]resolvedListener, 0, len(s.ListenerConfigs))
	for _, cfg := range s.ListenerConfigs {
		ip, err := getIP(cfg.Host)
		if err != nil {
			return errors.New(op).Err(err).Msgf("failed to resolve IP for listener: %s", cfg.Host)
		}

		addrStr := fmt.Sprintf("%s:%d", ip.String(), cfg.Port)
		proto := strings.ToLower(cfg.Protocol)

		rl := resolvedListener{
			config: cfg,
			proto:  proto,
		}

		switch proto {
		case "udp":
			addr, err := net.ResolveUDPAddr("udp", addrStr)
			if err != nil {
				return errors.New(op).Err(err).Msgf("failed to resolve UDP address: %s", addrStr)
			}
			rl.udpAddr = addr
		case "tcp":
			addr, err := net.ResolveTCPAddr("tcp", addrStr)
			if err != nil {
				return errors.New(op).Err(err).Msgf("failed to resolve TCP address: %s", addrStr)
			}
			rl.tcpAddr = addr
		default:
			return errors.New(op).Msgf("unsupported protocol for listener: %s", cfg.Protocol)
		}

		resolved = append(resolved, rl)
	}

	// Phase 2: All validations passed — now launch all listeners
	run := &runState{
		shutdownChannel: make(chan struct{}),
		activeConns:     make(map[string]closer),
	}

	for _, rl := range resolved {
		switch rl.proto {
		case "udp":
			s.Logger.InfoWith().Msgf("Launching UDP listener on %s", rl.udpAddr.String())
			s.launchListenerThread(run, rl.config, ctx, s.udpListener, fmt.Sprintf("udp_listener_%s", rl.udpAddr.String()))
		case "tcp":
			s.Logger.InfoWith().Msgf("Launching TCP listener on %s", rl.tcpAddr.String())
			s.launchListenerThread(run, rl.config, ctx, s.tcpListener, fmt.Sprintf("tcp_listener_%s", rl.tcpAddr.String()))
		default:
			// Should be unreachable due to earlier validation, but guard against future changes
			s.Logger.ErrorWith().Str("protocol", rl.proto).Msg("BUG: unhandled protocol in launch phase")
		}
	}

	// Only expose runState after all listeners are launched
	s.currentRun = run
	s.started.Store(true)
	s.Logger.InfoWith().Msgf("Started %d listener(s)", len(resolved))

	return nil
}

func (s *Service) Stop() error {
	const op errors.Op = "listeners.Service.Stop"

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started.Load() {
		s.Logger.InfoWith().Msg("Listeners not started, skipping stop")
		return nil
	}

	run := s.currentRun
	if run == nil {
		return nil
	}

	s.Logger.InfoWith().Msg("Stopping listeners...")

	// Signal all listener goroutines to shutdown
	close(run.shutdownChannel)

	// Forcefully close all active connections to unblock any waiting goroutines
	run.connsMu.Lock()
	for id, conn := range run.activeConns {
		s.Logger.DebugWith().Str("conn", id).Msg("Forcefully closing connection")
		if err := conn.Close(); err != nil {
			s.Logger.DebugWith().Err(err).Str("conn", id).Msg("Error closing connection during shutdown")
		}
	}
	run.connsMu.Unlock()

	// Wait for all goroutines to finish with a timeout
	done := make(chan struct{})
	go func() {
		run.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.Logger.InfoWith().Msg("All listeners stopped gracefully")
	case <-time.After(stopTimeoutSeconds * time.Second):
		s.Logger.WarnWith().Msg("Timeout waiting for listeners to stop, some goroutines may still be running")
		// Continue anyway - we've done what we can
	}

	s.currentRun = nil
	s.started.Store(false)

	return nil
}
