package contacts

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"telesrv/internal/app/userprojection"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

var (
	ErrContactIDInvalid  = errors.New("contact id invalid")
	ErrContactNameEmpty  = errors.New("contact name empty")
	ErrContactReqMissing = errors.New("contact request missing")
)

const maxSearchLimit = 50
const maxCloseFriendsCount = 5000

type phonePrivacyService interface {
	userprojection.PrivacyEvaluator
	userprojection.BatchPrivacyEvaluator
	AddAllowUser(ctx context.Context, ownerUserID int64, key domain.PrivacyKey, targetUserID int64) (domain.PrivacyRules, bool, error)
}

// Service 提供通讯录查询。
type Service struct {
	contacts  store.ContactStore
	users     store.UserStore
	photos    userprojection.ProfilePhotoProvider
	privacy   phonePrivacyService
	freezes   userprojection.AccountFreezeProvider
	projector *userprojection.Projector
	versions  store.ReadModelVersionStore
	cache     *contactListReadModelCache
}

// Option adjusts optional contacts service dependencies.
type Option func(*Service)

// WithPhotoProvider enables current profile photo enrichment for returned users.
func WithPhotoProvider(p userprojection.ProfilePhotoProvider) Option {
	return func(s *Service) { s.photos = p }
}

// WithPrivacyEvaluator enables viewer-specific privacy projection.
func WithPrivacyEvaluator(p phonePrivacyService) Option {
	return func(s *Service) { s.privacy = p }
}

func WithAccountFreezeProvider(p userprojection.AccountFreezeProvider) Option {
	return func(s *Service) { s.freezes = p }
}

// WithReadModelVersions enables durable hash-token fast paths for NotModified RPCs.
func WithReadModelVersions(v store.ReadModelVersionStore) Option {
	return func(s *Service) { s.versions = v }
}

// NewService 创建 contacts 服务。
func NewService(contacts store.ContactStore, users ...store.UserStore) *Service {
	s := &Service{contacts: contacts, cache: newContactListReadModelCache(defaultContactListReadModelTTL)}
	if len(users) > 0 {
		s.users = users[0]
	}
	s.rebuildProjector()
	return s
}

// Configure applies optional dependencies after construction.
func (s *Service) Configure(opts ...Option) *Service {
	if s == nil {
		return s
	}
	for _, opt := range opts {
		opt(s)
	}
	s.rebuildProjector()
	return s
}

func (s *Service) rebuildProjector() {
	if s == nil {
		return
	}
	s.projector = userprojection.New(
		userprojection.WithContactStore(s.contacts),
		userprojection.WithPhotoProvider(s.photos),
		userprojection.WithPrivacyEvaluator(s.privacy),
		userprojection.WithAccountFreezeProvider(s.freezes),
	)
}

// GetContacts 返回当前登录账号的通讯录。未登录或无持久化实现时按空账号处理。
func (s *Service) GetContacts(ctx context.Context, userID int64, hash int64) (domain.ContactList, bool, error) {
	if s == nil || s.contacts == nil || userID == 0 {
		return domain.ContactList{}, false, nil
	}
	currentHash, hasHash, err := s.contactAccountHash(ctx, userID)
	if err != nil {
		return domain.ContactList{}, false, err
	}
	if hash != 0 && hasHash && hash == currentHash {
		return domain.ContactList{Hash: currentHash}, true, nil
	}
	list, err := s.contactListReadModel(ctx, userID, currentHash, hasHash)
	if err != nil {
		return domain.ContactList{}, false, err
	}
	if hash != 0 && hash == list.Hash {
		return list, true, nil
	}
	return list, false, nil
}

func (s *Service) AddContact(ctx context.Context, userID int64, input domain.ContactInput) (domain.Contact, error) {
	if s == nil || s.contacts == nil || userID == 0 || input.ContactUserID == 0 || input.ContactUserID == userID {
		return domain.Contact{}, ErrContactIDInvalid
	}
	if input.FirstName == "" && input.LastName == "" {
		return domain.Contact{}, ErrContactNameEmpty
	}
	// Android 的 contacts.addContact 会提交带 "+" 前缀的号码（TDesktop 传纯数字或空），
	// 归一成纯数字。空串表示客户端只按 user id 添加联系人，必须原样保留；
	// TL 明确允许省略号码，服务端不得从 target 全局资料反向补出隐私号码。
	input.Phone = digitsOnly(input.Phone)
	if s.users != nil {
		_, found, err := s.users.ByID(ctx, input.ContactUserID)
		if err != nil {
			return domain.Contact{}, err
		}
		if !found {
			return domain.Contact{}, ErrContactIDInvalid
		}
	}
	contact, err := s.contacts.Upsert(ctx, userID, input)
	if err != nil {
		return domain.Contact{}, err
	}
	s.InvalidateViewers(userID, input.ContactUserID)
	if input.AddPhonePrivacyException && s.privacy != nil {
		if _, _, err := s.privacy.AddAllowUser(ctx, userID, domain.PrivacyKeyPhoneNumber, input.ContactUserID); err != nil {
			return domain.Contact{}, err
		}
	}
	return s.projectContact(ctx, userID, contact)
}

// AcceptContact creates the reciprocal contact for an existing one-way contact.
// Phone visibility remains governed exclusively by account privacy rules; this
// RPC has no protocol flag authorizing a hidden phone-number exception.
func (s *Service) AcceptContact(ctx context.Context, userID, contactUserID int64) (domain.Contact, error) {
	if s == nil || s.contacts == nil || s.users == nil || userID == 0 || contactUserID == 0 || contactUserID == userID {
		return domain.Contact{}, ErrContactIDInvalid
	}
	ownerContact, found, err := s.contacts.Get(ctx, userID, contactUserID)
	if err != nil {
		return domain.Contact{}, err
	}
	if !found {
		return domain.Contact{}, ErrContactReqMissing
	}
	self, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return domain.Contact{}, err
	}
	if !found {
		return domain.Contact{}, ErrContactIDInvalid
	}
	target, found, err := s.users.ByID(ctx, contactUserID)
	if err != nil {
		return domain.Contact{}, err
	}
	if !found {
		return domain.Contact{}, ErrContactIDInvalid
	}
	if ownerContact.Mutual {
		return ownerContact, nil
	}
	_, err = s.contacts.Upsert(ctx, contactUserID, domain.ContactInput{
		ContactUserID: userID,
		Phone:         self.Phone,
		FirstName:     self.FirstName,
		LastName:      self.LastName,
	})
	if err != nil {
		return domain.Contact{}, err
	}
	s.InvalidateViewers(userID, contactUserID)
	contact, found, err := s.contacts.Get(ctx, userID, target.ID)
	if err != nil {
		return domain.Contact{}, err
	}
	if !found {
		return domain.Contact{}, ErrContactReqMissing
	}
	return s.projectContact(ctx, userID, contact)
}

func (s *Service) ImportContacts(ctx context.Context, userID int64, inputs []domain.ContactInput) (domain.ImportContactsResult, error) {
	if s == nil || s.contacts == nil || s.users == nil || userID == 0 || len(inputs) == 0 {
		return domain.ImportContactsResult{}, nil
	}
	out := domain.ImportContactsResult{
		Imported: make([]domain.ImportedContact, 0, len(inputs)),
		Contacts: make([]domain.Contact, 0, len(inputs)),
	}
	normalized := make([]domain.ContactInput, 0, len(inputs))
	phones := make([]string, 0, len(inputs))
	seenPhones := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		phone := normalizePhone(input.Phone)
		if phone == "" {
			continue
		}
		input.Phone = phone
		normalized = append(normalized, input)
		if _, ok := seenPhones[phone]; ok {
			continue
		}
		seenPhones[phone] = struct{}{}
		phones = append(phones, phone)
	}
	if len(phones) == 0 {
		return out, nil
	}
	targets, err := s.users.ByPhones(ctx, phones)
	if err != nil {
		return domain.ImportContactsResult{}, err
	}
	if s.privacy != nil && len(targets) > 0 {
		targetIDs := make([]int64, 0, len(targets))
		for _, target := range targets {
			if target.ID != 0 && target.ID != userID {
				targetIDs = append(targetIDs, target.ID)
			}
		}
		visibility, err := s.privacy.CanSeeBatch(
			ctx,
			targetIDs,
			userID,
			[]domain.PrivacyKey{domain.PrivacyKeyAddedByPhone},
		)
		if err != nil {
			return domain.ImportContactsResult{}, err
		}
		allowed := targets[:0]
		for _, target := range targets {
			if visibility[target.ID][domain.PrivacyKeyAddedByPhone] {
				allowed = append(allowed, target)
			}
		}
		targets = allowed
	}
	byPhone := make(map[string]domain.User, len(targets))
	for _, target := range targets {
		if target.Phone != "" {
			byPhone[target.Phone] = target
		}
	}
	upsertsByTarget := make(map[int64]domain.ContactInput, len(targets))
	order := make([]int64, 0, len(targets))
	seenTargets := map[int64]struct{}{}
	for _, input := range normalized {
		target, found := byPhone[input.Phone]
		if !found || target.ID == userID || target.ID == 0 {
			continue
		}
		input.ContactUserID = target.ID
		if input.FirstName == "" && input.LastName == "" {
			input.FirstName = target.FirstName
			input.LastName = target.LastName
		}
		if _, ok := seenTargets[target.ID]; !ok {
			seenTargets[target.ID] = struct{}{}
			order = append(order, target.ID)
		}
		out.Imported = append(out.Imported, domain.ImportedContact{UserID: target.ID, ClientID: input.ClientID})
		upsertsByTarget[target.ID] = input
	}
	if len(order) == 0 {
		return out, nil
	}
	upserts := make([]domain.ContactInput, 0, len(order))
	for _, targetID := range order {
		upserts = append(upserts, upsertsByTarget[targetID])
	}
	contacts, err := s.contacts.UpsertMany(ctx, userID, upserts)
	if err != nil {
		return domain.ImportContactsResult{}, err
	}
	changedIDs := make([]int64, 0, len(upserts)+1)
	changedIDs = append(changedIDs, userID)
	for _, input := range upserts {
		changedIDs = append(changedIDs, input.ContactUserID)
	}
	s.InvalidateViewers(changedIDs...)
	if s.privacy != nil {
		for _, input := range upserts {
			if !input.AddPhonePrivacyException || input.ContactUserID == 0 {
				continue
			}
			if _, _, err := s.privacy.AddAllowUser(ctx, userID, domain.PrivacyKeyPhoneNumber, input.ContactUserID); err != nil {
				return domain.ImportContactsResult{}, err
			}
		}
	}
	out.Contacts = append(out.Contacts, contacts...)
	projected := domain.ContactList{Contacts: out.Contacts}
	if err := s.projectContactUsers(ctx, userID, &projected); err != nil {
		return domain.ImportContactsResult{}, err
	}
	out.Contacts = projected.Contacts
	return out, nil
}

func (s *Service) Search(ctx context.Context, userID int64, query string, limit int) (domain.UserSearchResult, error) {
	if s == nil || s.users == nil || userID == 0 {
		return domain.UserSearchResult{}, nil
	}
	query = strings.TrimSpace(query)
	query = strings.TrimPrefix(query, "@")
	query = strings.TrimSpace(query)
	if query == "" {
		return domain.UserSearchResult{}, nil
	}
	if limit <= 0 || limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	phoneQuery := ""
	if isPhoneSearchQuery(query) {
		phoneQuery = normalizePhone(query)
	}
	res, err := s.users.Search(ctx, userID, query, phoneQuery, limit)
	if err != nil {
		return domain.UserSearchResult{}, err
	}
	if s.privacy != nil && phoneQuery != "" && len(res.MyResults)+len(res.Results) > 0 {
		targetIDs := make([]int64, 0, len(res.MyResults)+len(res.Results))
		for _, target := range res.MyResults {
			if target.ID != 0 && target.ID != userID {
				targetIDs = append(targetIDs, target.ID)
			}
		}
		for _, target := range res.Results {
			if target.ID != 0 && target.ID != userID {
				targetIDs = append(targetIDs, target.ID)
			}
		}
		visibility, err := s.privacy.CanSeeBatch(
			ctx,
			targetIDs,
			userID,
			[]domain.PrivacyKey{domain.PrivacyKeyAddedByPhone},
		)
		if err != nil {
			return domain.UserSearchResult{}, err
		}
		knownContacts := map[int64]domain.Contact{}
		if s.contacts != nil && len(targetIDs) > 0 {
			knownContacts, err = s.contacts.GetMany(ctx, userID, targetIDs)
			if err != nil {
				return domain.UserSearchResult{}, err
			}
		}
		allowed := func(target domain.User) bool {
			if visibility[target.ID][domain.PrivacyKeyAddedByPhone] {
				return true
			}
			contact, found := knownContacts[target.ID]
			return found && contact.Phone != "" && strings.HasPrefix(contact.Phone, phoneQuery)
		}
		res.MyResults = filterSearchUsers(res.MyResults, allowed)
		res.Results = filterSearchUsers(res.Results, allowed)
	}
	return s.projectSearchResult(ctx, userID, res)
}

func (s *Service) DeleteContacts(ctx context.Context, userID int64, contactUserIDs []int64) (int, error) {
	if s == nil || s.contacts == nil || userID == 0 {
		return 0, nil
	}
	count, err := s.contacts.Delete(ctx, userID, contactUserIDs)
	if err == nil {
		ids := make([]int64, 0, len(contactUserIDs)+1)
		ids = append(ids, userID)
		ids = append(ids, contactUserIDs...)
		s.InvalidateViewers(ids...)
	}
	return count, err
}

func (s *Service) EditCloseFriends(ctx context.Context, userID int64, contactUserIDs []int64) (domain.CloseFriendsEditResult, error) {
	if s == nil || s.contacts == nil || userID == 0 || len(contactUserIDs) > maxCloseFriendsCount {
		return domain.CloseFriendsEditResult{}, ErrContactIDInvalid
	}
	ids := normalizeCloseFriendIDs(userID, contactUserIDs)
	if s.users != nil && len(ids) > 0 {
		users, err := s.users.ByIDs(ctx, ids)
		if err != nil {
			return domain.CloseFriendsEditResult{}, err
		}
		exists := make(map[int64]struct{}, len(users))
		for _, user := range users {
			if user.ID != 0 && !user.Bot {
				exists[user.ID] = struct{}{}
			}
		}
		filtered := ids[:0]
		for _, id := range ids {
			if _, ok := exists[id]; ok {
				filtered = append(filtered, id)
			}
		}
		ids = filtered
	}
	result, err := s.contacts.SetCloseFriends(ctx, userID, ids)
	if err == nil {
		s.InvalidateViewers(userID)
	}
	return result, err
}

func (s *Service) UpdateContactNote(ctx context.Context, userID, contactUserID int64, note string, entities []domain.MessageEntity) (domain.Contact, error) {
	if s == nil || s.contacts == nil || userID == 0 || contactUserID == 0 || contactUserID == userID {
		return domain.Contact{}, ErrContactIDInvalid
	}
	contact, found, err := s.contacts.UpdateNote(ctx, userID, contactUserID, note, entities)
	if err != nil {
		return domain.Contact{}, err
	}
	if !found {
		return domain.Contact{}, ErrContactIDInvalid
	}
	s.InvalidateViewers(userID)
	return contact, nil
}

func (s *Service) SetPersonalPhoto(ctx context.Context, userID, contactUserID int64, photo domain.Photo, date int) (domain.Contact, error) {
	if s == nil || s.contacts == nil || userID == 0 || contactUserID == 0 || contactUserID == userID || photo.ID == 0 {
		return domain.Contact{}, ErrContactIDInvalid
	}
	contact, found, err := s.contacts.SetPersonalPhoto(ctx, userID, contactUserID, photo.ID, date)
	if err != nil {
		return domain.Contact{}, err
	}
	if !found {
		return domain.Contact{}, ErrContactReqMissing
	}
	s.InvalidateViewers(userID)
	return s.projectContact(ctx, userID, contact)
}

func (s *Service) ClearPersonalPhoto(ctx context.Context, userID, contactUserID int64, date int) (domain.Contact, error) {
	if s == nil || s.contacts == nil || userID == 0 || contactUserID == 0 || contactUserID == userID {
		return domain.Contact{}, ErrContactIDInvalid
	}
	contact, found, err := s.contacts.SetPersonalPhoto(ctx, userID, contactUserID, 0, date)
	if err != nil {
		return domain.Contact{}, err
	}
	if !found {
		return domain.Contact{}, ErrContactReqMissing
	}
	s.InvalidateViewers(userID)
	return s.projectContact(ctx, userID, contact)
}

func (s *Service) PersonalPhotos(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	if s == nil || s.contacts == nil || userID == 0 || len(contactUserIDs) == 0 {
		return map[int64]domain.ProfilePhotoRef{}, nil
	}
	return s.contacts.PersonalPhotos(ctx, userID, contactUserIDs)
}

func (s *Service) GetPeerSettings(ctx context.Context, userID int64, peer domain.Peer) (domain.PeerSettings, error) {
	if s == nil || s.contacts == nil || userID == 0 || peer.Type != domain.PeerTypeUser || peer.ID == 0 || peer.ID == userID {
		return domain.PeerSettings{}, nil
	}
	contact, found, err := s.contacts.Get(ctx, userID, peer.ID)
	if err != nil {
		return domain.PeerSettings{}, err
	}
	blocked, err := s.contacts.IsBlocked(ctx, userID, peer.ID)
	if err != nil {
		return domain.PeerSettings{}, err
	}
	shareContact := found && !contact.Mutual
	needContactsException := false
	if s.privacy != nil {
		peerCanSeePhone, err := s.peerCanSeeCurrentUserPhone(ctx, userID, peer.ID)
		if err != nil {
			return domain.PeerSettings{}, err
		}
		needContactsException = !peerCanSeePhone
		shareContact = found && !peerCanSeePhone
	}
	return domain.PeerSettings{
		AddContact:            !found,
		BlockContact:          !blocked,
		ShareContact:          shareContact,
		NeedContactsException: needContactsException,
	}, nil
}

func (s *Service) peerCanSeeCurrentUserPhone(ctx context.Context, ownerUserID, viewerUserID int64) (bool, error) {
	allowed, err := s.privacy.CanSee(ctx, ownerUserID, viewerUserID, domain.PrivacyKeyPhoneNumber)
	if err != nil || allowed {
		return allowed, err
	}
	if s.contacts == nil {
		return false, nil
	}
	contact, found, err := s.contacts.Get(ctx, viewerUserID, ownerUserID)
	if err != nil {
		return false, err
	}
	// Merely adding the owner by user id does not mean the viewer knows the
	// owner's phone. Only a non-empty owner-scoped contact phone can suppress the
	// "share my phone" prompt when PhoneNumber privacy itself denies visibility.
	return found && contact.Phone != "", nil
}

// BlockContact adds peer to the current user's blocklist.
func (s *Service) BlockContact(ctx context.Context, userID, peerUserID int64, date int) (bool, error) {
	if s == nil || s.contacts == nil || userID == 0 || peerUserID == 0 || peerUserID == userID {
		return false, ErrContactIDInvalid
	}
	if s.users != nil {
		if _, found, err := s.users.ByID(ctx, peerUserID); err != nil {
			return false, err
		} else if !found {
			return false, ErrContactIDInvalid
		}
	}
	changed, err := s.contacts.Block(ctx, userID, peerUserID, date)
	if err == nil {
		s.InvalidateViewers(userID, peerUserID)
	}
	return changed, err
}

// UnblockContact removes peer from the current user's blocklist.
func (s *Service) UnblockContact(ctx context.Context, userID, peerUserID int64) (bool, error) {
	if s == nil || s.contacts == nil || userID == 0 || peerUserID == 0 || peerUserID == userID {
		return false, ErrContactIDInvalid
	}
	changed, err := s.contacts.Unblock(ctx, userID, peerUserID)
	if err == nil {
		s.InvalidateViewers(userID, peerUserID)
	}
	return changed, err
}

// IsBlocked reports whether owner has blocked peer.
func (s *Service) IsBlocked(ctx context.Context, userID, peerUserID int64) (bool, error) {
	if s == nil || s.contacts == nil || userID == 0 || peerUserID == 0 {
		return false, nil
	}
	return s.contacts.IsBlocked(ctx, userID, peerUserID)
}

// GetBlocked returns a bounded blocked contact page.
func (s *Service) GetBlocked(ctx context.Context, userID int64, offset, limit int) (domain.BlockedContactList, error) {
	if s == nil || s.contacts == nil || userID == 0 {
		return domain.BlockedContactList{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	list, err := s.contacts.ListBlocked(ctx, userID, offset, limit)
	if err != nil || len(list.Blocked) == 0 || s.projector == nil {
		return list, err
	}
	users := make([]domain.User, len(list.Blocked))
	for i := range list.Blocked {
		users[i] = list.Blocked[i].User
	}
	projected, err := s.projector.ForViewer(ctx, userID, users)
	if err != nil {
		return domain.BlockedContactList{}, err
	}
	for i := range list.Blocked {
		list.Blocked[i].User = projected[i]
	}
	return list, nil
}

func (s *Service) ContactIDs(ctx context.Context, userID int64, hash int64) ([]int, bool, error) {
	list, notModified, err := s.GetContacts(ctx, userID, hash)
	if err != nil || notModified {
		return nil, notModified, err
	}
	ids := make([]int, 0, len(list.Contacts))
	for _, contact := range list.Contacts {
		ids = append(ids, int(contact.User.ID))
	}
	return ids, false, nil
}

func (s *Service) projectContactUsers(ctx context.Context, userID int64, list *domain.ContactList) error {
	if s == nil || s.projector == nil || list == nil || len(list.Contacts) == 0 {
		return nil
	}
	users := make([]domain.User, len(list.Contacts))
	for i, contact := range list.Contacts {
		users[i] = contact.User
	}
	projected, err := s.projector.ForViewer(ctx, userID, users)
	if err != nil {
		return err
	}
	for i := range list.Contacts {
		list.Contacts[i].User = projected[i]
	}
	return nil
}

func (s *Service) projectContact(ctx context.Context, userID int64, contact domain.Contact) (domain.Contact, error) {
	list := domain.ContactList{Contacts: []domain.Contact{contact}}
	if err := s.projectContactUsers(ctx, userID, &list); err != nil {
		return domain.Contact{}, err
	}
	if len(list.Contacts) == 0 {
		return domain.Contact{}, nil
	}
	return list.Contacts[0], nil
}

func (s *Service) projectSearchResult(ctx context.Context, userID int64, res domain.UserSearchResult) (domain.UserSearchResult, error) {
	if s == nil || s.projector == nil {
		return res, nil
	}
	var err error
	res.MyResults, err = s.projector.ForViewer(ctx, userID, res.MyResults)
	if err != nil {
		return domain.UserSearchResult{}, err
	}
	res.Results, err = s.projector.ForViewer(ctx, userID, res.Results)
	if err != nil {
		return domain.UserSearchResult{}, err
	}
	return res, nil
}

// digitsOnly 只保留数字字符。保存进 contacts.contact_phone 的号码必须与 users.phone
// 一样是不带 "+" 的纯数字：下发时 contact_phone 优先充当 TL user.phone，而客户端展示
// user.phone 时会自行补 "+"，任何非数字前缀都会变成 "++<号码>" 这类坏显示。
func digitsOnly(phone string) string {
	var b strings.Builder
	b.Grow(len(phone))
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizePhone(phone string) string {
	if !utf8.ValidString(phone) {
		return ""
	}
	if digits := digitsOnly(phone); digits != "" {
		return digits
	}
	return phone
}

func isPhoneSearchQuery(query string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return false
	}
	hasDigit := false
	for _, r := range query {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '+', r == ' ', r == '-', r == '(', r == ')':
		default:
			return false
		}
	}
	return hasDigit
}

func filterSearchUsers(users []domain.User, keep func(domain.User) bool) []domain.User {
	out := users[:0]
	for _, user := range users {
		if keep(user) {
			out = append(out, user)
		}
	}
	return out
}

func normalizeCloseFriendIDs(userID int64, ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 || id == userID {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
