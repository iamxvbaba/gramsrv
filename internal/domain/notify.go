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

// NotifyScopeKind 区分通知设置的作用域：具体 peer / 三类全局默认（私聊/群/频道）。
type NotifyScopeKind string

const (
	NotifyScopePeer       NotifyScopeKind = "peer"
	NotifyScopeUsers      NotifyScopeKind = "users"
	NotifyScopeChats      NotifyScopeKind = "chats"
	NotifyScopeBroadcasts NotifyScopeKind = "broadcasts"
)

// NotifyScope 唯一标识一条通知设置：peer 作用域用 Peer(+TopicID 区分 forum 话题，
// 0=整 peer)，三类默认作用域 Peer/TopicID 为空。
type NotifyScope struct {
	Kind    NotifyScopeKind
	Peer    Peer
	TopicID int
}

// PeerNotifySettings 是 peerNotifySettings 的业务层表达。各字段为指针=可选：
// nil 表示该项未设置（客户端按所属类别默认继承）。
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

// IsZero 报告该设置是否全部未设置（无需持久化/可视为继承默认）。
func (s PeerNotifySettings) IsZero() bool {
	return s.ShowPreviews == nil && s.Silent == nil && s.MuteUntil == nil &&
		s.StoriesMuted == nil && s.StoriesHideSender == nil && s.Sound == nil && s.StoriesSound == nil
}

// NotifyException 是一个 peer（可含 forum 话题）的非默认通知设置，供
// account.getNotifyExceptions 列出"有自定义通知设置的会话"全局索引。
type NotifyException struct {
	Peer     Peer
	TopicID  int
	Settings PeerNotifySettings
}

// Clone 深拷贝指针字段，供内存 store 隔离。
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
