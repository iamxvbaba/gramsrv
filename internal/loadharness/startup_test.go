package loadharness

import (
	"crypto/sha256"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

func TestStartupRampDelay(t *testing.T) {
	ramp := 30 * time.Second
	if got := startupRampDelay(ramp, 0, 4); got != 0 {
		t.Fatalf("first delay = %s", got)
	}
	if got := startupRampDelay(ramp, 1, 4); got != 10*time.Second {
		t.Fatalf("second delay = %s", got)
	}
	if got := startupRampDelay(ramp, 3, 4); got != ramp {
		t.Fatalf("last delay = %s", got)
	}
}

func TestStartupAccountOrder(t *testing.T) {
	sequential := startupAccountOrder(6, StartupOrderAccountIndex, 7)
	if !slices.Equal(sequential, []int{0, 1, 2, 3, 4, 5}) {
		t.Fatalf("sequential order = %v", sequential)
	}
	first := startupAccountOrder(100, StartupOrderShuffled, 20260827)
	second := startupAccountOrder(100, StartupOrderShuffled, 20260827)
	if !slices.Equal(first, second) {
		t.Fatal("shuffled startup order is not deterministic")
	}
	if slices.Equal(first, startupAccountOrder(100, StartupOrderShuffled, 20260828)) {
		t.Fatal("different shuffled seeds produced identical order")
	}
	seen := make(map[int]bool, len(first))
	for _, account := range first {
		if account < 0 || account >= len(first) || seen[account] {
			t.Fatalf("invalid shuffled account %d in %v", account, first)
		}
		seen[account] = true
	}
}

func TestResolveStartupProfiles(t *testing.T) {
	tdesktop, err := resolveStartupProfile(StartupProfileTDesktopReturningV1)
	if err != nil {
		t.Fatal(err)
	}
	if !tdesktop.GetStateBeforeDifference || tdesktop.AccountDifference || tdesktop.Dialogs.limit(0) != 20 || tdesktop.Dialogs.limit(1) != 500 || !tdesktop.ForceChannelDifference {
		t.Fatalf("tdesktop profile = %+v", tdesktop)
	}
	tdlib, err := resolveStartupProfile(StartupProfileTDLibReturningV1)
	if err != nil {
		t.Fatal(err)
	}
	if tdlib.GetStateBeforeDifference || !tdlib.AccountDifference || tdlib.Dialogs.limit(0) != 100 || tdlib.Dialogs.limit(1) != 100 {
		t.Fatalf("tdlib profile = %+v", tdlib)
	}
	if device := tdlib.device(); device.LangPack != "android" || device.SystemVersion != "Android SDK 36" {
		t.Fatalf("tdlib device = %+v", device)
	}
	if _, err := resolveStartupProfile("unknown"); err == nil {
		t.Fatal("unknown startup profile passed validation")
	}
}

func TestRequireExactMarkers(t *testing.T) {
	if err := requireExactMarkers(map[string]int{"a": 1, "b": 1}, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := requireExactMarkers(map[string]int{"a": 2, "b": 1}, []string{"a", "b"}); err == nil {
		t.Fatal("duplicate marker passed validation")
	}
	if err := requireExactMarkers(map[string]int{"a": 1, "b": 1, "wrong": 1}, []string{"a", "b"}); err == nil {
		t.Fatal("wrong-account marker passed validation")
	}
}

func TestCollectChannelMarkersIncludesEdits(t *testing.T) {
	runID := "run"
	markers := make(map[string]int)
	collectChannelMarkers(markers,
		[]tg.MessageClass{&tg.Message{Message: "[run offline channel 0000 message 0001]"}},
		[]tg.UpdateClass{&tg.UpdateEditChannelMessage{Message: &tg.Message{Message: "[run offline channel 0000 message 0001] edited"}}},
		runID,
	)
	if markers["[run offline channel 0000 message 0001]"] != 1 || markers["[run offline channel 0000 message 0001] edited"] != 1 {
		t.Fatalf("collected markers = %v", markers)
	}
}

func TestValidateStartupDialogsRequiresCurrentDirtyChannelPts(t *testing.T) {
	dataset, seedState, targets := snapshotFixture(t)
	plan := planOfflineMutation(dataset)
	mutation := &OfflineMutationState{
		PrivateMessageIDs: []int{1, 1, 1, 1},
		Channels:          []OfflineMutationChannelState{{LatestPts: 50}},
	}
	expected := expectedDatasetPeers(dataset, seedState, targets, 0)
	expected[clientPeerKey{typ: "user", id: targets[dataset.Config.Accounts-1].UserID}] = struct{}{}
	dialogs := make([]ClientDialogState, 0, len(expected))
	for peer := range expected {
		dialog := ClientDialogState{PeerType: peer.typ, PeerID: peer.id, AccessHash: 1, TopMessage: 1, TopMessageDate: 1}
		if peer.typ == "user" && peer.id == targets[1].UserID {
			dialog.topMessageDigest = sha256.Sum256([]byte(offlinePrivateMarker(dataset, 0, 1)))
		}
		if peer.typ == "user" && peer.id == targets[dataset.Config.Accounts-1].UserID {
			dialog.topMessageDigest = sha256.Sum256([]byte(offlinePrivateMarker(dataset, dataset.Config.Accounts-1, 0)))
		}
		if peer.typ == "channel" {
			dialog.HasPts, dialog.Pts = true, 50
		}
		dialogs = append(dialogs, dialog)
	}
	if err := validateStartupDialogs(dataset, seedState, plan, mutation, targets, 0, dialogs); err != nil {
		t.Fatal(err)
	}
	for i := range dialogs {
		if dialogs[i].PeerType == "channel" {
			dialogs[i].Pts = 49
		}
	}
	if err := validateStartupDialogs(dataset, seedState, plan, mutation, targets, 0, dialogs); err == nil {
		t.Fatal("stale current channel pts passed validation")
	}
}

func TestWriteStartupReportOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup-report.json")
	report := &StartupRunReport{Version: StartupReportVersion, DatasetSHA256: "plan", ExpectedAccounts: 1, BusinessReady: 1, Pass: true}
	if err := WriteStartupReport(path, report); err != nil {
		t.Fatal(err)
	}
	requireOwnerOnly(t, path)
}

func TestStartupResponseBytesUsesPerMethodCounterDeltas(t *testing.T) {
	baseline := map[string]float64{
		`telesrv_mtproto_rpc_result_inner_bytes_total{method="messages.getDialogs"}`: 100,
		`telesrv_mtproto_rpc_result_wire_bytes_total{method="messages.getDialogs"}`:  50,
	}
	final := map[string]float64{
		`telesrv_mtproto_rpc_result_inner_bytes_total{method="messages.getDialogs"}`:                             900,
		`telesrv_mtproto_rpc_result_wire_bytes_total{method="messages.getDialogs"}`:                              250,
		`telesrv_mtproto_rpc_result_delivered_bytes_total{method="messages.getDialogs",outcome="ok"}`:            200,
		`telesrv_mtproto_rpc_result_delivered_bytes_total{method="messages.getDialogs",outcome="edge_overload"}`: 40,
	}
	bytes := startupResponseBytes(baseline, final)["messages.getDialogs"]
	if bytes.Inner != 800 || bytes.Wire != 200 || bytes.Delivered != 200 {
		t.Fatalf("response bytes = %+v", bytes)
	}
}

func TestStartupDatabaseWorkUsesPerMethodCounterDeltas(t *testing.T) {
	baseline := map[string]float64{
		`telesrv_rpc_db_queries_total{method="messages.getDialogs"}`:      10,
		`telesrv_rpc_db_time_seconds_sum{method="messages.getDialogs"}`:   0.5,
		`telesrv_rpc_db_time_seconds_count{method="messages.getDialogs"}`: 1,
		`telesrv_rpc_db_errors_total{method="messages.getDialogs"}`:       1,
	}
	final := map[string]float64{
		`telesrv_rpc_db_queries_total{method="messages.getDialogs"}`:      210,
		`telesrv_rpc_db_time_seconds_sum{method="messages.getDialogs"}`:   2.75,
		`telesrv_rpc_db_time_seconds_count{method="messages.getDialogs"}`: 11,
		`telesrv_rpc_db_errors_total{method="messages.getDialogs"}`:       1,
	}
	work := startupDatabaseWork(baseline, final)["messages.getDialogs"]
	if work.Queries != 200 || work.RPCs != 10 || work.Errors != 0 || work.DurationSeconds != 2.25 {
		t.Fatalf("database work = %+v", work)
	}
}

func TestStartupRPCDeliveryOutcomesUsesBoundedMethodAndOutcomeDeltas(t *testing.T) {
	baseline := map[string]float64{
		`telesrv_mtproto_rpc_result_delivered_total{method="users.getUsers",outcome="ok"}`: 5,
	}
	final := map[string]float64{
		`telesrv_mtproto_rpc_result_delivered_total{method="users.getUsers",outcome="ok"}`:            12,
		`telesrv_mtproto_rpc_result_delivered_total{method="users.getUsers",outcome="edge_overload"}`: 3,
	}
	outcomes := startupRPCDeliveryOutcomes(baseline, final)
	if outcomes["users.getUsers"]["ok"] != 7 || outcomes["users.getUsers"]["edge_overload"] != 3 {
		t.Fatalf("delivery outcomes = %#v", outcomes)
	}
}

func TestUpdateMetricPeaks(t *testing.T) {
	peaks := map[string]float64{"heap": 10}
	updateMetricPeaks(peaks, map[string]float64{"heap": 9, "connections": 5})
	updateMetricPeaks(peaks, map[string]float64{"heap": 12, "connections": 2})
	if peaks["heap"] != 12 || peaks["connections"] != 5 {
		t.Fatalf("peaks = %v", peaks)
	}
}
