package botapi

import (
	"testing"

	"telesrv/internal/rpc"
)

// The optional gateway surfaces are reached through runtime type assertions, so a
// signature drift between Router and this package would not break the build: it
// would quietly turn the methods into 501 METHOD_NOT_FOUND answers. Pin the
// contracts the production gateway must keep satisfying.
var (
	_ StarGiftGatewayService      = (*rpc.Router)(nil)
	_ PremiumGatewayService       = (*rpc.Router)(nil)
	_ EphemeralGatewayService     = (*rpc.Router)(nil)
	_ MessageActionGatewayService = (*rpc.Router)(nil)
)

func TestProductionRouterExposesStarGiftSurface(t *testing.T) {
	gateway := any((*rpc.Router)(nil))
	if _, ok := gateway.(StarGiftGatewayService); !ok {
		t.Fatal("rpc.Router does not satisfy StarGiftGatewayService")
	}
}
