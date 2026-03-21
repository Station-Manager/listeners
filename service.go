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
	"github.com/go-playground/validator/v10"
)

const (
	ServiceName        = "listeners"
	stopTimeoutSeconds = 5
)

// serviceState represents the current lifecycle state of the service.
type serviceState int32

const (
	stateUninitialized serviceState = iota
	stateInitialized
	stateStarting
	stateRunning
	stateStopping
	stateStopped
)

func (s serviceState) String() string {
	switch s {
	case stateUninitialized:
		return "uninitialized"
	case stateInitialized:
		return "initialized"
	case stateStarting:
		return "starting"
	case stateRunning:
		return "running"
	case stateStopping:
		return "stopping"
	case stateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// closer is any type that can be closed (net.Conn, net.Listener, etc.)
type closer interface {
	Close() error
}

// runState holds the runtime state for active listeners.
// This struct is disposable: after Stop() completes, no further operations
// should be performed on it and it will be garbage collected.
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

	// Lifecycle state machine
	state    atomic.Int32 // holds serviceState
	initOnce sync.Once
	mu       sync.Mutex // protects state transitions and currentRun

	currentRun *runState

	// Validator instance owned by Service (not global)
	validate *validator.Validate
}

// resolvedListener holds validated config and resolved address for a listener.
type resolvedListener struct {
	config  types.ListenerConfig
	proto   string
	udpAddr *net.UDPAddr
	tcpAddr *net.TCPAddr
}

func (s *Service) getState() serviceState {
	return serviceState(s.state.Load())
}

func (s *Service) setState(state serviceState) {
	s.state.Store(int32(state))
}

// compareAndSwapState atomically changes state from old to new.
// Returns true if the swap succeeded.
func (s *Service) compareAndSwapState(old, new serviceState) bool {
	return s.state.CompareAndSwap(int32(old), int32(new))
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

		// Create validator instance owned by Service
		s.validate = validator.New(validator.WithRequiredStructEnabled())

		cfgs, err := s.ConfigService.ListenerConfigs()
		if err != nil {
			initErr = errors.New(op).Err(err).Msg("failed to load listener configurations")
			return
		}

		var skippedCount int
		var disabledCount int
		for _, cfg := range cfgs {
			if err = s.validateConfig(&cfg); err != nil {
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

		s.setState(stateInitialized)
	})

	return initErr
}

// validateConfig validates a listener configuration using the Service's validator.
func (s *Service) validateConfig(cfg *types.ListenerConfig) error {
	if cfg == nil {
		return fmt.Errorf("listener config is nil")
	}
	return s.validate.Struct(cfg)
}

func (s *Service) Start(ctx context.Context) error {
	const op errors.Op = "listeners.Service.Start"

	// Atomically transition from initialized -> starting
	if !s.compareAndSwapState(stateInitialized, stateStarting) {
		currentState := s.getState()
		switch currentState {
		case stateUninitialized:
			return errors.New(op).Msg(ErrServiceNotInitialized)
		case stateStarting:
			return errors.New(op).Msg("service is already starting")
		case stateRunning:
			s.Logger.InfoWith().Msg("Listeners already running, skipping start")
			return nil
		case stateStopping:
			return errors.New(op).Msg("service is stopping, cannot start")
		case stateStopped:
			return errors.New(op).Msg("service is stopped, reinitialize to start again")
		default:
			return errors.New(op).Msgf("invalid service state: %s", currentState)
		}
	}

	// From here, state is "starting". All exit paths must set a valid end state.
	if len(s.ListenerConfigs) < 1 {
		s.Logger.InfoWith().Msg("No enabled listeners configured, skipping start")
		s.setState(stateInitialized)
		return nil
	}

	// Phase 1: Normalize and validate configs (no side effects)
	normalized, err := s.normalizeConfigs(op)
	if err != nil {
		s.setState(stateInitialized)
		return err
	}

	// Phase 2: Resolve all addresses (may involve DNS, no side effects)
	resolved, err := s.resolveAddresses(op, normalized)
	if err != nil {
		s.setState(stateInitialized)
		return err
	}

	// Phase 3: Create runState and launch listeners (transactional)
	s.mu.Lock()
	defer s.mu.Unlock()

	run := &runState{
		shutdownChannel: make(chan struct{}),
		activeConns:     make(map[string]closer),
	}

	// Launch all listeners. Track success so we can rollback if needed.
	launchErr := s.launchAllListeners(run, resolved, ctx)
	if launchErr != nil {
		// Rollback: shutdown any goroutines that were started
		s.rollbackStart(run)
		s.setState(stateInitialized)
		return errors.New(op).Err(launchErr).Msg("failed to launch listeners, rolled back")
	}

	// Success: commit the runState and transition to running
	s.currentRun = run
	s.setState(stateRunning)
	s.Logger.InfoWith().Int("count", len(resolved)).Msg("All listeners started")

	return nil
}

// launchAllListeners launches all listener goroutines.
// Returns an error if any launch fails (currently not possible, but future-proofed).
// The caller must handle rollback if this returns an error.
func (s *Service) launchAllListeners(run *runState, resolved []resolvedListener, ctx context.Context) error {
	for _, rl := range resolved {
		switch rl.proto {
		case "udp":
			s.Logger.InfoWith().Str("name", rl.config.Name).Str("addr", rl.udpAddr.String()).Msg("Launching UDP listener")
			s.launchListenerThread(run, rl, ctx, s.udpListener)
		case "tcp":
			s.Logger.InfoWith().Str("name", rl.config.Name).Str("addr", rl.tcpAddr.String()).Msg("Launching TCP listener")
			s.launchListenerThread(run, rl, ctx, s.tcpListener)
		default:
			// This should be unreachable due to normalizeConfigs validation
			return fmt.Errorf("BUG: unhandled protocol in launch phase: %s", rl.proto)
		}
	}
	return nil
}

// rollbackStart performs cleanup when Start() fails after launching some goroutines.
// It signals shutdown and waits for all started goroutines to exit.
func (s *Service) rollbackStart(run *runState) {
	s.Logger.WarnWith().Msg("Rolling back partial startup...")

	// Signal shutdown
	run.stopOnce.Do(func() {
		close(run.shutdownChannel)
	})

	// Close any connections that were registered
	run.connsMu.Lock()
	for _, conn := range run.activeConns {
		_ = conn.Close()
	}
	clear(run.activeConns)
	run.connsMu.Unlock()

	// Wait for goroutines with timeout
	done := make(chan struct{})
	go func() {
		run.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(stopTimeoutSeconds * time.Second)
	defer timer.Stop()

	select {
	case <-done:
		s.Logger.InfoWith().Msg("Rollback complete, all goroutines stopped")
	case <-timer.C:
		s.Logger.WarnWith().Msg("Rollback timeout, some goroutines may still be running")
	}
}

// normalizeConfigs prepares configs for resolution by normalizing protocol and host values.
func (s *Service) normalizeConfigs(op errors.Op) ([]types.ListenerConfig, error) {
	normalized := make([]types.ListenerConfig, 0, len(s.ListenerConfigs))
	for _, cfg := range s.ListenerConfigs {
		cfg.Protocol = strings.TrimSpace(strings.ToLower(cfg.Protocol))
		cfg.Host = strings.TrimSpace(cfg.Host)

		// Validate protocol is supported
		switch cfg.Protocol {
		case "udp", "tcp":
			// OK
		default:
			return nil, errors.New(op).Msgf("unsupported protocol for listener %s: %s", cfg.Name, cfg.Protocol)
		}

		normalized = append(normalized, cfg)
	}
	return normalized, nil
}

// resolveAddresses resolves network addresses for all normalized configs.
// This may involve DNS lookups and should be called outside any mutex.
func (s *Service) resolveAddresses(op errors.Op, configs []types.ListenerConfig) ([]resolvedListener, error) {
	resolved := make([]resolvedListener, 0, len(configs))

	for _, cfg := range configs {
		ip, err := getIP(cfg.Host)
		if err != nil {
			return nil, errors.New(op).Err(err).Msgf("failed to resolve IP for listener %s: %s", cfg.Name, cfg.Host)
		}

		addrStr := fmt.Sprintf("%s:%d", ip.String(), cfg.Port)

		rl := resolvedListener{
			config: cfg,
			proto:  cfg.Protocol,
		}

		switch cfg.Protocol {
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
		}

		resolved = append(resolved, rl)
	}

	return resolved, nil
}

func (s *Service) Stop() error {
	const op errors.Op = "listeners.Service.Stop"

	s.mu.Lock()
	defer s.mu.Unlock()

	currentState := s.getState()
	switch currentState {
	case stateUninitialized, stateInitialized:
		s.Logger.InfoWith().Msg("Listeners not started, skipping stop")
		return nil
	case stateStarting:
		// Wait briefly for startup to complete, or force stop
		s.Logger.WarnWith().Msg("Stop called during startup, attempting to stop")
	case stateRunning:
		// Normal case
	case stateStopping:
		s.Logger.InfoWith().Msg("Already stopping, skipping")
		return nil
	case stateStopped:
		s.Logger.InfoWith().Msg("Already stopped")
		return nil
	}

	s.setState(stateStopping)

	run := s.currentRun
	if run == nil {
		s.setState(stateStopped)
		return nil
	}

	s.Logger.InfoWith().Msg("Stopping listeners...")

	// Use stopOnce to ensure shutdown channel is closed exactly once
	run.stopOnce.Do(func() {
		close(run.shutdownChannel)
	})

	// Copy active connections under lock, then close outside lock.
	// After this point, runState is considered disposed and no further
	// register/unregister operations should be expected.
	run.connsMu.Lock()
	connsToClose := make([]closer, 0, len(run.activeConns))
	for _, conn := range run.activeConns {
		connsToClose = append(connsToClose, conn)
	}
	// Clear the map - runState is disposable after stop
	clear(run.activeConns)
	run.connsMu.Unlock()

	// Close connections outside the lock
	for _, conn := range connsToClose {
		if err := conn.Close(); err != nil {
			s.Logger.DebugWith().Err(err).Msg("Error closing connection during shutdown")
		}
	}

	// Wait for all goroutines to finish with a timeout
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
	s.setState(stateStopped)

	return nil
}

// State returns the current service state (for testing/debugging).
func (s *Service) State() string {
	return s.getState().String()
}
