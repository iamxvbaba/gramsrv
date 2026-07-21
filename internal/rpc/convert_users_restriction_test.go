package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
)

func TestTgUserEncodesFrozenRestriction(t *testing.T) {
	user := tgUser(domain.User{
		ID:                 1001,
		FirstName:          "Frozen",
		RestrictionReasons: domain.AccountFrozenRestrictionReasons(),
	})
	if !user.Restricted {
		t.Fatal("tg user restricted=false, want true")
	}
	reasons, ok := user.GetRestrictionReason()
	if !ok || len(reasons) != 1 {
		t.Fatalf("restriction_reason = %+v ok=%v, want one reason", reasons, ok)
	}
	if got := reasons[0]; got.Platform != "all" || got.Reason != "frozen" || got.Text != "This account is frozen." {
		t.Fatalf("restriction_reason = %+v", got)
	}

	for profile := tlprofile.Profile225; profile <= tlprofile.Profile228; profile++ {
		wire := &bin.Buffer{}
		if err := tlprofile.EncodeObject(profile, user, wire); err != nil {
			t.Fatalf("encode layer %d frozen user: %v", profile, err)
		}
		decoded, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: wire.Copy()}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode layer %d frozen user: %v", profile, err)
		}
		exact, ok := decoded.(*tg.User)
		if !ok || !exact.Restricted {
			t.Fatalf("layer %d user = %#v, want restricted", profile, decoded)
		}
		exactReasons, ok := exact.GetRestrictionReason()
		if !ok || len(exactReasons) != 1 || exactReasons[0].Reason != "frozen" {
			t.Fatalf("layer %d restriction = %+v ok=%v", profile, exactReasons, ok)
		}
	}
}

func TestTgUserSkipsIncompleteRestriction(t *testing.T) {
	user := tgUser(domain.User{
		ID:                 1001,
		FirstName:          "Active",
		RestrictionReasons: []domain.UserRestrictionReason{{Platform: "all", Reason: "frozen"}},
	})
	if user.Restricted {
		t.Fatal("incomplete restriction was encoded")
	}
	if reasons, ok := user.GetRestrictionReason(); ok || len(reasons) != 0 {
		t.Fatalf("restriction_reason = %+v ok=%v, want omitted", reasons, ok)
	}
}
