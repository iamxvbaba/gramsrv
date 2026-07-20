package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/branding"
	androidcompat "telesrv/internal/compat/android"
	ioscompat "telesrv/internal/compat/ios"
	"telesrv/internal/compat/tdesktop"
)

// registerHelp 注册 help.* RPC handler（DC 配置、最近 DC）。
func (r *Router) registerHelp(d *tlprofile.Dispatcher) {
	registerRPC[*tg.HelpGetConfigRequest](d, tlprofile.SemanticMethodHelpGetConfig, func(ctx context.Context, layerRequest *tg.HelpGetConfigRequest) (any, error) {
		return r.onHelpGetConfig(ctx)
	})
	registerRPC[*tg.HelpGetNearestDCRequest](d, tlprofile.SemanticMethodHelpGetNearestDC, func(ctx context.Context, layerRequest *tg.HelpGetNearestDCRequest) (any, error) {
		return tdesktop.NearestDC(r.cfg.DC), nil
	})
	registerRPC[*tg.HelpGetInviteTextRequest](d, tlprofile.SemanticMethodHelpGetInviteText, func(ctx context.Context, layerRequest *tg.HelpGetInviteTextRequest) (any, error) {
		return &tg.HelpInviteText{Message: "Join me on " + branding.ProductName + "."}, nil
	})
	registerRPC[*tg.HelpSaveAppLogRequest](d, tlprofile.SemanticMethodHelpSaveAppLog, func(ctx context.Context, _ *tg.HelpSaveAppLogRequest) (any, error) {
		return r.onHelpSaveAppLog(ctx)
	})
	registerRPC[*tg.HelpGetAppUpdateRequest](d, tlprofile.SemanticMethodHelpGetAppUpdate, func(ctx context.Context, layerRequest *tg.HelpGetAppUpdateRequest) (any, error) {
		source := layerRequest.
			Source
		_ = source

		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		return ioscompat.NoAppUpdate(), nil
	})
	registerRPC[*tg.HelpGetAppConfigRequest](d, tlprofile.SemanticMethodHelpGetAppConfig, func(ctx context.Context, layerRequest *tg.HelpGetAppConfigRequest) (any, error) {
		hash := layerRequest.
			Hash
		_ = hash

		if r.deps.Help == nil {
			return tdesktop.AppConfig(hash), nil
		}
		userID, _ := UserIDFrom(ctx)
		cfg, notModified, err := r.deps.Help.GetAppConfig(ctx, userID, hash)
		if err != nil {
			return nil, internalErr()
		}
		if notModified {
			return &tg.HelpAppConfigNotModified{}, nil
		}
		return &tg.HelpAppConfig{Hash: cfg.Hash, Config: tgJSONValue(cfg.JSON)}, nil
	})
	registerRPC[*tg.HelpGetCountriesListRequest](d, tlprofile.SemanticMethodHelpGetCountriesList, func(ctx context.Context, req *tg.HelpGetCountriesListRequest) (any, error) {
		if r.deps.Help == nil {
			return tdesktop.CountriesList(req.Hash), nil
		}
		list, notModified, err := r.deps.Help.GetCountries(ctx, req.LangCode, req.Hash)
		if err != nil {
			return nil, internalErr()
		}
		if notModified {
			return &tg.HelpCountriesListNotModified{}, nil
		}
		return tgCountriesList(list), nil
	})
	registerRPC[*tg.HelpGetTimezonesListRequest](d, tlprofile.SemanticMethodHelpGetTimezonesList, func(ctx context.Context, layerRequest *tg.HelpGetTimezonesListRequest) (any, error) {
		hash := layerRequest.
			Hash
		_ = hash

		return tdesktop.TimezonesList(hash), nil
	})
	registerRPC[*tg.HelpGetPeerColorsRequest](d, tlprofile.SemanticMethodHelpGetPeerColors, func(ctx context.Context, layerRequest *tg.HelpGetPeerColorsRequest) (any, error) {
		hash := layerRequest.
			Hash
		_ = hash

		return tdesktop.PeerColors(hash), nil
	})
	registerRPC[*tg.HelpGetPeerProfileColorsRequest](d, tlprofile.SemanticMethodHelpGetPeerProfileColors, func(ctx context.Context, layerRequest *tg.HelpGetPeerProfileColorsRequest) (any, error) {
		hash := layerRequest.
			Hash
		_ = hash

		return tdesktop.PeerProfileColors(hash), nil
	})
	registerRPC[*tg.HelpGetPromoDataRequest](d, tlprofile.SemanticMethodHelpGetPromoData, func(ctx context.Context, layerRequest *tg.HelpGetPromoDataRequest) (any, error) {
		return tdesktop.PromoData(r.clock.Now()), nil
	})
	registerRPC[*tg.HelpGetTermsOfServiceUpdateRequest](d, tlprofile.SemanticMethodHelpGetTermsOfServiceUpdate, func(ctx context.Context, layerRequest *tg.HelpGetTermsOfServiceUpdateRequest) (any, error) {
		return tdesktop.TermsOfServiceUpdate(r.clock.Now()), nil
	})
	registerRPC[

	// 客户端遇到无法识别的 tg:// 深链时会查询 help.getDeepLinkInfo。telesrv 不维护
	// “需更新 App”的特殊深链提示库，对所有 path 返回 deepLinkInfoEmpty——这是规范的
	// “无特殊信息”应答：DrKLO 仅在收到非空 deepLinkInfo 时才弹“请更新 App”弹窗
	// （LaunchActivity.java:5175），收到 Empty 则静默放行按普通链接处理。此前未注册
	// handler 会落 fallback 返回 500 NOT_IMPLEMENTED（污染日志且非正确协议行为）。
	*tg.HelpGetDeepLinkInfoRequest](d, tlprofile.SemanticMethodHelpGetDeepLinkInfo, func(ctx context.Context, layerRequest *tg.HelpGetDeepLinkInfoRequest) (any, error) {
		path := layerRequest.
			Path
		_ = path

		return &tg.HelpDeepLinkInfoEmpty{}, nil
	})
	registerRPC[*tg.HelpDismissSuggestionRequest](d, tlprofile.SemanticMethodHelpDismissSuggestion, func(ctx context.Context, layerRequest *tg.HelpDismissSuggestionRequest) (any, error) {
		return r.onHelpDismissSuggestion(ctx, layerRequest)
	})
	registerRPC[*tg.HelpGetPremiumPromoRequest](d, tlprofile.SemanticMethodHelpGetPremiumPromo, func(ctx context.Context, layerRequest *tg.HelpGetPremiumPromoRequest) (any, error) {
		return r.onHelpGetPremiumPromo(ctx)
	})
}

// onHelpSaveAppLog 为官方客户端的 fire-and-forget 应用遥测提供有界兼容应答。
// telesrv 当前不运营遥测产品，因此不读取、记录或持久化事件内容；请求已在 exact
// Layer admission 处受 wire/vector/aggregate/depth 限制。该方法按 TL 访问约束允许
// 未授权连接调用，但已登录 bot 必须拒绝。
func (r *Router) onHelpSaveAppLog(ctx context.Context) (bool, error) {
	userID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if authorized && r.userIsBot(ctx, userID) {
		return false, botMethodInvalidErr()
	}
	return true, nil
}

func (r *Router) onHelpGetConfig(ctx context.Context) (*tg.Config, error) {
	config := tdesktop.BuildConfig(r.cfg.DC, r.cfg.IP, r.cfg.Port, r.clock.Now(), r.cfg.PublicBaseURL)
	userID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !authorized || userID == 0 {
		return config, nil
	}
	if svc, ok := r.deps.Account.(accountReactionSettingsReader); ok {
		settings, err := svc.GetReactionSettings(ctx, userID)
		if err != nil {
			return nil, internalErr()
		}
		reaction := tgMessageReaction(settings.DefaultReaction)
		if reaction == nil {
			return nil, internalErr()
		}
		config.SetReactionsDefault(reaction)
	}
	return config, nil
}

// onHelpDismissSuggestion 为 DrKLO 改号成功后的 suggestion 清理提供有界兼容。
// Android 会先把 suggestion 从本地状态删除，再发送该 RPC，且 generic 500 会被
// 连接层持续重试。当前 server 不发布 pending suggestions，故非空 dismissal
// 无需持久化，幂等 BoolTrue 即为完整的当前边界语义。
func (r *Router) onHelpDismissSuggestion(ctx context.Context, req *tg.HelpDismissSuggestionRequest) (bool, error) {
	userID, found, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if !found {
		return false, authKeyUnregisteredErr()
	}
	if r.userIsBot(ctx, userID) {
		return false, botMethodInvalidErr()
	}
	if req == nil {
		return false, nil
	}
	return androidcompat.DismissSuggestion(req.Suggestion), nil
}

// onHelpGetPremiumPromo 返回最小真实的 Premium 状态页数据：状态文案按 viewer
// 的会员有效期生成；videos/period_options 留空——购买入口已被 appConfig
// premium_purchase_blocked=true 关闭，订阅价格 UI 不会消费这些字段（TDesktop
// 空 period_options 仅隐藏价格按钮，DrKLO 回退到无价文案，均不报错）。
// 六个字段全是 TL 必填项，空值也必须给出空集合而非缺失。
func (r *Router) onHelpGetPremiumPromo(ctx context.Context) (*tg.HelpPremiumPromo, error) {
	promo := &tg.HelpPremiumPromo{
		StatusText:     branding.PremiumName + " is not active on this account.",
		StatusEntities: []tg.MessageEntityClass{},
		VideoSections:  []string{},
		Videos:         []tg.DocumentClass{},
		PeriodOptions:  []tg.PremiumSubscriptionOption{},
		Users:          []tg.UserClass{},
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || r.deps.Users == nil {
		return promo, nil
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return promo, nil
	}
	if u.PremiumActiveAt(r.clock.Now().Unix()) {
		until := time.Unix(int64(u.PremiumUntil), 0)
		promo.StatusText = branding.PremiumName + " is active until " + until.Format("2006-01-02") + "."
	}
	return promo, nil
}
