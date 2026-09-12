package domain

import (
	"errors"
	"time"
)

// ErrAutoSubscribeChannelInvalid rejects a malformed auto-subscribe request.
var ErrAutoSubscribeChannelInvalid = errors.New("auto-subscribe channel invalid")

// AutoSubscribeChannel is one admin-configured "join everyone" channel/
// supergroup -- see channel_auto_subscribe's own migration comment for the
// full add/remove semantics.
type AutoSubscribeChannel struct {
	ChannelID int64
	Title     string
	AddedBy   string
	AddedAt   time.Time
}
