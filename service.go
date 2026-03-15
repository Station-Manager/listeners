package listeners

import (
	"sync"

	"github.com/Station-Manager/config"
	"github.com/Station-Manager/errors"
	"github.com/Station-Manager/logging"
)

type Service struct {
	ConfigService *config.Service  `di.inject:"configservice"`
	Logger        *logging.Service `di.inject:"loggingservice"`

	initOnce sync.Once
}

func (s *Service) Initialize() error {
	const op errors.Op = "listeners.Service.Initialize"

	if s.ConfigService == nil {
		return errors.New(op).Msg("ConfigService is nil")
	}
	if s.Logger == nil {
		return errors.New(op).Msg("LoggerService is nil")
	}

	s.initOnce.Do(func() {

	})

	return nil
}
