package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"

	androidcompat "telesrv/internal/compat/android"
	ioscompat "telesrv/internal/compat/ios"
	"telesrv/internal/compat/tdesktop"
)

// registerHelp 注册 help.* RPC handler（DC 配置、最近 DC）。
func (r *Router) registerHelp(d *tg.ServerDispatcher) {
	d.OnHelpGetConfig(r.onHelpGetConfig)
	d.OnHelpGetNearestDC(func(ctx context.Context) (*tg.NearestDC, error) {
		return tdesktop.NearestDC(r.cfg.DC), nil
	})
	d.OnHelpGetInviteText(func(ctx context.Context) (*tg.HelpInviteText, error) {
		return &tg.HelpInviteText{Message: "Join me on Telegram."}, nil
	})
	d.OnHelpGetAppUpdate(func(ctx context.Context, source string) (tg.HelpAppUpdateClass, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		return ioscompat.NoAppUpdate(), nil
	})
	d.OnHelpGetAppConfig(func(ctx context.Context, hash int) (tg.HelpAppConfigClass, error) {
		if r.deps.Help == nil {
			return tdesktop.AppConfig(hash), nil
		}
		cfg, notModified, err := r.deps.Help.GetAppConfig(ctx, hash)
		if err != nil {
			return nil, internalErr()
		}
		if notModified {
			return &tg.HelpAppConfigNotModified{}, nil
		}
		return &tg.HelpAppConfig{Hash: cfg.Hash, Config: tgJSONValue(cfg.JSON)}, nil
	})
	d.OnHelpGetCountriesList(func(ctx context.Context, req *tg.HelpGetCountriesListRequest) (tg.HelpCountriesListClass, error) {
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
	d.OnHelpGetTimezonesList(func(ctx context.Context, hash int) (tg.HelpTimezonesListClass, error) {
		return tdesktop.TimezonesList(hash), nil
	})
	d.OnHelpGetPeerColors(func(ctx context.Context, hash int) (tg.HelpPeerColorsClass, error) {
		return tdesktop.PeerColors(hash), nil
	})
	d.OnHelpGetPeerProfileColors(func(ctx context.Context, hash int) (tg.HelpPeerColorsClass, error) {
		return tdesktop.PeerProfileColors(hash), nil
	})
	d.OnHelpGetPromoData(func(ctx context.Context) (tg.HelpPromoDataClass, error) {
		return tdesktop.PromoData(r.clock.Now()), nil
	})
	d.OnHelpGetTermsOfServiceUpdate(func(ctx context.Context) (tg.HelpTermsOfServiceUpdateClass, error) {
		return tdesktop.TermsOfServiceUpdate(r.clock.Now()), nil
	})
	// 客户端遇到无法识别的 tg:// 深链时会查询 help.getDeepLinkInfo。telesrv 不维护
	// “需更新 App”的特殊深链提示库，对所有 path 返回 deepLinkInfoEmpty——这是规范的
	// “无特殊信息”应答：DrKLO 仅在收到非空 deepLinkInfo 时才弹“请更新 App”弹窗
	// （LaunchActivity.java:5175），收到 Empty 则静默放行按普通链接处理。此前未注册
	// handler 会落 fallback 返回 500 NOT_IMPLEMENTED（污染日志且非正确协议行为）。
	d.OnHelpGetDeepLinkInfo(func(ctx context.Context, path string) (tg.HelpDeepLinkInfoClass, error) {
		return &tg.HelpDeepLinkInfoEmpty{}, nil
	})
	d.OnHelpDismissSuggestion(r.onHelpDismissSuggestion)
	d.OnHelpGetPremiumPromo(r.onHelpGetPremiumPromo)
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
		StatusText:     "Telegram Premium is not active on this account.",
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
		promo.StatusText = "Telegram Premium is active until " + until.Format("2006-01-02") + "."
	}
	return promo, nil
}
