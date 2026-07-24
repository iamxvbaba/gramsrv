package domain

import (
	"bytes"
	"testing"
	"time"
)

func TestNewModerationReportCanonicalizesEvidenceItemsAndHolds(t *testing.T) {
	now := time.Unix(1_750_000_000, 0).UTC()
	draft := ModerationReportDraft{
		ReporterUserID: 101,
		Source:         ModerationSourceMessages,
		Target:         Peer{Type: PeerTypeChannel, ID: 202},
		Reason:         ModerationReasonSpam,
		Option:         "v1/spam",
		Comment:        "review",
		CreatedAt:      now,
		Items: []ModerationReportItem{
			{
				Kind: ModerationItemStory, Peer: Peer{Type: PeerTypeChannel, ID: 202},
				ItemID: 20, AuthorUserID: 303, EvidenceSchemaVersion: 1,
				Evidence: []byte(`{ "z": 1, "a": {"two": 2, "one": 1} }`),
			},
			{
				Kind: ModerationItemMessage, Peer: Peer{Type: PeerTypeChannel, ID: 202},
				ItemID: 10, AuthorUserID: 303, EvidenceSchemaVersion: 1,
				Evidence: []byte(`{"message":"spam"}`),
			},
		},
		MediaHolds: []ModerationMediaHold{{
			ItemIndex: 0, Kind: ModerationMediaPhoto, StorageKey: "photo/20",
		}},
	}
	report, err := NewModerationReport(draft)
	if err != nil {
		t.Fatalf("NewModerationReport: %v", err)
	}
	if report.Items[0].Kind != ModerationItemMessage || report.Items[1].Kind != ModerationItemStory {
		t.Fatalf("items not canonicalized: %+v", report.Items)
	}
	if report.MediaHolds[0].ItemIndex != 1 {
		t.Fatalf("media hold item index = %d, want 1 after canonical sort", report.MediaHolds[0].ItemIndex)
	}
	if got, want := report.Items[1].Evidence, []byte(`{"a":{"one":1,"two":2},"z":1}`); !bytes.Equal(got, want) {
		t.Fatalf("canonical evidence = %s, want %s", got, want)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	retry := draft
	retry.CreatedAt = now.Add(time.Hour)
	retryReport, err := NewModerationReport(retry)
	if err != nil {
		t.Fatalf("retry NewModerationReport: %v", err)
	}
	if retryReport.Fingerprint != report.Fingerprint {
		t.Fatalf("retry fingerprint changed with CreatedAt")
	}
	retry.Items = append([]ModerationReportItem(nil), retry.Items...)
	retry.Items[0].Evidence = []byte(`{"z":2}`)
	changed, err := NewModerationReport(retry)
	if err != nil {
		t.Fatalf("changed NewModerationReport: %v", err)
	}
	if changed.Fingerprint == report.Fingerprint {
		t.Fatalf("evidence change did not change fingerprint")
	}
}

func TestModerationReportRejectsDuplicateItemIdentity(t *testing.T) {
	item := ModerationReportItem{
		Kind: ModerationItemMessage, Peer: Peer{Type: PeerTypeUser, ID: 2},
		ItemID: 7, AuthorUserID: 2, EvidenceSchemaVersion: 1,
		Evidence: []byte(`{"message":"bad"}`),
	}
	_, err := NewModerationReport(ModerationReportDraft{
		ReporterUserID: 1, Source: ModerationSourceMessages,
		Target: Peer{Type: PeerTypeUser, ID: 2},
		Reason: ModerationReasonSpam, Option: "v1/spam",
		Items: []ModerationReportItem{item, item}, CreatedAt: time.Now().UTC(),
	})
	if err != ErrModerationReportInvalid {
		t.Fatalf("error = %v, want ErrModerationReportInvalid", err)
	}
}
