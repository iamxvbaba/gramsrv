package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Live review: browse a user's chats and read them as they are right now. The
// message bodies come from the existing /api/messages(+groups) routes; the only
// thing the console needs that did not exist is the list of a user's dialogs.

// LiveChatRow is one dialog in a user's chat list.
type LiveChatRow struct {
	PeerType       string `json:"PeerType"`
	PeerID         int64  `json:"PeerID"`
	TopMessageID   int    `json:"TopMessageID"`
	TopMessageDate int    `json:"TopMessageDate"`
	UnreadCount    int    `json:"UnreadCount"`
}

// ListDialogsForUser returns a user's dialogs, most recent first.
func (s *readStore) ListDialogsForUser(ctx context.Context, userID int64, limit int) ([]LiveChatRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT peer_type, peer_id, top_message_id, top_message_date, unread_count
		FROM dialogs
		WHERE user_id = $1
		ORDER BY top_message_date DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LiveChatRow{}
	for rows.Next() {
		var row LiveChatRow
		if err := rows.Scan(&row.PeerType, &row.PeerID, &row.TopMessageID, &row.TopMessageDate, &row.UnreadCount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *server) handleLiveChatsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	userID, _ := parseInt64(r.URL.Query().Get("user_id"))
	if userID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	limit, _ := parseInt(r.URL.Query().Get("limit"))
	chats, err := s.read.ListDialogsForUser(r.Context(), userID, limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "chats": chats})
}

// parseIDList parses "1,2,3" into ids, ignoring blanks.
func parseIDList(raw string) []int64 {
	out := []int64{}
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		if id, err := parseInt64(part); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// ResolvePeerNames returns display names for users and channels by id.
func (s *readStore) ResolvePeerNames(ctx context.Context, userIDs, channelIDs []int64) (map[int64]string, map[int64]string, error) {
	users := map[int64]string{}
	channels := map[int64]string{}
	if len(userIDs) > 0 {
		rows, err := s.pool.Query(ctx, `SELECT id, first_name, last_name, username FROM users WHERE id = ANY($1)`, userIDs)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id int64
			var first, last, username string
			if err := rows.Scan(&id, &first, &last, &username); err != nil {
				rows.Close()
				return nil, nil, err
			}
			name := strings.TrimSpace(first + " " + last)
			if name == "" {
				name = strings.TrimSpace(username)
			}
			if name != "" {
				users[id] = name
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	if len(channelIDs) > 0 {
		rows, err := s.pool.Query(ctx, `SELECT id, title FROM channels WHERE id = ANY($1)`, channelIDs)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id int64
			var title string
			if err := rows.Scan(&id, &title); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if strings.TrimSpace(title) != "" {
				channels[id] = title
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	return users, channels, nil
}

func (s *server) handleLivePeersAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	userIDs := parseIDList(r.URL.Query().Get("users"))
	channelIDs := parseIDList(r.URL.Query().Get("channels"))
	users, channels, err := s.read.ResolvePeerNames(r.Context(), userIDs, channelIDs)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	outUsers := map[string]string{}
	for id, name := range users {
		outUsers[strconv.FormatInt(id, 10)] = name
	}
	outChannels := map[string]string{}
	for id, name := range channels {
		outChannels[strconv.FormatInt(id, 10)] = name
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": outUsers, "channels": outChannels})
}

// LookupBlob maps a media location key (e.g. "photo:123:m", "doc:456") to the
// stored blob object key and its mime type.
func (s *readStore) LookupBlob(ctx context.Context, locationKey string) (string, string, error) {
	var objectKey, mime string
	err := s.pool.QueryRow(ctx,
		`SELECT object_key, mime_type FROM file_blobs WHERE location_key = $1`, locationKey).
		Scan(&objectKey, &mime)
	return objectKey, mime, err
}

// blobFilePath mirrors internal LocalFS.pathFor: a short key lives under "_",
// a full sha key is sharded by its first two byte-pairs.
func blobFilePath(root, objectKey string) string {
	if strings.HasPrefix(objectKey, "_") || len(objectKey) < 4 {
		return filepath.Join(root, "_", objectKey)
	}
	return filepath.Join(root, objectKey[:2], objectKey[2:4], objectKey)
}

// handleLiveMediaAPI serves media bytes by location key (photo:/doc:/blob:).
func (s *server) handleLiveMediaAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		writeAPIError(w, http.StatusBadRequest, "missing key")
		return
	}
	objectKey, mime, err := s.read.LookupBlob(r.Context(), key)
	if err != nil {
		// Photos/documents store sized variants (`photo:<id>:x`, `doc:<id>:m`);
		// fall back to them when the bare key is not present.
		for _, suffix := range []string{":x", ":m", ":c", ":a"} {
			if k, m, e := s.read.LookupBlob(r.Context(), key+suffix); e == nil {
				objectKey, mime, err = k, m, nil
				break
			}
		}
	}
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "media not found")
		return
	}
	data, err := os.ReadFile(blobFilePath(s.cfg.DiskStatsPath, objectKey))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "media file not found")
		return
	}
	if strings.TrimSpace(mime) == "" {
		mime = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
