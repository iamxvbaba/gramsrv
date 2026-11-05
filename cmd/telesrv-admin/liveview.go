package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
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

// ListDialogsForUser returns one page of a user's dialogs, most recent first.
// (beforeDate, beforePeerID) is a keyset cursor ordered by
// (top_message_date, peer_id) so older dialogs are reachable; peer_id breaks
// ties between dialogs sharing a top-message date. A dialog can have
// top_message_date 0 (a row created by a scheduled message, a TTL or a chat
// theme before any message), so a zero beforeDate is a real cursor value: only
// beforePeerID 0 means "first page".
func (s *readStore) ListDialogsForUser(ctx context.Context, userID, beforeDate, beforePeerID int64, limit int) ([]LiveChatRow, bool, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	// Read one extra row to know whether a next page exists.
	rows, err := s.pool.Query(ctx, `
		SELECT peer_type, peer_id, top_message_id, top_message_date, unread_count
		FROM dialogs
		WHERE user_id = $1
			AND ($3::bigint = 0 OR (top_message_date, peer_id) < ($2::bigint, $3::bigint))
		ORDER BY top_message_date DESC, peer_id DESC
		LIMIT $4`, userID, beforeDate, beforePeerID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []LiveChatRow{}
	for rows.Next() {
		var row LiveChatRow
		if err := rows.Scan(&row.PeerType, &row.PeerID, &row.TopMessageID, &row.TopMessageDate, &row.UnreadCount); err != nil {
			return nil, false, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

func (s *server) handleLiveChatsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	q := r.URL.Query()
	userID, _ := parseInt64(q.Get("user_id"))
	if userID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	limit, _ := parseInt(q.Get("limit"))
	beforeDate, _ := parseInt64(q.Get("before_date"))
	beforePeerID, _ := parseInt64(q.Get("before_peer_id"))
	chats, hasMore, err := s.read.ListDialogsForUser(r.Context(), userID, beforeDate, beforePeerID, limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "chats": chats, "has_more": hasMore})
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

// handleLiveMediaAPI proxies a media blob from the internal admin API. The
// panel never reads storage itself and never trusts the uploader's MIME: the
// admin service reads through the blob backend, bounds the read and
// sniffs/allowlists the type; this handler forwards that safely.
func (s *server) handleLiveMediaAPI(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		writeAPIError(w, http.StatusBadRequest, "missing key")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.AdminAPIURL+"/v1/media?key="+url.QueryEscape(key), nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminAPIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = io.WriteString(w, "This file is larger than 16 MiB and can't be opened from the review console.")
		return
	}
	if resp.StatusCode != http.StatusOK {
		http.NotFound(w, r)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil || len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Content-Type", contentType)
	if disposition := resp.Header.Get("Content-Disposition"); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	// Private message media must not stay fresh in a browser cache after the
	// session ends or loses messages.read.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
