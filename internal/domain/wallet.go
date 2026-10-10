package domain

import (
	"errors"
	"time"
)

var (
	ErrWalletProofInvalid     = errors.New("wallet ownership proof invalid")
	ErrWalletPasswordRequired = errors.New("wallet replacement requires password check")
)

type WalletLink struct {
	UserID    int64
	Address   string
	PublicKey []byte
}

type WalletChallenge struct {
	UserID    int64
	AuthKeyID [8]byte
	Payload   string
	Domain    string
	IssuedAt  time.Time
	ExpiresAt time.Time
}
