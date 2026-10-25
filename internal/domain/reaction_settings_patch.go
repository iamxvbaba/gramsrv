package domain

import "fmt"

type AccountReactionSettingsPatch struct {
	Notify          *ReactionsNotifySettings
	DefaultReaction *MessageReaction
	PaidPrivacy     *PaidReactionPrivacy
}

// NotifyOnly reports a patch of the constructor-less reaction-notify
// preference, the only change committed without a delivery effect.
func (p AccountReactionSettingsPatch) NotifyOnly() bool {
	return p.Notify != nil && p.DefaultReaction == nil && p.PaidPrivacy == nil
}

func (p AccountReactionSettingsPatch) Apply(current AccountReactionSettings) (AccountReactionSettings, error) {
	if p.Notify != nil {
		current.Notify = *p.Notify
	}
	if p.DefaultReaction != nil {
		current.DefaultReaction = *p.DefaultReaction
	}
	if p.PaidPrivacy != nil {
		current.PaidPrivacy = *p.PaidPrivacy
	}
	return current, ValidateAccountReactionSettings(current)
}
func ValidateAccountReactionSettings(s AccountReactionSettings) error {
	for _, source := range []ReactionNotifyFrom{s.Notify.MessagesFrom, s.Notify.StoriesFrom, s.Notify.PollVotesFrom} {
		if source != ReactionNotifyFromNone && source != ReactionNotifyFromContacts && source != ReactionNotifyFromAll {
			return fmt.Errorf("invalid reaction notification source")
		}
	}
	if !s.Notify.Sound.Valid() {
		return fmt.Errorf("invalid reaction notification sound")
	}
	if !s.DefaultReaction.Valid() {
		return fmt.Errorf("invalid default reaction")
	}
	switch s.PaidPrivacy.Kind {
	case PaidReactionPrivacyDefault, PaidReactionPrivacyAnonymous:
		if s.PaidPrivacy.Peer != nil {
			return fmt.Errorf("unexpected reaction privacy peer")
		}
	case PaidReactionPrivacyPeer:
		if s.PaidPrivacy.Peer == nil || s.PaidPrivacy.Peer.ID <= 0 || (s.PaidPrivacy.Peer.Type != PeerTypeUser && s.PaidPrivacy.Peer.Type != PeerTypeChannel) {
			return fmt.Errorf("invalid reaction privacy peer")
		}
	default:
		return fmt.Errorf("invalid reaction privacy")
	}
	return nil
}
