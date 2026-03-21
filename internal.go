package listeners

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/Station-Manager/types"
)

// listenerFunc is the signature for listener goroutine functions.
type listenerFunc func(run *runState, cfg types.ListenerConfig, ctx context.Context)

func (s *Service) launchListenerThread(run *runState, cfg types.ListenerConfig, ctx context.Context, fn listenerFunc, workerName string) {
	run.wg.Add(1)
	go func() {
		defer run.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				s.Logger.ErrorWith().
					Str("worker", workerName).
					Str("panic", fmt.Sprintf("%v", r)).
					Str("stack", string(debug.Stack())).
					Msg("Listener panicked")
			}
		}()
		s.Logger.InfoWith().Str("worker", workerName).Msg("Listener starting")
		fn(run, cfg, ctx)
		s.Logger.InfoWith().Str("worker", workerName).Msg("Listener stopped")
	}()
}

// registerConn adds a connection/listener to the active connections map for forceful shutdown.
func (run *runState) registerConn(id string, c closer) {
	run.connsMu.Lock()
	defer run.connsMu.Unlock()
	run.activeConns[id] = c
}

// unregisterConn removes a connection/listener from the active connections map.
func (run *runState) unregisterConn(id string) {
	run.connsMu.Lock()
	defer run.connsMu.Unlock()
	delete(run.activeConns, id)
}
