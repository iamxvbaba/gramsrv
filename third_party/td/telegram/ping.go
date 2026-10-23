package telegram

import (
	"context"

	"github.com/go-faster/errors"
)

// Ping sends low level ping request to Telegram server.
func (c *Client) Ping(ctx context.Context) error {
	for {
		c.connMux.Lock()
		conn := c.conn
		connChanged := c.connChanged
		c.connMux.Unlock()

		err := conn.Ping(ctx)
		if err == nil || !errRetryableOnNewConn(err) {
			return err
		}

		var clientDone <-chan struct{}
		if c.ctx != nil {
			clientDone = c.ctx.Done()
		}
		select {
		case <-ctx.Done():
			return errors.Wrap(ctx.Err(), "wait for reconnect")
		case <-clientDone:
			return errors.Wrap(c.ctx.Err(), "client closed")
		case <-connChanged:
		}
	}
}
