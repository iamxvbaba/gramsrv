package rpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/app/users"
	"telesrv/internal/domain"
)

// Layer 232 free-messages links. A user who charges Stars for private messages
// exports a link (https://<public host>/<username>?free=<token>); whoever
// redeems it is added to the owner's PrivacyKeyNoPaidMessages allow list and
// may write for free from then on.

func noPaidMessagesTokenInvalidErr() error {
	return tgerr.New(400, "NO_PAID_MESSAGES_TOKEN_INVALID")
}

func noPaidMessagesUsernameRequiredErr() error {
	return tgerr.New(400, "USERNAME_REQUIRED")
}

func newNoPaidMessagesTokenValue() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (r *Router) onContactsExportNoPaidMessagesToken(ctx context.Context, req *tg.ContactsExportNoPaidMessagesTokenRequest) (*tg.ExportedNoPaidMessagesToken, error) {
	if req == nil || r.deps.NoPaidMessagesTokens == nil || r.deps.Users == nil {
		return nil, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.userIsBot(ctx, userID) {
		return nil, botMethodInvalidErr()
	}
	self, found, err := r.deps.Users.ByID(ctx, userID, userID)
	if err != nil || !found {
		return nil, internalErr()
	}
	// The link opens the owner's public username page; clients resolve the
	// owner from it before redeeming. A user without a username has no
	// shareable public link on this server.
	username := strings.TrimSpace(self.Username)
	if username == "" {
		return nil, noPaidMessagesUsernameRequiredErr()
	}
	candidate, err := newNoPaidMessagesTokenValue()
	if err != nil {
		return nil, internalErr()
	}
	token, err := r.deps.NoPaidMessagesTokens.ExportNoPaidMessagesToken(ctx, userID, req.Temporary, req.Revoke, candidate, r.clock.Now())
	if err != nil {
		r.log.Warn("export no paid messages token failed", zap.Int64("user_id", userID), zap.Error(err))
		return nil, internalErr()
	}
	out := &tg.ExportedNoPaidMessagesToken{
		Token: token.Token,
		URL:   r.publicLinkQuery(username, url.Values{"free": []string{token.Token}}),
	}
	if !token.ExpiresAt.IsZero() {
		out.SetExpires(int(token.ExpiresAt.Unix()))
	}
	return out, nil
}

func (r *Router) onContactsImportNoPaidMessagesToken(ctx context.Context, req *tg.ContactsImportNoPaidMessagesTokenRequest) (tg.UserClass, error) {
	if req == nil || r.deps.NoPaidMessagesTokens == nil || r.deps.Users == nil || r.deps.Privacy == nil {
		return nil, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.userIsBot(ctx, userID) {
		return nil, botMethodInvalidErr()
	}
	owner, found, err := r.userFromInput(ctx, userID, req.UserID)
	if err != nil {
		if errors.Is(err, users.ErrNotAuthorized) {
			return nil, userIDInvalidErr()
		}
		return nil, internalErr()
	}
	if !found || owner.ID == userID || owner.Bot || owner.Deleted {
		return nil, userIDInvalidErr()
	}
	if _, ok, err := r.deps.NoPaidMessagesTokens.ResolveNoPaidMessagesToken(ctx, owner.ID, strings.TrimSpace(req.Token), r.clock.Now()); err != nil {
		return nil, internalErr()
	} else if !ok {
		return nil, noPaidMessagesTokenInvalidErr()
	}
	rules, added, err := r.deps.Privacy.AddAllowUser(ctx, owner.ID, domain.PrivacyKeyNoPaidMessages, userID)
	if err != nil {
		return nil, internalErr()
	}
	if added {
		r.invalidateRPCProjectionForUser(owner.ID)
		r.invalidateRPCProjectionForUser(userID)
		r.pushUserUpdates(ctx, owner.ID, &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdatePrivacy{
				Key:   tgPrivacyKey(rules.Key),
				Rules: tgPrivacyRules(rules.Rules),
			}},
			Users: []tg.UserClass{},
			Chats: []tg.ChatClass{},
			Date:  int(r.clock.Now().Unix()),
		})
		if err := r.sendNoPaidMessagesTokenUsedMessage(ctx, userID, owner.ID); err != nil {
			// The exemption is already durable; the notice is best effort.
			r.log.Warn("no paid messages token used notice failed",
				zap.Int64("user_id", userID), zap.Int64("owner_id", owner.ID), zap.Error(err))
		}
	}
	user := r.tgUser(owner)
	restriction, err := r.privateContactRestrictionFor(ctx, userID, owner.ID)
	if err != nil {
		return nil, err
	}
	applyPrivateContactRestrictionToUser(user, restriction)
	r.applyPeerReadModels(ctx, userID, []tg.UserClass{user}, nil)
	return user, nil
}

// sendNoPaidMessagesTokenUsedMessage posts messageActionNoPaidMessagesTokenUsed
// from the redeemer into the private chat. No origin session is set: the RPC
// returns a User, so every session of both parties must receive the message.
func (r *Router) sendNoPaidMessagesTokenUsedMessage(ctx context.Context, userID, ownerID int64) error {
	if r.deps.Messages == nil {
		return nil
	}
	blocked, err := r.peerBlocksUser(ctx, userID, ownerID)
	if err != nil {
		return err
	}
	randomID, err := newServiceRandomID()
	if err != nil {
		return err
	}
	_, err = r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
		SenderUserID:    userID,
		RecipientUserID: ownerID,
		RandomID:        randomID,
		Media: &domain.MessageMedia{
			Kind:          domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionNoPaidMessagesTokenUsed},
		},
		Date:             int(r.clock.Now().Unix()),
		RecipientBlocked: blocked,
	})
	return err
}

func newServiceRandomID() (int64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	id := int64(uint64(raw[0]) | uint64(raw[1])<<8 | uint64(raw[2])<<16 | uint64(raw[3])<<24 |
		uint64(raw[4])<<32 | uint64(raw[5])<<40 | uint64(raw[6])<<48 | uint64(raw[7]&0x7f)<<56)
	if id == 0 {
		id = 1
	}
	return -id, nil
}
