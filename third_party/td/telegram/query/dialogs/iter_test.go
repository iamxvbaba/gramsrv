package dialogs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgmock"
)

func generateDialogs(count int) []tg.DialogClass {
	r := make([]tg.DialogClass, 0, count)

	for i := 0; i < count; i++ {
		r = append(r, &tg.Dialog{
			Peer: &tg.PeerChannel{ChannelID: int64(i)},
		})
	}

	return r
}

func result(r []tg.DialogClass, count int) tg.MessagesDialogsClass {
	msgs := make([]tg.MessageClass, 0, len(r))
	for i, dlg := range r {
		peerID, ok := peerFromDialog(dlg)
		if !ok {
			continue
		}
		msgs = append(msgs, &tg.Message{
			ID:     i,
			PeerID: peerID,
		})
	}

	chats := make([]tg.ChatClass, 0, len(r))
	for i, dlg := range r {
		peerID, ok := peerFromDialog(dlg)
		if !ok {
			continue
		}
		id := peerID.(*tg.PeerChannel).ChannelID
		chats = append(chats, &tg.Channel{
			ID:         id,
			AccessHash: 10,
			Photo: &tg.ChatPhoto{
				PhotoID: int64(i),
			},
		})
	}

	return &tg.MessagesDialogsSlice{
		Dialogs:  r,
		Messages: msgs,
		Chats:    chats,
		Count:    count,
	}
}

func TestIterator(t *testing.T) {
	ctx := context.Background()
	mock := tgmock.NewRequire(t)
	limit := 10
	totalRows := 3 * limit
	expected := generateDialogs(totalRows)
	raw := tg.NewClient(mock)

	mock.Expect().ThenResult(result(expected[0:limit], totalRows))
	mock.Expect().ThenResult(result(expected[limit:2*limit], totalRows))
	mock.Expect().ThenResult(result(expected[2*limit:3*limit], totalRows))
	mock.Expect().ThenResult(result(expected[3*limit:], totalRows))

	iter := NewQueryBuilder(raw).GetDialogs().BatchSize(10).Iter()
	i := 0
	for iter.Next(ctx) {
		expectedPeer, ok := peerFromDialog(expected[i])
		require.True(t, ok)
		actualPeer, ok := peerFromDialog(iter.Value().Dialog)
		require.True(t, ok)
		require.Equal(t, expectedPeer, actualPeer)
		i++
	}
	require.NoError(t, iter.Err())
	require.Equal(t, totalRows, i)

	total, err := iter.Total(ctx)
	require.NoError(t, err)
	require.Equal(t, totalRows, total)

	mock.ExpectCall(&tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      1,
	}).ThenResult(result(expected[:0], totalRows))
	total, err = iter.FetchTotal(ctx)
	require.NoError(t, err)
	require.Equal(t, totalRows, total)
}

func TestIteratorSkipsDialogCommunity(t *testing.T) {
	ctx := context.Background()
	mock := tgmock.NewRequire(t)
	raw := tg.NewClient(mock)

	const channelID = int64(42)
	dialog := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: channelID}}
	community := &tg.DialogCommunity{CommunityID: 100}
	mock.Expect().ThenResult(&tg.MessagesDialogs{
		Dialogs: []tg.DialogClass{community, dialog, community},
		Messages: []tg.MessageClass{&tg.Message{
			ID:     7,
			PeerID: dialog.Peer,
		}},
		Chats: []tg.ChatClass{&tg.Channel{
			ID:         channelID,
			AccessHash: 10,
			Photo:      &tg.ChatPhoto{},
		}},
	})
	mock.Expect().ThenResult(&tg.MessagesDialogs{})

	iter := NewQueryBuilder(raw).GetDialogs().BatchSize(10).Iter()
	require.True(t, iter.Next(ctx))
	value := iter.Value()
	require.Equal(t, dialog, value.Dialog)
	require.Equal(t, &tg.InputPeerChannel{ChannelID: channelID, AccessHash: 10}, value.Peer)
	require.Equal(t, 7, value.Last.GetID())
	require.False(t, iter.Next(ctx))
	require.NoError(t, iter.Err())
}
