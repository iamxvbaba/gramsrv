package android

const clientType = "android"

// OfferInitialMessageReportOptions preserves DrKLO's official channel-report
// flow. DrKLO starts that flow with an empty message-id vector and only opens
// its message selector after a chosen option receives MESSAGE_ID_REQUIRED.
//
// This exception is deliberately limited to the non-mutating first request:
// a selected option, a comment, or any non-Android caller must still pass the
// normal messages.report message-id validation.
func OfferInitialMessageReportOptions(
	client string,
	messageIDCount int,
	option []byte,
	comment string,
) bool {
	return client == clientType &&
		messageIDCount == 0 &&
		len(option) == 0 &&
		comment == ""
}
