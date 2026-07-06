package domain

import "errors"

// LiveStreamChannel 是直播时间轴上一个可拉流通道的快照（RTMP unified 模式恒为
// channel=1 / scale=0；last_timestamp_ms 是最新可取 segment 的起始时间戳）。
type LiveStreamChannel struct {
	Channel         int
	Scale           int
	LastTimestampMs int64
}

// 直播拉流业务错误；rpc 层映射见 upload.getFile(inputGroupCallStream)：
//   - PartNotReady → TIME_TOO_BIG（客户端 100ms 后原样重试）
//   - PartExpired / NoStream → 普通 400（客户端重新对时 resync）
var (
	ErrLiveStreamPartNotReady = errors.New("live stream part not ready")
	ErrLiveStreamPartExpired  = errors.New("live stream part expired")
	ErrLiveStreamNoStream     = errors.New("live stream not active")
)
