package rpc

import (
	"context"
	"strings"

	"github.com/iamxvbaba/td/tg"

	ioscompat "telesrv/internal/compat/ios"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// onAccountGetThemes returns the built-in default themes (for the emoji preview strip) plus the
// current user's "created ∪ installed" custom cloud themes (cross-device sync: signing in on
// another device with the same account shows the self-created themes). The hash is computed over
// the whole theme set, so a change to the set drives the client to re-fetch.
//
// It branches on req.Format (which is exactly what the official server does):
//   - Android (format="android") treats the is_default themes in account.getThemes as emoji
//     preview-strip material and renders them straight from settings, needing no document (see
//     DrKLO Theme.loadRemoteThemes / MediaDataController.generateEmojiPreviewThemes).
//   - TDesktop (format="tdesktop") treats the account.getThemes result as a "cloud themes" grid
//     and, when applying one, requires the theme to carry a .tdesktop-theme document, otherwise
//     it shows lng_theme_no_desktop "doesn't include a version for Telegram Desktop" (see
//     data_cloud_themes.cpp CloudThemes::showPreview: documentId==0 and not creator -> error).
//
// Our synthesized emoji chat themes have only settings and no document, so they are only suited
// to Android's preview strip. Therefore we do not send these default themes to tdesktop, and
// return only the user's self-created/installed (document-bearing) cloud themes -- which for a
// brand-new account is an empty grid, matching official TDesktop behavior; TDesktop's emoji chat
// themes go through account.getChatThemes' "private-chat theme picker" (settings-based, no
// document needed).
func (r *Router) onAccountGetThemes(ctx context.Context, req *tg.AccountGetThemesRequest) (tg.AccountThemesClass, error) {
	var themes []tg.Theme
	format := ""
	if req != nil {
		format = req.GetFormat()
	}
	if !strings.EqualFold(format, "tdesktop") {
		themes = tdesktop.DefaultThemeList()
	}
	if r.deps.Themes != nil {
		if userID, _, err := r.currentUserID(ctx); err == nil && userID != 0 {
			if userThemes, err := r.deps.Themes.ListForUser(ctx, userID); err == nil {
				for _, t := range userThemes {
					themes = append(themes, *r.tgTheme(ctx, t, userID))
				}
			}
		}
	}
	if ClientTypeFrom(ctx) == ClientTypeIOS {
		themes = ioscompat.ProjectThemes(themes)
	}
	hash, err := themesListHash(themes)
	if err != nil {
		return nil, internalErr()
	}
	if req != nil && req.GetHash() == hash {
		return &tg.AccountThemesNotModified{}, nil
	}
	return &tg.AccountThemes{Hash: hash, Themes: themes}, nil
}

// onAccountGetChatThemes applies the same iOS ARGB projection as account.getThemes.
// The projected content hash is intentionally distinct from the historical
// Android/TDesktop catalog hash, forcing clients with the transparent-color
// response cached to fetch the corrected payload once.
func (r *Router) onAccountGetChatThemes(ctx context.Context, hash int64) (tg.AccountThemesClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if ClientTypeFrom(ctx) != ClientTypeIOS {
		return tdesktop.ChatThemes(hash), nil
	}
	base, ok := tdesktop.ChatThemes(0).(*tg.AccountThemes)
	if !ok {
		return nil, internalErr()
	}
	themes := ioscompat.ProjectThemes(base.Themes)
	projectedHash, err := themesListHash(themes)
	if err != nil {
		return nil, internalErr()
	}
	if hash == projectedHash {
		return &tg.AccountThemesNotModified{}, nil
	}
	return &tg.AccountThemes{Hash: projectedHash, Themes: themes}, nil
}

// onAccountUploadTheme turns a client-uploaded .attheme file into a downloadable Document and
// returns it. It does not create a theme entity -- the client then calls createTheme/updateTheme
// with the returned Document.
func (r *Router) onAccountUploadTheme(ctx context.Context, req *tg.AccountUploadThemeRequest) (tg.DocumentClass, error) {
	if req == nil {
		return nil, tgerr400("THEME_FILE_INVALID")
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	if r.deps.Files == nil {
		return nil, notImplementedErr()
	}
	if req.File == nil {
		return nil, tgerr400("THEME_FILE_INVALID")
	}
	if !strings.HasPrefix(req.MimeType, "application/x-tgtheme-") {
		return nil, tgerr400("THEME_MIME_INVALID")
	}
	ref, ok := uploadedFileRef(userID, req.File)
	if !ok {
		return nil, tgerr400("THEME_FILE_INVALID")
	}
	spec := domain.DocumentSpec{
		MimeType:   req.MimeType,
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: req.FileName}},
	}
	if thumb, ok := req.GetThumb(); ok {
		if tref, ok2 := uploadedFileRef(userID, thumb); ok2 {
			spec.Thumb = &tref
		}
	}
	doc, err := r.deps.Files.CreateDocumentFromUpload(ctx, ref, spec)
	if err != nil {
		return nil, mediaUploadErr(err)
	}
	return tgDocument(doc), nil
}

// onAccountCreateTheme creates a new custom cloud theme. An empty slug is assigned automatically
// by the server; the returned tg.Theme sets creator=true so that the client's next re-upload goes
// through updateTheme rather than create again.
func (r *Router) onAccountCreateTheme(ctx context.Context, req *tg.AccountCreateThemeRequest) (*tg.Theme, error) {
	if req == nil {
		return nil, themeInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	if r.deps.Themes == nil {
		return nil, notImplementedErr()
	}
	spec := domain.ThemeSpec{
		CreatorUserID: userID,
		Slug:          req.Slug,
		Title:         req.Title,
	}
	if doc, ok := req.GetDocument(); ok {
		if d, ok2 := doc.(*tg.InputDocument); ok2 {
			spec.DocumentID = d.ID
		}
	}
	if settings, ok := req.GetSettings(); ok {
		spec.Settings = domainThemeSettingsFromInput(settings)
	}
	t, err := r.deps.Themes.Create(ctx, spec)
	if err != nil {
		return nil, themeErr(err)
	}
	r.invalidateRPCProjectionForUser(userID)
	return projectThemeForClient(ctx, r.tgTheme(ctx, t, userID)), nil
}

// onAccountUpdateTheme updates the creator's own theme (partial fields).
func (r *Router) onAccountUpdateTheme(ctx context.Context, req *tg.AccountUpdateThemeRequest) (*tg.Theme, error) {
	if req == nil {
		return nil, themeInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	if r.deps.Themes == nil {
		return nil, notImplementedErr()
	}
	ref, ok := themeRefFromInput(req.Theme)
	if !ok {
		return nil, themeInvalidErr()
	}
	var upd domain.ThemeUpdate
	if v, ok := req.GetSlug(); ok {
		upd.Slug = &v
	}
	if v, ok := req.GetTitle(); ok {
		upd.Title = &v
	}
	if doc, ok := req.GetDocument(); ok {
		if d, ok2 := doc.(*tg.InputDocument); ok2 {
			id := d.ID
			upd.DocumentID = &id
		}
	}
	if settings, ok := req.GetSettings(); ok {
		s := domainThemeSettingsFromInput(settings)
		upd.Settings = &s
	}
	t, err := r.deps.Themes.Update(ctx, userID, ref, upd)
	if err != nil {
		return nil, themeErr(err)
	}
	r.invalidateRPCProjectionForUser(userID)
	return projectThemeForClient(ctx, r.tgTheme(ctx, t, userID)), nil
}

// onAccountSaveTheme adds/removes a theme from the user's saved list.
func (r *Router) onAccountSaveTheme(ctx context.Context, req *tg.AccountSaveThemeRequest) (bool, error) {
	if req == nil {
		return false, themeInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if userID == 0 {
		return false, authKeyUnregisteredErr()
	}
	if _, ok := tdesktop.LookupDefaultTheme(req.Theme); ok {
		// Default catalog themes are always present in account.getThemes. Saving
		// or unsaving one is therefore an idempotent signal and must not create a
		// synthetic custom-theme row or user install.
		return true, nil
	}
	if r.deps.Themes == nil {
		return false, notImplementedErr()
	}
	ref, ok := themeRefFromInput(req.Theme)
	if !ok {
		return false, themeInvalidErr()
	}
	if req.Unsave {
		err = r.deps.Themes.Unsave(ctx, userID, ref)
	} else {
		err = r.deps.Themes.Save(ctx, userID, ref)
	}
	if err != nil {
		return false, themeErr(err)
	}
	r.invalidateRPCProjectionForUser(userID)
	return true, nil
}

// onAccountInstallTheme applies a theme (count +1, added to the installed list). With no theme
// reference it is a no-op "install local/base theme".
func (r *Router) onAccountInstallTheme(ctx context.Context, req *tg.AccountInstallThemeRequest) (bool, error) {
	if req == nil {
		return false, themeInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if userID == 0 {
		return false, authKeyUnregisteredErr()
	}
	dark := req.GetDark()
	theme, ok := req.GetTheme()
	if !ok {
		return true, nil // no theme reference: base-theme no-op install
	}
	if _, ok := tdesktop.LookupDefaultTheme(theme); ok {
		// The immutable defaults were issued by account.getThemes but do not
		// live in the custom theme store. Applying one is a successful signal;
		// the Android client owns the active day/night choice locally.
		return true, nil
	}
	if r.deps.Themes == nil {
		return false, notImplementedErr()
	}
	ref, ok := themeRefFromInput(theme)
	if !ok {
		return false, themeInvalidErr()
	}
	if err := r.deps.Themes.Install(ctx, userID, ref, dark); err != nil {
		return false, themeErr(err)
	}
	r.invalidateRPCProjectionForUser(userID)
	return true, nil
}

// onAccountGetTheme fetches a theme by id or slug (public addtheme deep link).
func (r *Router) onAccountGetTheme(ctx context.Context, req *tg.AccountGetThemeRequest) (*tg.Theme, error) {
	if req == nil {
		return nil, themeInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if t, ok := tdesktop.LookupDefaultTheme(req.Theme); ok {
		return projectThemeForClient(ctx, &t), nil
	}
	if r.deps.Themes == nil {
		return nil, notImplementedErr()
	}
	ref, ok := themeRefFromInput(req.Theme)
	if !ok {
		return nil, themeInvalidErr()
	}
	t, ok, err := r.deps.Themes.Get(ctx, ref)
	if err != nil {
		return nil, themeErr(err)
	}
	if !ok {
		return nil, themeInvalidErr()
	}
	return projectThemeForClient(ctx, r.tgTheme(ctx, t, userID)), nil
}

func projectThemeForClient(ctx context.Context, theme *tg.Theme) *tg.Theme {
	if theme == nil || ClientTypeFrom(ctx) != ClientTypeIOS {
		return theme
	}
	projected := ioscompat.ProjectThemes([]tg.Theme{*theme})
	if len(projected) == 0 {
		return theme
	}
	return &projected[0]
}
