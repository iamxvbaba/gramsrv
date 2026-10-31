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

// TempAuthKeyBindingResult is the exact auth-key default committed by the
// temp-to-permanent binding transaction. LayerObservationID is the durable
// ordering token; zero denotes the legacy unordered default.
type TempAuthKeyBindingResult struct {
	Layer              int
	LayerObservationID int64
}
