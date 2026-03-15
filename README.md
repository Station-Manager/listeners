# Station Manager: listeners package

This package contains the implementation of various listeners for the Station Manager application. Listeners are
responsible for handling incoming network connections, events and triggering appropriate actions within the system –
generally by logging a QSO.

## Available Listeners

- **TCPListener**: Listens for incoming TCP connections and processes QSO data.
- **UDPListener**: Listens for incoming UDP packets and logs QSO information.
- **WebSocketListener**: Handles WebSocket connections for real-time QSO updates.

