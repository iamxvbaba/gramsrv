package domain

// TempAuthKeyBinding is the persisted record of auth.bindTempAuthKey.
type TempAuthKeyBinding struct {
	TempAuthKeyID    [8]byte
	PermAuthKeyID    int64
	Nonce            int64
	TempSessionID    int64
	ExpiresAt        int
	EncryptedMessage []byte
}

// TempAuthKeyBindingResult is the effective protocol-Layer default after a
// temp-to-permanent bind. In v2 production this tuple is returned by the Redis
// Layer authority after PostgreSQL commits the canonical identity binding.
// LayerObservationID is the ordering token; zero means no default is known.
type TempAuthKeyBindingResult struct {
	Layer              int
	LayerObservationID int64
}
