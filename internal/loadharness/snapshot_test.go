package loadharness

import (
	"path/filepath"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

func TestMergeDialogPageCapturesChannelCursorAndOffset(t *testing.T) {
	user := &tg.User{ID: 11}
	user.SetAccessHash(111)
	channel := &tg.Channel{ID: 22, Title: "group", Megagroup: true}
	channel.SetAccessHash(222)
	userDialog := &tg.Dialog{Peer: &tg.PeerUser{UserID: 11}, TopMessage: 7}
	channelDialog := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 22}, TopMessage: 8}
	channelDialog.SetPts(12)
	destination := make(map[clientPeerKey]ClientDialogState)
	last, err := mergeDialogPage(destination,
		[]tg.DialogClass{userDialog, channelDialog},
		[]tg.MessageClass{
			&tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 11}, Date: 101},
			&tg.Message{ID: 8, PeerID: &tg.PeerChannel{ChannelID: 22}, Date: 102},
		},
		[]tg.ChatClass{channel}, []tg.UserClass{user}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(destination) != 2 || last.PeerType != "channel" || last.PeerID != 22 || last.Pts != 12 || !last.HasPts || last.TopMessageDate != 102 {
		t.Fatalf("merged dialogs = %+v, last = %+v", destination, last)
	}
	if _, err := mergeDialogPage(destination,
		[]tg.DialogClass{channelDialog},
		[]tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerChannel{ChannelID: 22}, Date: 102}},
		[]tg.ChatClass{channel}, nil, false,
	); err == nil {
		t.Fatal("duplicate dialog page passed validation")
	}
	overlapDestination := make(map[clientPeerKey]ClientDialogState)
	pinnedDialog := *channelDialog
	pinnedDialog.Pinned = true
	if _, err := mergeDialogPage(overlapDestination,
		[]tg.DialogClass{&pinnedDialog},
		[]tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerChannel{ChannelID: 22}, Date: 102}},
		[]tg.ChatClass{channel}, nil, true,
	); err != nil {
		t.Fatal(err)
	}
	allowed := map[clientPeerKey]struct{}{{typ: "channel", id: 22}: {}}
	if _, overlaps, err := mergeDialogPageKnownOverlap(overlapDestination,
		[]tg.DialogClass{&pinnedDialog},
		[]tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerChannel{ChannelID: 22}, Date: 102}},
		[]tg.ChatClass{channel}, nil, false, allowed,
	); err != nil || overlaps != 1 {
		t.Fatalf("known pinned overlap count=%d err=%v", overlaps, err)
	}
	if _, _, err := mergeDialogPageKnownOverlap(overlapDestination,
		[]tg.DialogClass{&pinnedDialog},
		[]tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerChannel{ChannelID: 22}, Date: 102}},
		[]tg.ChatClass{channel}, nil, false, allowed,
	); err == nil {
		t.Fatal("second copy of a consumed pinned overlap passed validation")
	}
	channelWithoutPts := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 22}, TopMessage: 8}
	if _, err := mergeDialogPage(make(map[clientPeerKey]ClientDialogState),
		[]tg.DialogClass{channelWithoutPts},
		[]tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerChannel{ChannelID: 22}, Date: 102}},
		[]tg.ChatClass{channel}, nil, false,
	); err == nil {
		t.Fatal("channel dialog without pts passed validation")
	}
}

func TestClientStateRoundTripLocksSeededChannelIdentity(t *testing.T) {
	dataset, seedState, targets := snapshotFixture(t)
	seedIdentity, err := seedIdentitySHA256(seedState)
	if err != nil {
		t.Fatal(err)
	}
	state := &ClientState{
		Version: ClientStateVersion, DatasetSHA256: dataset.PlanSHA256, SeedIdentitySHA: seedIdentity,
		Accounts: make([]ClientAccountState, dataset.Config.Accounts),
	}
	for account := range state.Accounts {
		state.Accounts[account] = ClientAccountState{
			AccountIndex: account, UserID: targets[account].UserID,
			State: ClientUpdateState{Pts: account + 1, Date: 100},
		}
		expected := expectedDatasetPeers(dataset, seedState, targets, account)
		for peer := range expected {
			dialog := ClientDialogState{
				PeerType: peer.typ, PeerID: peer.id, AccessHash: 99,
				TopMessage: 1, TopMessageDate: 100, DatasetExpected: true,
			}
			if peer.typ == "channel" {
				dialog.HasPts, dialog.Pts = true, 5
			}
			state.Accounts[account].Dialogs = append(state.Accounts[account].Dialogs, dialog)
		}
	}
	if err := state.Validate(dataset, seedState, targets); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "client-state.json")
	if err := WriteClientState(path, state); err != nil {
		t.Fatal(err)
	}
	requireOwnerOnly(t, path)
	loaded, err := LoadClientState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Validate(dataset, seedState, targets); err != nil {
		t.Fatal(err)
	}
	seedState.Groups[0].ChannelID++
	if err := loaded.Validate(dataset, seedState, targets); err == nil {
		t.Fatal("client state accepted different seeded channel identity")
	}
}

func TestClientStateRejectsMissingExpectedPeer(t *testing.T) {
	dataset, seedState, targets := snapshotFixture(t)
	expected := expectedDatasetPeers(dataset, seedState, targets, 0)
	account := ClientAccountState{AccountIndex: 0, UserID: targets[0].UserID, State: ClientUpdateState{Date: 100}}
	for peer := range expected {
		dialog := ClientDialogState{PeerType: peer.typ, PeerID: peer.id, AccessHash: 1, TopMessage: 1, TopMessageDate: 1, DatasetExpected: true}
		if peer.typ == "channel" {
			dialog.HasPts, dialog.Pts = true, 1
		}
		account.Dialogs = append(account.Dialogs, dialog)
	}
	account.Dialogs = account.Dialogs[1:]
	if err := validateExpectedDatasetPeers(dataset, seedState, targets, &account); err == nil {
		t.Fatal("account with missing expected peer passed validation")
	}
}

func TestDialogsPaginationOnlyFinishesOnFullOrEmptyResponse(t *testing.T) {
	if dialogsPaginationDone(false, 100) {
		t.Fatal("non-empty dialogsSlice was treated as final when the client requested a larger page")
	}
	if !dialogsPaginationDone(false, 0) {
		t.Fatal("empty dialogsSlice did not finish pagination")
	}
	if !dialogsPaginationDone(true, 100) {
		t.Fatal("messages.dialogs full constructor did not finish pagination")
	}
}

func snapshotFixture(t *testing.T) (*Dataset, *DatasetSeedState, []SessionRecord) {
	t.Helper()
	cfg := DatasetConfig{
		Accounts: 4, Seed: 7, PrivateFanout: 1,
		HotGroups: 1, HotMembers: 4, HotHistory: 1,
	}
	dataset, err := PlanDataset(cfg)
	if err != nil {
		t.Fatal(err)
	}
	seedState, err := NewDatasetSeedState(dataset)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture models a pre-rich-state journal unless an individual test
	// explicitly opts into the newer phase.
	seedState.RichStateByAccount = nil
	for account := 0; account < cfg.Accounts; account++ {
		seedState.PrivateSentByAccount[account] = cfg.PrivateFanout
	}
	seedState.Groups[0].ChannelID = 500
	seedState.Groups[0].AccessHash = 600
	seedState.Groups[0].InviteCursor = 3
	seedState.Groups[0].InvitePendingEnd = 3
	seedState.HistorySentByAccount[dataset.Groups[0].MemberAccounts[0]] = 1
	targets := make([]SessionRecord, cfg.Accounts)
	for account := range targets {
		targets[account] = SessionRecord{AccountIndex: account, UserID: int64(100 + account), AccessHash: int64(200 + account), SessionFile: "session"}
	}
	return dataset, seedState, targets
}
