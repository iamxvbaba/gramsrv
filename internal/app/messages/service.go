package messages

import (
	"context"
	"fmt"

	"telesrv/internal/app/userprojection"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Service provides message history, search, and read-state business logic.
type Service struct {
	messages  store.MessageStore
	dialogs   store.DialogStore
	contacts  store.ContactStore
	photos    userprojection.ProfilePhotoProvider
	privacy   userprojection.PrivacyEvaluator
	freezes   userprojection.AccountFreezeProvider
	phones    userprojection.CollectiblePhoneProvider
	versions  store.ReadModelVersionStore
	projector *userprojection.Projector
	// viewerProjectionComplete is true only when every viewer-scoped user
	// overlay used by the shared RPC Users service is configured here too.
	// A partially configured service may still project the dependencies it has,
	// but RPC must not trust that partial envelope as authoritative.
	viewerProjectionComplete bool
	botResponder             BotResponder
	sendGate                 SendPermissionChecker
	// recipientUsers resolves the private recipient without viewer projection.
	// It is only a tombstone probe: viewer-scoped fields are irrelevant to
	// whether the peer still accepts writes.
	recipientUsers RecipientUserReader
	business       *businessAutomationConfig

	privateMediaCountCache *privateMediaCountReadModelCache
}

type SendPermissionChecker interface {
	CanSendMessages(ctx context.Context, userID int64) error
}

// RecipientUserReader is the minimal unprojected user lookup the send gate
// needs. There is no viewer: the answer only depends on the target row.
type RecipientUserReader interface {
	ByID(ctx context.Context, userID int64) (domain.User, bool, error)
}

// BotResponder responds to private messages delivered to a server-side built-in bot (BotFather).
// The implementation is called synchronously after the user message has been persisted
// successfully; a reply failure can only be logged and must never affect the user's send result.
type BotResponder interface {
	// HandlesBot reports whether botUserID is a built-in bot this responder handles.
	HandlesBot(botUserID int64) bool
	// OnPrivateMessage handles a message delivered to a built-in bot; msg is the inbox row from the bot's perspective.
	OnPrivateMessage(ctx context.Context, botUserID int64, msg domain.Message, session domain.ClientSessionMetadata)
}

// Option adjusts optional message service dependencies.
type Option func(*Service)

// WithContactStore enables viewer-specific user projection for message history.
func WithContactStore(c store.ContactStore) Option {
	return func(s *Service) { s.contacts = c }
}

// WithPhotoProvider enables current profile photo enrichment for message users.
func WithPhotoProvider(p userprojection.ProfilePhotoProvider) Option {
	return func(s *Service) { s.photos = p }
}

// WithPrivacyEvaluator enables viewer-specific privacy projection for message users.
func WithPrivacyEvaluator(p userprojection.PrivacyEvaluator) Option {
	return func(s *Service) { s.privacy = p }
}

func WithAccountFreezeProvider(p userprojection.AccountFreezeProvider) Option {
	return func(s *Service) { s.freezes = p }
}

func WithCollectiblePhoneProvider(p userprojection.CollectiblePhoneProvider) Option {
	return func(s *Service) { s.phones = p }
}

// WithBotResponder enables automatic replies from the server-side built-in bot (BotFather) to private messages.
func WithBotResponder(r BotResponder) Option {
	return func(s *Service) { s.botResponder = r }
}

func WithSendPermissionChecker(c SendPermissionChecker) Option {
	return func(s *Service) { s.sendGate = c }
}

// WithRecipientUserReader wires the tombstone probe that blocks private writes to
// deleted accounts.
func WithRecipientUserReader(r RecipientUserReader) Option {
	return func(s *Service) { s.recipientUsers = r }
}

// WithReadModelVersions enables durable hash-token guarded media count caching.
func WithReadModelVersions(v store.ReadModelVersionStore) Option {
	return func(s *Service) { s.versions = v }
}

// NewService creates the messages service.
func NewService(messages store.MessageStore, dialogs store.DialogStore, opts ...Option) *Service {
	s := &Service{
		messages:               messages,
		dialogs:                dialogs,
		privateMediaCountCache: newPrivateMediaCountReadModelCache(defaultPrivateMediaCountReadModelTTL),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.projector = userprojection.New(
		userprojection.WithContactStore(s.contacts),
		userprojection.WithPhotoProvider(s.photos),
		userprojection.WithPrivacyEvaluator(s.privacy),
		userprojection.WithAccountFreezeProvider(s.freezes),
		userprojection.WithCollectiblePhoneProvider(s.phones),
	)
	s.viewerProjectionComplete = s.contacts != nil && s.photos != nil && s.privacy != nil && s.freezes != nil && s.phones != nil
	return s
}

// ProjectsMessageUsersForViewer reports that history/search results returned by
// this service have already passed through the viewer-specific user projection
// boundary. RPC may reuse that envelope and resolve only nested message refs;
// raw stores and test doubles do not implicitly gain this trust marker.
func (s *Service) ProjectsMessageUsersForViewer() bool {
	return s != nil && s.viewerProjectionComplete
}

// SendPrivateText sends a private text message.
func (s *Service) SendPrivateText(ctx context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.SendPrivateTextResult{}, nil
	}
	if req.SenderUserID == 0 {
		req.SenderUserID = userID
	}
	if req.SenderUserID != userID {
		return domain.SendPrivateTextResult{}, domain.ErrAuthenticatedScopeInvalid
	}
	return s.sendPrivateText(ctx, userID, req)
}

// PostPrivateServiceMessage lets the server itself persist a private service message whose
// sender is not the current authenticated user. Bot Stars payment receipts are one such case:
// the message comes from the payer and lands in the bot's chat, while the identity the server
// is acting as at that point is the bot itself, which SendPrivateText's sender check cannot
// express. Not enforcing the auth scope is safe — the caller is server logic with no
// request-controlled entry point.
func (s *Service) PostPrivateServiceMessage(ctx context.Context, recipientUserID, fromUserID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	if s == nil || s.messages == nil || recipientUserID == 0 || fromUserID == 0 {
		return domain.SendPrivateTextResult{}, domain.ErrMessageEmpty
	}
	if req.Media == nil || req.Media.Kind != domain.MessageMediaKindService {
		return domain.SendPrivateTextResult{}, domain.ErrMessageEmpty
	}
	req.SenderUserID = fromUserID
	req.RecipientUserID = recipientUserID
	return s.sendPrivateText(ctx, fromUserID, req)
}

func (s *Service) sendPrivateText(ctx context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	if err := domain.ValidateReplyMarkup(req.ReplyMarkup); err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	if req.RandomID != 0 && !req.IdempotencyPreflighted {
		fingerprint, err := store.PrivateSendFingerprint(req)
		if err != nil {
			return domain.SendPrivateTextResult{}, err
		}
		req.IdempotencyFingerprint = fingerprint
		if replayStore, ok := s.messages.(store.PrivateSendReplayStore); ok {
			replay, found, err := replayStore.LookupPrivateSendReplay(ctx, domain.PrivateSendReplayRequest{
				SenderUserID:           req.SenderUserID,
				RecipientUserID:        req.RecipientUserID,
				RandomID:               req.RandomID,
				IdempotencyFingerprint: fingerprint,
			})
			if err != nil || found {
				return replay, err
			}
			req.IdempotencyPreflighted = true
		}
	}
	if err := s.ensureCanSend(ctx, req.SenderUserID); err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	if err := s.ensureCanSendRecipient(ctx, req.RecipientUserID); err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	automation, automationOK := s.prepareBusinessAutomation(ctx, req)
	res, err := s.messages.SendPrivateText(ctx, req)
	if err == nil && !res.Duplicate && automationOK {
		s.runBusinessAutomation(ctx, req, res, automation)
	}
	// Built-in bot reply: triggered synchronously after the user message is committed
	// (except for idempotent replays); the responder swallows its own errors and does not
	// propagate failures. Messages sent by the bot itself do not trigger it (SenderUserID is
	// never the direction the built-in bot's conversation-partner set cares about — the hook
	// only looks at the recipient).
	if err == nil && !res.Duplicate && req.BusinessAutomationKind == "" && s.botResponder != nil && s.botResponder.HandlesBot(req.RecipientUserID) {
		s.botResponder.OnPrivateMessage(ctx, req.RecipientUserID, res.RecipientMessage, req.OriginClientSession)
	}
	return res, err
}

// LookupPrivateSendReplay exposes the immutable receipt to the RPC boundary without executing
// send permission checks, business automation or bot responders. Sender identity is still bound
// to the authenticated app-service caller.
func (s *Service) LookupPrivateSendReplay(ctx context.Context, userID int64, req domain.PrivateSendReplayRequest) (domain.SendPrivateTextResult, bool, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.SendPrivateTextResult{}, false, nil
	}
	if req.SenderUserID == 0 {
		req.SenderUserID = userID
	}
	if req.SenderUserID != userID || req.RecipientUserID == 0 || req.RandomID == 0 {
		return domain.SendPrivateTextResult{}, false, fmt.Errorf("private send replay: invalid authenticated scope")
	}
	replayStore, ok := s.messages.(store.PrivateSendReplayStore)
	if !ok {
		return domain.SendPrivateTextResult{}, false, nil
	}
	return replayStore.LookupPrivateSendReplay(ctx, req)
}

func (s *Service) ensureCanSend(ctx context.Context, userID int64) error {
	if s == nil || s.sendGate == nil || userID == 0 {
		return nil
	}
	return s.sendGate.CanSendMessages(ctx, userID)
}

// SetChatTheme updates the shared private-chat theme and records the timeline service message.
// ensureCanSendRecipient blocks private writes to a tombstone. A deleted account
// keeps its rows for history, so the store happily accepts the message; without
// this gate the sender's dialog would show a delivered message nobody can ever
// read back. Rejecting with PEER_ID_INVALID is the official shape: the peer is
// no longer a writable contact.
func (s *Service) ensureCanSendRecipient(ctx context.Context, recipientUserID int64) error {
	if s == nil || recipientUserID == 0 || s.recipientUsers == nil {
		return nil
	}
	u, found, err := s.recipientUsers.ByID(ctx, recipientUserID)
	if err != nil {
		return err
	}
	if !found || u.Deleted {
		return domain.ErrPeerDeleted
	}
	return nil
}

func (s *Service) SetChatTheme(ctx context.Context, userID int64, req domain.SetPrivateChatThemeRequest) (domain.SetPrivateChatThemeResult, error) {
	out := domain.SetPrivateChatThemeResult{
		OwnerUserID: userID,
		Peer:        req.Peer,
		Emoticon:    req.Emoticon,
	}
	if s == nil || s.messages == nil || s.dialogs == nil || userID == 0 {
		return out, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	if req.OwnerUserID != userID || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 {
		return out, domain.ErrMessageIDInvalid
	}
	changedSelf, err := s.dialogs.SetChatTheme(ctx, userID, req.Peer, req.Emoticon)
	if err != nil {
		return out, err
	}
	otherPeer := domain.Peer{Type: domain.PeerTypeUser, ID: userID}
	changedPeer := false
	if req.Peer.ID != userID && !req.RecipientBlocked {
		changedPeer, err = s.dialogs.SetChatTheme(ctx, req.Peer.ID, otherPeer, req.Emoticon)
		if err != nil {
			return out, err
		}
	}
	out.Changed = changedSelf || changedPeer
	if !out.Changed {
		return out, nil
	}
	send, err := s.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
		SenderUserID:     userID,
		RecipientUserID:  req.Peer.ID,
		RandomID:         chatThemeServiceMessageRandomID(userID, req.Peer.ID, req.Emoticon, req.Date),
		Media:            chatThemeServiceMedia(req.Emoticon),
		Silent:           true,
		Date:             req.Date,
		OriginAuthKeyID:  req.OriginAuthKeyID,
		OriginSessionID:  req.OriginSessionID,
		RecipientBlocked: req.RecipientBlocked,
	})
	if err != nil {
		return out, err
	}
	out.Send = send
	return out, nil
}

// GetPrivateNoForwards returns the canonical content-protection state for one
// ordinary private chat.
func (s *Service) GetPrivateNoForwards(ctx context.Context, userID, peerUserID int64) (domain.PrivateNoForwardsState, error) {
	if s == nil || s.messages == nil || userID == 0 || peerUserID == 0 || userID == peerUserID {
		return domain.PrivateNoForwardsState{}, domain.ErrMessageIDInvalid
	}
	backend, ok := s.messages.(store.PrivateNoForwardsStore)
	if !ok {
		return domain.PrivateNoForwardsState{}, nil
	}
	return backend.GetPrivateNoForwards(ctx, userID, peerUserID)
}

// TogglePrivateNoForwards atomically mutates the pair state and appends the
// corresponding service message when the official state machine requires one.
func (s *Service) TogglePrivateNoForwards(ctx context.Context, userID int64, req domain.TogglePrivateNoForwardsRequest) (domain.TogglePrivateNoForwardsResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.TogglePrivateNoForwardsResult{}, domain.ErrMessageIDInvalid
	}
	if req.ActorUserID == 0 {
		req.ActorUserID = userID
	}
	if req.ActorUserID != userID || req.PeerUserID == 0 || req.PeerUserID == userID ||
		req.RequestMsgID < 0 || req.RequestMsgID > domain.MaxMessageBoxID {
		return domain.TogglePrivateNoForwardsResult{}, domain.ErrMessageIDInvalid
	}
	if err := s.ensureCanSend(ctx, userID); err != nil {
		return domain.TogglePrivateNoForwardsResult{}, err
	}
	backend, ok := s.messages.(store.PrivateNoForwardsStore)
	if !ok {
		return domain.TogglePrivateNoForwardsResult{}, domain.ErrMessageIDInvalid
	}
	return backend.TogglePrivateNoForwards(ctx, req)
}

func chatThemeServiceMedia(emoticon string) *domain.MessageMedia {
	return &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind:              domain.MessageServiceActionSetChatTheme,
			ChatThemeEmoticon: emoticon,
		},
	}
}

func chatThemeServiceMessageRandomID(userID, peerUserID int64, emoticon string, date int) int64 {
	var id int64 = 0x43485448454d45
	id ^= userID << 21
	id ^= peerUserID << 7
	id ^= int64(date) << 33
	for _, r := range emoticon {
		id = (id << 5) - id + int64(r)
	}
	if id == 0 {
		return 0x43485401
	}
	return id
}

// ForwardPrivateMessages forwards private text messages visible to the current account.
func (s *Service) ForwardPrivateMessages(ctx context.Context, userID int64, req domain.ForwardPrivateMessagesRequest) (domain.ForwardPrivateMessagesResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.ForwardPrivateMessagesResult{OwnerUserID: userID}, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	if err := s.ensureCanSend(ctx, req.OwnerUserID); err != nil {
		return domain.ForwardPrivateMessagesResult{OwnerUserID: req.OwnerUserID}, err
	}
	return s.messages.ForwardPrivateMessages(ctx, req)
}

// GetMessages returns exact owner-visible message boxes in request order.
func (s *Service) GetMessages(ctx context.Context, userID int64, ids []int) (domain.MessageList, error) {
	if s == nil || s.messages == nil || userID == 0 || len(ids) == 0 {
		return domain.MessageList{}, nil
	}
	list, err := s.messages.GetByIDs(ctx, userID, ids)
	if err != nil {
		return domain.MessageList{}, err
	}
	return s.projectMessageUsers(ctx, userID, list)
}

type messageByUIDStore interface {
	GetByUID(ctx context.Context, userID, uid int64) (domain.Message, bool, error)
}

// GetMessageByUID translates a shared private message id into one owner's exact box row.
// It is intentionally an optional capability so lightweight MessageStore test doubles that
// never exercise callback translation do not need a meaningless implementation.
func (s *Service) GetMessageByUID(ctx context.Context, userID, uid int64) (domain.Message, bool, error) {
	if s == nil || userID == 0 || uid == 0 {
		return domain.Message{}, false, nil
	}
	provider, ok := s.messages.(messageByUIDStore)
	if !ok {
		return domain.Message{}, false, nil
	}
	msg, found, err := provider.GetByUID(ctx, userID, uid)
	if err != nil || !found {
		return domain.Message{}, found, err
	}
	list, err := s.projectMessageUsers(ctx, userID, domain.MessageList{Messages: []domain.Message{msg}})
	if err != nil || len(list.Messages) != 1 {
		return domain.Message{}, false, err
	}
	return list.Messages[0], true, nil
}

// GetHistory returns the history messages of a peer for the current account.
func (s *Service) GetHistory(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error) {
	return s.list(ctx, userID, filter)
}

// Search returns the current account's message search results. An empty Query is equivalent to a history list filter.
func (s *Service) Search(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error) {
	return s.list(ctx, userID, filter)
}

// SearchPrivateMedia returns messages in a private chat that belong to the given media category (the shared media tab).
func (s *Service) SearchPrivateMedia(ctx context.Context, userID, peerID int64, req domain.MediaSearchRequest) (domain.MessageList, error) {
	if s == nil || s.messages == nil || userID == 0 || peerID == 0 {
		return domain.MessageList{}, nil
	}
	list, err := s.messages.SearchPrivateMedia(ctx, userID, peerID, req)
	if err != nil {
		return domain.MessageList{}, err
	}
	return s.projectMessageUsers(ctx, userID, list)
}

// CountPrivateMediaCategories returns exact counts per base media category for a private chat.
func (s *Service) CountPrivateMediaCategories(ctx context.Context, userID, peerID int64) (domain.MediaCategoryCounts, error) {
	if s == nil || s.messages == nil || userID == 0 || peerID == 0 {
		return domain.MediaCategoryCounts{}, nil
	}
	return s.cachedPrivateMediaCounts(ctx, userID, peerID)
}

// ReadHistory marks the current account's inbox for a peer as read and generates an outbox read receipt for the sender.
func (s *Service) ReadHistory(ctx context.Context, userID int64, req domain.ReadHistoryRequest) (domain.ReadHistoryResult, error) {
	if s == nil || userID == 0 {
		return domain.ReadHistoryResult{Peer: req.Peer, MaxID: req.MaxID}, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	if s.messages != nil {
		return s.messages.ReadHistory(ctx, req)
	}
	if s.dialogs != nil {
		return s.dialogs.MarkRead(ctx, userID, req.Peer, req.MaxID)
	}
	return domain.ReadHistoryResult{OwnerUserID: userID, Peer: req.Peer, MaxID: req.MaxID}, nil
}

// ReadMessageContents checks exact owner-visible private message IDs for content-read sync.
func (s *Service) ReadMessageContents(ctx context.Context, userID int64, req domain.ReadMessageContentsRequest) (domain.ReadMessageContentsResult, error) {
	res := domain.ReadMessageContentsResult{OwnerUserID: userID}
	if s == nil || s.messages == nil || userID == 0 {
		return res, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	if req.OwnerUserID != userID || len(req.IDs) > domain.MaxGetMessageIDs {
		return res, domain.ErrMessageIDInvalid
	}
	for _, id := range req.IDs {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return res, domain.ErrMessageIDInvalid
		}
	}
	return s.messages.ReadMessageContents(ctx, req)
}

// GetOutboxReadDate returns the time a given outgoing private message of the current account was read by the peer.
func (s *Service) GetOutboxReadDate(ctx context.Context, userID int64, req domain.OutboxReadDateRequest) (int, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return 0, domain.ErrMessageIDInvalid
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	return s.messages.GetOutboxReadDate(ctx, req)
}

// SetMessageReactions replaces the current user's reactions on a visible private message.
func (s *Service) SetMessageReactions(ctx context.Context, userID int64, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	if req.UserID == 0 {
		req.UserID = userID
	}
	if req.UserID != userID || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	return s.messages.SetMessageReactions(ctx, req)
}

// VoteMessagePoll votes on a poll attached to a private message (empty options = retract vote).
func (s *Service) VoteMessagePoll(ctx context.Context, userID int64, req domain.VotePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.PrivateMessagePollResult{}, domain.ErrMessageIDInvalid
	}
	if req.UserID == 0 {
		req.UserID = userID
	}
	if req.UserID != userID || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.PrivateMessagePollResult{}, domain.ErrMessageIDInvalid
	}
	return s.messages.VoteMessagePoll(ctx, req)
}

// CloseMessagePoll closes a poll attached to a private message (only the poll creator).
func (s *Service) CloseMessagePoll(ctx context.Context, userID int64, req domain.ClosePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.PrivateMessagePollResult{}, domain.ErrMessageIDInvalid
	}
	if req.UserID == 0 {
		req.UserID = userID
	}
	if req.UserID != userID || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.PrivateMessagePollResult{}, domain.ErrMessageIDInvalid
	}
	return s.messages.CloseMessagePoll(ctx, req)
}

// GetMessageReactions returns reaction summaries for visible private messages.
func (s *Service) GetMessageReactions(ctx context.Context, userID int64, req domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.PrivateMessageReactionsResult{}, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	if req.OwnerUserID != userID || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || len(req.IDs) > domain.MaxGetMessageIDs {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	for _, id := range req.IDs {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
		}
	}
	return s.messages.GetMessageReactions(ctx, req)
}

// SavedReactionTags returns the global or one-sub-dialog Saved Messages tag list.
func (s *Service) SavedReactionTags(ctx context.Context, userID int64, savedPeer domain.Peer, limit int) ([]domain.SavedReactionTag, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > domain.MaxSavedReactionTags {
		limit = domain.MaxSavedReactionTags
	}
	return s.messages.ListSavedReactionTags(ctx, domain.SavedReactionTagsRequest{
		UserID:    userID,
		SavedPeer: savedPeer,
		Limit:     limit,
	})
}

// UpdateSavedReactionTag stores or removes the optional global title for one
// tag that is currently assigned to at least one visible Saved Message.
func (s *Service) UpdateSavedReactionTag(ctx context.Context, userID int64, tag domain.SavedReactionTag) error {
	if s == nil || s.messages == nil || userID == 0 || !tag.Reaction.Valid() {
		return domain.ErrReactionInvalid
	}
	tag.UserID = userID
	return s.messages.UpsertSavedReactionTag(ctx, tag)
}

// EditMessage edits a private text message sent by the current account.
func (s *Service) EditMessage(ctx context.Context, userID int64, req domain.EditMessageRequest) (domain.EditMessageResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.EditMessageResult{OwnerUserID: userID}, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	if req.SetReplyMarkup {
		if err := domain.ValidateReplyMarkup(req.ReplyMarkup); err != nil {
			return domain.EditMessageResult{OwnerUserID: userID}, err
		}
	}
	return s.messages.EditMessage(ctx, req)
}

// PinPrivateMessage toggles the pin state of a private message visible to the current
// account; when not pm_oneside it also flips the peer's view.
func (s *Service) PinPrivateMessage(ctx context.Context, userID int64, req domain.PinPrivateMessageRequest) (domain.PinPrivateMessageResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.PinPrivateMessageResult{OwnerUserID: userID}, domain.ErrMessageIDInvalid
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	if req.OwnerUserID != userID {
		return domain.PinPrivateMessageResult{OwnerUserID: userID}, domain.ErrMessageIDInvalid
	}
	var serviceReq domain.SendPrivateTextRequest
	var automation businessAutomationContext
	var automationOK bool
	if req.Pinned && !req.PmOneside && req.Peer.ID != userID {
		if err := s.ensureCanSend(ctx, userID); err != nil {
			return domain.PinPrivateMessageResult{OwnerUserID: userID}, err
		}
		serviceReq = domain.SendPrivateTextRequest{
			SenderUserID: userID, RecipientUserID: req.Peer.ID, Date: req.Date, Silent: req.Silent,
			RecipientBlocked: req.RecipientBlocked, OriginAuthKeyID: req.OriginAuthKeyID, OriginSessionID: req.OriginSessionID,
			Media:   &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionPinMessage}},
			ReplyTo: &domain.MessageReply{MessageID: req.MessageID, Peer: req.Peer},
		}
		automation, automationOK = s.prepareBusinessAutomation(ctx, serviceReq)
	}
	res, err := s.messages.PinPrivateMessage(ctx, req)
	// Preserve normal private-send observers, only after the combined commit.
	if err == nil && res.ServiceMessage.SenderMessage.ID != 0 {
		serviceReq.RandomID = res.ServiceMessage.SenderMessage.RandomID
		serviceReq.Date = res.ServiceMessage.SenderMessage.Date
		if automationOK {
			s.runBusinessAutomation(ctx, serviceReq, res.ServiceMessage, automation)
		}
		if s.botResponder != nil && s.botResponder.HandlesBot(req.Peer.ID) {
			s.botResponder.OnPrivateMessage(ctx, req.Peer.ID, res.ServiceMessage.RecipientMessage, domain.ClientSessionMetadata{})
		}
	}
	return res, err
}

// UnpinAllPrivateMessages clears all pins between the current account and a private peer.
func (s *Service) UnpinAllPrivateMessages(ctx context.Context, userID int64, req domain.UnpinAllPrivateMessagesRequest) (domain.PinPrivateMessageResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.PinPrivateMessageResult{OwnerUserID: userID}, domain.ErrMessageIDInvalid
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	if req.OwnerUserID != userID {
		return domain.PinPrivateMessageResult{OwnerUserID: userID}, domain.ErrMessageIDInvalid
	}
	return s.messages.UnpinAllPrivateMessages(ctx, req)
}

// DeleteMessages deletes a set of messages from the current account's perspective; when revoke is set it also deletes the peer's private box.
func (s *Service) DeleteMessages(ctx context.Context, userID int64, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.DeleteMessagesResult{OwnerUserID: userID}, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	return s.messages.DeleteMessages(ctx, req)
}

// DeleteHistory clears the current account's history with a peer; when revoke is set it also deletes the peer's private box.
func (s *Service) DeleteHistory(ctx context.Context, userID int64, req domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.DeleteMessagesResult{OwnerUserID: userID}, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	return s.messages.DeleteHistory(ctx, req)
}

// GetSavedDialogs returns a page of saved-message subdialogs (messages.getSavedDialogs).
func (s *Service) GetSavedDialogs(ctx context.Context, userID int64, filter domain.SavedDialogsFilter) (domain.SavedDialogList, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.SavedDialogList{Full: true}, nil
	}
	return s.messages.ListSavedDialogs(ctx, userID, filter)
}

// GetPinnedSavedDialogs returns all pinned saved-message subdialogs (messages.getPinnedSavedDialogs).
func (s *Service) GetPinnedSavedDialogs(ctx context.Context, userID int64) (domain.SavedDialogList, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.SavedDialogList{Full: true}, nil
	}
	return s.messages.ListPinnedSavedDialogs(ctx, userID)
}

// GetSavedDialogsByPeers returns the specified saved-message subdialogs (messages.getSavedDialogsByID).
func (s *Service) GetSavedDialogsByPeers(ctx context.Context, userID int64, peers []domain.Peer) (domain.SavedDialogList, error) {
	if s == nil || s.messages == nil || userID == 0 || len(peers) == 0 {
		return domain.SavedDialogList{Full: true}, nil
	}
	return s.messages.ListSavedDialogsByPeers(ctx, userID, peers)
}

// ToggleSavedDialogPin toggles a saved-message subdialog's pin state and returns whether it actually changed.
func (s *Service) ToggleSavedDialogPin(ctx context.Context, userID int64, peer domain.Peer, pinned bool) (bool, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return false, nil
	}
	return s.messages.ToggleSavedDialogPin(ctx, userID, peer, pinned)
}

// ReorderPinnedSavedDialogs fully reorders the pinned saved-message ordering.
func (s *Service) ReorderPinnedSavedDialogs(ctx context.Context, userID int64, order []domain.Peer, force bool) error {
	if s == nil || s.messages == nil || userID == 0 {
		return nil
	}
	return s.messages.ReorderPinnedSavedDialogs(ctx, userID, order, force)
}

// DeleteSavedHistory deletes a saved-message subdialog's messages (single batch).
func (s *Service) DeleteSavedHistory(ctx context.Context, userID int64, req domain.DeleteSavedHistoryRequest) (domain.DeleteSavedHistoryResult, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.DeleteSavedHistoryResult{}, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	return s.messages.DeleteSavedHistory(ctx, req)
}

func (s *Service) ScheduleMessage(ctx context.Context, userID int64, req domain.ScheduleMessageRequest) (domain.ScheduledMessage, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.ScheduledMessage{}, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	scheduled, ok := s.messages.(store.ScheduledMessageStore)
	if !ok {
		return domain.ScheduledMessage{}, nil
	}
	return scheduled.CreateScheduledMessage(ctx, req)
}

func (s *Service) ListScheduledMessages(ctx context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.ScheduledMessageList{}, nil
	}
	if filter.OwnerUserID == 0 {
		filter.OwnerUserID = userID
	}
	scheduled, ok := s.messages.(store.ScheduledMessageStore)
	if !ok {
		return domain.ScheduledMessageList{}, nil
	}
	return scheduled.ListScheduledMessages(ctx, filter)
}

func (s *Service) EditScheduledMessage(ctx context.Context, userID int64, req domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.ScheduledMessage{}, nil
	}
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	scheduled, ok := s.messages.(store.ScheduledMessageStore)
	if !ok {
		return domain.ScheduledMessage{}, nil
	}
	return scheduled.EditScheduledMessage(ctx, req)
}

func (s *Service) GetScheduledMessages(ctx context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.ScheduledMessageList{}, nil
	}
	if filter.OwnerUserID == 0 {
		filter.OwnerUserID = userID
	}
	scheduled, ok := s.messages.(store.ScheduledMessageStore)
	if !ok {
		return domain.ScheduledMessageList{}, nil
	}
	return scheduled.GetScheduledMessages(ctx, filter)
}

func (s *Service) DeleteScheduledMessages(ctx context.Context, userID int64, filter domain.ScheduledMessageFilter, date int) ([]domain.ScheduledMessage, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return nil, nil
	}
	if filter.OwnerUserID == 0 {
		filter.OwnerUserID = userID
	}
	scheduled, ok := s.messages.(store.ScheduledMessageStore)
	if !ok {
		return nil, nil
	}
	return scheduled.DeleteScheduledMessages(ctx, filter, date)
}

func (s *Service) ClaimScheduledMessages(ctx context.Context, userID int64, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return nil, nil
	}
	if claim.OwnerUserID == 0 {
		claim.OwnerUserID = userID
	}
	scheduled, ok := s.messages.(store.ScheduledMessageStore)
	if !ok {
		return nil, nil
	}
	return scheduled.ClaimScheduledMessages(ctx, claim)
}

func (s *Service) ClaimDueScheduledMessages(ctx context.Context, now, limit, leaseSeconds int) ([]domain.ScheduledMessage, error) {
	if s == nil || s.messages == nil {
		return nil, nil
	}
	scheduled, ok := s.messages.(store.ScheduledMessageStore)
	if !ok {
		return nil, nil
	}
	return scheduled.ClaimDueScheduledMessages(ctx, now, limit, leaseSeconds)
}

func (s *Service) MarkScheduledMessageSent(ctx context.Context, ownerUserID int64, id, sentMessageID, date int) error {
	if s == nil || s.messages == nil {
		return nil
	}
	scheduled, ok := s.messages.(store.ScheduledMessageStore)
	if !ok {
		return nil
	}
	return scheduled.MarkScheduledMessageSent(ctx, ownerUserID, id, sentMessageID, date)
}

func (s *Service) ReleaseScheduledMessage(ctx context.Context, ownerUserID int64, id int, errText string) error {
	if s == nil || s.messages == nil {
		return nil
	}
	scheduled, ok := s.messages.(store.ScheduledMessageStore)
	if !ok {
		return nil
	}
	return scheduled.ReleaseScheduledMessage(ctx, ownerUserID, id, errText)
}

func (s *Service) HasScheduledMessages(ctx context.Context, userID int64, peer domain.Peer) (bool, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return false, nil
	}
	scheduled, ok := s.messages.(store.ScheduledMessageStore)
	if !ok {
		return false, nil
	}
	return scheduled.HasScheduledMessages(ctx, userID, peer)
}

func (s *Service) GetPrivateHistoryTTL(ctx context.Context, userID int64, peer domain.Peer) (int, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return 0, nil
	}
	ttl, ok := s.messages.(store.HistoryTTLStore)
	if !ok {
		return 0, nil
	}
	return ttl.GetPrivateHistoryTTL(ctx, userID, peer)
}

func (s *Service) SetPrivateHistoryTTL(ctx context.Context, userID int64, peer domain.Peer, period int) error {
	if s == nil || s.messages == nil || userID == 0 {
		return nil
	}
	ttl, ok := s.messages.(store.HistoryTTLStore)
	if !ok {
		return nil
	}
	return ttl.SetPrivateHistoryTTL(ctx, userID, peer, period)
}

func (s *Service) DefaultHistoryTTL(ctx context.Context, userID int64) (int, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return 0, nil
	}
	ttl, ok := s.messages.(store.HistoryTTLStore)
	if !ok {
		return 0, nil
	}
	return ttl.DefaultHistoryTTL(ctx, userID)
}

func (s *Service) SetDefaultHistoryTTL(ctx context.Context, userID int64, period int) error {
	if s == nil || s.messages == nil || userID == 0 {
		return nil
	}
	ttl, ok := s.messages.(store.HistoryTTLStore)
	if !ok {
		return nil
	}
	return ttl.SetDefaultHistoryTTL(ctx, userID, period)
}

func (s *Service) ClaimExpiredPrivateMessages(ctx context.Context, now, limit int) ([]domain.DeleteMessagesRequest, error) {
	if s == nil || s.messages == nil {
		return nil, nil
	}
	ttl, ok := s.messages.(store.HistoryTTLStore)
	if !ok {
		return nil, nil
	}
	return ttl.ClaimExpiredPrivateMessages(ctx, now, limit)
}

func (s *Service) list(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error) {
	if s == nil || s.messages == nil || userID == 0 {
		return domain.MessageList{}, nil
	}
	list, err := s.messages.ListByUser(ctx, userID, filter)
	if err != nil {
		return domain.MessageList{}, err
	}
	return s.projectMessageUsers(ctx, userID, list)
}

func (s *Service) projectMessageUsers(ctx context.Context, userID int64, list domain.MessageList) (domain.MessageList, error) {
	if s == nil || s.projector == nil {
		return list, nil
	}
	users, err := s.projector.ForViewer(ctx, userID, list.Users)
	if err != nil {
		return domain.MessageList{}, err
	}
	list.Users = users
	return list, nil
}

type privateUnreadReactionsStore interface {
	ListUnreadReactionMessages(ctx context.Context, ownerUserID int64, peer domain.Peer, limit int) ([]domain.Message, error)
	ReadPeerReactions(ctx context.Context, ownerUserID int64, peer domain.Peer) (int, error)
}

// ListUnreadReactionMessages returns messages with unread reactions under a private peer.
func (s *Service) ListUnreadReactionMessages(ctx context.Context, userID int64, peer domain.Peer, limit int) ([]domain.Message, error) {
	store, ok := s.messages.(privateUnreadReactionsStore)
	if !ok || userID == 0 {
		return nil, nil
	}
	return store.ListUnreadReactionMessages(ctx, userID, peer, limit)
}

// ReadPeerReactions clears all unread reactions under a private peer.
func (s *Service) ReadPeerReactions(ctx context.Context, userID int64, peer domain.Peer) (int, error) {
	store, ok := s.messages.(privateUnreadReactionsStore)
	if !ok || userID == 0 {
		return 0, nil
	}
	return store.ReadPeerReactions(ctx, userID, peer)
}
