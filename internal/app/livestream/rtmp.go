package livestream

import (
	"context"
	"io"
	"net"

	rtmp "github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
	"go.uber.org/zap"
)

// rtmpHandler 是单条 RTMP 连接的回调：publish 时用 stream key 鉴权并绑定
// channel，media tag 直通 FLV → ffmpeg。一条连接只允许一路 publish。
type rtmpHandler struct {
	rtmp.DefaultHandler
	svc    *Service
	conn   net.Conn
	stream *stream
}

func (h *rtmpHandler) OnPublish(_ *rtmp.StreamContext, _ uint32, cmd *rtmpmsg.NetStreamPublish) error {
	if h.stream != nil {
		return errPublishRejected
	}
	st, err := h.svc.startPublish(context.Background(), cmd.PublishingName, h.conn)
	if err != nil {
		h.svc.log.Warn("rtmp publish rejected", zap.Error(err))
		return errPublishRejected
	}
	h.stream = st
	return nil
}

func (h *rtmpHandler) OnSetDataFrame(timestamp uint32, data *rtmpmsg.NetStreamSetDataFrame) error {
	if h.stream == nil {
		return nil
	}
	// onMetaData 原样透传给 ffmpeg（可选信息，写失败不断流）。
	_ = h.stream.writeTag(18, timestamp, data.Payload)
	return nil
}

func (h *rtmpHandler) OnAudio(timestamp uint32, payload io.Reader) error {
	return h.writeMedia(8, timestamp, payload)
}

func (h *rtmpHandler) OnVideo(timestamp uint32, payload io.Reader) error {
	return h.writeMedia(9, timestamp, payload)
}

func (h *rtmpHandler) writeMedia(tagType byte, timestamp uint32, payload io.Reader) error {
	if h.stream == nil {
		return nil
	}
	body, err := io.ReadAll(payload)
	if err != nil {
		return err
	}
	return h.stream.writeTag(tagType, timestamp, body)
}

func (h *rtmpHandler) OnClose() {
	if h.stream != nil {
		h.svc.endPublish(h.stream)
		h.stream = nil
	}
}
