package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/links"
)

const (
	stickersBotCmdNewPack  = "newpack"
	stickersBotCmdNewEmoji = "newemoji"
	stickersBotCmdPublish  = "publish"
	stickersBotCmdPacks    = "packs"
	stickersBotCmdAdd      = "addsticker"
	stickersBotCmdDel      = "delsticker"

	stickersBotStepSet       = "set"
	stickersBotStepTitle     = "title"
	stickersBotStepDocument  = "document"
	stickersBotStepEmoji     = "emoji"
	stickersBotStepShortName = "short_name"

	stickersBotDraftKind            = "kind"
	stickersBotDraftTitle           = "title"
	stickersBotDraftSetID           = "set_id"
	stickersBotDraftSetAccessHash   = "set_access_hash"
	stickersBotDraftSetShortName    = "set_short_name"
	stickersBotDraftSetTitle        = "set_title"
	stickersBotDraftItems           = "items"
	stickersBotDraftPendingDocID    = "pending_doc_id"
	stickersBotDraftPendingDocHash  = "pending_doc_access_hash"
	stickersBotCreateSoftware       = "telesrv-stickers-bot"
	stickersBotCreatedListPageLimit = 20
)

const stickersBotHelpText = `I can help you create sticker and custom emoji packs for telesrv.

Send /newpack to create a sticker pack.
Send /newemoji to create a custom emoji pack.
Send /addsticker to add an item to one of your packs.
Send /delsticker to remove an item from one of your packs.

Send a sticker/custom emoji, or upload a TGS, Lottie JSON, WebP, WebM, or MP4 file as a document. Send /publish when your pack is ready, then choose a short name for the link.

/packs - list your created packs
/cancel - cancel the current operation
/help - show this message`

var stickersBotGlobalCommands = map[string]bool{
	"start": true, "help": true, "cancel": true,
	stickersBotCmdNewPack: true, stickersBotCmdNewEmoji: true,
	stickersBotCmdPublish: true, stickersBotCmdPacks: true,
	stickersBotCmdAdd: true, stickersBotCmdDel: true,
}

type stickersBotDraftItem struct {
	DocumentID         int64  `json:"document_id"`
	DocumentAccessHash int64  `json:"document_access_hash"`
	Emoji              string `json:"emoji"`
}

func (s *Service) respondAsStickers(userID int64, msg domain.Message) {
	mu := s.serviceBotReplyLock(domain.StickersBotUserID, userID)
	mu.Lock()
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reply := s.handleStickers(ctx, userID, msg)
	s.sendServiceBotReply(ctx, domain.StickersBotUserID, userID, reply)
}

func (s *Service) handleStickers(ctx context.Context, userID int64, msg domain.Message) botReply {
	text := strings.TrimSpace(msg.Body)
	state, found, err := s.bots.GetBotChatState(ctx, domain.StickersBotUserID, userID)
	if err != nil {
		s.log.Error("stickersbot: get chat state", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	if cmd, ok := parseBotCommand(text); ok {
		if stickersBotGlobalCommands[cmd] {
			return s.handleStickersCommand(ctx, userID, cmd, state, found)
		}
		return botReply{Text: "Unrecognized command. Send /help for a list of commands."}
	}
	if !found {
		if stickersBotDocument(msg) != nil {
			return botReply{Text: "Start a pack first with /newpack or /newemoji."}
		}
		return botReply{Text: "Send /newpack to create a sticker pack or /newemoji to create a custom emoji pack."}
	}
	switch state.Step {
	case stickersBotStepSet:
		if text == "" {
			return stickersBotStepPrompt(state)
		}
		return s.handleStickersSet(ctx, state, text)
	case stickersBotStepTitle:
		if text == "" {
			return stickersBotStepPrompt(state)
		}
		return s.handleStickersTitle(ctx, state, text)
	case stickersBotStepDocument:
		switch state.Command {
		case stickersBotCmdAdd:
			return s.handleStickersAddDocument(ctx, state, msg)
		case stickersBotCmdDel:
			return s.handleStickersDeleteDocument(ctx, state, msg)
		}
		return s.handleStickersDocument(ctx, state, msg)
	case stickersBotStepEmoji:
		if text == "" {
			return stickersBotStepPrompt(state)
		}
		if state.Command == stickersBotCmdAdd {
			return s.handleStickersAddEmoji(ctx, state, text)
		}
		return s.handleStickersEmoji(ctx, state, text)
	case stickersBotStepShortName:
		if text == "" {
			return stickersBotStepPrompt(state)
		}
		return s.handleStickersShortName(ctx, state, text)
	default:
		if err := s.bots.DeleteBotChatState(ctx, domain.StickersBotUserID, userID); err != nil {
			s.log.Error("stickersbot: delete corrupt state", zap.Int64("user_id", userID), zap.Error(err))
		}
		return botReply{Text: "Something went wrong, I forgot what we were doing. Send /newpack or /newemoji to start again."}
	}
}

func (s *Service) handleStickersCommand(ctx context.Context, userID int64, cmd string, state domain.BotChatState, found bool) botReply {
	switch cmd {
	case "start":
		_ = s.bots.DeleteBotChatState(ctx, domain.StickersBotUserID, userID)
		return botReply{Text: stickersBotHelpText}
	case "help":
		return botReply{Text: stickersBotHelpText}
	case "cancel":
		if !found {
			return botReply{Text: "No active pack to cancel."}
		}
		if err := s.bots.DeleteBotChatState(ctx, domain.StickersBotUserID, userID); err != nil {
			s.log.Error("stickersbot: delete chat state", zap.Int64("user_id", userID), zap.Error(err))
			return internalReply()
		}
		return botReply{Text: "Cancelled. Send /newpack or /newemoji when you are ready."}
	case stickersBotCmdNewPack:
		return s.startStickersFlow(ctx, userID, stickersBotCmdNewPack, domain.StickerSetKindStickers)
	case stickersBotCmdNewEmoji:
		return s.startStickersFlow(ctx, userID, stickersBotCmdNewEmoji, domain.StickerSetKindEmoji)
	case stickersBotCmdAdd:
		return s.startStickersEditFlow(ctx, userID, stickersBotCmdAdd)
	case stickersBotCmdDel:
		return s.startStickersEditFlow(ctx, userID, stickersBotCmdDel)
	case stickersBotCmdPublish:
		if !found || !stickersBotCreateCommand(state.Command) {
			return botReply{Text: "Start a pack first with /newpack or /newemoji."}
		}
		if len(stickersBotDraftItemsFromState(state)) == 0 {
			return botReply{Text: "Add at least one sticker material document before publishing."}
		}
		state.Step = stickersBotStepShortName
		if err := s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state)); err != nil {
			s.log.Error("stickersbot: save publish state", zap.Int64("user_id", userID), zap.Error(err))
			return internalReply()
		}
		return botReply{Text: "Choose a short name for this pack. It will be used in the public link, for example: my_fun_pack"}
	case stickersBotCmdPacks:
		return s.listStickersBotPacks(ctx, userID)
	default:
		return botReply{Text: "Unrecognized command. Send /help for a list of commands."}
	}
}

func (s *Service) startStickersEditFlow(ctx context.Context, userID int64, cmd string) botReply {
	state := domain.BotChatState{
		BotUserID: domain.StickersBotUserID,
		UserID:    userID,
		Command:   cmd,
		Step:      stickersBotStepSet,
		Draft:     map[string]string{},
	}
	if err := s.bots.UpsertBotChatState(ctx, state); err != nil {
		s.log.Error("stickersbot: save edit state", zap.Int64("user_id", userID), zap.String("command", cmd), zap.Error(err))
		return internalReply()
	}
	if cmd == stickersBotCmdDel {
		return botReply{Text: "Send the short name or telesrv link of the pack you want to edit. Use /packs to see your packs."}
	}
	return botReply{Text: "Send the short name or telesrv link of the pack you want to add to. Use /packs to see your packs."}
}

func (s *Service) startStickersFlow(ctx context.Context, userID int64, cmd string, kind domain.StickerSetKind) botReply {
	state := domain.BotChatState{
		BotUserID: domain.StickersBotUserID,
		UserID:    userID,
		Command:   cmd,
		Step:      stickersBotStepTitle,
		Draft: map[string]string{
			stickersBotDraftKind: string(kind),
		},
	}
	if err := s.bots.UpsertBotChatState(ctx, state); err != nil {
		s.log.Error("stickersbot: save chat state", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	if kind == domain.StickerSetKindEmoji {
		return botReply{Text: "Alright, a new custom emoji pack. Send me a title for it."}
	}
	return botReply{Text: "Alright, a new sticker pack. Send me a title for it."}
}

func (s *Service) handleStickersSet(ctx context.Context, state domain.BotChatState, raw string) botReply {
	if s.stickers == nil {
		return botReply{Text: "Sticker pack editing is not available right now."}
	}
	shortName := normalizeStickersBotShortName(raw)
	if shortName == "" || strings.HasPrefix(shortName, "/") {
		return botReply{Text: "Send the pack short name or telesrv link. Use /packs to list your packs, or /cancel."}
	}
	set, _, found, err := s.stickers.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: shortName})
	if err != nil {
		s.log.Error("stickersbot: resolve edit set", zap.Int64("user_id", state.UserID), zap.String("short_name", shortName), zap.Error(err))
		return internalReply()
	}
	if !found || set.Deleted || set.ID == 0 {
		return botReply{Text: "I couldn't find that pack. Send a short name from /packs, or /cancel."}
	}
	if set.CreatorUserID != state.UserID {
		return botReply{Text: "I can only edit packs created by you. Send one of your pack links, or /cancel."}
	}
	if state.Draft == nil {
		state.Draft = map[string]string{}
	}
	state.Draft[stickersBotDraftSetID] = strconv.FormatInt(set.ID, 10)
	state.Draft[stickersBotDraftSetAccessHash] = strconv.FormatInt(set.AccessHash, 10)
	state.Draft[stickersBotDraftSetShortName] = set.ShortName
	state.Draft[stickersBotDraftSetTitle] = set.Title
	state.Draft[stickersBotDraftKind] = string(stickersBotSetKind(set))
	state.Step = stickersBotStepDocument
	if err := s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state)); err != nil {
		s.log.Error("stickersbot: save edit set", zap.Int64("user_id", state.UserID), zap.Int64("set_id", set.ID), zap.Error(err))
		return internalReply()
	}
	if state.Command == stickersBotCmdDel {
		return botReply{Text: fmt.Sprintf("Selected %s. Send the sticker or custom emoji from this pack that you want to remove.", stickersBotSetTitleFromState(state))}
	}
	return botReply{Text: fmt.Sprintf("Selected %s. Now send the sticker material document to add.", stickersBotSetTitleFromState(state))}
}

func (s *Service) handleStickersTitle(ctx context.Context, state domain.BotChatState, title string) botReply {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > domain.MaxStickerSetTitleLen {
		return botReply{Text: fmt.Sprintf("The title must be 1-%d characters. Send another title or /cancel.", domain.MaxStickerSetTitleLen)}
	}
	if state.Draft == nil {
		state.Draft = map[string]string{}
	}
	state.Draft[stickersBotDraftTitle] = title
	if _, ok := state.Draft[stickersBotDraftKind]; !ok {
		state.Draft[stickersBotDraftKind] = string(stickersBotKindFromState(state))
	}
	state.Step = stickersBotStepDocument
	if err := s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state)); err != nil {
		s.log.Error("stickersbot: save title", zap.Int64("user_id", state.UserID), zap.Error(err))
		return internalReply()
	}
	return botReply{Text: "Good. Now send me a sticker, custom emoji, or a TGS/Lottie JSON/WebP/WebM/MP4 document to add to this pack."}
}

func (s *Service) handleStickersDocument(ctx context.Context, state domain.BotChatState, msg domain.Message) botReply {
	doc := stickersBotDocument(msg)
	if doc == nil || doc.ID == 0 || doc.AccessHash == 0 || !doc.IsStickerSetMaterial() {
		return botReply{Text: "Please send a sticker, custom emoji, TGS, Lottie JSON, or WebP document. WebM/MP4 must include video metadata. /cancel to stop."}
	}
	if len(stickersBotDraftItemsFromState(state)) >= domain.MaxStickerSetItems {
		return botReply{Text: fmt.Sprintf("This pack already has the maximum of %d items. Send /publish to finish.", domain.MaxStickerSetItems)}
	}
	if state.Draft == nil {
		state.Draft = map[string]string{}
	}
	state.Draft[stickersBotDraftPendingDocID] = strconv.FormatInt(doc.ID, 10)
	state.Draft[stickersBotDraftPendingDocHash] = strconv.FormatInt(doc.AccessHash, 10)
	state.Step = stickersBotStepEmoji
	if err := s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state)); err != nil {
		s.log.Error("stickersbot: save document", zap.Int64("user_id", state.UserID), zap.Error(err))
		return internalReply()
	}
	return botReply{Text: "Now send the emoji that should be associated with this item."}
}

func (s *Service) handleStickersAddDocument(ctx context.Context, state domain.BotChatState, msg domain.Message) botReply {
	doc := stickersBotDocument(msg)
	if doc == nil || doc.ID == 0 || doc.AccessHash == 0 || !doc.IsStickerSetMaterial() {
		return botReply{Text: "Please send a sticker, custom emoji, TGS, Lottie JSON, or WebP document to add. WebM/MP4 must include video metadata. /cancel to stop."}
	}
	if state.Draft == nil {
		state.Draft = map[string]string{}
	}
	state.Draft[stickersBotDraftPendingDocID] = strconv.FormatInt(doc.ID, 10)
	state.Draft[stickersBotDraftPendingDocHash] = strconv.FormatInt(doc.AccessHash, 10)
	state.Step = stickersBotStepEmoji
	if err := s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state)); err != nil {
		s.log.Error("stickersbot: save add document", zap.Int64("user_id", state.UserID), zap.Error(err))
		return internalReply()
	}
	return botReply{Text: "Now send the emoji that should be associated with this added item."}
}

func (s *Service) handleStickersEmoji(ctx context.Context, state domain.BotChatState, emoji string) botReply {
	emoji = strings.TrimSpace(emoji)
	if !validStickersBotEmoji(emoji) {
		return botReply{Text: "That doesn't look like a valid emoji. Send an emoji like 🙂, or /cancel."}
	}
	docID, docHash := pendingStickersBotDocument(state)
	if docID == 0 || docHash == 0 {
		state.Step = stickersBotStepDocument
		delete(state.Draft, stickersBotDraftPendingDocID)
		delete(state.Draft, stickersBotDraftPendingDocHash)
		_ = s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state))
		return botReply{Text: "I lost the document for this item. Please send it again."}
	}
	items := stickersBotDraftItemsFromState(state)
	for _, item := range items {
		if item.DocumentID == docID {
			state.Step = stickersBotStepDocument
			delete(state.Draft, stickersBotDraftPendingDocID)
			delete(state.Draft, stickersBotDraftPendingDocHash)
			_ = s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state))
			return botReply{Text: "That document is already in this pack. Send another document or /publish."}
		}
	}
	items = append(items, stickersBotDraftItem{DocumentID: docID, DocumentAccessHash: docHash, Emoji: emoji})
	if err := setStickersBotDraftItems(&state, items); err != nil {
		s.log.Error("stickersbot: encode items", zap.Int64("user_id", state.UserID), zap.Error(err))
		return internalReply()
	}
	delete(state.Draft, stickersBotDraftPendingDocID)
	delete(state.Draft, stickersBotDraftPendingDocHash)
	state.Step = stickersBotStepDocument
	if err := s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state)); err != nil {
		s.log.Error("stickersbot: save emoji", zap.Int64("user_id", state.UserID), zap.Error(err))
		return internalReply()
	}
	return botReply{Text: fmt.Sprintf("Added. This pack has %d item(s). Send another sticker material document, or /publish when ready.", len(items))}
}

func (s *Service) handleStickersAddEmoji(ctx context.Context, state domain.BotChatState, emoji string) botReply {
	if s.stickers == nil {
		return botReply{Text: "Sticker pack editing is not available right now."}
	}
	emoji = strings.TrimSpace(emoji)
	if !validStickersBotEmoji(emoji) {
		return botReply{Text: "That doesn't look like a valid emoji. Send an emoji like 🙂, or /cancel."}
	}
	docID, docHash := pendingStickersBotDocument(state)
	if docID == 0 || docHash == 0 {
		state.Step = stickersBotStepDocument
		delete(state.Draft, stickersBotDraftPendingDocID)
		delete(state.Draft, stickersBotDraftPendingDocHash)
		_ = s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state))
		return botReply{Text: "I lost the document for this item. Please send it again."}
	}
	ref, ok := stickersBotSetRefFromState(state)
	if !ok {
		_ = s.bots.DeleteBotChatState(ctx, domain.StickersBotUserID, state.UserID)
		return botReply{Text: "I lost the pack selection. Send /addsticker to start again."}
	}
	set, _, found, err := s.stickers.ResolveStickerSet(ctx, ref)
	if err != nil {
		s.log.Error("stickersbot: resolve add set", zap.Int64("user_id", state.UserID), zap.Error(err))
		return internalReply()
	}
	if !found || set.Deleted || set.CreatorUserID != state.UserID {
		_ = s.bots.DeleteBotChatState(ctx, domain.StickersBotUserID, state.UserID)
		return botReply{Text: "That pack is no longer available. Send /addsticker to start again."}
	}
	if containsInt64(set.DocumentIDs, docID) {
		state.Step = stickersBotStepDocument
		delete(state.Draft, stickersBotDraftPendingDocID)
		delete(state.Draft, stickersBotDraftPendingDocHash)
		if err := s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state)); err != nil {
			s.log.Error("stickersbot: save duplicate add state", zap.Int64("user_id", state.UserID), zap.Error(err))
			return internalReply()
		}
		return botReply{Text: "That document is already in this pack. Send another sticker material document, or /cancel."}
	}
	set, _, err = s.stickers.AddStickerToSet(ctx, state.UserID, ref, domain.StickerSetItemInput{
		DocumentID:         docID,
		DocumentAccessHash: docHash,
		Emoji:              emoji,
	})
	if err != nil {
		return s.stickersBotEditError(state.UserID, err)
	}
	if err := s.bots.DeleteBotChatState(ctx, domain.StickersBotUserID, state.UserID); err != nil {
		s.log.Error("stickersbot: delete add state", zap.Int64("user_id", state.UserID), zap.Error(err))
	}
	if s.hooks != nil {
		s.hooks.PushStickerSetsChanged(ctx, state.UserID, stickersBotSetKind(set))
	}
	return botReply{Text: fmt.Sprintf("Done. Added to %s.\n\n%s", stickersBotSetTitle(set), s.stickersBotPublicURL(set))}
}

func (s *Service) handleStickersDeleteDocument(ctx context.Context, state domain.BotChatState, msg domain.Message) botReply {
	if s.stickers == nil {
		return botReply{Text: "Sticker pack editing is not available right now."}
	}
	doc, err := s.stickersBotDocumentForDelete(ctx, msg)
	if err != nil {
		s.log.Error("stickersbot: load delete document", zap.Int64("user_id", state.UserID), zap.Error(err))
		return internalReply()
	}
	if doc == nil || doc.ID == 0 || doc.AccessHash == 0 || !doc.IsStickerLike() {
		return botReply{Text: "Please send the sticker or custom emoji from the selected pack that you want to remove, or /cancel."}
	}
	setID, setHash, ok := stickersBotSetIdentityFromState(state)
	if !ok {
		_ = s.bots.DeleteBotChatState(ctx, domain.StickersBotUserID, state.UserID)
		return botReply{Text: "I lost the pack selection. Send /delsticker to start again."}
	}
	docSetID, docSetHash, docHasSet := doc.StickerSetRef()
	if !docHasSet || docSetID != setID || docSetHash != setHash {
		return botReply{Text: "That sticker is not from the selected pack. Send a sticker from that pack, or /cancel."}
	}
	set, _, err := s.stickers.RemoveStickerFromSet(ctx, state.UserID, doc.ID, doc.AccessHash)
	if err != nil {
		return s.stickersBotEditError(state.UserID, err)
	}
	if err := s.bots.DeleteBotChatState(ctx, domain.StickersBotUserID, state.UserID); err != nil {
		s.log.Error("stickersbot: delete remove state", zap.Int64("user_id", state.UserID), zap.Error(err))
	}
	if s.hooks != nil {
		s.hooks.PushStickerSetsChanged(ctx, state.UserID, stickersBotSetKind(set))
	}
	return botReply{Text: fmt.Sprintf("Done. Removed from %s.\n\n%s", stickersBotSetTitle(set), s.stickersBotPublicURL(set))}
}

func (s *Service) handleStickersShortName(ctx context.Context, state domain.BotChatState, raw string) botReply {
	if s.stickers == nil || s.installer == nil {
		return botReply{Text: "Sticker pack creation is not available right now. Please try again later."}
	}
	shortName := normalizeStickersBotShortName(raw)
	items := stickersBotDraftItemsFromState(state)
	if len(items) == 0 {
		state.Step = stickersBotStepDocument
		if err := s.bots.UpsertBotChatState(ctx, cloneStickersBotState(state)); err != nil {
			s.log.Error("stickersbot: save empty publish state", zap.Int64("user_id", state.UserID), zap.Error(err))
			return internalReply()
		}
		return botReply{Text: "Add at least one sticker material document before publishing."}
	}
	kind := stickersBotKindFromState(state)
	set, _, err := s.stickers.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: state.UserID,
		Title:         state.Draft[stickersBotDraftTitle],
		ShortName:     shortName,
		Kind:          kind,
		Items:         stickersBotCreateItems(items),
		Software:      stickersBotCreateSoftware,
		Date:          int(s.now().Unix()),
	})
	if err != nil {
		return s.stickersBotCreateError(state.UserID, err)
	}
	installKind := stickersBotSetKind(set)
	if err := s.installer.InstallUserStickerSet(ctx, state.UserID, set.ID, installKind, false, int(s.now().Unix())); err != nil {
		s.log.Error("stickersbot: install created set", zap.Int64("user_id", state.UserID), zap.Int64("set_id", set.ID), zap.Error(err))
		return internalReply()
	}
	if err := s.bots.DeleteBotChatState(ctx, domain.StickersBotUserID, state.UserID); err != nil {
		s.log.Error("stickersbot: delete published state", zap.Int64("user_id", state.UserID), zap.Error(err))
	}
	if s.hooks != nil {
		s.hooks.PushStickerSetsChanged(ctx, state.UserID, installKind)
	}
	return botReply{Text: "Done. Your pack is published and installed.\n\n" + s.stickersBotPublicURL(set)}
}

func (s *Service) stickersBotCreateError(userID int64, err error) botReply {
	switch {
	case errors.Is(err, domain.ErrStickerSetShortNameInvalid):
		return botReply{Text: "That short name is invalid. Use 5-32 lowercase letters, digits or underscores, starting with a letter."}
	case errors.Is(err, domain.ErrStickerSetShortNameOccupied):
		return botReply{Text: "That short name is already taken. Please send another one."}
	case errors.Is(err, domain.ErrStickerSetTitleInvalid):
		return botReply{Text: "The title is invalid. Send /cancel and start again."}
	case errors.Is(err, domain.ErrStickerSetEmpty):
		return botReply{Text: "Add at least one sticker material document before publishing."}
	case errors.Is(err, domain.ErrStickerSetEmojiInvalid):
		return botReply{Text: "One of the emoji values is invalid. Send /cancel and start again."}
	case errors.Is(err, domain.ErrStickerSetFileInvalid):
		return botReply{Text: "One of the documents is no longer a valid sticker material file. Send /cancel and start again."}
	default:
		s.log.Error("stickersbot: create sticker set", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
}

func (s *Service) stickersBotEditError(userID int64, err error) botReply {
	switch {
	case errors.Is(err, domain.ErrStickerSetInvalid):
		return botReply{Text: "That pack is no longer available. Send /packs and try again."}
	case errors.Is(err, domain.ErrStickerSetNotOwned):
		return botReply{Text: "I can only edit packs created by you."}
	case errors.Is(err, domain.ErrStickerSetTooMuch):
		return botReply{Text: fmt.Sprintf("That pack already has the maximum of %d items.", domain.MaxStickerSetItems)}
	case errors.Is(err, domain.ErrStickerSetEmpty):
		return botReply{Text: "A pack must keep at least one item. Add another item before removing this one."}
	case errors.Is(err, domain.ErrStickerSetEmojiInvalid):
		return botReply{Text: "That doesn't look like a valid emoji. Send /addsticker and try again."}
	case errors.Is(err, domain.ErrStickerSetFileInvalid):
		return botReply{Text: "That document is not a valid sticker material or is not in the selected pack. Send the command again and try another document."}
	default:
		s.log.Error("stickersbot: edit sticker set", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
}

func (s *Service) listStickersBotPacks(ctx context.Context, userID int64) botReply {
	if s.stickers == nil {
		return botReply{Text: "Sticker pack listing is not available right now."}
	}
	sets, total, err := s.stickers.ListCreatedStickerSets(ctx, userID, 0, stickersBotCreatedListPageLimit)
	if err != nil {
		s.log.Error("stickersbot: list packs", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	if len(sets) == 0 {
		return botReply{Text: "You don't have any packs yet. Use /newpack or /newemoji to create one."}
	}
	lines := make([]string, 0, len(sets)+1)
	lines = append(lines, "Your packs:")
	for _, set := range sets {
		lines = append(lines, fmt.Sprintf("%s - %s", set.Title, s.stickersBotPublicURL(set)))
	}
	if total > len(sets) {
		lines = append(lines, fmt.Sprintf("Showing %d of %d.", len(sets), total))
	}
	lines = append(lines, "Use /addsticker to add to a pack, or /delsticker to remove one item.")
	return botReply{Text: strings.Join(lines, "\n")}
}

func stickersBotStepPrompt(state domain.BotChatState) botReply {
	switch state.Step {
	case stickersBotStepSet:
		return botReply{Text: "Send the pack short name or telesrv link, or /cancel."}
	case stickersBotStepTitle:
		return botReply{Text: "Send a title for this pack, or /cancel."}
	case stickersBotStepDocument:
		switch state.Command {
		case stickersBotCmdAdd:
			return botReply{Text: "Send a sticker material document to add, or /cancel."}
		case stickersBotCmdDel:
			return botReply{Text: "Send the sticker or custom emoji from the selected pack to remove, or /cancel."}
		}
		return botReply{Text: "Send a sticker material document, or /publish if the pack already has items."}
	case stickersBotStepEmoji:
		return botReply{Text: "Send the emoji for the last document, or /cancel."}
	case stickersBotStepShortName:
		return botReply{Text: "Send a short name for the public link, or /cancel."}
	default:
		return botReply{Text: "Send /help for a list of commands."}
	}
}

func stickersBotDocument(msg domain.Message) *domain.Document {
	if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindDocument || msg.Media.Document == nil {
		return nil
	}
	doc := *msg.Media.Document
	return &doc
}

func (s *Service) stickersBotDocumentForDelete(ctx context.Context, msg domain.Message) (*domain.Document, error) {
	if doc := stickersBotDocument(msg); doc != nil {
		return doc, nil
	}
	for _, entity := range msg.Entities {
		if entity.Type != domain.MessageEntityCustomEmoji || entity.DocumentID == 0 {
			continue
		}
		docs, err := s.stickers.GetDocuments(ctx, []int64{entity.DocumentID})
		if err != nil {
			return nil, err
		}
		for _, doc := range docs {
			if doc.ID == entity.DocumentID {
				doc := doc
				return &doc, nil
			}
		}
		return nil, nil
	}
	return nil, nil
}

func stickersBotKindFromState(state domain.BotChatState) domain.StickerSetKind {
	if state.Draft != nil {
		switch domain.StickerSetKind(state.Draft[stickersBotDraftKind]) {
		case domain.StickerSetKindEmoji:
			return domain.StickerSetKindEmoji
		case domain.StickerSetKindMasks:
			return domain.StickerSetKindMasks
		}
	}
	if state.Command == stickersBotCmdNewEmoji {
		return domain.StickerSetKindEmoji
	}
	return domain.StickerSetKindStickers
}

func stickersBotCreateCommand(command string) bool {
	return command == stickersBotCmdNewPack || command == stickersBotCmdNewEmoji
}

func stickersBotSetKind(set domain.StickerSet) domain.StickerSetKind {
	switch {
	case set.Kind == domain.StickerSetKindMasks || set.Masks:
		return domain.StickerSetKindMasks
	case set.Kind == domain.StickerSetKindEmoji || set.Emojis:
		return domain.StickerSetKindEmoji
	default:
		return domain.StickerSetKindStickers
	}
}

func stickersBotSetTitle(set domain.StickerSet) string {
	title := strings.TrimSpace(set.Title)
	if title == "" {
		title = set.ShortName
	}
	if title == "" {
		return "the pack"
	}
	return title
}

func stickersBotSetTitleFromState(state domain.BotChatState) string {
	if state.Draft != nil {
		if title := strings.TrimSpace(state.Draft[stickersBotDraftSetTitle]); title != "" {
			return title
		}
		if shortName := strings.TrimSpace(state.Draft[stickersBotDraftSetShortName]); shortName != "" {
			return shortName
		}
	}
	return "the pack"
}

func stickersBotSetRefFromState(state domain.BotChatState) (domain.StickerSetRef, bool) {
	id, accessHash, ok := stickersBotSetIdentityFromState(state)
	if ok {
		return domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: id, AccessHash: accessHash}, true
	}
	if state.Draft != nil {
		if shortName := strings.TrimSpace(state.Draft[stickersBotDraftSetShortName]); shortName != "" {
			return domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: shortName}, true
		}
	}
	return domain.StickerSetRef{}, false
}

func stickersBotSetIdentityFromState(state domain.BotChatState) (int64, int64, bool) {
	if state.Draft == nil {
		return 0, 0, false
	}
	id, _ := strconv.ParseInt(state.Draft[stickersBotDraftSetID], 10, 64)
	accessHash, _ := strconv.ParseInt(state.Draft[stickersBotDraftSetAccessHash], 10, 64)
	return id, accessHash, id != 0 && accessHash != 0
}

func stickersBotDraftItemsFromState(state domain.BotChatState) []stickersBotDraftItem {
	if state.Draft == nil || strings.TrimSpace(state.Draft[stickersBotDraftItems]) == "" {
		return nil
	}
	var items []stickersBotDraftItem
	if err := json.Unmarshal([]byte(state.Draft[stickersBotDraftItems]), &items); err != nil {
		return nil
	}
	return items
}

func setStickersBotDraftItems(state *domain.BotChatState, items []stickersBotDraftItem) error {
	if state.Draft == nil {
		state.Draft = map[string]string{}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	state.Draft[stickersBotDraftItems] = string(raw)
	return nil
}

func pendingStickersBotDocument(state domain.BotChatState) (int64, int64) {
	if state.Draft == nil {
		return 0, 0
	}
	id, _ := strconv.ParseInt(state.Draft[stickersBotDraftPendingDocID], 10, 64)
	accessHash, _ := strconv.ParseInt(state.Draft[stickersBotDraftPendingDocHash], 10, 64)
	return id, accessHash
}

func stickersBotCreateItems(items []stickersBotDraftItem) []domain.StickerSetItemInput {
	out := make([]domain.StickerSetItemInput, 0, len(items))
	for _, item := range items {
		out = append(out, domain.StickerSetItemInput{
			DocumentID:         item.DocumentID,
			DocumentAccessHash: item.DocumentAccessHash,
			Emoji:              item.Emoji,
		})
	}
	return out
}

func containsInt64(values []int64, want int64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cloneStickersBotState(state domain.BotChatState) domain.BotChatState {
	out := state
	if state.Draft != nil {
		out.Draft = make(map[string]string, len(state.Draft))
		for k, v := range state.Draft {
			out.Draft[k] = v
		}
	}
	return out
}

func normalizeStickersBotShortName(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "telesrv://addstickers?set=")
	raw = strings.TrimPrefix(raw, "telesrv://addemoji?set=")
	raw = strings.TrimPrefix(raw, "tg://addstickers?set=")
	raw = strings.TrimPrefix(raw, "tg://addemoji?set=")
	if strings.Contains(raw, "://") {
		if parsed, err := url.Parse(raw); err == nil {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			for i, part := range parts {
				if (part == "addstickers" || part == "addemoji") && i+1 < len(parts) {
					raw = parts[i+1]
					break
				}
			}
		}
	}
	return strings.ToLower(strings.Trim(raw, " /"))
}

func validStickersBotEmoji(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "/") || utf8.RuneCountInString(raw) > 64 {
		return false
	}
	hasEmoji := false
	for _, r := range raw {
		switch {
		case r == 0x200D || r == 0xFE0E || r == 0xFE0F:
			continue
		case r >= 0x1F3FB && r <= 0x1F3FF:
			continue
		case r == 0x20E3:
			hasEmoji = true
		case r == '#' || r == '*' || (r >= '0' && r <= '9'):
			continue
		case r == 0x00A9 || r == 0x00AE || r == 0x3030 || r == 0x303D || r == 0x3297 || r == 0x3299:
			hasEmoji = true
		case r >= 0x2600 && r <= 0x27BF:
			hasEmoji = true
		case r >= 0x1F000 && r <= 0x1FAFF:
			hasEmoji = true
		default:
			return false
		}
	}
	return hasEmoji
}

func (s *Service) stickersBotPublicURL(set domain.StickerSet) string {
	part := "addstickers"
	if stickersBotSetKind(set) == domain.StickerSetKindEmoji {
		part = "addemoji"
	}
	return s.publicURL(part + "/" + set.ShortName)
}

func (s *Service) publicURL(path string) string {
	baseURL := links.DefaultPublicBaseURL
	if s != nil && s.publicBaseURL != "" {
		baseURL = s.publicBaseURL
	}
	return links.Build(baseURL, path, nil)
}
