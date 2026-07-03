package domain

import "errors"

const (
	// AICompose 最大文本长度沿用普通消息正文限制，避免 provider 请求被客户端巨大值打爆。
	MaxAIComposeTextLength = MaxMessageTextLength
	// MaxAIComposeEntityCount 限制 TextWithEntities 的实体数量。
	MaxAIComposeEntityCount = MaxMessageEntityCount
	// AICompose tone 的 appConfig 默认值与 TDesktop/DrKLO 消费点一致。
	MaxAIComposeToneTitleLength    = 12
	MaxAIComposeTonePromptLength   = 1024
	AIComposeToneSavedLimitDefault = 5
	AIComposeToneSavedLimitPremium = 20
	AIComposeToneExamplesNum       = 3
)

var (
	ErrAIComposeDisabled            = errors.New("ai compose disabled")
	ErrAIComposeInvalid             = errors.New("ai compose invalid")
	ErrAIComposeToneInvalid         = errors.New("ai compose tone invalid")
	ErrAIComposeToneNotFound        = errors.New("ai compose tone not found")
	ErrAIComposeToneLimitExceeded   = errors.New("ai compose tone limit exceeded")
	ErrAIComposeRateLimited         = errors.New("ai compose rate limited")
	ErrAIComposeProviderUnavailable = errors.New("ai compose provider unavailable")
	ErrAIComposeProviderTimeout     = errors.New("ai compose provider timeout")
)

type AIComposeText struct {
	Text     string
	Entities []MessageEntity
}

func (t AIComposeText) Clone() AIComposeText {
	out := t
	out.Entities = append([]MessageEntity(nil), t.Entities...)
	return out
}

type AIComposeToneRefKind string

const (
	AIComposeToneRefDefault AIComposeToneRefKind = "default"
	AIComposeToneRefID      AIComposeToneRefKind = "id"
	AIComposeToneRefSlug    AIComposeToneRefKind = "slug"
)

type AIComposeToneRef struct {
	Kind        AIComposeToneRefKind
	DefaultTone string
	ID          int64
	AccessHash  int64
	Slug        string
}

func (r AIComposeToneRef) Empty() bool {
	return r.Kind == "" && r.DefaultTone == "" && r.ID == 0 && r.Slug == ""
}

type AIComposeRequest struct {
	UserID          int64
	Text            AIComposeText
	Proofread       bool
	Emojify         bool
	TranslateToLang string
	Tone            AIComposeToneRef
}

type AIComposeResult struct {
	ResultText AIComposeText
	DiffText   *AIComposeText
}

type AITextGenerationRequest struct {
	UserID      int64
	Text        AIComposeText
	Instruction string
}

type AIComposeTone struct {
	Default        bool
	Creator        bool
	ID             int64
	AccessHash     int64
	OwnerUserID    int64
	Slug           string
	Title          string
	EmojiID        int64
	Prompt         string
	InstallsCount  int
	AuthorID       int64
	DisplayAuthor  bool
	CreatedAt      int64
	UpdatedAt      int64
	Saved          bool
	ExampleEnglish *AIComposeToneExample
}

func (t AIComposeTone) Clone() AIComposeTone {
	out := t
	if t.ExampleEnglish != nil {
		ex := t.ExampleEnglish.Clone()
		out.ExampleEnglish = &ex
	}
	return out
}

type AIComposeToneExample struct {
	From AIComposeText
	To   AIComposeText
}

func (e AIComposeToneExample) Clone() AIComposeToneExample {
	return AIComposeToneExample{
		From: e.From.Clone(),
		To:   e.To.Clone(),
	}
}

type AIComposeToneInput struct {
	UserID        int64
	DisplayAuthor bool
	EmojiID       int64
	Title         string
	Prompt        string
}

type AIComposeToneUpdate struct {
	Ref           AIComposeToneRef
	UserID        int64
	DisplayAuthor *bool
	EmojiID       *int64
	Title         *string
	Prompt        *string
}

type AIComposeTones struct {
	Hash  int64
	Tones []AIComposeTone
}

func (t AIComposeTones) Clone() AIComposeTones {
	out := t
	out.Tones = make([]AIComposeTone, 0, len(t.Tones))
	for _, tone := range t.Tones {
		out.Tones = append(out.Tones, tone.Clone())
	}
	return out
}
