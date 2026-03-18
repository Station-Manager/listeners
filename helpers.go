package listeners

import (
	"net"
	"strings"

	"github.com/Station-Manager/errors"
)

func getIP(address string) (net.IP, error) {
	const op = errors.Op("listeners.getIP")
	host := strings.ToLower(address)
	var ip net.IP
	if host == "localhost" {
		return net.ParseIP("127.0.0.1"), nil
	}
	ip = net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return nil, errors.New(op).Msgf("invalid IP address for listener: %s", address)
	}
	return ip, nil
}
