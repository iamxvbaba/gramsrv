package rpc

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

// validFiniteCoord 校验坐标是有限值且在 [-bound, bound] 内（NaN/±Inf 一律拒绝）。
func validFiniteCoord(v, bound float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= -bound && v <= bound
}

// 本文件实现 upload.getWebFile。当前唯一真实路径是 geo/venue 消息的地图缩略图
// （inputWebFileGeoPointLocation）：TDesktop 与 DrKLO Android 渲染位置消息时都会调它。
// 地图图像由 files 服务提供：配置 Mapbox 代理时为真实静态地图（落盘缓存保证分片一致），
// 否则确定性占位合成（见 app/files/maptile.go 与 maptile_proxy.go）；
// access_hash 不做校验（发送时随机生成且未持久化，地图缩略图无机密性可言）。
// URL 形态（inputWebFileLocation）只代理 inline registry 中已登记的 web document；
// 任意客户端自造 URL/access_hash 仍显式拒绝，避免把服务端变成开放代理。

// maxWebFileChunkLimit 与 upload.getFile 的单次上限一致，防止客户端超大 limit 放大内存。
const (
	maxWebFileChunkLimit      = maxUploadGetFileChunkLimit
	inlineWebFileFetchTimeout = 10 * time.Second
)

type inlineWebFileFetcher func(ctx context.Context, document domain.BotInlineWebDocument) ([]byte, string, error)

var fetchInlineWebFile inlineWebFileFetcher = defaultFetchInlineWebFile

func (r *Router) registerUploadWebFile(d *tlprofile.Dispatcher) {
	registerRPC[*tg.UploadGetWebFileRequest](d, tlprofile.SemanticMethodUploadGetWebFile, func(ctx context.Context, layerRequest *tg.UploadGetWebFileRequest) (any, error) {
		return r.onUploadGetWebFile(ctx, layerRequest)
	})
}

func (r *Router) onUploadGetWebFile(ctx context.Context, req *tg.UploadGetWebFileRequest) (*tg.UploadWebFile, error) {
	if r.deps.Files == nil {
		return nil, notImplementedErr()
	}
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !ok || userID == 0 {
		return nil, locationInvalidErr()
	}
	if req.Offset < 0 || req.Limit <= 0 || req.Limit > maxWebFileChunkLimit {
		return nil, limitInvalidErr()
	}
	if loc, ok := req.Location.(*tg.InputWebFileLocation); ok {
		return r.onUploadGetRegisteredWebFile(ctx, loc, req.Offset, req.Limit)
	}
	loc, ok := req.Location.(*tg.InputWebFileGeoPointLocation)
	if !ok {
		return nil, locationInvalidErr()
	}
	point, ok := loc.GeoPoint.(*tg.InputGeoPoint)
	// NaN 与所有比较都为 false，会穿过区间校验，必须显式拒绝（NaN/Inf 会污染缓存 key 与上游 URL）。
	if !ok || !validFiniteCoord(point.Lat, 90) || !validFiniteCoord(point.Long, 180) {
		return nil, locationInvalidErr()
	}
	full, mime := r.deps.Files.GeoMapTile(point.Lat, point.Long, loc.W, loc.H, loc.Zoom, loc.Scale)
	if len(full) == 0 {
		return nil, locationInvalidErr()
	}
	fileType := webFileStorageType(mime)
	if req.Offset >= len(full) {
		return &tg.UploadWebFile{
			Size:     len(full),
			MimeType: mime,
			FileType: fileType,
			Mtime:    int(r.clock.Now().Unix()),
			Bytes:    []byte{},
		}, nil
	}
	end := req.Offset + req.Limit
	if end > len(full) {
		end = len(full)
	}
	return &tg.UploadWebFile{
		Size:     len(full),
		MimeType: mime,
		FileType: fileType,
		Mtime:    int(r.clock.Now().Unix()),
		Bytes:    full[req.Offset:end],
	}, nil
}

func (r *Router) onUploadGetRegisteredWebFile(ctx context.Context, loc *tg.InputWebFileLocation, offset, limit int) (*tg.UploadWebFile, error) {
	if loc.URL == "" || loc.AccessHash == 0 {
		return nil, locationInvalidErr()
	}
	_, data, mime, err := r.registeredInlineWebDocumentBytes(ctx, loc.URL, loc.AccessHash)
	if err != nil {
		return nil, locationInvalidErr()
	}
	fileType := storageFileType(mime, data)
	if offset >= len(data) {
		return &tg.UploadWebFile{
			Size:     len(data),
			MimeType: mime,
			FileType: fileType,
			Mtime:    int(r.clock.Now().Unix()),
			Bytes:    []byte{},
		}, nil
	}
	end := offset + limit
	if end > len(data) {
		end = len(data)
	}
	return &tg.UploadWebFile{
		Size:     len(data),
		MimeType: mime,
		FileType: fileType,
		Mtime:    int(r.clock.Now().Unix()),
		Bytes:    append([]byte(nil), data[offset:end]...),
	}, nil
}

func (r *Router) registeredInlineWebDocumentBytes(ctx context.Context, url string, accessHash int64) (domain.BotInlineWebDocument, []byte, string, error) {
	document, data, mime, ok := r.inlines.webDocumentForDownloadContext(ctx, r.clock.Now(), url, accessHash)
	if !ok {
		return domain.BotInlineWebDocument{}, nil, "", locationInvalidErr()
	}
	if len(data) == 0 {
		fetched, fetchedMime, err := fetchInlineWebFile(ctx, document)
		if err != nil {
			return domain.BotInlineWebDocument{}, nil, "", err
		}
		if len(fetched) == 0 || len(fetched) > domain.MaxBotInlineWebSize {
			return domain.BotInlineWebDocument{}, nil, "", locationInvalidErr()
		}
		data = fetched
		mime = normalizeInlineWebFileMime(fetchedMime, data, document.MimeType)
		r.inlines.cacheWebDocumentBytesContext(ctx, r.clock.Now(), document.URL, document.AccessHash, data, mime)
	}
	if mime == "" {
		mime = document.MimeType
	}
	return document, data, mime, nil
}

func webFileStorageType(mime string) tg.StorageFileTypeClass {
	switch mime {
	case "image/jpeg":
		return &tg.StorageFileJpeg{}
	case "image/webp":
		return &tg.StorageFileWebp{}
	default:
		return &tg.StorageFilePng{}
	}
}

func defaultFetchInlineWebFile(ctx context.Context, document domain.BotInlineWebDocument) ([]byte, string, error) {
	if err := validateInlineWebURL(document.URL, true); err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, inlineWebFileFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, document.URL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "telesrv-inline-webfile")
	client := &http.Client{
		Timeout: inlineWebFileFetchTimeout,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: safeExternalDialContext,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", errors.New("webfile fetch status " + strconv.Itoa(resp.StatusCode))
	}
	if resp.ContentLength > int64(domain.MaxBotInlineWebSize) {
		return nil, "", webDocumentSizeTooBigErr()
	}
	limited := io.LimitReader(resp.Body, int64(domain.MaxBotInlineWebSize)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", err
	}
	if len(data) > domain.MaxBotInlineWebSize {
		return nil, "", webDocumentSizeTooBigErr()
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func safeExternalDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if port == "" {
		port = "443"
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	for _, candidate := range ips {
		if !safeExternalIP(candidate.IP) {
			lastErr = errors.New("unsafe webfile host")
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("webfile host has no safe address")
}

func safeExternalIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		first, second := v4[0], v4[1]
		if first == 0 || first >= 224 || first == 127 || first == 169 && second == 254 {
			return false
		}
		if first == 100 && second >= 64 && second <= 127 {
			return false
		}
		if first == 198 && (second == 18 || second == 19) {
			return false
		}
	}
	return true
}

func normalizeInlineWebFileMime(header string, data []byte, fallback string) string {
	mime := strings.TrimSpace(strings.Split(header, ";")[0])
	if !validInlineWebMime(mime) {
		mime = http.DetectContentType(data)
	}
	if !validInlineWebMime(mime) {
		mime = fallback
	}
	return mime
}
