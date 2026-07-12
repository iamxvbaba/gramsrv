// Package translation implements protocol-neutral chat translation and the
// per-account peer visibility preference consumed by Telegram clients.
package translation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"telesrv/internal/domain"
)

const defaultTimeout = 15 * time.Second

type PrivateMessages interface {
	GetMessages(ctx context.Context, userID int64, ids []int) (domain.MessageList, error)
}

type ChannelMessages interface {
	GetMessages(ctx context.Context, userID, channelID int64, ids []int) (domain.ChannelHistory, error)
}

type SettingsStore interface {
	SetTranslationDisabled(ctx context.Context, userID int64, peer domain.Peer, disabled bool) (bool, error)
	TranslationDisabled(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
}

type RateLimiter interface {
	AllowN(ctx context.Context, key string, cost, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
}

type Provider interface {
	Name() string
	Translate(ctx context.Context, texts []domain.TranslationText, toLang, tone string) ([]domain.TranslationText, error)
}

type Service struct {
	private    PrivateMessages
	channels   ChannelMessages
	settings   SettingsStore
	providers  []Provider
	enabled    bool
	timeout    time.Duration
	limiter    RateLimiter
	rateLimit  int
	rateWindow time.Duration
}

type Option func(*Service)

func WithProviders(providers ...Provider) Option {
	return func(s *Service) {
		for _, provider := range providers {
			if provider != nil {
				s.providers = append(s.providers, provider)
			}
		}
	}
}

func WithEnabled(enabled bool) Option { return func(s *Service) { s.enabled = enabled } }

func WithTimeout(timeout time.Duration) Option {
	return func(s *Service) {
		if timeout > 0 {
			s.timeout = timeout
		}
	}
}

func WithRateLimiter(limiter RateLimiter, limit int, window time.Duration) Option {
	return func(s *Service) {
		s.limiter = limiter
		s.rateLimit = limit
		if window > 0 {
			s.rateWindow = window
		}
	}
}

func NewService(private PrivateMessages, channels ChannelMessages, settings SettingsStore, opts ...Option) *Service {
	s := &Service{
		private:    private,
		channels:   channels,
		settings:   settings,
		enabled:    true,
		timeout:    defaultTimeout,
		rateLimit:  60,
		rateWindow: time.Minute,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (s *Service) Translate(ctx context.Context, req domain.TranslationRequest) (domain.TranslationResult, error) {
	if s == nil || !s.enabled || len(s.providers) == 0 {
		return domain.TranslationResult{}, domain.ErrTranslationDisabled
	}
	toLang := strings.ToLower(strings.TrimSpace(req.ToLang))
	if req.UserID == 0 || !validLanguage(toLang) || utf8.RuneCountInString(req.Tone) > domain.MaxTranslationToneRunes {
		return domain.TranslationResult{}, domain.ErrTranslationLanguageInvalid
	}
	texts, err := s.resolveTexts(ctx, req)
	if err != nil {
		return domain.TranslationResult{}, err
	}
	if err := validateTexts(texts); err != nil {
		return domain.TranslationResult{}, err
	}
	if s.limiter != nil && s.rateLimit > 0 {
		allowed, _, err := s.limiter.AllowN(ctx, fmt.Sprintf("translation:%d", req.UserID), len(texts), s.rateLimit, s.rateWindow)
		if err != nil {
			return domain.TranslationResult{}, err
		}
		if !allowed {
			return domain.TranslationResult{}, domain.ErrTranslationRateLimited
		}
	}

	providerCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var lastErr error
	for _, provider := range s.providers {
		translated, err := provider.Translate(providerCtx, cloneTexts(texts), toLang, req.Tone)
		if err != nil {
			lastErr = err
			if providerCtx.Err() != nil {
				return domain.TranslationResult{}, domain.ErrTranslationTimeout
			}
			continue
		}
		if len(translated) != len(texts) {
			lastErr = domain.ErrTranslationProviderUnavailable
			continue
		}
		if err := validateTranslatedTexts(translated); err != nil {
			lastErr = err
			translated = nil
		}
		if translated != nil {
			return domain.TranslationResult{Texts: cloneTexts(translated)}, nil
		}
	}
	if errors.Is(lastErr, context.DeadlineExceeded) || errors.Is(lastErr, domain.ErrTranslationTimeout) {
		return domain.TranslationResult{}, domain.ErrTranslationTimeout
	}
	return domain.TranslationResult{}, domain.ErrTranslationProviderUnavailable
}

func (s *Service) SetPeerDisabled(ctx context.Context, userID int64, peer domain.Peer, disabled bool) (bool, error) {
	if s == nil || s.settings == nil || userID == 0 || !validPeer(peer) {
		return false, domain.ErrTranslationPeerInvalid
	}
	return s.settings.SetTranslationDisabled(ctx, userID, peer, disabled)
}

func (s *Service) PeerDisabled(ctx context.Context, userID int64, peer domain.Peer) (bool, error) {
	if s == nil || s.settings == nil || userID == 0 || !validPeer(peer) {
		return false, domain.ErrTranslationPeerInvalid
	}
	return s.settings.TranslationDisabled(ctx, userID, peer)
}

func (s *Service) resolveTexts(ctx context.Context, req domain.TranslationRequest) ([]domain.TranslationText, error) {
	idMode := len(req.IDs) > 0 || req.Peer.ID != 0
	textMode := len(req.Texts) > 0
	if idMode == textMode {
		return nil, domain.ErrTranslationInputEmpty
	}
	if textMode {
		if len(req.Texts) > domain.MaxTranslationTexts {
			return nil, domain.ErrTranslationInputTooLong
		}
		return cloneTexts(req.Texts), nil
	}
	if !validPeer(req.Peer) {
		return nil, domain.ErrTranslationPeerInvalid
	}
	if len(req.IDs) == 0 {
		return nil, domain.ErrTranslationInputEmpty
	}
	if len(req.IDs) > domain.MaxTranslationTexts {
		return nil, domain.ErrTranslationInputTooLong
	}
	for _, id := range req.IDs {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return nil, domain.ErrTranslationMessageInvalid
		}
	}
	switch req.Peer.Type {
	case domain.PeerTypeUser:
		if s.private == nil {
			return nil, domain.ErrTranslationProviderUnavailable
		}
		list, err := s.private.GetMessages(ctx, req.UserID, req.IDs)
		if err != nil {
			return nil, err
		}
		byID := make(map[int]domain.Message, len(list.Messages))
		for _, message := range list.Messages {
			if message.Peer == req.Peer {
				byID[message.ID] = message
			}
		}
		out := make([]domain.TranslationText, 0, len(req.IDs))
		for _, id := range req.IDs {
			message, ok := byID[id]
			if !ok {
				return nil, domain.ErrTranslationMessageInvalid
			}
			out = append(out, domain.TranslationText{Text: message.Body, Entities: append([]domain.MessageEntity(nil), message.Entities...)})
		}
		return out, nil
	case domain.PeerTypeChannel:
		if s.channels == nil {
			return nil, domain.ErrTranslationProviderUnavailable
		}
		history, err := s.channels.GetMessages(ctx, req.UserID, req.Peer.ID, req.IDs)
		if err != nil {
			return nil, err
		}
		byID := make(map[int]domain.ChannelMessage, len(history.Messages))
		for _, message := range history.Messages {
			if message.ChannelID == req.Peer.ID && !message.Deleted {
				byID[message.ID] = message
			}
		}
		out := make([]domain.TranslationText, 0, len(req.IDs))
		for _, id := range req.IDs {
			message, ok := byID[id]
			if !ok {
				return nil, domain.ErrTranslationMessageInvalid
			}
			out = append(out, domain.TranslationText{Text: message.Body, Entities: append([]domain.MessageEntity(nil), message.Entities...)})
		}
		return out, nil
	default:
		return nil, domain.ErrTranslationPeerInvalid
	}
}

func validateTexts(texts []domain.TranslationText) error {
	if len(texts) == 0 {
		return domain.ErrTranslationInputEmpty
	}
	total := 0
	for _, text := range texts {
		if strings.TrimSpace(text.Text) == "" {
			return domain.ErrTranslationInputEmpty
		}
		total += len(text.Text)
		if total > domain.MaxTranslationInputBytes {
			return domain.ErrTranslationInputTooLong
		}
	}
	return nil
}

func validateTranslatedTexts(texts []domain.TranslationText) error {
	total := 0
	for _, text := range texts {
		if strings.TrimSpace(text.Text) == "" {
			return domain.ErrTranslationProviderUnavailable
		}
		total += len(text.Text)
		if total > domain.MaxTranslationOutputBytes {
			return domain.ErrTranslationProviderUnavailable
		}
	}
	return nil
}

func validPeer(peer domain.Peer) bool {
	return peer.ID > 0 && (peer.Type == domain.PeerTypeUser || peer.Type == domain.PeerTypeChannel)
}

func validLanguage(lang string) bool {
	_, ok := supportedLanguages[strings.ToLower(strings.TrimSpace(lang))]
	return ok
}

var supportedLanguages = func() map[string]struct{} {
	// ISO 639-1. Keeping this explicit makes TO_LANG_INVALID deterministic and
	// still covers the wider DrKLO language picker, not just TDesktop's shortlist.
	const codes = "aa ab ae af ak am an ar as av ay az ba be bg bh bi bm bn bo br bs ca ce ch co cr cs cu cv cy da de dv dz ee el en eo es et eu fa ff fi fj fo fr fy ga gd gl gn gu gv ha he hi ho hr ht hu hy hz ia id ie ig ii ik io is it iu ja jv ka kg ki kj kk kl km kn ko kr ks ku kv kw ky la lb lg li ln lo lt lu lv mg mh mi mk ml mn mr ms mt my na nb nd ne ng nl nn no nr nv ny oc oj om or os pa pi pl ps pt qu rm rn ro ru rw sa sc sd se sg si sk sl sm sn so sq sr ss st su sv sw ta te tg th ti tk tl tn to tr ts tt tw ty ug uk ur uz ve vi vo wa wo xh yi yo za zh zu"
	out := make(map[string]struct{}, 184)
	for _, code := range strings.Fields(codes) {
		out[code] = struct{}{}
	}
	return out
}()

func cloneTexts(in []domain.TranslationText) []domain.TranslationText {
	out := make([]domain.TranslationText, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}
