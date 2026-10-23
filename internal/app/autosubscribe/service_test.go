package autosubscribe

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

type fakeStore struct {
	channels []int64
	users    []int64
}

func (f *fakeStore) AddAutoSubscribeChannel(_ context.Context, channelID int64, _ string) (bool, error) {
	for _, id := range f.channels {
		if id == channelID {
			return false, nil
		}
	}
	f.channels = append(f.channels, channelID)
	return true, nil
}

func (f *fakeStore) RemoveAutoSubscribeChannel(_ context.Context, channelID int64) (bool, error) {
	for i, id := range f.channels {
		if id == channelID {
			f.channels = append(f.channels[:i], f.channels[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) ListAutoSubscribeChannels(context.Context) ([]domain.AutoSubscribeChannel, error) {
	out := make([]domain.AutoSubscribeChannel, 0, len(f.channels))
	for _, id := range f.channels {
		out = append(out, domain.AutoSubscribeChannel{ChannelID: id})
	}
	return out, nil
}

func (f *fakeStore) AutoSubscribeChannelIDs(context.Context) ([]int64, error) {
	return append([]int64(nil), f.channels...), nil
}

func (f *fakeStore) EligibleAutoSubscribeUserIDs(context.Context) ([]int64, error) {
	return append([]int64(nil), f.users...), nil
}

type fakeChannels struct {
	joins   map[[2]int64]int
	members map[int64]bool
	fail    map[int64]bool
}

func (f *fakeChannels) JoinChannel(_ context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error) {
	if date <= 0 {
		return domain.CreateChannelResult{}, errors.New("join date must be set")
	}
	if f.fail[userID] {
		return domain.CreateChannelResult{}, errors.New("banned")
	}
	if f.members[userID] {
		return domain.CreateChannelResult{}, domain.ErrUserAlreadyParticipant
	}
	f.joins[[2]int64{userID, channelID}]++
	return domain.CreateChannelResult{}, nil
}

func (f *fakeChannels) GetChannelByID(_ context.Context, channelID int64) (domain.Channel, error) {
	if channelID == 404 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	return domain.Channel{ID: channelID}, nil
}

func TestAddJoinsExistingUsersAndSignupJoinsNewOnes(t *testing.T) {
	ctx := context.Background()
	st := &fakeStore{users: []int64{1, 2, 3, 4}}
	ch := &fakeChannels{joins: map[[2]int64]int{}, members: map[int64]bool{2: true}, fail: map[int64]bool{3: true}}
	svc := NewService(WithStore(st), WithChannels(ch))

	added, joined, err := svc.Add(ctx, 100, "admin")
	if err != nil || !added || joined != 2 {
		t.Fatalf("add = %v, %d, %v; want added, 2 joined (1 member, 1 failure)", added, joined, err)
	}
	if added, _, err := svc.Add(ctx, 100, "admin"); err != nil || added {
		t.Fatalf("second add = %v, %v", added, err)
	}
	if _, _, err := svc.Add(ctx, 404, "admin"); err == nil {
		t.Fatal("unknown channel accepted")
	}

	svc.JoinNewUser(ctx, 9)
	if ch.joins[[2]int64{9, 100}] != 1 {
		t.Fatalf("new user not joined: %v", ch.joins)
	}
	if removed, err := svc.Remove(ctx, 100); err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	svc.JoinNewUser(ctx, 10)
	if ch.joins[[2]int64{10, 100}] != 0 {
		t.Fatal("user joined a removed channel")
	}
}
