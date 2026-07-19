package postgres

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// ContactStore 用 PostgreSQL 实现 store.ContactStore。
type ContactStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewContactStore 基于 pgx 连接池（或事务）创建 ContactStore。
func NewContactStore(db sqlcgen.DBTX) *ContactStore {
	return &ContactStore{db: db, q: sqlcgen.New(db)}
}

func (s *ContactStore) ListByUser(ctx context.Context, userID int64) (domain.ContactList, error) {
	rows, err := s.q.ListContactsByUser(ctx, userID)
	if err != nil {
		return domain.ContactList{}, fmt.Errorf("list contacts: %w", err)
	}
	out := domain.ContactList{Contacts: make([]domain.Contact, 0, len(rows))}
	for _, row := range rows {
		contact, err := contactFromListRow(row)
		if err != nil {
			return domain.ContactList{}, err
		}
		out.Contacts = append(out.Contacts, contact)
	}
	out.Hash = contactListHash(out.Contacts)
	return out, nil
}

func (s *ContactStore) Get(ctx context.Context, userID, contactUserID int64) (domain.Contact, bool, error) {
	row, err := s.q.GetContact(ctx, sqlcgen.GetContactParams{
		UserID:        userID,
		ContactUserID: contactUserID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Contact{}, false, nil
		}
		return domain.Contact{}, false, fmt.Errorf("get contact: %w", err)
	}
	contact, err := contactFromGetRow(row)
	if err != nil {
		return domain.Contact{}, false, err
	}
	return contact, true, nil
}

func (s *ContactStore) GetMany(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.Contact, error) {
	out := make(map[int64]domain.Contact, len(contactUserIDs))
	if userID == 0 || len(contactUserIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT
  c.contact_user_id,
  c.mutual,
  c.close_friend,
  c.contact_phone,
  c.contact_first_name,
  c.contact_last_name,
  c.note,
  COALESCE(c.note_entities::text, '[]')::text AS note_entities_json,
  u.id,
  u.access_hash,
  COALESCE(NULLIF(c.contact_phone, ''), u.phone)::text AS phone,
  COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)::text AS first_name,
  COALESCE(c.contact_last_name, u.last_name)::text AS last_name,
  u.username,
  u.country_code,
  u.verified,
  u.support,
  COALESCE(EXTRACT(EPOCH FROM u.premium_expires_at), 0)::bigint AS premium_until,
  u.emoji_status_document_id,
  u.emoji_status_until,
  u.emoji_status_collectible_id,
  u.emoji_status_collectible,
  u.last_seen_at
FROM contacts c
JOIN users u ON u.id = c.contact_user_id
WHERE c.user_id = $1
  AND c.contact_user_id = ANY($2::bigint[])
`, userID, contactUserIDs)
	if err != nil {
		return nil, fmt.Errorf("get contacts many: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		contact, err := scanContactRows(rows)
		if err != nil {
			return nil, err
		}
		out[contact.User.ID] = contact
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ContactStore) GetReverseContacts(ctx context.Context, userID int64, ownerUserIDs []int64) (map[int64]domain.Contact, error) {
	out := make(map[int64]domain.Contact, len(ownerUserIDs))
	if userID == 0 || len(ownerUserIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT
  c.user_id AS owner_user_id,
  c.mutual,
  c.close_friend,
  c.contact_phone,
  c.contact_first_name,
  c.contact_last_name,
  c.note,
  COALESCE(c.note_entities::text, '[]')::text AS note_entities_json,
  u.id,
  u.access_hash,
  COALESCE(NULLIF(c.contact_phone, ''), u.phone)::text AS phone,
  COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)::text AS first_name,
  COALESCE(c.contact_last_name, u.last_name)::text AS last_name,
  u.username,
  u.country_code,
  u.verified,
  u.support,
  COALESCE(EXTRACT(EPOCH FROM u.premium_expires_at), 0)::bigint AS premium_until,
  u.emoji_status_document_id,
  u.emoji_status_until,
  u.emoji_status_collectible_id,
  u.emoji_status_collectible,
  u.last_seen_at
FROM contacts c
JOIN users u ON u.id = c.contact_user_id
WHERE c.contact_user_id = $1
  AND c.user_id = ANY($2::bigint[])
`, userID, ownerUserIDs)
	if err != nil {
		return nil, fmt.Errorf("get reverse contacts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		ownerID, contact, err := scanReverseContactRows(rows)
		if err != nil {
			return nil, err
		}
		out[ownerID] = contact
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ContactStore) Upsert(ctx context.Context, userID int64, input domain.ContactInput) (domain.Contact, error) {
	entities, err := encodeMessageEntities(input.NoteEntities)
	if err != nil {
		return domain.Contact{}, err
	}
	row, err := s.q.UpsertContact(ctx, sqlcgen.UpsertContactParams{
		UserID:           userID,
		ContactUserID:    input.ContactUserID,
		ContactPhone:     input.Phone,
		ContactFirstName: input.FirstName,
		ContactLastName:  input.LastName,
		Note:             input.Note,
		NoteEntities:     entities,
	})
	if err != nil {
		return domain.Contact{}, fmt.Errorf("upsert contact: %w", err)
	}
	contact, err := contactFromUpsertRow(row)
	if err != nil {
		return domain.Contact{}, err
	}
	return contact, nil
}

const upsertContactsManySQL = `
WITH input AS (
  SELECT
    $1::bigint AS user_id,
    i.contact_user_id,
    i.contact_phone,
    i.contact_first_name,
    i.contact_last_name,
    i.note,
    i.note_entities_json::jsonb AS note_entities,
    i.ord
  FROM unnest(
    $2::bigint[],
    $3::text[],
    $4::text[],
    $5::text[],
    $6::text[],
    $7::text[]
  ) WITH ORDINALITY AS i(contact_user_id, contact_phone, contact_first_name, contact_last_name, note, note_entities_json, ord)
),
reverse AS (
  SELECT
    i.contact_user_id,
    EXISTS (
      SELECT 1
      FROM contacts c
      WHERE c.user_id = i.contact_user_id
        AND c.contact_user_id = i.user_id
    )::boolean AS mutual
  FROM input i
),
upserted AS (
  INSERT INTO contacts (
    user_id,
    contact_user_id,
    contact_phone,
    contact_first_name,
    contact_last_name,
    note,
    note_entities,
    mutual
  )
  SELECT
    i.user_id,
    i.contact_user_id,
    i.contact_phone,
    i.contact_first_name,
    i.contact_last_name,
    i.note,
    i.note_entities,
    r.mutual
  FROM input i
  JOIN reverse r ON r.contact_user_id = i.contact_user_id
  ON CONFLICT (user_id, contact_user_id) DO UPDATE SET
    contact_phone = EXCLUDED.contact_phone,
    contact_first_name = EXCLUDED.contact_first_name,
    contact_last_name = EXCLUDED.contact_last_name,
    note = EXCLUDED.note,
    note_entities = EXCLUDED.note_entities,
    mutual = contacts.mutual OR EXCLUDED.mutual,
    updated_at = now()
  RETURNING *
),
reverse_updated AS (
  UPDATE contacts c
  SET mutual = true,
      updated_at = now()
  FROM upserted u
  WHERE c.user_id = u.contact_user_id
    AND c.contact_user_id = $1::bigint
    AND NOT c.mutual
  RETURNING c.user_id
)
SELECT
  c.contact_user_id,
  c.mutual,
  c.close_friend,
  c.contact_phone,
  c.contact_first_name,
  c.contact_last_name,
  c.note,
  COALESCE(c.note_entities::text, '[]')::text AS note_entities_json,
  u.id,
  u.access_hash,
  COALESCE(NULLIF(c.contact_phone, ''), u.phone)::text AS phone,
  COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)::text AS first_name,
  COALESCE(c.contact_last_name, u.last_name)::text AS last_name,
  u.username,
  u.country_code,
  u.verified,
  u.support,
  COALESCE(EXTRACT(EPOCH FROM u.premium_expires_at), 0)::bigint AS premium_until,
  u.emoji_status_document_id,
  u.emoji_status_until,
  u.emoji_status_collectible_id,
  u.emoji_status_collectible,
  u.last_seen_at,
  EXISTS (SELECT 1 FROM reverse_updated ru WHERE ru.user_id = c.contact_user_id)::boolean AS reverse_mutual_changed
FROM upserted c
JOIN users u ON u.id = c.contact_user_id
JOIN input i ON i.contact_user_id = c.contact_user_id
ORDER BY i.ord
`

func (s *ContactStore) UpsertMany(ctx context.Context, userID int64, inputs []domain.ContactInput) ([]domain.Contact, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	contactUserIDs := make([]int64, 0, len(inputs))
	phones := make([]string, 0, len(inputs))
	firstNames := make([]string, 0, len(inputs))
	lastNames := make([]string, 0, len(inputs))
	notes := make([]string, 0, len(inputs))
	noteEntities := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if input.ContactUserID == 0 {
			continue
		}
		raw, err := encodeMessageEntities(input.NoteEntities)
		if err != nil {
			return nil, err
		}
		contactUserIDs = append(contactUserIDs, input.ContactUserID)
		phones = append(phones, input.Phone)
		firstNames = append(firstNames, input.FirstName)
		lastNames = append(lastNames, input.LastName)
		notes = append(notes, input.Note)
		noteEntities = append(noteEntities, string(raw))
	}
	if len(contactUserIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, upsertContactsManySQL, userID, contactUserIDs, phones, firstNames, lastNames, notes, noteEntities)
	if err != nil {
		return nil, fmt.Errorf("upsert contacts many: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Contact, 0, len(contactUserIDs))
	for rows.Next() {
		var (
			contactUserID        int64
			mutual               bool
			closeFriend          bool
			contactPhone         string
			contactFirstName     string
			contactLastName      string
			note                 string
			noteEntitiesJSON     string
			id                   int64
			accessHash           int64
			phone                string
			firstName            string
			lastName             string
			username             string
			countryCode          string
			verified             bool
			support              bool
			premiumUntil         int64
			emojiStatusDocID     int64
			emojiStatusUntil     int64
			emojiCollectibleID   *int64
			emojiCollectibleJSON []byte
			lastSeenAt           int64
			reverseMutualChanged bool
		)
		if err := rows.Scan(
			&contactUserID,
			&mutual,
			&closeFriend,
			&contactPhone,
			&contactFirstName,
			&contactLastName,
			&note,
			&noteEntitiesJSON,
			&id,
			&accessHash,
			&phone,
			&firstName,
			&lastName,
			&username,
			&countryCode,
			&verified,
			&support,
			&premiumUntil,
			&emojiStatusDocID,
			&emojiStatusUntil,
			&emojiCollectibleID,
			&emojiCollectibleJSON,
			&lastSeenAt,
			&reverseMutualChanged,
		); err != nil {
			return nil, fmt.Errorf("scan upsert contacts many: %w", err)
		}
		_ = reverseMutualChanged
		entities, err := decodeMessageEntities(noteEntitiesJSON)
		if err != nil {
			return nil, fmt.Errorf("decode contact note entities: %w", err)
		}
		out = append(out, contactFromFields(id, accessHash, phone, firstName, lastName, username, countryCode, verified, support, false, 0, int(premiumUntil), emojiStatusDocID, int(emojiStatusUntil), emojiCollectibleID, emojiCollectibleJSON, int(lastSeenAt), contactFirstName, contactLastName, contactPhone, note, entities, mutual, closeFriend))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate upsert contacts many: %w", err)
	}
	return out, nil
}

func (s *ContactStore) UpdateNote(ctx context.Context, userID, contactUserID int64, note string, entities []domain.MessageEntity) (domain.Contact, bool, error) {
	raw, err := encodeMessageEntities(entities)
	if err != nil {
		return domain.Contact{}, false, err
	}
	row, err := s.q.UpdateContactNote(ctx, sqlcgen.UpdateContactNoteParams{
		UserID:        userID,
		ContactUserID: contactUserID,
		Note:          note,
		NoteEntities:  raw,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Contact{}, false, nil
		}
		return domain.Contact{}, false, fmt.Errorf("update contact note: %w", err)
	}
	contact, err := contactFromUpdateNoteRow(row)
	if err != nil {
		return domain.Contact{}, false, err
	}
	return contact, true, nil
}

func (s *ContactStore) SetCloseFriends(ctx context.Context, userID int64, contactUserIDs []int64) (domain.CloseFriendsEditResult, error) {
	if userID == 0 {
		return domain.CloseFriendsEditResult{}, nil
	}
	if contactUserIDs == nil {
		contactUserIDs = []int64{}
	}
	beforeList, err := s.ListByUser(ctx, userID)
	if err != nil {
		return domain.CloseFriendsEditResult{}, err
	}
	contactSet := make(map[int64]struct{}, len(beforeList.Contacts))
	before := make(map[int64]struct{})
	for _, contact := range beforeList.Contacts {
		id := contact.User.ID
		if id == 0 {
			continue
		}
		contactSet[id] = struct{}{}
		if contact.CloseFriend || contact.User.CloseFriend {
			before[id] = struct{}{}
		}
	}
	after := make(map[int64]struct{}, len(contactUserIDs))
	for _, id := range contactUserIDs {
		if id <= 0 || id == userID {
			continue
		}
		if _, ok := contactSet[id]; ok {
			after[id] = struct{}{}
		}
	}
	_, err = s.db.Exec(ctx, `
UPDATE contacts
SET close_friend = contact_user_id = ANY($2::bigint[]),
    updated_at = now()
WHERE user_id = $1
  AND (
    close_friend
    OR contact_user_id = ANY($2::bigint[])
  )`, userID, contactUserIDs)
	if err != nil {
		return domain.CloseFriendsEditResult{}, fmt.Errorf("set close friends: %w", err)
	}
	return closeFriendsEditDiff(before, after), nil
}

func closeFriendsEditDiff(before, after map[int64]struct{}) domain.CloseFriendsEditResult {
	var result domain.CloseFriendsEditResult
	for id := range after {
		if _, ok := before[id]; !ok {
			result.AddedUserIDs = append(result.AddedUserIDs, id)
		}
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			result.RemovedUserIDs = append(result.RemovedUserIDs, id)
		}
	}
	sort.Slice(result.AddedUserIDs, func(i, j int) bool { return result.AddedUserIDs[i] < result.AddedUserIDs[j] })
	sort.Slice(result.RemovedUserIDs, func(i, j int) bool { return result.RemovedUserIDs[i] < result.RemovedUserIDs[j] })
	return result
}

func (s *ContactStore) SetPersonalPhoto(ctx context.Context, userID, contactUserID int64, photoID int64, date int) (domain.Contact, bool, error) {
	tag, err := s.db.Exec(ctx, `
UPDATE contacts
SET personal_photo_id = $3,
    personal_photo_date = CASE WHEN $3::bigint = 0 THEN 0 ELSE $4::int END,
    updated_at = now()
WHERE user_id = $1
  AND contact_user_id = $2
`, userID, contactUserID, photoID, date)
	if err != nil {
		return domain.Contact{}, false, fmt.Errorf("set contact personal photo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Contact{}, false, nil
	}
	contact, found, err := s.Get(ctx, userID, contactUserID)
	return contact, found, err
}

func (s *ContactStore) PersonalPhotos(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	out := make(map[int64]domain.ProfilePhotoRef, len(contactUserIDs))
	if userID == 0 || len(contactUserIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT
  c.contact_user_id,
  c.personal_photo_id,
  ph.dc_id,
  ph.sizes::text AS sizes_json
FROM contacts c
JOIN photos ph ON ph.id = c.personal_photo_id
WHERE c.user_id = $1
  AND c.contact_user_id = ANY($2::bigint[])
  AND c.personal_photo_id <> 0
`, userID, contactUserIDs)
	if err != nil {
		return nil, fmt.Errorf("list contact personal photos: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var contactUserID, photoID int64
		var dcID int32
		var sizesJSON string
		if err := rows.Scan(&contactUserID, &photoID, &dcID, &sizesJSON); err != nil {
			return nil, err
		}
		sizes, err := decodePhotoSizes(sizesJSON)
		if err != nil {
			return nil, err
		}
		out[contactUserID] = domain.ProfilePhotoRef{
			PhotoID:  photoID,
			DCID:     int(dcID),
			Stripped: domain.StrippedFromSizes(sizes),
			Personal: true,
			HasVideo: domain.PhotoHasVideo(sizes),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ContactStore) Delete(ctx context.Context, userID int64, contactUserIDs []int64) (int, error) {
	if len(contactUserIDs) == 0 {
		return 0, nil
	}
	count, err := s.q.DeleteContacts(ctx, sqlcgen.DeleteContactsParams{
		UserID:         userID,
		ContactUserIds: contactUserIDs,
	})
	if err != nil {
		return 0, fmt.Errorf("delete contacts: %w", err)
	}
	return int(count), nil
}

func contactFromListRow(row sqlcgen.ListContactsByUserRow) (domain.Contact, error) {
	entities, err := decodeMessageEntities(row.NoteEntitiesJson)
	if err != nil {
		return domain.Contact{}, fmt.Errorf("decode contact note entities: %w", err)
	}
	return contactFromFields(row.ID, row.AccessHash, row.Phone, row.FirstName, row.LastName, row.Username, row.CountryCode, row.Verified, row.Support, row.IsBot, int(row.BotInfoVersion), premiumUntilFromModel(row.PremiumExpiresAt), row.EmojiStatusDocumentID, int(row.EmojiStatusUntil), row.EmojiStatusCollectibleID, row.EmojiStatusCollectible, int(row.LastSeenAt), row.ContactFirstName, row.ContactLastName, row.ContactPhone, row.Note, entities, row.Mutual, row.CloseFriend), nil
}

func contactFromGetRow(row sqlcgen.GetContactRow) (domain.Contact, error) {
	entities, err := decodeMessageEntities(row.NoteEntitiesJson)
	if err != nil {
		return domain.Contact{}, fmt.Errorf("decode contact note entities: %w", err)
	}
	return contactFromFields(row.ID, row.AccessHash, row.Phone, row.FirstName, row.LastName, row.Username, row.CountryCode, row.Verified, row.Support, row.IsBot, int(row.BotInfoVersion), premiumUntilFromModel(row.PremiumExpiresAt), row.EmojiStatusDocumentID, int(row.EmojiStatusUntil), row.EmojiStatusCollectibleID, row.EmojiStatusCollectible, int(row.LastSeenAt), row.ContactFirstName, row.ContactLastName, row.ContactPhone, row.Note, entities, row.Mutual, row.CloseFriend), nil
}

func contactFromUpsertRow(row sqlcgen.UpsertContactRow) (domain.Contact, error) {
	entities, err := decodeMessageEntities(row.NoteEntitiesJson)
	if err != nil {
		return domain.Contact{}, fmt.Errorf("decode contact note entities: %w", err)
	}
	return contactFromFields(row.ID, row.AccessHash, row.Phone, row.FirstName, row.LastName, row.Username, row.CountryCode, row.Verified, row.Support, row.IsBot, int(row.BotInfoVersion), premiumUntilFromModel(row.PremiumExpiresAt), row.EmojiStatusDocumentID, int(row.EmojiStatusUntil), row.EmojiStatusCollectibleID, row.EmojiStatusCollectible, int(row.LastSeenAt), row.ContactFirstName, row.ContactLastName, row.ContactPhone, row.Note, entities, row.Mutual, row.CloseFriend), nil
}

func contactFromUpdateNoteRow(row sqlcgen.UpdateContactNoteRow) (domain.Contact, error) {
	entities, err := decodeMessageEntities(row.NoteEntitiesJson)
	if err != nil {
		return domain.Contact{}, fmt.Errorf("decode contact note entities: %w", err)
	}
	return contactFromFields(row.ID, row.AccessHash, row.Phone, row.FirstName, row.LastName, row.Username, row.CountryCode, row.Verified, row.Support, row.IsBot, int(row.BotInfoVersion), premiumUntilFromModel(row.PremiumExpiresAt), row.EmojiStatusDocumentID, int(row.EmojiStatusUntil), row.EmojiStatusCollectibleID, row.EmojiStatusCollectible, int(row.LastSeenAt), row.ContactFirstName, row.ContactLastName, row.ContactPhone, row.Note, entities, row.Mutual, row.CloseFriend), nil
}

// contactFromFields 组装 domain.Contact。getContacts 主路径（List/Get/Upsert/UpdateNote
// 四个 sqlc 查询）传真实 is_bot/bot_info_version；ImportContacts 等手机号批量导入的
// raw-scan 调用传 false/0——bot 无 phone 不经手机号导入，bot 加联系人走 username
// 的单条 UpsertContact 路径（已带真实 bot 列）。premium/emoji status 列所有路径必须
// 传真实值：TDesktop 对任何缺 emoji_status 字段的 user TL 一律清空本地状态。
func contactFromFields(id, accessHash int64, phone, firstName, lastName, username, countryCode string, verified, support, isBot bool, botInfoVersion, premiumUntil int, emojiStatusDocumentID int64, emojiStatusUntil int, emojiCollectibleID *int64, emojiCollectibleJSON []byte, lastSeenAt int, contactFirstName, contactLastName, contactPhone, note string, noteEntities []domain.MessageEntity, mutual, closeFriend bool) domain.Contact {
	return domain.Contact{
		User: domain.User{
			ID:                     id,
			AccessHash:             accessHash,
			Phone:                  phone,
			FirstName:              firstName,
			LastName:               lastName,
			Username:               username,
			CountryCode:            countryCode,
			Verified:               verified,
			Support:                support,
			Bot:                    isBot,
			BotInfoVersion:         botInfoVersion,
			PremiumUntil:           premiumUntil,
			EmojiStatusDocumentID:  emojiStatusDocumentID,
			EmojiStatusUntil:       emojiStatusUntil,
			EmojiStatusCollectible: mustDecodeEmojiStatusCollectible(emojiCollectibleID, emojiCollectibleJSON),
			LastSeenAt:             lastSeenAt,
			Contact:                true,
			Mutual:                 mutual,
			CloseFriend:            closeFriend,
		},
		FirstName:    contactFirstName,
		LastName:     contactLastName,
		Phone:        contactPhone,
		Note:         note,
		NoteEntities: noteEntities,
		Mutual:       mutual,
		CloseFriend:  closeFriend,
	}
}

type contactScanner interface {
	Scan(dest ...any) error
}

func scanContactRows(row contactScanner) (domain.Contact, error) {
	var (
		contactUserID        int64
		mutual               bool
		closeFriend          bool
		contactPhone         string
		contactFirstName     string
		contactLastName      string
		note                 string
		noteEntitiesJSON     string
		id                   int64
		accessHash           int64
		phone                string
		firstName            string
		lastName             string
		username             string
		countryCode          string
		verified             bool
		support              bool
		premiumUntil         int64
		emojiStatusDocID     int64
		emojiStatusUntil     int64
		emojiCollectibleID   *int64
		emojiCollectibleJSON []byte
		lastSeenAt           int32
	)
	if err := row.Scan(
		&contactUserID,
		&mutual,
		&closeFriend,
		&contactPhone,
		&contactFirstName,
		&contactLastName,
		&note,
		&noteEntitiesJSON,
		&id,
		&accessHash,
		&phone,
		&firstName,
		&lastName,
		&username,
		&countryCode,
		&verified,
		&support,
		&premiumUntil,
		&emojiStatusDocID,
		&emojiStatusUntil,
		&emojiCollectibleID,
		&emojiCollectibleJSON,
		&lastSeenAt,
	); err != nil {
		return domain.Contact{}, err
	}
	entities, err := decodeMessageEntities(noteEntitiesJSON)
	if err != nil {
		return domain.Contact{}, err
	}
	return contactFromFields(id, accessHash, phone, firstName, lastName, username, countryCode, verified, support, false, 0, int(premiumUntil), emojiStatusDocID, int(emojiStatusUntil), emojiCollectibleID, emojiCollectibleJSON, int(lastSeenAt), contactFirstName, contactLastName, contactPhone, note, entities, mutual, closeFriend), nil
}

func scanReverseContactRows(row contactScanner) (int64, domain.Contact, error) {
	var (
		ownerUserID          int64
		mutual               bool
		closeFriend          bool
		contactPhone         string
		contactFirstName     string
		contactLastName      string
		note                 string
		noteEntitiesJSON     string
		id                   int64
		accessHash           int64
		phone                string
		firstName            string
		lastName             string
		username             string
		countryCode          string
		verified             bool
		support              bool
		premiumUntil         int64
		emojiStatusDocID     int64
		emojiStatusUntil     int64
		emojiCollectibleID   *int64
		emojiCollectibleJSON []byte
		lastSeenAt           int32
	)
	if err := row.Scan(
		&ownerUserID,
		&mutual,
		&closeFriend,
		&contactPhone,
		&contactFirstName,
		&contactLastName,
		&note,
		&noteEntitiesJSON,
		&id,
		&accessHash,
		&phone,
		&firstName,
		&lastName,
		&username,
		&countryCode,
		&verified,
		&support,
		&premiumUntil,
		&emojiStatusDocID,
		&emojiStatusUntil,
		&emojiCollectibleID,
		&emojiCollectibleJSON,
		&lastSeenAt,
	); err != nil {
		return 0, domain.Contact{}, err
	}
	entities, err := decodeMessageEntities(noteEntitiesJSON)
	if err != nil {
		return 0, domain.Contact{}, err
	}
	contact := contactFromFields(id, accessHash, phone, firstName, lastName, username, countryCode, verified, support, false, 0, int(premiumUntil), emojiStatusDocID, int(emojiStatusUntil), emojiCollectibleID, emojiCollectibleJSON, int(lastSeenAt), contactFirstName, contactLastName, contactPhone, note, entities, mutual, closeFriend)
	return ownerUserID, contact, nil
}

func (s *ContactStore) Block(ctx context.Context, userID, blockedUserID int64, date int) (bool, error) {
	if userID == 0 || blockedUserID == 0 || userID == blockedUserID {
		return false, nil
	}
	tag, err := s.db.Exec(ctx, `
INSERT INTO contact_blocks (owner_user_id, blocked_user_id, date)
VALUES ($1, $2, $3)
ON CONFLICT (owner_user_id, blocked_user_id) DO UPDATE SET
  date = EXCLUDED.date,
  created_at = contact_blocks.created_at`, userID, blockedUserID, date)
	if err != nil {
		return false, fmt.Errorf("block contact: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *ContactStore) Unblock(ctx context.Context, userID, blockedUserID int64) (bool, error) {
	if userID == 0 || blockedUserID == 0 {
		return false, nil
	}
	tag, err := s.db.Exec(ctx, `
DELETE FROM contact_blocks
WHERE owner_user_id = $1
  AND blocked_user_id = $2`, userID, blockedUserID)
	if err != nil {
		return false, fmt.Errorf("unblock contact: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *ContactStore) IsBlocked(ctx context.Context, userID, blockedUserID int64) (bool, error) {
	if userID == 0 || blockedUserID == 0 {
		return false, nil
	}
	var blocked bool
	if err := s.db.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM contact_blocks
  WHERE owner_user_id = $1
    AND blocked_user_id = $2
)`, userID, blockedUserID).Scan(&blocked); err != nil {
		return false, fmt.Errorf("check contact block: %w", err)
	}
	return blocked, nil
}

func (s *ContactStore) ListBlocked(ctx context.Context, userID int64, offset, limit int) (domain.BlockedContactList, error) {
	if userID == 0 {
		return domain.BlockedContactList{}, nil
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM contact_blocks
WHERE owner_user_id = $1`, userID).Scan(&count); err != nil {
		return domain.BlockedContactList{}, fmt.Errorf("count blocked contacts: %w", err)
	}
	rows, err := s.db.Query(ctx, `
SELECT
  b.blocked_user_id,
  b.date,
  u.access_hash,
  u.phone,
  u.first_name,
  u.last_name,
  u.username,
  u.country_code,
  u.verified,
  u.support,
  u.last_seen_at
FROM contact_blocks b
JOIN users u ON u.id = b.blocked_user_id
WHERE b.owner_user_id = $1
ORDER BY b.date DESC, b.blocked_user_id DESC
OFFSET $2
LIMIT $3`, userID, offset, limit)
	if err != nil {
		return domain.BlockedContactList{}, fmt.Errorf("list blocked contacts: %w", err)
	}
	defer rows.Close()
	out := domain.BlockedContactList{Count: count, Blocked: make([]domain.BlockedContact, 0, limit)}
	for rows.Next() {
		var item domain.BlockedContact
		var lastSeen int64
		if err := rows.Scan(
			&item.User.ID,
			&item.Date,
			&item.User.AccessHash,
			&item.User.Phone,
			&item.User.FirstName,
			&item.User.LastName,
			&item.User.Username,
			&item.User.CountryCode,
			&item.User.Verified,
			&item.User.Support,
			&lastSeen,
		); err != nil {
			return domain.BlockedContactList{}, err
		}
		item.User.LastSeenAt = int(lastSeen)
		out.Blocked = append(out.Blocked, item)
	}
	return out, rows.Err()
}

func contactListHash(contacts []domain.Contact) int64 {
	if len(contacts) == 0 {
		return 0
	}
	h := fnv.New64a()
	var buf [16]byte
	for _, c := range contacts {
		binary.LittleEndian.PutUint64(buf[:8], uint64(c.User.ID))
		if c.Mutual {
			buf[8] = 1
		} else {
			buf[8] = 0
		}
		if c.CloseFriend || c.User.CloseFriend {
			buf[9] = 1
		} else {
			buf[9] = 0
		}
		_, _ = h.Write(buf[:10])
		_, _ = h.Write([]byte(c.FirstName))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(c.LastName))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(c.Phone))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(c.Note))
		_, _ = h.Write([]byte{0})
	}
	return int64(h.Sum64())
}
