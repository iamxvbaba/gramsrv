package android

import "testing"

func TestOfferInitialMessageReportOptions(t *testing.T) {
	tests := []struct {
		name           string
		client         string
		messageIDCount int
		option         []byte
		comment        string
		want           bool
	}{
		{name: "android initial discovery", client: "android", want: true},
		{name: "desktop keeps protocol error", client: "tdesktop"},
		{name: "selected option requires messages", client: "android", option: []byte("spam")},
		{name: "comment requires messages", client: "android", comment: "details"},
		{name: "message ids use normal flow", client: "android", messageIDCount: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := OfferInitialMessageReportOptions(
				test.client,
				test.messageIDCount,
				test.option,
				test.comment,
			); got != test.want {
				t.Fatalf("OfferInitialMessageReportOptions() = %v, want %v", got, test.want)
			}
		})
	}
}
