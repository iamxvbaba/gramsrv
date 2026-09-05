// Package autosubscribe backs the admin panel's "auto-subscribe channel"
// list: mark a channel/supergroup so every current user joins it right away
// and every account created afterwards joins it the moment it signs up.
// Removing a channel from the list only stops future signups from joining --
// it never removes anyone already a member. See
// deploy/migrations/0206_channel_auto_subscribe.up.sql for the full
// semantics this implements.
package autosubscribe

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// channelJoiner is the one channels.Service method this package needs --
// narrow on purpose, same idiom as every other app package's dependency
// interfaces in this codebase.
type channelJoiner interface {
	JoinChannel(ctx context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error)
	GetChannelByID(ctx context.Context, channelID int64) (domain.Channel, error)
}

type Service struct {
	store    store.AutoSubscribeStore
	channels channelJoiner
	log      *zap.Logger
}

type Option func(*Service)

func WithStore(st store.AutoSubscribeStore) Option {
	return func(s *Service) { s.store = st }
}

func WithChannels(channels channelJoiner) Option {
	return func(s *Service) { s.channels = channels }
}

func WithLogger(log *zap.Logger) Option {
	return func(s *Service) {
		if log != nil {
			s.log = log
		}
	}
}

func NewService(opts ...Option) *Service {
	s := &Service{log: zap.NewNop()}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) Ready() bool {
	return s != nil && s.store != nil && s.channels != nil
}

// List returns the current auto-subscribe channels for the admin panel.
func (s *Service) List(ctx context.Context) ([]domain.AutoSubscribeChannel, error) {
	if !s.Ready() {
		return nil, errors.New("autosubscribe service is not configured")
	}
	return s.store.ListAutoSubscribeChannels(ctx)
}

// Add puts channelID on the list and joins every existing real user to it.
// A per-user join failure (already a member, banned, channel gone private,
// ...) is logged and skipped rather than aborting the whole batch -- this
// mirrors broadcast's own "best effort across many recipients" idiom, since
// one bad row must not silently drop the other 52.
func (s *Service) Add(ctx context.Context, channelID int64, addedBy string) (added bool, joined int, err error) {
	if !s.Ready() {
		return false, 0, errors.New("autosubscribe service is not configured")
	}
	if channelID == 0 {
		return false, 0, domain.ErrAutoSubscribeChannelInvalid
	}
	if _, err := s.channels.GetChannelByID(ctx, channelID); err != nil {
		return false, 0, err
	}
	added, err = s.store.AddAutoSubscribeChannel(ctx, channelID, addedBy)
	if err != nil {
		return false, 0, err
	}
	userIDs, err := s.store.EligibleAutoSubscribeUserIDs(ctx)
	if err != nil {
		return added, 0, err
	}
	for _, userID := range userIDs {
		if _, err := s.channels.JoinChannel(ctx, userID, channelID, 0); err != nil {
			if errors.Is(err, domain.ErrUserAlreadyParticipant) {
				continue
			}
			s.log.Warn("auto-subscribe bulk join failed for one user",
				zap.Int64("channel_id", channelID), zap.Int64("user_id", userID), zap.Error(err))
			continue
		}
		joined++
	}
	return added, joined, nil
}

// Remove takes channelID off the list. It deliberately never leaves anyone
// already joined -- see this package's own doc comment.
func (s *Service) Remove(ctx context.Context, channelID int64) (bool, error) {
	if !s.Ready() {
		return false, errors.New("autosubscribe service is not configured")
	}
	if channelID == 0 {
		return false, domain.ErrAutoSubscribeChannelInvalid
	}
	return s.store.RemoveAutoSubscribeChannel(ctx, channelID)
}

// JoinNewUser is SignUp's own post-creation hook (see auth.Service.SignUp):
// best-effort, every channel currently on the list, never blocking or
// failing the signup itself over a channel join going wrong.
func (s *Service) JoinNewUser(ctx context.Context, userID int64) {
	if !s.Ready() || userID == 0 {
		return
	}
	channelIDs, err := s.store.AutoSubscribeChannelIDs(ctx)
	if err != nil {
		s.log.Warn("auto-subscribe: could not load channel list for new user",
			zap.Int64("user_id", userID), zap.Error(err))
		return
	}
	for _, channelID := range channelIDs {
		if _, err := s.channels.JoinChannel(ctx, userID, channelID, 0); err != nil &&
			!errors.Is(err, domain.ErrUserAlreadyParticipant) {
			s.log.Warn("auto-subscribe: join failed for new user",
				zap.Int64("user_id", userID), zap.Int64("channel_id", channelID), zap.Error(err))
		}
	}
}
