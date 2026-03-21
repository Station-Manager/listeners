package listeners

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Station-Manager/config"
	"github.com/Station-Manager/errors"
	"github.com/Station-Manager/logging"
	"github.com/Station-Manager/types"
)

const (
	ServiceName = "udp_listener"
)

type runState struct {
	shutdownChannel chan struct{}
	wg              sync.WaitGroup
}

type Service struct {
	ConfigService   *config.Service  `di.inject:"configservice"`
	Logger          *logging.Service `di.inject:"loggingservice"`
	ListenerConfigs []types.ListenerConfig

	initialized atomic.Bool
	started     atomic.Bool
	initOnce    sync.Once
	mu          sync.Mutex

	udpConn *net.UDPConn
	tcpConn *net.TCPConn

	currentRun *runState
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
			initErr = err
			return
		}

		for _, cfg := range cfgs {
			if err = validateConfig(&cfg); err != nil {
				s.Logger.ErrorWith().Err(err).Msgf("Invalid listener configuration: %v", cfg)
				continue
			}
			if cfg.Enabled {
				s.ListenerConfigs = append(s.ListenerConfigs, cfg)
			}
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

	run := &runState{
		shutdownChannel: make(chan struct{}),
	}
	s.currentRun = run

	for _, l := range s.ListenerConfigs {
		ip, err := getIP(l.Host)
		if err != nil {
			return errors.New(op).Msgf("failed to get IP for listener: %s", l.Host)
		}
		proto := strings.ToLower(l.Protocol)
		switch proto {
		case "udp":
			addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ip.String(), l.Port))
			if err != nil {
				return errors.New(op).Msgf("failed to resolve UDP address for listener: %s", l.Host)
			}
			s.launchListenerThread(run, l, ctx, s.udpListener, fmt.Sprintf("udp_listener_%s", addr.String()))
		case "tcp":
		default:
			return errors.New(op).Msgf("unsupported protocol for listener: %s", l.Protocol)
		}
	}

	s.started.Store(true)

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

	// Wait for all goroutines to finish
	run.wg.Wait()

	s.currentRun = nil
	s.started.Store(false)

	s.Logger.InfoWith().Msg("All listeners stopped")
	return nil
}
