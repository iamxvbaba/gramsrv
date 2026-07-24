package domain

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

const (
	MaxAuthDeliveryMNCBytes           = 8
	MaxAuthDeliveryClientTypeBytes    = 32
	MaxAuthDeliveryIDBytes            = 128
	MaxAuthDeliveryReportsPerHour     = 10
	MaxAuthDeliveryReportsPerPhoneDay = 20
)

var (
	ErrAuthDeliveryReportInvalid = errors.New("auth delivery report invalid")
	ErrAuthDeliveryRateLimited   = errors.New("auth delivery report rate limited")
)

// AuthDeliveryReport is operational delivery telemetry, not an abuse report.
// It deliberately stores only hashes of the phone and phone_code_hash and
// never stores the authentication code.
type AuthDeliveryReport struct {
	ID           int64
	AuthKeyID    [8]byte
	SessionID    int64
	ClientType   string
	PhoneHash    [sha256.Size]byte
	CodeHash     [sha256.Size]byte
	IssuedUserID int64
	DeliveryID   string
	Channel      AuthCodeDeliveryKind
	MNC          string
	Fingerprint  [sha256.Size]byte
	CreatedAt    time.Time
}

type AuthMissingCodeReportRequest struct {
	AuthKeyID     [8]byte
	SessionID     int64
	ClientType    string
	Phone         string
	PhoneCodeHash string
	MNC           string
	CreatedAt     time.Time
}

func NewAuthDeliveryReport(authKeyID [8]byte, sessionID int64, clientType, phone, phoneCodeHash string, issuedUserID int64, deliveryID string, channel AuthCodeDeliveryKind, mnc string, createdAt time.Time) (AuthDeliveryReport, error) {
	report := AuthDeliveryReport{
		AuthKeyID: authKeyID, SessionID: sessionID, ClientType: clientType,
		PhoneHash: sha256.Sum256([]byte(phone)), CodeHash: sha256.Sum256([]byte(phoneCodeHash)),
		IssuedUserID: issuedUserID, DeliveryID: deliveryID,
		Channel: channel, MNC: mnc, CreatedAt: createdAt,
	}
	raw, err := json.Marshal(struct {
		Version    int
		AuthKeyID  [8]byte
		SessionID  int64
		PhoneHash  [sha256.Size]byte
		CodeHash   [sha256.Size]byte
		DeliveryID string
		Channel    AuthCodeDeliveryKind
		MNC        string
	}{
		Version: 1, AuthKeyID: authKeyID, SessionID: sessionID,
		PhoneHash: report.PhoneHash, CodeHash: report.CodeHash,
		DeliveryID: deliveryID, Channel: channel, MNC: mnc,
	})
	if err != nil {
		return AuthDeliveryReport{}, ErrAuthDeliveryReportInvalid
	}
	report.Fingerprint = sha256.Sum256(raw)
	if err := report.Validate(); err != nil {
		return AuthDeliveryReport{}, err
	}
	return report, nil
}

func (r AuthDeliveryReport) Validate() error {
	if r.ID < 0 || r.AuthKeyID == ([8]byte{}) || r.SessionID == 0 ||
		r.PhoneHash == ([sha256.Size]byte{}) || r.CodeHash == ([sha256.Size]byte{}) ||
		r.Fingerprint == ([sha256.Size]byte{}) || r.IssuedUserID < 0 ||
		len(r.ClientType) > MaxAuthDeliveryClientTypeBytes || !utf8.ValidString(r.ClientType) ||
		len(r.DeliveryID) > MaxAuthDeliveryIDBytes || !utf8.ValidString(r.DeliveryID) ||
		!validAuthDeliveryChannel(r.Channel) || !validMNC(r.MNC) || r.CreatedAt.IsZero() {
		return ErrAuthDeliveryReportInvalid
	}
	return nil
}

func validAuthDeliveryChannel(channel AuthCodeDeliveryKind) bool {
	switch channel {
	case AuthCodeDeliveryPhone, AuthCodeDeliverySMS:
		return true
	default:
		return false
	}
}

func validMNC(mnc string) bool {
	if len(mnc) > MaxAuthDeliveryMNCBytes {
		return false
	}
	for _, r := range mnc {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
