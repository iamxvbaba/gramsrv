package rpc

import (
	"testing"

	"github.com/gotd/td/tgerr"

	"telesrv/internal/domain"
)

func TestPasswordErrMapsOccupiedLoginEmailToNotAllowed(t *testing.T) {
	if err := passwordErr(domain.ErrEmailOccupied); !tgerr.Is(err, "EMAIL_NOT_ALLOWED") {
		t.Fatalf("passwordErr(ErrEmailOccupied) = %v, want EMAIL_NOT_ALLOWED", err)
	}
}
