package listeners

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
