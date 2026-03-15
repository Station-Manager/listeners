package listeners

import (
	"net"
	"sync"
	"sync/atomic"

	"github.com/Station-Manager/config"
	"github.com/Station-Manager/errors"
	"github.com/Station-Manager/logging"
	"github.com/Station-Manager/types"
)

const (
	ServiceName = "WSJT-X UDP Listener"
)

type runState struct {
	shutdownChannel chan struct{}
	wg              sync.WaitGroup
}

type Service struct {
	ConfigService  *config.Service  `di.inject:"configservice"`
	Logger         *logging.Service `di.inject:"loggingservice"`
	ListnerConfigs []types.ListenerConfig

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
			if cfg.Enabled {
				s.ListnerConfigs = append(s.ListnerConfigs, cfg)
			}
		}

		s.initialized.Store(true)
	})

	return initErr
}

func (s *Service) Start() error {
	const op errors.Op = "listeners.Service.Start"
	if !s.initialized.Load() {
		return errors.New(op).Msg(ErrServiceNotInitialized)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started.Load() {
		return nil
	}

	return nil
}
