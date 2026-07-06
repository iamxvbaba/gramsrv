package rpc

import (
	"context"
	"strings"

	"github.com/gotd/td/tg"

	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// onAccountGetThemes 返回内置默认主题(emoji 预览条用)+ 当前用户「创建 ∪ 安装」的
// 自定义云主题(跨设备同步:换设备登录同账号即可看到自建主题)。哈希按整体主题集合计算,
// 集合变化即驱动客户端重取。
//
// 按 req.Format 分流(这正是官方服务端的做法):
//   - Android(format="android")把 account.getThemes 里 is_default 的主题当作 emoji
//     预览条素材,直接从 settings 渲染,不需要 document(见 DrKLO
//     Theme.loadRemoteThemes / MediaDataController.generateEmojiPreviewThemes)。
//   - TDesktop(format="tdesktop")把 account.getThemes 的结果当作「云主题」网格,点击
//     应用时要求主题携带 .tdesktop-theme document,否则弹 lng_theme_no_desktop
//     「doesn't include a version for Telegram Desktop」(见 data_cloud_themes.cpp
//     CloudThemes::showPreview:documentId==0 且非 creator → 报错)。
//
// 我们合成的 emoji 聊天主题只有 settings、没有 document,只适合 Android 的预览条。因此对
// tdesktop 不下发这些默认主题,只返回用户自建/安装(带 document)的云主题——对全新账号即
// 空网格,与官方 TDesktop 行为一致;TDesktop 的 emoji 聊天主题走 account.getChatThemes
// 的「单聊主题选择器」(基于 settings,无需 document)。
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
	hash := themesListHash(themes)
	if req != nil && req.GetHash() == hash {
		return &tg.AccountThemesNotModified{}, nil
	}
	return &tg.AccountThemes{Hash: hash, Themes: themes}, nil
}

// onAccountUploadTheme 把客户端上传的 .attheme 文件落成可下载的 Document 并返回。
// 它不创建主题实体——客户端随后用返回的 Document 调 createTheme/updateTheme。
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

// onAccountCreateTheme 创建一份新的自定义云主题。空 slug 由服务端自动分配;返回的
// tg.Theme 设 creator=true,使客户端下次重传走 updateTheme 而非再次 create。
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
	return r.tgTheme(ctx, t, userID), nil
}

// onAccountUpdateTheme 更新创建者自己的主题(部分字段)。
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
	return r.tgTheme(ctx, t, userID), nil
}

// onAccountSaveTheme 把主题加入/移出用户的已存列表。
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

// onAccountInstallTheme 应用主题(计数 +1,加入已安装列表)。无 theme 引用时为
// 「安装本地/基础主题」的 no-op。
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
	if r.deps.Themes == nil {
		return false, notImplementedErr()
	}
	dark := req.GetDark()
	theme, ok := req.GetTheme()
	if !ok {
		return true, nil // 无 theme 引用:基础主题 no-op 安装
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

// onAccountGetTheme 按 id 或 slug(公开 addtheme 深链)取主题。
func (r *Router) onAccountGetTheme(ctx context.Context, req *tg.AccountGetThemeRequest) (*tg.Theme, error) {
	if req == nil {
		return nil, themeInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
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
	return r.tgTheme(ctx, t, userID), nil
}
