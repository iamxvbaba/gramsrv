package mtprotoedge

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"time"

	gofaster "github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/exchange"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/proto/codec"
	"github.com/gotd/td/transport"
)

// runServerExchange is a gotd server exchange compatibility shim.
//
// DrKLO Android marks media temporary auth-key exchange with a negative DC in
// p_q_inner_data_temp_dc (for example DC 2 -> -2). gotd v0.158.0 validates this
// field by exact equality and rejects that legitimate media-temp path. Keep the
// permanent-key check strict, but allow temp-key DC values whose absolute value
// matches this server DC.
func (s *Server) runServerExchange(ctx context.Context, conn transport.Conn) (exchange.ServerExchangeResult, error) {
	ex := serverExchangeCompat{
		conn:      conn,
		clock:     s.clock,
		rand:      s.rand,
		timeout:   exchange.DefaultTimeout,
		key:       s.key,
		dc:        s.dc,
		log:       s.log.Named("exchange"),
		rng:       compatServerRNG{rand: s.rand},
		commitKey: s.commitExchangeAuthKey,
	}
	return ex.run(ctx)
}

// commitExchangeAuthKey is the durable commit point of the server exchange.
// It must complete before DhGenOk is put on the wire: after that response the
// client is allowed to immediately use the new key, possibly on another TCP
// connection. Persisting after the response creates a split-brain window when
// storage fails or the process exits between those two operations.
func (s *Server) commitExchangeAuthKey(ctx context.Context, result exchange.ServerExchangeResult) error {
	createdAt := s.clock.Now().Unix()
	if err := s.authKeys.Save(ctx, authKeyData(result.Key, result.ServerSalt, createdAt)); err != nil {
		return fmt.Errorf("persist auth key before DhGenOk: %w", err)
	}
	return nil
}

type serverExchangeCompat struct {
	conn      transport.Conn
	clock     clock.Clock
	rand      io.Reader
	timeout   time.Duration
	key       exchange.PrivateKey
	dc        int
	log       *zap.Logger
	rng       compatServerRNG
	commitKey func(context.Context, exchange.ServerExchangeResult) error
}

func (s serverExchangeCompat) run(ctx context.Context) (exchange.ServerExchangeResult, error) {
	wrapKeyNotFound := func(err error) error {
		return exchangeError(codec.CodeAuthKeyNotFound, err)
	}

	var req compatReqPQ
	b := new(bin.Buffer)
	if err := s.readUnencrypted(ctx, b, &req); err != nil {
		return exchange.ServerExchangeResult{}, err
	}
	s.log.Debug("Received client ReqPqMultiRequest")

	serverNonce, err := crypto.RandInt128(s.rand)
	if err != nil {
		return exchange.ServerExchangeResult{}, gofaster.Wrap(err, "generate server nonce")
	}

	pq, err := s.rng.PQ()
	if err != nil {
		return exchange.ServerExchangeResult{}, gofaster.Wrap(err, "generate pq")
	}

SendResPQ:
	s.log.Debug("Sending ResPQ", zap.String("pq", pq.String()))
	if err := s.writeUnencrypted(ctx, b, &mt.ResPQ{
		Pq:          pq.Bytes(),
		Nonce:       req.Nonce,
		ServerNonce: serverNonce,
		ServerPublicKeyFingerprints: []int64{
			s.key.Fingerprint(),
		},
	}); err != nil {
		return exchange.ServerExchangeResult{}, err
	}

	var dhParams compatReqOrDH
	if err := s.readUnencrypted(ctx, b, &dhParams); err != nil {
		return exchange.ServerExchangeResult{}, err
	}
	switch dhParams.Type {
	case mt.ReqPqRequestTypeID, mt.ReqPqMultiRequestTypeID:
		s.log.Debug("Received ReqPQ again")
		req = dhParams.Req
		goto SendResPQ
	default:
		s.log.Debug("Received client ReqDHParamsRequest")
	}

	var innerData mt.PQInnerData
	{
		r, err := crypto.DecodeRSAPad(dhParams.DH.EncryptedData, s.key.RSA)
		if err != nil {
			return exchange.ServerExchangeResult{}, wrapKeyNotFound(err)
		}
		b.ResetTo(r)

		d, err := mt.DecodePQInnerData(b)
		if err != nil {
			return exchange.ServerExchangeResult{}, err
		}
		if err := s.validatePQInnerDataDC(d); err != nil {
			return exchange.ServerExchangeResult{}, err
		}

		innerData = mt.PQInnerData{
			Pq:          d.GetPq(),
			P:           d.GetP(),
			Q:           d.GetQ(),
			Nonce:       d.GetNonce(),
			ServerNonce: d.GetServerNonce(),
			NewNonce:    d.GetNewNonce(),
		}
	}

	dhPrime, err := s.rng.DhPrime()
	if err != nil {
		return exchange.ServerExchangeResult{}, gofaster.Wrap(err, "generate dh_prime")
	}

	g := 3
	a, ga, err := s.rng.GA(g, dhPrime)
	if err != nil {
		return exchange.ServerExchangeResult{}, gofaster.Wrap(err, "generate g_a")
	}

	data := mt.ServerDHInnerData{
		Nonce:       req.Nonce,
		ServerNonce: serverNonce,
		G:           g,
		GA:          ga.Bytes(),
		DhPrime:     dhPrime.Bytes(),
		ServerTime:  int(s.clock.Now().Unix()),
	}

	b.Reset()
	if err := data.Encode(b); err != nil {
		return exchange.ServerExchangeResult{}, err
	}

	key, iv := crypto.TempAESKeys(innerData.NewNonce.BigInt(), serverNonce.BigInt())
	answer, err := crypto.EncryptExchangeAnswer(s.rand, b.Raw(), key, iv)
	if err != nil {
		return exchange.ServerExchangeResult{}, err
	}

	s.log.Debug("Sending ServerDHParamsOk", zap.Int("g", g))
	if err := s.writeUnencrypted(ctx, b, &mt.ServerDHParamsOk{
		Nonce:           req.Nonce,
		ServerNonce:     serverNonce,
		EncryptedAnswer: answer,
	}); err != nil {
		return exchange.ServerExchangeResult{}, err
	}

	var clientDhParams mt.SetClientDHParamsRequest
	if err := s.readUnencrypted(ctx, b, &clientDhParams); err != nil {
		return exchange.ServerExchangeResult{}, err
	}
	s.log.Debug("Received client SetClientDHParamsRequest")

	decrypted, err := crypto.DecryptExchangeAnswer(clientDhParams.EncryptedData, key, iv)
	if err != nil {
		err = gofaster.Wrap(err, "decrypt exchange answer")
		return exchange.ServerExchangeResult{}, wrapKeyNotFound(err)
	}
	b.ResetTo(decrypted)

	var clientInnerData mt.ClientDHInnerData
	if err := clientInnerData.Decode(b); err != nil {
		return exchange.ServerExchangeResult{}, wrapKeyNotFound(err)
	}

	gB := big.NewInt(0).SetBytes(clientInnerData.GB)
	var authKey crypto.Key
	if !crypto.FillBytes(big.NewInt(0).Exp(gB, a, dhPrime), authKey[:]) {
		err := gofaster.New("auth_key is too big")
		return exchange.ServerExchangeResult{}, wrapKeyNotFound(err)
	}

	serverResult := exchange.ServerExchangeResult{
		Key:        authKey.WithID(),
		ServerSalt: crypto.ServerSalt(innerData.NewNonce, serverNonce),
	}
	// DhGenOk is the externally visible commit acknowledgement. Require a
	// durable key commit before sending it, rather than allowing callers to
	// persist after run returns. A nil hook is rejected so a future call site
	// cannot accidentally reintroduce the unsafe ordering.
	if s.commitKey == nil {
		return exchange.ServerExchangeResult{}, gofaster.New("auth key commit hook is required before DhGenOk")
	}
	if err := s.commitKey(ctx, serverResult); err != nil {
		return exchange.ServerExchangeResult{}, err
	}

	s.log.Debug("Sending DhGenOk")
	if err := s.writeUnencrypted(ctx, b, &mt.DhGenOk{
		Nonce:         req.Nonce,
		ServerNonce:   serverNonce,
		NewNonceHash1: crypto.NonceHash1(innerData.NewNonce, authKey),
	}); err != nil {
		return exchange.ServerExchangeResult{}, err
	}

	return serverResult, nil
}

func (s serverExchangeCompat) validatePQInnerDataDC(d mt.PQInnerDataClass) error {
	switch innerDataDC := d.(type) {
	case *mt.PQInnerDataDC:
		if innerDataDC.DC != s.dc {
			return wrongDCError(s.dc, innerDataDC.DC)
		}
	case *mt.PQInnerDataTempDC:
		if !sameDCByAbs(innerDataDC.DC, s.dc) {
			return wrongDCError(s.dc, innerDataDC.DC)
		}
		if innerDataDC.DC < 0 {
			s.log.Warn("Accepted Android media temp auth key negative DC",
				zap.Int("server_dc", s.dc),
				zap.Int("client_dc", innerDataDC.DC),
				zap.Int("expires_in", innerDataDC.ExpiresIn))
		}
	}
	return nil
}

func sameDCByAbs(got, want int) bool {
	g := int64(got)
	if g < 0 {
		g = -g
	}
	return g == int64(want)
}

func wrongDCError(want, got int) error {
	return exchangeError(codec.CodeWrongDC, gofaster.Errorf("wrong DC ID, want %d, got %d", want, got))
}

func exchangeError(code int32, err error) error {
	return &exchange.ServerExchangeError{
		Code: code,
		Err:  err,
	}
}

func (s serverExchangeCompat) writeUnencrypted(ctx context.Context, b *bin.Buffer, data bin.Encoder) error {
	b.Reset()
	if err := data.Encode(b); err != nil {
		return err
	}
	msg := proto.UnencryptedMessage{
		MessageID:   int64(proto.NewMessageID(s.clock.Now(), proto.MessageServerResponse)),
		MessageData: b.Copy(),
	}
	b.Reset()
	if err := msg.Encode(b); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.conn.Send(ctx, b)
}

func (s serverExchangeCompat) readUnencrypted(ctx context.Context, b *bin.Buffer, data bin.Decoder) error {
	b.Reset()

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.conn.Recv(ctx, b); err != nil {
		return err
	}

	var keyID [8]byte
	if err := b.PeekN(keyID[:], len(keyID)); err == nil && keyID != ([8]byte{}) {
		// The exchange aborts immediately on an encrypted frame, so transfer the received backing
		// to the replay error instead of making an unbudgeted near-transport-limit copy. serveConn
		// keeps the existing frame reservation until replay dispatch has finished.
		frame := b.Buf
		b.Buf = nil
		return &exchange.UnexpectedEncryptedError{
			AuthKeyID: keyID,
			Frame:     frame,
		}
	}

	var msg proto.UnencryptedMessage
	if err := msg.Decode(b); err != nil {
		return err
	}
	if proto.MessageID(msg.MessageID).Type() != proto.MessageFromClient {
		return gofaster.New("bad msg type")
	}
	b.ResetTo(msg.MessageData)

	return data.Decode(b)
}

type compatReqPQ struct {
	Type  uint32
	Nonce bin.Int128
}

func (r *compatReqPQ) Decode(b *bin.Buffer) error {
	var (
		legacy mt.ReqPqRequest
		multi  mt.ReqPqMultiRequest
	)
	id, err := b.PeekID()
	if err != nil {
		return err
	}
	r.Type = id
	switch id {
	case legacy.TypeID():
		if err := legacy.Decode(b); err != nil {
			return err
		}
		r.Nonce = legacy.Nonce
		return nil
	case multi.TypeID():
		if err := multi.Decode(b); err != nil {
			return err
		}
		r.Nonce = multi.Nonce
		return nil
	default:
		return bin.NewUnexpectedID(id)
	}
}

type compatReqOrDH struct {
	Type uint32
	DH   mt.ReqDHParamsRequest
	Req  compatReqPQ
}

func (r *compatReqOrDH) Decode(b *bin.Buffer) error {
	id, err := b.PeekID()
	if err != nil {
		return err
	}
	r.Type = id
	switch id {
	case r.DH.TypeID():
		return r.DH.Decode(b)
	default:
		return r.Req.Decode(b)
	}
}

type compatServerRNG struct {
	rand io.Reader
}

func (s compatServerRNG) PQ() (*big.Int, error) {
	return big.NewInt(0x17ED48941A08F981), nil
}

func (s compatServerRNG) GA(g int, dhPrime *big.Int) (a, ga *big.Int, err error) {
	if err := crypto.CheckGP(g, dhPrime); err != nil {
		return nil, nil, err
	}

	gBig := big.NewInt(int64(g))
	one := big.NewInt(1)
	dhPrimeMinusOne := big.NewInt(0).Sub(dhPrime, one)

	safetyRangeMin := big.NewInt(0).Exp(big.NewInt(2), big.NewInt(crypto.RSAKeyBits-64), nil)
	safetyRangeMax := big.NewInt(0).Sub(dhPrime, safetyRangeMin)

	randMax := big.NewInt(0).SetBit(big.NewInt(0), crypto.RSAKeyBits, 1)
	for {
		a, err = crand.Int(s.rand, randMax)
		if err != nil {
			return nil, nil, err
		}

		ga = big.NewInt(0).Exp(gBig, a, dhPrime)
		if crypto.InRange(ga, one, dhPrimeMinusOne) && crypto.InRange(ga, safetyRangeMin, safetyRangeMax) {
			return a, ga, nil
		}
	}
}

func (s compatServerRNG) DhPrime() (*big.Int, error) {
	data, err := hex.DecodeString("C71CAEB9C6B1C9048E6C522F70F13F73980D40238E3E21C14934D037563D930F" +
		"48198A0AA7C14058229493D22530F4DBFA336F6E0AC925139543AED44CCE7C37" +
		"20FD51F69458705AC68CD4FE6B6B13ABDC9746512969328454F18FAF8C595F64" +
		"2477FE96BB2A941D5BCD1D4AC8CC49880708FA9B378E3C4F3A9060BEE67CF9A4" +
		"A4A695811051907E162753B56B0F6B410DBA74D8A84B2A14B3144E0EF1284754" +
		"FD17ED950D5965B4B9DD46582DB1178D169C6BC465B0D6FF9CA3928FEF5B9AE4" +
		"E418FC15E83EBEA0F87FA9FF5EED70050DED2849F47BF959D956850CE929851F" +
		"0D8115F635B105EE2E4E15D04B2454BF6F4FADF034B10403119CD8E3B92FCC5B")
	if err != nil {
		return nil, fmt.Errorf("decode dh_prime: %w", err)
	}
	return big.NewInt(0).SetBytes(data), nil
}
