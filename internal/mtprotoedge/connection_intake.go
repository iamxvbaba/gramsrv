package mtprotoedge

import (
	"net"
	"time"

	"go.uber.org/zap"
)

type connectionIntakeEvent struct {
	stage     string
	outcome   string
	transport string
	remote    string
	local     string
	duration  time.Duration
	bytes     int
	err       error
}

func (s *Server) signalServing(addr net.Addr) {
	if s.onServing != nil {
		s.onServing(addr)
	}
}

func (s *Server) observeConnectionIntake(event connectionIntakeEvent) {
	fields := []zap.Field{
		zap.String("phase", event.stage),
		zap.String("outcome", event.outcome),
	}
	if event.transport != "" {
		fields = append(fields, zap.String("transport", event.transport))
	}
	if event.remote != "" {
		fields = append(fields, zap.String("remote_addr", event.remote))
	}
	if event.local != "" {
		fields = append(fields, zap.String("local_addr", event.local))
	}
	if event.duration > 0 {
		fields = append(fields, zap.Duration("duration", event.duration))
	}
	if event.bytes > 0 {
		fields = append(fields, zap.Int("bytes", event.bytes))
	}
	if event.err != nil {
		fields = append(fields, zap.Error(event.err))
	}
	s.log.Debug("Connection intake", fields...)
	if metrics, ok := s.metrics.(ConnectionIntakeMetrics); ok {
		metrics.ConnectionIntake(event.stage, event.outcome, event.duration)
	}
}

func (s *Server) observeRawAccepts(ln net.Listener) net.Listener {
	return &connectionObservedListener{Listener: ln, observe: s.observeConnectionIntake}
}

type connectionObservedListener struct {
	net.Listener
	observe func(connectionIntakeEvent)
}

func (l *connectionObservedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.observe(connectionIntakeEvent{
		stage: "raw_accept", outcome: "accepted", remote: connRemote(conn), local: connLocal(conn),
	})
	return conn, nil
}

func connRemote(conn net.Conn) string {
	if conn == nil || conn.RemoteAddr() == nil {
		return ""
	}
	return conn.RemoteAddr().String()
}

func connLocal(conn net.Conn) string {
	if conn == nil || conn.LocalAddr() == nil {
		return ""
	}
	return conn.LocalAddr().String()
}

func intakeTransport(obfuscated bool) string {
	if obfuscated {
		return "obfuscated_tcp"
	}
	return "tcp"
}
