package domain

import "fmt"

type NotificationSoundKind uint8

const (
	NotificationSoundDefault NotificationSoundKind = iota
	NotificationSoundNone
	NotificationSoundLocal
	NotificationSoundRingtone
)

// NotificationSound preserves the wire choice without importing TL types.
type NotificationSound struct {
	Kind  NotificationSoundKind `json:"kind"`
	Title string                `json:"title,omitempty"`
	Data  string                `json:"data,omitempty"`
	ID    int64                 `json:"id,omitempty"`
}

func (s NotificationSound) Valid() bool {
	switch s.Kind {
	case NotificationSoundDefault, NotificationSoundNone:
		return s.Title == "" && s.Data == "" && s.ID == 0
	case NotificationSoundLocal:
		return len(s.Title) <= 256 && len(s.Data) <= 4096 && s.Data != "" && s.ID == 0
	case NotificationSoundRingtone:
		return s.ID != 0 && s.Title == "" && s.Data == ""
	default:
		return false
	}
}

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
// nil means the item is unset (the client inherits the category default).
type PeerNotifySettings struct {
	ShowPreviews      *bool
	Silent            *bool
	MuteUntil         *int
	StoriesMuted      *bool
	StoriesHideSender *bool
	Sound             *NotificationSound
	StoriesSound      *NotificationSound
}

func (s PeerNotifySettings) Validate() error {
	if (s.Sound != nil && !s.Sound.Valid()) || (s.StoriesSound != nil && !s.StoriesSound.Valid()) {
		return fmt.Errorf("invalid notification sound")
	}
	return nil
}

// IsZero reports whether every setting is unset (no need to persist / treat as inherited default).
func (s PeerNotifySettings) IsZero() bool {
	return s.ShowPreviews == nil && s.Silent == nil && s.MuteUntil == nil &&
		s.StoriesMuted == nil && s.StoriesHideSender == nil && s.Sound == nil && s.StoriesSound == nil
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
	if s.Sound != nil {
		v := *s.Sound
		out.Sound = &v
	}
	if s.StoriesSound != nil {
		v := *s.StoriesSound
		out.StoriesSound = &v
	}
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
