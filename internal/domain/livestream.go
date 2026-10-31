package domain

import "errors"

// LiveStreamChannel is a snapshot of one pullable channel on the live timeline (in RTMP unified mode it is always
// channel=1 / scale=0; last_timestamp_ms is the start timestamp of the latest fetchable segment).
type LiveStreamChannel struct {
	Channel         int
	Scale           int
	LastTimestampMs int64
}

// Live-stream pull business errors; for the rpc-layer mapping see upload.getFile(inputGroupCallStream):
//   - PartNotReady → TIME_TOO_BIG (the client retries unchanged after 100ms)
//   - PartExpired / NoStream → a plain 400 (the client resyncs)
var (
	ErrLiveStreamPartNotReady = errors.New("live stream part not ready")
	ErrLiveStreamPartExpired  = errors.New("live stream part expired")
	ErrLiveStreamNoStream     = errors.New("live stream not active")
)
