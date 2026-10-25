package loadharness

import (
	"path/filepath"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

func TestPlanOfflineMutationDefaultTopology(t *testing.T) {
	dataset, err := PlanDataset(DefaultDatasetConfig(1000))
	if err != nil {
		t.Fatal(err)
	}
	plan := planOfflineMutation(dataset)
	if got, want := len(plan), 40; got != want {
		t.Fatalf("dirty channels = %d, want %d", got, want)
	}
	total := 0
	for _, channel := range plan {
		total += channel.Messages
	}
	if got, want := total, 304; got != want {
		t.Fatalf("channel mutations = %d, want %d", got, want)
	}
	if plan[0].Messages != 120 || dataset.Groups[plan[0].GroupPosition].Tier != "hot" {
		t.Fatalf("multi-page channel plan = %+v", plan[0])
	}
	if plan[1].Messages != 100 {
		t.Fatalf("full-page boundary channel plan = %+v", plan[1])
	}
	group := dataset.Groups[plan[0].GroupPosition]
	for message := 0; message < 3; message++ {
		if got := offlineMutationSender(group, message); got != group.CreatorAccount {
			t.Fatalf("action message %d sender = %d, want creator %d", message, got, group.CreatorAccount)
		}
	}
}

func TestSentMessageObservation(t *testing.T) {
	peer := clientPeerKey{typ: "channel", id: 22}
	marker := "marker"
	observation, err := sentMessageObservation(&tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateNewChannelMessage{
			Message: &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 22}, Message: marker},
			Pts:     12, PtsCount: 1,
		},
	}}, peer, marker)
	if err != nil {
		t.Fatal(err)
	}
	if observation.ID != 7 || observation.Pts != 12 {
		t.Fatalf("observation = %+v", observation)
	}
	short, err := sentMessageObservation(&tg.UpdateShortSentMessage{ID: 8, Pts: 13}, clientPeerKey{typ: "user", id: 1}, "private")
	if err != nil || short.ID != 8 || short.Pts != 13 {
		t.Fatalf("short observation = %+v err=%v", short, err)
	}
	if _, err := sentMessageObservation(&tg.Updates{}, peer, marker); err == nil {
		t.Fatal("updates without marker passed validation")
	}
}

func TestOfflineMutationJournalPersistsMessagesAndActions(t *testing.T) {
	dataset, seedState, _ := snapshotFixture(t)
	seedIdentity, err := seedIdentitySHA256(seedState)
	if err != nil {
		t.Fatal(err)
	}
	plan := planOfflineMutation(dataset)
	path := filepath.Join(t.TempDir(), "mutation-state.json")
	state, err := loadOrCreateOfflineMutationState(path, dataset, seedIdentity, "baseline", plan)
	if err != nil {
		t.Fatal(err)
	}
	journal := &mutationJournal{path: path, dataset: dataset, plan: plan, state: state}
	if err := journal.persist(); err != nil {
		t.Fatal(err)
	}
	if err := journal.commitPrivate(0, 10, 20); err != nil {
		t.Fatal(err)
	}
	if err := journal.commitChannelMessage(0, 0, 30, 40); err != nil {
		t.Fatal(err)
	}
	if err := journal.beginAction(0, "edit"); err != nil {
		t.Fatal(err)
	}
	if err := journal.commitAction(0, "edit", 41); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateOfflineMutationState(path, dataset, seedIdentity, "baseline", plan)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PrivateMessageIDs[0] != 10 || loaded.AccountObservedPts[0] != 20 || loaded.Channels[0].MessageIDs[0] != 30 || loaded.Channels[0].LatestPts != 41 || !loaded.Channels[0].EditDone || loaded.Channels[0].EditPending {
		t.Fatalf("persisted mutation state = %+v", loaded)
	}
	requireOwnerOnly(t, path)
}

func TestOfflineMutationStateRejectsDifferentBaseline(t *testing.T) {
	dataset, seedState, _ := snapshotFixture(t)
	seedIdentity, err := seedIdentitySHA256(seedState)
	if err != nil {
		t.Fatal(err)
	}
	plan := planOfflineMutation(dataset)
	state, err := loadOrCreateOfflineMutationState(filepath.Join(t.TempDir(), "missing.json"), dataset, seedIdentity, "baseline-a", plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Validate(dataset, seedIdentity, "baseline-b", plan); err == nil {
		t.Fatal("mutation state accepted different baseline snapshot")
	}
}
