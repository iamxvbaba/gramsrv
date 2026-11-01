// Package turnsrv is the TURN/STUN media and signaling boundary for private calls P3
// (pion/turn/v5).
//
// Roles: the private call media plane is P2P between clients (tgcalls/WebRTC); the server acts as
// a relay only when NAT traversal fails. The SFU role owns the UDP listener/relay allocations; the
// Core role only uses the same shared secret to issue temporary phoneCall.connections credentials
// and holds no media socket.
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

// Config holds the runtime parameters of the TURN credential/listener boundary.
type Config struct {
	// UDPPort is the TURN/STUN listening port (independent of the SFU port: both
	// must exclusively consume the STUN traffic on their own socket and cannot
	// share one). The Windows firewall must allow it.
	UDPPort int
	// BindIP is the local IPv4 address used by both the TURN listener and relay
	// allocation sockets. It matters with host networking because Docker no
	// longer applies a host_ip publishing boundary.
	BindIP string
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
	*credentialIssuer
	server *turn.Server
}

type credentialIssuer struct {
	cfg Config
}

// NewCredentialIssuer creates the Core-side signaling projection without
// opening a TURN listener. A stable shared secret is mandatory because the SFU
// role validates these credentials in a different process.
func NewCredentialIssuer(cfg Config) (Service, error) {
	if cfg.SharedSecret == "" {
		return nil, fmt.Errorf("turnsrv: shared secret is required for split credential issuer")
	}
	normalized, err := normalizeCredentialConfig(cfg, false)
	if err != nil {
		return nil, err
	}
	return &credentialIssuer{cfg: normalized}, nil
}

// New starts the SFU-owned TURN media listener and relay allocator.
func New(cfg Config) (Service, error) {
	var err error
	cfg, err = normalizeCredentialConfig(cfg, true)
	if err != nil {
		return nil, err
	}
	if cfg.Realm == "" {
		cfg.Realm = "telesrv"
	}
	if cfg.RelayMinPort <= 0 || cfg.RelayMaxPort < cfg.RelayMinPort {
		return nil, fmt.Errorf("turnsrv: invalid relay port range [%d,%d]", cfg.RelayMinPort, cfg.RelayMaxPort)
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	relayIP := net.ParseIP(cfg.AdvertiseIP)
	if relayIP == nil {
		return nil, fmt.Errorf("turnsrv: invalid advertise ip %q", cfg.AdvertiseIP)
	}
	conn, err := net.ListenPacket("udp4", net.JoinHostPort(cfg.BindIP, fmt.Sprint(cfg.UDPPort)))
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
				Address:      cfg.BindIP,
			},
		}},
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("turnsrv: new server: %w", err)
	}
	cfg.Logger.Info("turn listening",
		zap.Int("udp_port", cfg.UDPPort),
		zap.String("bind_ip", cfg.BindIP),
		zap.String("advertise_ip", cfg.AdvertiseIP),
		zap.Int("relay_min_port", cfg.RelayMinPort),
		zap.Int("relay_max_port", cfg.RelayMaxPort))
	if cfg.AdvertiseIP == "127.0.0.1" {
		cfg.Logger.Warn("TELESRV_TURN_ADVERTISE_IP is 127.0.0.1: the relay candidate a real device gets is unreachable (the cross-network media plane fails silently); cross-device calls must set it to a client-reachable IP")
	}
	return &pionTURN{credentialIssuer: &credentialIssuer{cfg: cfg}, server: server}, nil
}

func normalizeCredentialConfig(cfg Config, allowRandomSecret bool) (Config, error) {
	if cfg.UDPPort <= 0 {
		return Config{}, fmt.Errorf("turnsrv: invalid udp port %d", cfg.UDPPort)
	}
	if cfg.AdvertiseIP == "" {
		cfg.AdvertiseIP = "127.0.0.1"
	}
	if net.ParseIP(cfg.AdvertiseIP) == nil {
		return Config{}, fmt.Errorf("turnsrv: invalid advertise ip %q", cfg.AdvertiseIP)
	}
	if cfg.BindIP == "" {
		cfg.BindIP = "0.0.0.0"
	}
	bindIP := net.ParseIP(cfg.BindIP)
	if bindIP == nil || bindIP.To4() == nil {
		return Config{}, fmt.Errorf("turnsrv: invalid IPv4 bind ip %q", cfg.BindIP)
	}
	if cfg.SharedSecret == "" {
		if !allowRandomSecret {
			return Config{}, fmt.Errorf("turnsrv: shared secret is required")
		}
		secret, err := randomSecret()
		if err != nil {
			return Config{}, err
		}
		cfg.SharedSecret = secret
	}
	if cfg.CredentialTTL <= 0 {
		cfg.CredentialTTL = 24 * time.Hour
	}
	return cfg, nil
}

func (t *credentialIssuer) Enabled() bool { return true }

func (t *credentialIssuer) Credentials(user string) (string, string, error) {
	username, password, err := turn.GenerateLongTermTURNRESTCredentials(t.cfg.SharedSecret, user, t.cfg.CredentialTTL)
	if err != nil {
		return "", "", fmt.Errorf("turnsrv: generate credentials: %w", err)
	}
	return username, password, nil
}

func (t *credentialIssuer) IP() string   { return t.cfg.AdvertiseIP }
func (t *credentialIssuer) Port() int    { return t.cfg.UDPPort }
func (t *credentialIssuer) Close() error { return nil }

func (t *pionTURN) Close() error { return t.server.Close() }

func randomSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("turnsrv: random secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
