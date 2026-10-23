package telegram

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func TestClient_fetchConfig(t *testing.T) {
	a := require.New(t)
	cfg := &tg.Config{
		ThisDC: 10,
	}
	client := newTestClient(func(id int64, body bin.Encoder) (bin.Encoder, error) {
		a.IsType(&tg.HelpGetConfigRequest{}, body)
		return cfg, nil
	})

	a.NoError(client.processUpdates(&tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateConfig{}},
	}))

	a.Equal(*cfg, client.Config())
}
