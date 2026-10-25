package rpc

import (
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func domainNotificationSound(value tg.NotificationSoundClass) (domain.NotificationSound, error) {
	var sound domain.NotificationSound
	switch v := value.(type) {
	case *tg.NotificationSoundDefault:
		sound.Kind = domain.NotificationSoundDefault
	case *tg.NotificationSoundNone:
		sound.Kind = domain.NotificationSoundNone
	case *tg.NotificationSoundLocal:
		sound = domain.NotificationSound{Kind: domain.NotificationSoundLocal, Title: v.Title, Data: v.Data}
	case *tg.NotificationSoundRingtone:
		sound = domain.NotificationSound{Kind: domain.NotificationSoundRingtone, ID: v.ID}
	default:
		return sound, inputRequestInvalidErr()
	}
	if !sound.Valid() {
		return sound, inputRequestInvalidErr()
	}
	return sound, nil
}

func tgNotificationSound(sound domain.NotificationSound) tg.NotificationSoundClass {
	switch sound.Kind {
	case domain.NotificationSoundDefault:
		return &tg.NotificationSoundDefault{}
	case domain.NotificationSoundNone:
		return &tg.NotificationSoundNone{}
	case domain.NotificationSoundLocal:
		return &tg.NotificationSoundLocal{Title: sound.Title, Data: sound.Data}
	case domain.NotificationSoundRingtone:
		return &tg.NotificationSoundRingtone{ID: sound.ID}
	default:
		panic("invalid persisted notification sound")
	}
}
