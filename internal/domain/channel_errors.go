package domain

import (
	"errors"
	"fmt"
)

var (
	ErrChannelInvalid                 = errors.New("channel invalid")
	ErrChannelPrivate                 = errors.New("channel private")
	ErrChannelTitleInvalid            = errors.New("channel title invalid")
	ErrChannelUserBanned              = errors.New("user banned in channel")
	ErrChannelWriteForbidden          = errors.New("chat write forbidden")
	ErrChannelAdminRequired           = errors.New("chat admin required")
	ErrChannelNotModified             = errors.New("chat not modified")
	ErrChannelForumMissing            = errors.New("channel forum missing")
	ErrChannelMonoforumUnsupported    = errors.New("channel monoforum unsupported")
	ErrLinkNotModified                = errors.New("discussion link not modified")
	ErrChatDiscussionUnallowed        = errors.New("chat discussion unallowed")
	ErrBroadcastIDInvalid             = errors.New("broadcast id invalid")
	ErrMegagroupIDInvalid             = errors.New("megagroup id invalid")
	ErrMegagroupPrehistoryHidden      = errors.New("megagroup prehistory hidden")
	ErrChatPublicRequired             = errors.New("chat public required")
	ErrChannelUserCreator             = errors.New("channel user creator")
	ErrChannelRightForbidden          = errors.New("channel right forbidden")
	ErrPersistentTimestamp            = errors.New("persistent timestamp invalid")
	ErrInviteHashEmpty                = errors.New("invite hash empty")
	ErrInviteHashInvalid              = errors.New("invite hash invalid")
	ErrInviteHashExpired              = errors.New("invite hash expired")
	ErrInvitePermanent                = errors.New("chat invite permanent")
	ErrInviteRevokedMissing           = errors.New("invite revoked missing")
	ErrInviteRequestSent              = errors.New("invite request sent")
	ErrHideRequesterMissing           = errors.New("hide requester missing")
	ErrUsersTooMuch                   = errors.New("users too much")
	ErrUserAlreadyParticipant         = errors.New("user already participant")
	ErrUserKicked                     = errors.New("user kicked")
	ErrUserNotParticipant             = errors.New("user not participant")
	ErrBotGroupsBlocked               = errors.New("bot groups blocked")
	ErrReactionInvalid                = errors.New("reaction invalid")
	ErrReactionsTooMany               = errors.New("reactions too many")
	ErrSuggestedPostInvalid           = errors.New("suggested post invalid")
	ErrSuggestedPostAlreadyHandled    = errors.New("suggested post already handled")
	ErrSuggestedPostApprovalForbidden = errors.New("suggested post approval forbidden")
)

// SlowModeWaitError carries the remaining wait seconds for a channel slow mode violation.
type SlowModeWaitError struct {
	Seconds int
}

func (e SlowModeWaitError) Error() string {
	if e.Seconds <= 0 {
		return "slowmode wait"
	}
	return fmt.Sprintf("slowmode wait %d seconds", e.Seconds)
}

// NewSlowModeWaitError creates a bounded slow mode wait error.
func NewSlowModeWaitError(seconds int) error {
	if seconds <= 0 {
		seconds = 1
	}
	return SlowModeWaitError{Seconds: seconds}
}

// SlowModeWaitSeconds extracts the wait duration from err.
func SlowModeWaitSeconds(err error) (int, bool) {
	var wait SlowModeWaitError
	if !errors.As(err, &wait) {
		return 0, false
	}
	if wait.Seconds <= 0 {
		return 1, true
	}
	return wait.Seconds, true
}
