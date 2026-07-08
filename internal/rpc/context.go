package rpc

import (
	"context"
	"regexp"
	"strings"
)

type ctxKey int

const (
	layerKey ctxKey = iota
	clientInfoKey
	rawAuthKeyIDKey
	authKeyIDKey
	sessionIDKey
	userIDKey
	invokeWithoutUpdatesKey
)

const currentClientLayer = 227

var androidSDKVersionRE = regexp.MustCompile(`\bsdk\s+\d+\b`)

type ClientType string

const (
	ClientTypeUnknown  ClientType = "unknown"
	ClientTypeTDesktop ClientType = "tdesktop"
	ClientTypeAndroid  ClientType = "android"
)

// ClientInfo 是 initConnection 携带的客户端信息。
type ClientInfo struct {
	APIID          int
	DeviceModel    string
	SystemVersion  string
	AppVersion     string
	SystemLangCode string
	LangPack       string
	LangCode       string
	Type           ClientType
}

// WithLayer 在 ctx 注入客户端 layer（来自 invokeWithLayer）。
func WithLayer(ctx context.Context, layer int) context.Context {
	return context.WithValue(ctx, layerKey, layer)
}

// LayerFrom 返回 ctx 中的客户端 layer，未设置时为 0。
func LayerFrom(ctx context.Context) int {
	if v, ok := ctx.Value(layerKey).(int); ok {
		return v
	}
	return 0
}

// WithClientInfo 在 ctx 注入客户端信息（来自 initConnection）。
func WithClientInfo(ctx context.Context, info ClientInfo) context.Context {
	info = normalizeClientInfo(info)
	return context.WithValue(ctx, clientInfoKey, info)
}

// ClientInfoFrom 返回 ctx 中的客户端信息。
func ClientInfoFrom(ctx context.Context) (ClientInfo, bool) {
	v, ok := ctx.Value(clientInfoKey).(ClientInfo)
	return v, ok
}

func ClientTypeFrom(ctx context.Context) ClientType {
	if info, ok := ClientInfoFrom(ctx); ok {
		return info.ClientType()
	}
	return ClientTypeUnknown
}

func normalizeClientInfo(info ClientInfo) ClientInfo {
	if !knownClientType(info.Type) {
		info.Type = detectClientType(info)
	}
	return info
}

func (info ClientInfo) ClientType() ClientType {
	if knownClientType(info.Type) {
		return info.Type
	}
	return detectClientType(info)
}

func knownClientType(t ClientType) bool {
	switch t {
	case ClientTypeTDesktop, ClientTypeAndroid:
		return true
	default:
		return false
	}
}

func clientTypeFromAPIID(apiID int) ClientType {
	switch apiID {
	// DrKLO local BuildVars.APP_ID uses 4; TDesktop's active session
	// classifier also recognizes the official Android ids below.
	case 4, 5, 6, 24, 1026, 1083, 2458, 2521, 21724:
		return ClientTypeAndroid
	case 2040, 17349, 611335:
		return ClientTypeTDesktop
	default:
		return ClientTypeUnknown
	}
}

func detectClientType(info ClientInfo) ClientType {
	if t := clientTypeFromAPIID(info.APIID); t != ClientTypeUnknown {
		return t
	}
	if strings.EqualFold(info.LangPack, string(ClientTypeAndroid)) {
		return ClientTypeAndroid
	}
	if strings.EqualFold(info.LangPack, string(ClientTypeTDesktop)) {
		return ClientTypeTDesktop
	}
	client := strings.ToLower(info.DeviceModel + " " + info.SystemVersion + " " + info.AppVersion)
	switch {
	case strings.Contains(client, "android"), androidSDKVersionRE.MatchString(client):
		return ClientTypeAndroid
	case strings.Contains(client, "tdesktop"), strings.Contains(client, "desktop"):
		return ClientTypeTDesktop
	default:
		return ClientTypeUnknown
	}
}

// WithRawAuthKeyID 在 ctx 注入连接实际使用的 auth_key_id。
func WithRawAuthKeyID(ctx context.Context, id [8]byte) context.Context {
	return context.WithValue(ctx, rawAuthKeyIDKey, id)
}

// RawAuthKeyIDFrom 返回连接实际使用的 auth_key_id。
func RawAuthKeyIDFrom(ctx context.Context) ([8]byte, bool) {
	v, ok := ctx.Value(rawAuthKeyIDKey).([8]byte)
	return v, ok
}

// WithAuthKeyID 在 ctx 注入业务视角的 auth_key_id；temp auth_key 绑定后会解析为 perm auth_key。
func WithAuthKeyID(ctx context.Context, id [8]byte) context.Context {
	return context.WithValue(ctx, authKeyIDKey, id)
}

// AuthKeyIDFrom 返回 ctx 中业务视角的 auth_key_id。已握手连接均有（即便尚未登录）。
func AuthKeyIDFrom(ctx context.Context) ([8]byte, bool) {
	v, ok := ctx.Value(authKeyIDKey).([8]byte)
	return v, ok
}

// WithSessionID 在 ctx 注入调用方的 MTProto session_id。
func WithSessionID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, sessionIDKey, id)
}

// SessionIDFrom 返回 ctx 中调用方的 MTProto session_id。
func SessionIDFrom(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(sessionIDKey).(int64)
	return v, ok
}

// WithUserID 在 ctx 注入当前已登录用户 id。
func WithUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserIDFrom 返回 ctx 中当前已登录用户 id。
func UserIDFrom(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(userIDKey).(int64)
	if !ok || v == 0 {
		return 0, false
	}
	return v, true
}

// withInvokeWithoutUpdates 标记当前请求被 invokeWithoutUpdates 包装：
// 客户端声明该 session 不接收主动 updates（media/temp 连接的请求一律带此包装）。
func withInvokeWithoutUpdates(ctx context.Context) context.Context {
	return context.WithValue(ctx, invokeWithoutUpdatesKey, true)
}

func invokeWithoutUpdatesFrom(ctx context.Context) bool {
	v, _ := ctx.Value(invokeWithoutUpdatesKey).(bool)
	return v
}
