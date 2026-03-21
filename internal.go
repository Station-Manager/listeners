package listeners

import (
	"context"

	"github.com/Station-Manager/types"
)

func (s *Service) launchListenerThread(run *runState, cfg types.ListenerConfig, ctx context.Context, listenerFunc func(types.ListenerConfig, context.Context, <-chan struct{}), workerName string) {
	run.wg.Add(1)
	go func() {
		defer run.wg.Done()
		s.Logger.InfoWith().Str("worker", workerName).Msg("Listener starting")
		listenerFunc(cfg, ctx, run.shutdownChannel)
		s.Logger.InfoWith().Str("worker", workerName).Msg("Listener stopped")
	}()
}
