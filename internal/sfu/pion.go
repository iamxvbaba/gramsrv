package sfu

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/datachannel"
	"github.com/pion/dtls/v3"
	dtlsnet "github.com/pion/dtls/v3/pkg/net"
	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/sctp"
	"github.com/pion/srtp/v3"
	"go.uber.org/zap"
)

// PionConfig holds the runtime parameters of the embedded SFU.
type PionConfig struct {
	// UDPPort is the single UDP listening port (pion ICE UDPMux, shared by all endpoints).
	UDPPort int
	// AdvertiseIP is the client-reachable address written into downstream candidates.
	// ⚠ 127.0.0.1 makes real-device ICE never connect and produces no RPC error at all
	// (a pure media-plane silent failure).
	AdvertiseIP string
	Logger      *zap.Logger
	// Touch is the media-plane liveness reporting hook: for endpoints still alive
	// (recent SRTP receive / ICE connectivity) it periodically refreshes
	// group_call_participants.last_check_date, so the sweeper's single watermark carries
	// both "heartbeat ∨ media activity" (the implementation of the P0-2 double-expiry
	// criterion).
	Touch func(callID, userID int64)
	// LivenessInterval is the liveness reporting period (default 15s, far below the sweeper's 45s TTL).
	LivenessInterval time.Duration
	// ActivityWindow is the most-recent-receive window used to judge an endpoint's media plane alive (default 25s).
	ActivityWindow time.Duration
}

// pionSFU is the pion implementation of Service: full-ICE CONTROLLING + active DTLS
// handshake (client role, corresponding to downstream setup:"active") + verbatim
// in-room SRTP forwarding (no SSRC rewriting, preserving RTP extensions such as
// audio-level; the speaking indication is rendered by the client).
type pionSFU struct {
	cfg         PionConfig
	log         *zap.Logger
	udpConn     net.PacketConn
	mux         *ice.UDPMuxDefault
	cert        tls.Certificate
	fingerprint string

	mu    sync.Mutex
	rooms map[int64]*room

	cancel context.CancelFunc
}

type epKey struct {
	userID int64
	kind   EndpointKind
}

type room struct {
	callID    int64
	endpoints map[epKey]*endpoint
}

// mediaPlan is the ssrc forwarding plan of a publishing endpoint (computed from
// ssrc-groups at Join): the subscriber creates a decode sink only for SIM[0] (the lowest
// layer) — only that layer and its FID RTX partner are forwarded, while the remaining
// simulcast layers are dropped at the SFU (the client would drop them anyway, so
// forwarding only wastes downlink).
type mediaPlan struct {
	audioSSRC uint32
	forward   map[uint32]bool // forwarded video ssrcs (SIM[0] layer + its RTX)
	drop      map[uint32]bool // the remaining declared video ssrcs (higher layers and their RTX)
}

func buildMediaPlan(offer ClientOffer) mediaPlan {
	plan := mediaPlan{
		audioSSRC: offer.AudioSSRC,
		forward:   make(map[uint32]bool),
		drop:      make(map[uint32]bool),
	}
	var primary uint32
	for _, g := range offer.SsrcGroups {
		if g.Semantics == "SIM" && len(g.Sources) > 0 {
			primary = g.Sources[0]
			break
		}
	}
	// Single-layer publish (conference mode) has no SIM group: the first ssrc of the sole group is the primary stream.
	if primary == 0 && len(offer.SsrcGroups) == 1 && len(offer.SsrcGroups[0].Sources) > 0 {
		primary = offer.SsrcGroups[0].Sources[0]
	}
	for _, g := range offer.SsrcGroups {
		for i, ssrc := range g.Sources {
			keep := ssrc == primary ||
				(g.Semantics == "FID" && i == 1 && len(g.Sources) == 2 && g.Sources[0] == primary)
			if keep {
				plan.forward[ssrc] = true
			} else if !plan.forward[ssrc] {
				plan.drop[ssrc] = true
			}
		}
	}
	for ssrc := range plan.forward {
		delete(plan.drop, ssrc)
	}
	return plan
}

// shouldForward reports whether the stream of this ssrc enters the fan-out (true) or is
// terminated at the SFU (false). Undeclared ssrcs are always forwarded: audio, future
// probing streams — the subscriber safely drops packets with no sink.
func (p mediaPlan) shouldForward(ssrc uint32) bool {
	return !p.drop[ssrc]
}

type endpoint struct {
	callID int64
	userID int64
	kind   EndpointKind
	plan   mediaPlan
	sfu    *pionSFU

	agent  *ice.Agent
	demux  *demuxer
	closed chan struct{}

	mu           sync.Mutex
	writeStream  *srtp.WriteStreamSRTP
	rtcpWriter   *srtp.WriteStreamSRTCP
	lastActivity time.Time
	connected    bool
	closeOnce    sync.Once
}

// NewPion starts the embedded SFU: it binds the UDP port and generates a process-wide DTLS certificate.
func NewPion(cfg PionConfig) (Service, error) {
	if cfg.UDPPort <= 0 {
		return nil, fmt.Errorf("sfu: invalid udp port %d", cfg.UDPPort)
	}
	if cfg.AdvertiseIP == "" {
		cfg.AdvertiseIP = "127.0.0.1"
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	if cfg.LivenessInterval <= 0 {
		cfg.LivenessInterval = 15 * time.Second
	}
	if cfg.ActivityWindow <= 0 {
		cfg.ActivityWindow = 25 * time.Second
	}
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: cfg.UDPPort})
	if err != nil {
		return nil, fmt.Errorf("sfu: listen udp %d: %w", cfg.UDPPort, err)
	}
	cert, fingerprint, err := newDTLSCertificate()
	if err != nil {
		_ = udpConn.Close()
		return nil, err
	}
	s := &pionSFU{
		cfg:         cfg,
		log:         cfg.Logger,
		udpConn:     udpConn,
		mux:         ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: udpConn}),
		cert:        cert,
		fingerprint: fingerprint,
		rooms:       make(map[int64]*room),
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.livenessLoop(ctx)
	s.log.Info("sfu listening",
		zap.Int("udp_port", cfg.UDPPort),
		zap.String("advertise_ip", cfg.AdvertiseIP))
	if cfg.AdvertiseIP == "127.0.0.1" {
		s.log.Warn("TELESRV_SFU_ADVERTISE_IP is 127.0.0.1: real-device ICE will fail to connect (pure media-plane silent failure); multi-device bring-up must set it to the host LAN IP")
	}
	return s, nil
}

func (s *pionSFU) Enabled() bool { return true }

// Join establishes a participant's media endpoint: the local side generates its own
// ufrag/pwd, ICE CONTROLLING waits for the client's Binding Request (the upstream JSON
// has no candidates, so the peer address is learned via peer-reflexive), and once
// connected it actively handshakes as a DTLS client and establishes the SRTP session.
func (s *pionSFU) Join(ctx context.Context, callID, userID int64, kind EndpointKind, offer ClientOffer) (ServerAnswer, error) {
	if offer.Ufrag == "" || offer.Pwd == "" {
		return ServerAnswer{}, fmt.Errorf("sfu: empty remote ice credentials")
	}
	localUfrag, err := randomICEString(8)
	if err != nil {
		return ServerAnswer{}, err
	}
	localPwd, err := randomICEString(24)
	if err != nil {
		return ServerAnswer{}, err
	}
	agent, err := ice.NewAgent(&ice.AgentConfig{
		NetworkTypes:    []ice.NetworkType{ice.NetworkTypeUDP4},
		CandidateTypes:  []ice.CandidateType{ice.CandidateTypeHost},
		UDPMux:          s.mux,
		LocalUfrag:      localUfrag,
		LocalPwd:        localPwd,
		IncludeLoopback: true,
		LoggerFactory:   &pionLoggerFactory{log: s.log.Named("ice")},
	})
	if err != nil {
		return ServerAnswer{}, fmt.Errorf("sfu: new ice agent: %w", err)
	}
	// pion requires registering OnCandidate in order to gather; we write the candidate
	// address directly into the downstream JSON as AdvertiseIP:UDPPort, so the callback
	// itself need not consume anything.
	if err := agent.OnCandidate(func(ice.Candidate) {}); err != nil {
		_ = agent.Close()
		return ServerAnswer{}, fmt.Errorf("sfu: on candidate: %w", err)
	}
	if err := agent.GatherCandidates(); err != nil {
		_ = agent.Close()
		return ServerAnswer{}, fmt.Errorf("sfu: gather candidates: %w", err)
	}
	ep := &endpoint{
		callID: callID,
		userID: userID,
		kind:   kind,
		plan:   buildMediaPlan(offer),
		sfu:    s,
		agent:  agent,
		closed: make(chan struct{}),
	}
	s.attachEndpoint(ep)
	go ep.run(offer)
	return ServerAnswer{
		Ufrag:             localUfrag,
		Pwd:               localPwd,
		FingerprintSHA256: s.fingerprint,
		Candidates: []Candidate{{
			IP:       s.cfg.AdvertiseIP,
			Port:     s.cfg.UDPPort,
			Protocol: "udp",
			Type:     "host",
		}},
	}, nil
}

func (s *pionSFU) attachEndpoint(ep *endpoint) {
	key := epKey{userID: ep.userID, kind: ep.kind}
	s.mu.Lock()
	rm, ok := s.rooms[ep.callID]
	if !ok {
		rm = &room{callID: ep.callID, endpoints: make(map[epKey]*endpoint)}
		s.rooms[ep.callID] = rm
	}
	old := rm.endpoints[key]
	rm.endpoints[key] = ep
	var oldPresentation *endpoint
	if ep.kind == EndpointMain {
		// Main connection rejoin: the old presentation registration is voided along with it
		// (the signaling side likewise clears presentation_json, and the client then resends
		// joinGroupCallPresentation).
		pk := epKey{userID: ep.userID, kind: EndpointPresentation}
		oldPresentation = rm.endpoints[pk]
		delete(rm.endpoints, pk)
	}
	s.mu.Unlock()
	if old != nil {
		old.close() // rejoin: replace the old endpoint
	}
	if oldPresentation != nil {
		oldPresentation.close()
	}
}

func (s *pionSFU) Leave(_ context.Context, callID, userID int64, kind EndpointKind) error {
	var victims []*endpoint
	s.mu.Lock()
	if rm, ok := s.rooms[callID]; ok {
		keys := []epKey{{userID: userID, kind: kind}}
		if kind == EndpointMain {
			// Leaving the call as a whole never sends a follow-up leaveGroupCallPresentation:
			// main endpoint departure tears down the screen share alongside it.
			keys = append(keys, epKey{userID: userID, kind: EndpointPresentation})
		}
		for _, key := range keys {
			if ep := rm.endpoints[key]; ep != nil {
				victims = append(victims, ep)
				delete(rm.endpoints, key)
			}
		}
		if len(rm.endpoints) == 0 {
			delete(s.rooms, callID)
		}
	}
	s.mu.Unlock()
	for _, ep := range victims {
		ep.close()
	}
	return nil
}

func (s *pionSFU) CloseRoom(_ context.Context, callID int64) error {
	s.mu.Lock()
	rm := s.rooms[callID]
	delete(s.rooms, callID)
	s.mu.Unlock()
	if rm != nil {
		for _, ep := range rm.endpoints {
			ep.close()
		}
	}
	return nil
}

func (s *pionSFU) AliveUserIDs(callID int64) []int64 {
	cutoff := time.Now().Add(-s.cfg.ActivityWindow)
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, ok := s.rooms[callID]
	if !ok {
		return nil
	}
	seen := make(map[int64]bool)
	var out []int64
	for key, ep := range rm.endpoints {
		// Either connection (main/screen) being alive means the participant's media plane is alive.
		if !seen[key.userID] && ep.aliveSince(cutoff) {
			seen[key.userID] = true
			out = append(out, key.userID)
		}
	}
	return out
}

// livenessLoop periodically reports media-plane liveness back to the signaling side (refreshing the keepalive watermark).
func (s *pionSFU) livenessLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.LivenessInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.cfg.Touch == nil {
				continue
			}
			cutoff := time.Now().Add(-s.cfg.ActivityWindow)
			type key struct{ callID, userID int64 }
			seen := make(map[key]bool)
			var alive []key
			s.mu.Lock()
			for callID, rm := range s.rooms {
				for ek, ep := range rm.endpoints {
					k := key{callID, ek.userID}
					if !seen[k] && ep.aliveSince(cutoff) {
						seen[k] = true
						alive = append(alive, k)
					}
				}
			}
			s.mu.Unlock()
			for _, k := range alive {
				s.cfg.Touch(k.callID, k.userID)
			}
		}
	}
}

// forwardRTP forwards a decrypted RTP packet verbatim to the **main** endpoint of every
// other participant in the room (no SSRC rewriting; the extension header passes through
// with the packet — audio-level is the data source for the speaking indication). Watching
// a screen share also goes over the viewer's main connection; the presentation connection
// receives no remote stream.
func (s *pionSFU) forwardRTP(from *endpoint, packet []byte) {
	for _, ep := range s.mainTargets(from) {
		ep.writeRTP(packet)
	}
}

// mainTargets returns every main endpoint in the room except that of from's participant.
func (s *pionSFU) mainTargets(from *endpoint) []*endpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, ok := s.rooms[from.callID]
	if !ok {
		return nil
	}
	targets := make([]*endpoint, 0, len(rm.endpoints))
	for key, ep := range rm.endpoints {
		if key.userID != from.userID && key.kind == EndpointMain {
			targets = append(targets, ep)
		}
	}
	return targets
}

// publisherBySSRC finds the publishing endpoint that declared this ssrc (audio / any
// video layer / RTX), so that RTCP feedback (PLI/NACK) can be routed back to the
// publisher by media ssrc. Rooms are small, so it just iterates.
func (s *pionSFU) publisherBySSRC(callID int64, ssrc uint32) *endpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, ok := s.rooms[callID]
	if !ok {
		return nil
	}
	for _, ep := range rm.endpoints {
		if ep.plan.audioSSRC == ssrc || ep.plan.forward[ssrc] || ep.plan.drop[ssrc] {
			return ep
		}
	}
	return nil
}

// ---- endpoint ----

func (ep *endpoint) run(offer ClientOffer) {
	s := ep.sfu
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// CONTROLLING: actively initiates connectivity checks; the peer candidate is learned
	// from a Binding Request authenticated with ufrag/pwd (peer-reflexive).
	conn, err := ep.agent.Dial(ctx, offer.Ufrag, offer.Pwd)
	if err != nil {
		s.log.Debug("sfu ice dial", zap.Int64("call_id", ep.callID), zap.Int64("user_id", ep.userID), zap.Error(err))
		ep.close()
		return
	}
	ep.markActivity()
	ep.demux = newDemuxer(conn)

	dtlsConfig := &dtls.Config{
		Certificates:         []tls.Certificate{s.cert},
		InsecureSkipVerify:   true, // identity is verified via fingerprint (signaling-plane commit), not a CA chain
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		SRTPProtectionProfiles: []dtls.SRTPProtectionProfile{
			dtls.SRTP_AEAD_AES_128_GCM,
			dtls.SRTP_AES128_CM_HMAC_SHA1_80,
		},
	}
	dtlsRaw := ep.demux.dtlsConn()
	dtlsConn, err := dtls.Client(dtlsnet.PacketConnFromConn(dtlsRaw), dtlsRaw.RemoteAddr(), dtlsConfig)
	if err != nil {
		s.log.Debug("sfu dtls client", zap.Int64("user_id", ep.userID), zap.Error(err))
		ep.close()
		return
	}
	// The pion/dtls v3 constructor handshakes lazily (only on first read/write): we must
	// call HandshakeContext explicitly to completion with a hard limit, or ConnectionState
	// has no state and a stalled handshake hangs the goroutine.
	hsCtx, hsCancel := context.WithTimeout(context.Background(), 20*time.Second)
	err = dtlsConn.HandshakeContext(hsCtx)
	hsCancel()
	if err != nil {
		s.log.Debug("sfu dtls handshake", zap.Int64("user_id", ep.userID), zap.Error(err))
		ep.close()
		return
	}
	// Fingerprint verification: the DTLS certificate must match the sha-256 fingerprint promised in the join JSON.
	state, ok := dtlsConn.ConnectionState()
	if !ok || len(state.PeerCertificates) == 0 {
		ep.close()
		return
	}
	gotFP, err := certificateFingerprint(state.PeerCertificates[0])
	if err != nil || normalizeFingerprint(gotFP) != normalizeFingerprint(offer.FingerprintSHA256) {
		s.log.Warn("sfu dtls fingerprint mismatch", zap.Int64("user_id", ep.userID))
		ep.close()
		return
	}
	profile, ok := dtlsConn.SelectedSRTPProtectionProfile()
	if !ok {
		ep.close()
		return
	}
	srtpConfig := &srtp.Config{}
	switch profile {
	case dtls.SRTP_AEAD_AES_128_GCM:
		srtpConfig.Profile = srtp.ProtectionProfileAeadAes128Gcm
	case dtls.SRTP_AES128_CM_HMAC_SHA1_80:
		srtpConfig.Profile = srtp.ProtectionProfileAes128CmHmacSha1_80
	default:
		ep.close()
		return
	}
	// The SFU is the DTLS client: isClient=true determines the SRTP read/write key direction.
	if err := srtpConfig.ExtractSessionKeysFromDTLS(&state, true); err != nil {
		s.log.Debug("sfu srtp keys", zap.Error(err))
		ep.close()
		return
	}
	session, err := srtp.NewSessionSRTP(ep.demux.srtpConn(), srtpConfig)
	if err != nil {
		s.log.Debug("sfu srtp session", zap.Error(err))
		ep.close()
		return
	}
	writeStream, err := session.OpenWriteStream()
	if err != nil {
		ep.close()
		return
	}
	// RTCP is upgraded to an SRTCP session (reusing the same srtpConfig): the read side
	// routes PLI/NACK, the write side relays feedback to the publisher. Note: once
	// SessionSRTCP is created, srtcpConn must never be read raw again (double reads steal
	// packets from each other).
	rtcpSession, err := srtp.NewSessionSRTCP(ep.demux.srtcpConn(), srtpConfig)
	if err != nil {
		s.log.Debug("sfu srtcp session", zap.Error(err))
		ep.close()
		return
	}
	rtcpWriter, err := rtcpSession.OpenWriteStream()
	if err != nil {
		ep.close()
		return
	}
	ep.mu.Lock()
	ep.writeStream = writeStream
	ep.rtcpWriter = rtcpWriter
	ep.connected = true
	ep.mu.Unlock()
	ep.markActivity()
	s.log.Info("sfu endpoint connected",
		zap.Int64("call_id", ep.callID),
		zap.Int64("user_id", ep.userID),
		zap.Int("kind", int(ep.kind)))

	go ep.serveRTCP(rtcpSession)
	// The SCTP data channel rides on the same DTLS connection (application data after the handshake is SCTP packets).
	go ep.serveData(dtlsConn)

	// Inbound SRTP: receive streams by SSRC. The forward/terminate decision is made once
	// at stream granularity (AcceptStream demultiplexes by SSRC, giving selective
	// forwarding zero per-packet overhead).
	for {
		stream, ssrc, err := session.AcceptStream()
		if err != nil {
			ep.close()
			return
		}
		if ep.plan.shouldForward(ssrc) {
			go ep.readStream(stream, ssrc)
		} else {
			// Non-forwarded layers (higher simulcast layers and their RTX): must be drained
			// continuously to prevent buffer buildup.
			go ep.drainStream(stream)
		}
	}
}

func (ep *endpoint) readStream(stream *srtp.ReadStreamSRTP, ssrc uint32) {
	buf := make([]byte, 1500)
	for {
		n, err := stream.Read(buf)
		if err != nil {
			return
		}
		ep.markActivity()
		packet := make([]byte, n)
		copy(packet, buf[:n])
		ep.sfu.forwardRTP(ep, packet)
	}
}

func (ep *endpoint) drainStream(stream *srtp.ReadStreamSRTP) {
	buf := make([]byte, 1500)
	for {
		if _, err := stream.Read(buf); err != nil {
			return
		}
		ep.markActivity()
	}
}

// serveRTCP consumes and routes the inbound RTCP of this endpoint:
//   - PLI / FIR / NACK (subscriber feedback) → find the publishing endpoint by media
//     ssrc and relay to it — under verbatim forwarding the ssrc in subscriber feedback
//     is the publisher's original ssrc, so no rewriting is needed; the publisher provides
//     its own RTX retransmission and 300ms keyframe rate limit;
//   - SR (publisher report) → forward to the other main endpoints (the NTP↔RTP mapping
//     source for A/V sync);
//   - RR / REMB / transport-cc: dropped (v1 does no bandwidth adaptation; layer0's
//     required bitrate is far below the publisher's 400kbps starting value, so it won't
//     starve).
func (ep *endpoint) serveRTCP(session *srtp.SessionSRTCP) {
	for {
		stream, _, err := session.AcceptStream()
		if err != nil {
			return
		}
		go func() {
			buf := make([]byte, 1500)
			for {
				n, err := stream.Read(buf)
				if err != nil {
					return
				}
				ep.markActivity()
				pkts, err := rtcp.Unmarshal(buf[:n])
				if err != nil {
					continue
				}
				ep.routeRTCP(pkts)
			}
		}()
	}
}

func (ep *endpoint) routeRTCP(pkts []rtcp.Packet) {
	for _, pkt := range pkts {
		switch fb := pkt.(type) {
		case *rtcp.PictureLossIndication:
			ep.sfu.writeRTCPToPublisher(ep.callID, fb.MediaSSRC, pkt)
		case *rtcp.FullIntraRequest:
			ep.sfu.writeRTCPToPublisher(ep.callID, fb.MediaSSRC, pkt)
		case *rtcp.TransportLayerNack:
			ep.sfu.writeRTCPToPublisher(ep.callID, fb.MediaSSRC, pkt)
		case *rtcp.SenderReport:
			for _, target := range ep.sfu.mainTargets(ep) {
				target.writeRTCP(pkt)
			}
		}
	}
}

func (s *pionSFU) writeRTCPToPublisher(callID int64, mediaSSRC uint32, pkt rtcp.Packet) {
	publisher := s.publisherBySSRC(callID, mediaSSRC)
	if publisher != nil {
		publisher.writeRTCP(pkt)
	}
}

func (ep *endpoint) writeRTCP(pkt rtcp.Packet) {
	ep.mu.Lock()
	w := ep.rtcpWriter
	ep.mu.Unlock()
	if w == nil {
		return
	}
	raw, err := rtcp.Marshal([]rtcp.Packet{pkt})
	if err != nil {
		return
	}
	_, _ = w.Write(raw)
}

// serveData passively accepts SCTP associations and DCEP data channels on the DTLS connection:
//   - the client is always the initiator of SCTP INIT and DATA_CHANNEL_OPEN (sid=0,
//     label="data"); the SFU never OPENs on its own (the client has no logic to accept an
//     inbound OPEN);
//   - when the association is rebuilt by the client (restartDataChannel), it re-accepts on
//     the same DTLS;
//   - no message on the data channel is a hard prerequisite for audio/video work: v1 only
//     consumes (records) ReceiverVideoConstraints and ignores the rest.
func (ep *endpoint) serveData(conn *dtls.Conn) {
	logf := logging.NewDefaultLoggerFactory()
	logf.DefaultLogLevel = logging.LogLevelError
	for {
		select {
		case <-ep.closed:
			return
		default:
		}
		assoc, err := sctp.Server(sctp.Config{
			NetConn:       conn,
			LoggerFactory: logf,
		})
		if err != nil {
			return // DTLS closed / endpoint torn down
		}
		for {
			dc, err := datachannel.Accept(assoc, &datachannel.Config{LoggerFactory: logf})
			if err != nil {
				break // association ended: the outer loop retries to accept the new one the client rebuilt
			}
			go ep.serveDataChannel(dc)
		}
		_ = assoc.Close()
	}
}

func (ep *endpoint) serveDataChannel(dc *datachannel.DataChannel) {
	defer func() { _ = dc.Close() }()
	buf := make([]byte, 65536)
	for {
		n, isString, err := dc.ReadDataChannel(buf)
		if err != nil {
			return
		}
		if !isString || n == 0 {
			continue
		}
		ep.markActivity()
		var msg struct {
			ColibriClass string `json:"colibriClass"`
		}
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}
		// v1: ReceiverVideoConstraints is only recorded (the forwarding policy is fixed to
		// SIM[0]-layer broadcast; the client safely drops packets for endpoints it did not
		// subscribe to); layer selection / trimming by subscription is left to a later round.
		ep.sfu.log.Debug("sfu data channel message",
			zap.Int64("call_id", ep.callID),
			zap.Int64("user_id", ep.userID),
			zap.String("colibri_class", msg.ColibriClass))
	}
}

func (ep *endpoint) writeRTP(packet []byte) {
	ep.mu.Lock()
	ws := ep.writeStream
	ep.mu.Unlock()
	if ws == nil {
		return
	}
	_, _ = ws.Write(packet)
}

func (ep *endpoint) markActivity() {
	ep.mu.Lock()
	ep.lastActivity = time.Now()
	ep.mu.Unlock()
}

func (ep *endpoint) aliveSince(cutoff time.Time) bool {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	return ep.connected && ep.lastActivity.After(cutoff)
}

func (ep *endpoint) close() {
	ep.closeOnce.Do(func() {
		close(ep.closed)
		if ep.demux != nil {
			ep.demux.Close()
		}
		_ = ep.agent.Close()
		ep.mu.Lock()
		ep.connected = false
		ep.mu.Unlock()
	})
}

// pionLoggerFactory quiets pion logs (can be wired to zap on demand).
type pionLoggerFactory struct{ log *zap.Logger }

func (f *pionLoggerFactory) NewLogger(scope string) logging.LeveledLogger {
	return logging.NewDefaultLoggerFactory().NewLogger(scope)
}
