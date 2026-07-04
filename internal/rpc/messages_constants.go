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
	maxReportCommentLength   = 1024
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
	// maxTodoItemID 是清单项 id 的防御上限：协议字段是 int32，WebA 会用 8 位左右
	// 的稀疏本地 id，不能用条目数上限或顺序分配假设当 id 边界。
	maxTodoItemID          = 1<<31 - 1
	maxVenueTitleLength    = 256
	maxVenueAddressLength  = 512
	maxVenueProviderLength = 64
	maxVenueIDLength       = 256
	// live location 有效期边界（官方 60s..1d；0x7FFFFFFF = 手动停止前一直共享）。
	minLiveLocationPeriod     = 60
	maxLiveLocationPeriod     = 86400
	foreverLiveLocationPeriod = 0x7FFFFFFF
	maxLiveLocationHeading    = 360
	maxProximityRadiusMeters  = 100000
	// maxGeoAccuracyRadiusMeters 与 proximity radius 同量级上限；越界静默归零（按未知精度处理）。
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
