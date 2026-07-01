package rpc

import "telesrv/internal/domain"

const (
	clientDocumentIDAliasOffset      int64 = 4000000000000000000
	clientDocumentIDAliasMaxServerID int64 = 9223372036854775807 - clientDocumentIDAliasOffset
)

func clientDocumentIDForDocument(d domain.Document) int64 {
	if shouldAliasClientDocument(d) {
		return clientDocumentIDFromServerID(d.ID)
	}
	return d.ID
}

func shouldAliasClientDocument(d domain.Document) bool {
	return d.ID > 0 &&
		d.MimeType == mimeApplicationXTGSticker &&
		d.IsStickerLike()
}

func clientDocumentIDFromServerID(id int64) int64 {
	if id > 0 && id <= clientDocumentIDAliasMaxServerID {
		return id + clientDocumentIDAliasOffset
	}
	return id
}

func clientDocumentIDsFromServerIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		out = append(out, clientDocumentIDFromServerID(id))
	}
	return out
}

func serverDocumentIDFromClientID(id int64) int64 {
	if id > clientDocumentIDAliasOffset {
		return id - clientDocumentIDAliasOffset
	}
	return id
}

func inputDocumentIDsFromClientID(id int64) []int64 {
	serverID := serverDocumentIDFromClientID(id)
	if serverID != id {
		return []int64{serverID, id}
	}
	return []int64{id}
}

func clientDocumentIDAliases(docs []domain.Document) map[int64]int64 {
	var aliases map[int64]int64
	for _, d := range docs {
		clientID := clientDocumentIDForDocument(d)
		if clientID == d.ID {
			continue
		}
		if aliases == nil {
			aliases = make(map[int64]int64)
		}
		aliases[d.ID] = clientID
	}
	return aliases
}

func clientDocumentIDWithAliases(id int64, aliases map[int64]int64) int64 {
	if aliases != nil {
		if clientID, ok := aliases[id]; ok {
			return clientID
		}
	}
	return id
}
