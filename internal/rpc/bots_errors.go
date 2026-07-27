package rpc

import (
	"errors"

	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// bots.* 管理 RPC 的错误码（与 errors.go 同惯例，独立成文件便于 bot 模块维护）。
func userBotRequiredErr() error  { return tgerr.New(400, "USER_BOT_REQUIRED") }
func userBotInvalidErr() error   { return tgerr.New(400, "USER_BOT_INVALID") }
func botInvalidErr() error       { return tgerr.New(400, "BOT_INVALID") }
func botAppBotInvalidErr() error { return tgerr.New(400, "BOT_APP_BOT_INVALID") }
func botAppInvalidErr() error    { return tgerr.New(400, "BOT_APP_INVALID") }
func botAppShortNameInvalidErr() error {
	return tgerr.New(400, "BOT_APP_SHORTNAME_INVALID")
}
func botCommandInvalidErr() error    { return tgerr.New(400, "BOT_COMMAND_INVALID") }
func botMenuButtonInvalidErr() error { return tgerr.New(400, "BUTTON_INVALID") }
func botCreateLimitExceededErr() error {
	return tgerr.New(400, "BOT_CREATE_LIMIT_EXCEEDED")
}
func managerPermissionMissingErr() error { return tgerr.New(400, "MANAGER_PERMISSION_MISSING") }
func methodInvalidErr() error            { return tgerr.New(400, "METHOD_INVALID") }
func rightsNotModifiedErr() error        { return tgerr.New(400, "RIGHTS_NOT_MODIFIED") }
func botVerifierForbiddenErr() error     { return tgerr.New(403, "BOT_VERIFIER_FORBIDDEN") }
func userPermissionDeniedErr() error     { return tgerr.New(403, "USER_PERMISSION_DENIED") }

func setBotCommandsErr(err error) error {
	if errors.Is(err, domain.ErrBotCommandInvalid) {
		return botCommandInvalidErr()
	}
	if errors.Is(err, domain.ErrBotNotFound) {
		return userBotRequiredErr()
	}
	return internalErr()
}

func setBotInfoErr(err error) error {
	if errors.Is(err, domain.ErrBotInfoInvalid) || errors.Is(err, domain.ErrBotNotFound) {
		return botInvalidErr()
	}
	return internalErr()
}

func setBotMenuButtonErr(err error) error {
	if errors.Is(err, domain.ErrBotMenuButtonInvalid) {
		return botMenuButtonInvalidErr()
	}
	if errors.Is(err, domain.ErrBotNotFound) {
		return userBotRequiredErr()
	}
	return internalErr()
}

func botUsernameErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrBotUsernameInvalid):
		return usernameInvalidErr()
	case errors.Is(err, domain.ErrUsernameOccupied):
		return usernameOccupiedErr()
	default:
		return internalErr()
	}
}

func createBotErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrBotUsernameInvalid):
		return usernameInvalidErr()
	case errors.Is(err, domain.ErrUsernameOccupied):
		return usernameOccupiedErr()
	case errors.Is(err, domain.ErrBotsTooMany):
		return botCreateLimitExceededErr()
	case errors.Is(err, domain.ErrBotNameInvalid):
		return firstNameInvalidErr()
	default:
		return internalErr()
	}
}

func exportBotTokenErr(err error) error {
	if errors.Is(err, domain.ErrBotNotFound) {
		return botInvalidErr()
	}
	return internalErr()
}
