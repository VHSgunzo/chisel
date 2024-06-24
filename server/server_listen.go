package chserver

import (
	"net"
)

func (s *Server) listener(proto, address string) (net.Listener, error) {
	//tcp listen
	l, err := net.Listen(proto, address)
	if err != nil {
		return nil, err
	}
	if proto == "unix" {
		s.Infof("Listening on %s:%s", proto, address)
	} else {
		s.Infof("Listening on %s://%s", "ws", address)
	}
	return l, nil
}
