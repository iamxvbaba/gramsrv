package domain

// NotifyScopeKind distinguishes the scope of a notification setting: a specific peer / the three global defaults (private chats/groups/channels).
type NotifyScopeKind string

const (
	NotifyScopePeer       NotifyScopeKind = "peer"
	NotifyScopeUsers      NotifyScopeKind = "users"
	NotifyScopeChats      NotifyScopeKind = "chats"
	NotifyScopeBroadcasts NotifyScopeKind = "broadcasts"
)

// NotifyScope uniquely identifies a notification setting: a peer scope uses Peer (+TopicID to distinguish forum topics,
// 0=the whole peer), while the three default scopes leave Peer/TopicID empty.
type NotifyScope struct {
	Kind    NotifyScopeKind
	Peer    Peer
	TopicID int
}

// PeerNotifySettings is the business-layer representation of peerNotifySettings. Each field is a pointer = optional:
// nil means the item is unset (the client inherits the category default). The sound fields are always default; v1 does not model
// custom sounds (rare, deferred).
type PeerNotifySettings struct {
	ShowPreviews      *bool
	Silent            *bool
	MuteUntil         *int
	StoriesMuted      *bool
	StoriesHideSender *bool
}

// IsZero reports whether every setting is unset (no need to persist / treat as inherited default).
func (s PeerNotifySettings) IsZero() bool {
	return s.ShowPreviews == nil && s.Silent == nil && s.MuteUntil == nil &&
		s.StoriesMuted == nil && s.StoriesHideSender == nil
}

// NotifyException is a peer's (optionally forum topic's) non-default notification setting, used by
// account.getNotifyExceptions to list a global index of "chats with custom notification settings".
type NotifyException struct {
	Peer     Peer
	TopicID  int
	Settings PeerNotifySettings
}

// Clone deep-copies the pointer fields to isolate the in-memory store.
func (s PeerNotifySettings) Clone() PeerNotifySettings {
	out := PeerNotifySettings{}
	if s.ShowPreviews != nil {
		v := *s.ShowPreviews
		out.ShowPreviews = &v
	}
	if s.Silent != nil {
		v := *s.Silent
		out.Silent = &v
	}
	if s.MuteUntil != nil {
		v := *s.MuteUntil
		out.MuteUntil = &v
	}
	if s.StoriesMuted != nil {
		v := *s.StoriesMuted
		out.StoriesMuted = &v
	}
	if s.StoriesHideSender != nil {
		v := *s.StoriesHideSender
		out.StoriesHideSender = &v
	}
	return out
}
