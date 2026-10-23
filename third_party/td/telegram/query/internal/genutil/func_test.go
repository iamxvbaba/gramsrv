package genutil

import (
	"go/token"
	"go/types"
	"testing"
)

func TestFuncReceiverNamed(t *testing.T) {
	pkg := types.NewPackage("example.com/tg", "tg")
	client := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "Client", nil),
		types.NewStruct(nil, nil),
		nil,
	)
	layerClient := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "LayerClient", nil),
		types.NewStruct(nil, nil),
		nil,
	)

	method := func(name string, receiver types.Type) Func {
		recv := types.NewVar(token.NoPos, pkg, "receiver", receiver)
		sig := types.NewSignatureType(recv, nil, nil, nil, nil, false)
		return Func{
			Sig:  sig,
			Decl: types.NewFunc(token.NoPos, pkg, name, sig),
		}
	}

	for _, tt := range []struct {
		name string
		fn   Func
		want bool
	}{
		{name: "pointer Client", fn: method("Call", types.NewPointer(client)), want: true},
		{name: "value Client", fn: method("Call", client), want: true},
		{name: "LayerClient", fn: method("Call", types.NewPointer(layerClient)), want: false},
		{name: "function", fn: Func{Sig: types.NewSignatureType(nil, nil, nil, nil, nil, false)}, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.fn.ReceiverNamed("Client"); got != tt.want {
				t.Fatalf("ReceiverNamed(Client) = %v, want %v", got, tt.want)
			}
		})
	}
}
