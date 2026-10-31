// Package turnsrv is the embedded TURN/STUN service for private-call P3
// (pion/turn/v5).
//
// Role: the private-call media plane is client-to-client P2P (tgcalls/WebRTC); the
// server acts as a relay only when NAT traversal fails. This package exposes just
// three things externally: the reachable service address (written into the
// phoneConnectionWebrtc entries of phoneCall.connections), the temporary
// credentials issued per call, and the on/off switch.
//
// Credentials follow the TURN REST spec (draft-uberti-behave-turn-rest-00, same as
// coturn): username = "<unix expiry>:<identity>", password =
// base64(HMAC-SHA1(secret, username)). This means switching to an external coturn
// in the future only requires sharing the secret, with zero changes on the
// signaling side.
package turnsrv

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"go.uber.org/zap"
)

// Config is the operating parameters of the embedded TURN.
type Config struct {
	// UDPPort is the TURN/STUN listening port (independent of the SFU port: both
	// must exclusively consume the STUN traffic on their own socket and cannot
	// share one). The Windows firewall must allow it.
	UDPPort int
	// AdvertiseIP is the client-reachable address written into
	// phoneConnectionWebrtc and used for relay allocations. ⚠ With 127.0.0.1 the
	// relay candidate a real device gets is unreachable (the media plane fails
	// silently).
	AdvertiseIP string
	// Realm is the realm of the TURN long-term credential (any stable string works).
	Realm string
	// SharedSecret is the HMAC key for REST credentials; when empty it is generated
	// randomly at process level (self-consistent for a single instance; multiple
	// instances / an external coturn must explicitly configure the same value).
	SharedSecret string
	// RelayMinPort/RelayMaxPort bound the UDP port range for relay allocations (the
	// firewall's allowed range).
	RelayMinPort int
	RelayMaxPort int
	// CredentialTTL is the validity period of issued credentials. ⚠ It must be >=
	// the maximum duration of a single call: libwebrtc TurnPort's allocation
	// refresh reuses the initial credentials, so an expired credential cuts off an
	// in-progress relayed call (conclusion from reading the TDesktop code);
	// default 24h.
	CredentialTTL time.Duration
	Logger        *zap.Logger
}

// Service is the TURN boundary visible to the signaling layer.
type Service interface {
	// Enabled reports whether relaying is actually available (when false,
	// connections can only deliver an empty list and behavior falls back to P1's
	// LAN direct connection).
	Enabled() bool
	// Credentials issues temporary credentials for one call (user is any identity;
	// the convention is callID).
	Credentials(user string) (username, password string, err error)
	// IP/Port return the client-reachable service address.
	IP() string
	Port() int
	Close() error
}

// Disabled returns the not-enabled implementation.
func Disabled() Service { return disabled{} }

type disabled struct{}

func (disabled) Enabled() bool { return false }
func (disabled) Credentials(string) (string, string, error) {
	return "", "", fmt.Errorf("turnsrv: disabled")
}
func (disabled) IP() string   { return "" }
func (disabled) Port() int    { return 0 }
func (disabled) Close() error { return nil }

type pionTURN struct {
	cfg    Config
	server *turn.Server
}

// New starts the embedded TURN: binds the UDP port and wires up REST credential
// verification and the relay port range.
func New(cfg Config) (Service, error) {
	if cfg.UDPPort <= 0 {
		return nil, fmt.Errorf("turnsrv: invalid udp port %d", cfg.UDPPort)
	}
	if cfg.AdvertiseIP == "" {
		cfg.AdvertiseIP = "127.0.0.1"
	}
	if cfg.Realm == "" {
		cfg.Realm = "telesrv"
	}
	if cfg.SharedSecret == "" {
		secret, err := randomSecret()
		if err != nil {
			return nil, err
		}
		cfg.SharedSecret = secret
	}
	if cfg.RelayMinPort <= 0 || cfg.RelayMaxPort < cfg.RelayMinPort {
		return nil, fmt.Errorf("turnsrv: invalid relay port range [%d,%d]", cfg.RelayMinPort, cfg.RelayMaxPort)
	}
	if cfg.CredentialTTL <= 0 {
		cfg.CredentialTTL = 24 * time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	relayIP := net.ParseIP(cfg.AdvertiseIP)
	if relayIP == nil {
		return nil, fmt.Errorf("turnsrv: invalid advertise ip %q", cfg.AdvertiseIP)
	}
	conn, err := net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", cfg.UDPPort))
	if err != nil {
		return nil, fmt.Errorf("turnsrv: listen udp %d: %w", cfg.UDPPort, err)
	}
	pionLog := logging.NewDefaultLoggerFactory()
	server, err := turn.NewServer(turn.ServerConfig{
		Realm:         cfg.Realm,
		LoggerFactory: pionLog,
		AuthHandler:   turn.LongTermTURNRESTAuthHandler(cfg.SharedSecret, pionLog.NewLogger("turn-auth")),
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: conn,
			RelayAddressGenerator: &turn.RelayAddressGeneratorPortRange{
				RelayAddress: relayIP,
				MinPort:      uint16(cfg.RelayMinPort),
				MaxPort:      uint16(cfg.RelayMaxPort),
				Address:      "0.0.0.0",
			},
		}},
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("turnsrv: new server: %w", err)
	}
	cfg.Logger.Info("turn listening",
		zap.Int("udp_port", cfg.UDPPort),
		zap.String("advertise_ip", cfg.AdvertiseIP),
		zap.Int("relay_min_port", cfg.RelayMinPort),
		zap.Int("relay_max_port", cfg.RelayMaxPort))
	if cfg.AdvertiseIP == "127.0.0.1" {
		cfg.Logger.Warn("TELESRV_TURN_ADVERTISE_IP is 127.0.0.1: the relay candidate a real device gets is unreachable (the cross-network media plane fails silently); cross-device calls must set it to a client-reachable IP")
	}
	return &pionTURN{cfg: cfg, server: server}, nil
}

func (t *pionTURN) Enabled() bool { return true }

func (t *pionTURN) Credentials(user string) (string, string, error) {
	username, password, err := turn.GenerateLongTermTURNRESTCredentials(t.cfg.SharedSecret, user, t.cfg.CredentialTTL)
	if err != nil {
		return "", "", fmt.Errorf("turnsrv: generate credentials: %w", err)
	}
	return username, password, nil
}

func (t *pionTURN) IP() string   { return t.cfg.AdvertiseIP }
func (t *pionTURN) Port() int    { return t.cfg.UDPPort }
func (t *pionTURN) Close() error { return t.server.Close() }

func randomSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("turnsrv: random secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
