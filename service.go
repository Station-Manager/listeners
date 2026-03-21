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

	// Ensures shutdown is only triggered once
	stopOnce sync.Once
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
		var disabledCount int
		for _, cfg := range cfgs {
			if err = validateConfig(&cfg); err != nil {
				s.Logger.ErrorWith().Err(err).Str("name", cfg.Name).Msgf("Invalid listener configuration: %v", cfg)
				skippedCount++
				continue
			}
			if cfg.Enabled {
				s.ListenerConfigs = append(s.ListenerConfigs, cfg)
			} else {
				s.Logger.DebugWith().Str("name", cfg.Name).Msg("Listener disabled in configuration")
				disabledCount++
			}
		}

		if skippedCount > 0 {
			s.Logger.WarnWith().Int("skipped", skippedCount).Msg("Some listener configurations were invalid and skipped")
		}
		if disabledCount > 0 {
			s.Logger.InfoWith().Int("disabled", disabledCount).Msg("Some listeners are disabled in configuration")
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

	// Quick check without lock - will verify again under lock
	if s.started.Load() {
		s.Logger.InfoWith().Msg("Listeners already started, skipping start")
		return nil
	}

	if len(s.ListenerConfigs) < 1 {
		s.Logger.InfoWith().Msg("No enabled listeners configured, skipping start")
		return nil
	}

	// Phase 1: Validate and resolve all addresses OUTSIDE the mutex (slow operations)
	resolved, err := s.resolveAllListeners(op)
	if err != nil {
		return err
	}

	// Phase 2: Acquire lock for state transition
	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-check started under lock to prevent race
	if s.started.Load() {
		s.Logger.InfoWith().Msg("Listeners already started, skipping start")
		return nil
	}

	// Phase 3: Create runState and launch all listeners
	run := &runState{
		shutdownChannel: make(chan struct{}),
		activeConns:     make(map[string]closer),
	}

	// Assign currentRun BEFORE launching so workers can be reached for shutdown
	s.currentRun = run

	for _, rl := range resolved {
		switch rl.proto {
		case "udp":
			s.Logger.InfoWith().Str("name", rl.config.Name).Str("addr", rl.udpAddr.String()).Msg("Launching UDP listener")
			s.launchListenerThread(run, rl, ctx, s.udpListener)
		case "tcp":
			s.Logger.InfoWith().Str("name", rl.config.Name).Str("addr", rl.tcpAddr.String()).Msg("Launching TCP listener")
			s.launchListenerThread(run, rl, ctx, s.tcpListener)
		default:
			// Should be unreachable due to earlier validation, but guard against future changes
			s.Logger.ErrorWith().Str("protocol", rl.proto).Msg("BUG: unhandled protocol in launch phase")
		}
	}

	s.started.Store(true)
	s.Logger.InfoWith().Int("count", len(resolved)).Msg("All listeners started")

	return nil
}

// resolveAllListeners validates and resolves all listener addresses.
// This is done outside the mutex since it may involve slow DNS/network operations.
func (s *Service) resolveAllListeners(op errors.Op) ([]resolvedListener, error) {
	resolved := make([]resolvedListener, 0, len(s.ListenerConfigs))

	for _, cfg := range s.ListenerConfigs {
		ip, err := getIP(cfg.Host)
		if err != nil {
			return nil, errors.New(op).Err(err).Msgf("failed to resolve IP for listener %s: %s", cfg.Name, cfg.Host)
		}

		addrStr := fmt.Sprintf("%s:%d", ip.String(), cfg.Port)
		proto := strings.TrimSpace(strings.ToLower(cfg.Protocol))

		rl := resolvedListener{
			config: cfg,
			proto:  proto,
		}

		switch proto {
		case "udp":
			addr, err := net.ResolveUDPAddr("udp", addrStr)
			if err != nil {
				return nil, errors.New(op).Err(err).Msgf("failed to resolve UDP address for %s: %s", cfg.Name, addrStr)
			}
			rl.udpAddr = addr
		case "tcp":
			addr, err := net.ResolveTCPAddr("tcp", addrStr)
			if err != nil {
				return nil, errors.New(op).Err(err).Msgf("failed to resolve TCP address for %s: %s", cfg.Name, addrStr)
			}
			rl.tcpAddr = addr
		default:
			return nil, errors.New(op).Msgf("unsupported protocol for listener %s: %s", cfg.Name, cfg.Protocol)
		}

		resolved = append(resolved, rl)
	}

	return resolved, nil
}

func (s *Service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started.Load() {
		s.Logger.InfoWith().Msg("Listeners not started, skipping stop")
		return nil
	}

	run := s.currentRun
	if run == nil {
		s.started.Store(false)
		return nil
	}

	s.Logger.InfoWith().Msg("Stopping listeners...")

	// Use stopOnce to ensure shutdown channel is closed exactly once
	run.stopOnce.Do(func() {
		close(run.shutdownChannel)
	})

	// Copy active connections under lock, then close outside lock to avoid blocking unregister
	run.connsMu.Lock()
	connsToClose := make([]closer, 0, len(run.activeConns))
	for _, conn := range run.activeConns {
		connsToClose = append(connsToClose, conn)
	}
	// Clear the map to help GC and indicate shutdown state
	clear(run.activeConns)
	run.connsMu.Unlock()

	// Close connections outside the lock
	for _, conn := range connsToClose {
		if err := conn.Close(); err != nil {
			s.Logger.DebugWith().Err(err).Msg("Error closing connection during shutdown")
		}
	}

	// Wait for all goroutines to finish with a timeout using NewTimer
	done := make(chan struct{})
	go func() {
		run.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(stopTimeoutSeconds * time.Second)
	defer timer.Stop()

	select {
	case <-done:
		s.Logger.InfoWith().Msg("All listeners stopped gracefully")
	case <-timer.C:
		s.Logger.WarnWith().Msg("Timeout waiting for listeners to stop, some goroutines may still be running")
	}

	s.currentRun = nil
	s.started.Store(false)

	return nil
}
