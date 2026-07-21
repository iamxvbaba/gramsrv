package domain

// BotAPIRichMessageInput is the protocol-neutral HTTP Bot API input passed to
// the RPC edge. Exactly one of HTML, Markdown, or BlocksJSON must be present.
// BlocksJSON and MediaJSON are retained in the DTO so unsupported Bot API 10.2
// shapes are rejected explicitly at the conversion boundary instead of being
// flattened or silently dropped.
type BotAPIRichMessageInput struct {
	HTML                string
	Markdown            string
	BlocksJSON          []byte
	MediaJSON           []byte
	RTL                 bool
	SkipEntityDetection bool
}

func (m BotAPIRichMessageInput) SourceCount() int {
	n := 0
	if m.HTML != "" {
		n++
	}
	if m.Markdown != "" {
		n++
	}
	if len(m.BlocksJSON) != 0 {
		n++
	}
	return n
}
