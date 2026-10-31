package rpc

import (
	"telesrv/internal/domain"
	"time"
)

const (
	maxSendMessageTextLength = domain.MaxMessageTextLength
	maxReplyQuoteLength      = domain.MaxMessageReplyQuoteLength
	maxMessageSearchQLength  = 256
	maxMessageEntityCount    = domain.MaxMessageEntityCount
	maxGetMessagesIDs        = 100
	maxMessageSearchFilters  = 32
	maxForumTopicIDs         = 100
	maxDialogInputPeers      = 100
	maxSearchResultsLimit    = 100
	maxReactionVector        = 16
	maxReactionListOffset    = 128
	maxReportOptionLength    = 32
	maxReportCommentLength   = domain.MaxModerationCommentRunes
	maxReportRandomIDLength  = 128
	maxReadMetrics           = 100
	maxBusinessConnIDLength  = 128
	maxSendMultiMediaItems   = 10
	maxScheduledMessagePage  = 100
	maxForumTopicTitleLength = 128
	maxPollVoteOptions       = 10
	maxPollOptionBytes       = 256
	maxPollVotesOffsetLength = 128
	maxTodoItems             = 30
	maxTodoTitleLength       = 200
	// maxTodoItemID is a defensive upper bound on checklist item ids: the protocol
	// field is int32, and WebA uses sparse local ids of around 8 digits, so the item
	// count limit or a sequential-allocation assumption cannot serve as the id bound.
	maxTodoItemID          = 1<<31 - 1
	maxVenueTitleLength    = 256
	maxVenueAddressLength  = 512
	maxVenueProviderLength = 64
	maxVenueIDLength       = 256
	// live location period bounds (official 60s..1d; 0x7FFFFFFF = share until
	// stopped manually).
	minLiveLocationPeriod     = 60
	maxLiveLocationPeriod     = 86400
	foreverLiveLocationPeriod = 0x7FFFFFFF
	maxLiveLocationHeading    = 360
	maxProximityRadiusMeters  = 100000
	// maxGeoAccuracyRadiusMeters shares the proximity radius's order of magnitude;
	// out-of-range values silently clamp to zero (treated as unknown accuracy).
	maxGeoAccuracyRadiusMeters = 100000
	maxCommonChatsLimit        = domain.MaxCommonChannelsLimit
	maxStickerSearchQLength    = 128
	maxStickerSearchLangs      = 16
	maxEmojiLangCodeLength     = 32
	maxEmojiDocuments          = 100
	maxSavedReactionTagTitle   = 12
	defaultTopReactionsLimit   = 14
	sendMessageRateWindow      = time.Minute
	forumGeneralTopicID        = 1
	forumGeneralIconColor      = 0x6FB9F0
)
