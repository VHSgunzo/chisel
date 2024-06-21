package chserver

import (
	"net"
)

func (s *Server) listener(host, port string) (net.Listener, error) {
	//tcp listen
	l, err := net.Listen("tcp", host+":"+port)
	if err != nil {
		return nil, err
	}
	s.Infof("Listening on %s://%s:%s", "ws", host, port)
	return l, nil
}
