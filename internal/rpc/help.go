package rpc

import (
	"context"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/branding"
	androidcompat "telesrv/internal/compat/android"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// registerHelp registers the help.* RPC handlers (DC config, nearest DC).
func (r *Router) registerHelp(d *tlprofile.Dispatcher) {
	registerRPC[*tg.HelpGetConfigRequest](d, tlprofile.SemanticMethodHelpGetConfig, func(ctx context.Context, layerRequest *tg.HelpGetConfigRequest) (any, error) {
		return r.onHelpGetConfig(ctx)
	})
	registerRPC[*tg.HelpGetNearestDCRequest](d, tlprofile.SemanticMethodHelpGetNearestDC, func(ctx context.Context, layerRequest *tg.HelpGetNearestDCRequest) (any, error) {
		return &tg.NearestDC{
			Country:   r.cfg.DefaultCountryCode,
			ThisDC:    r.cfg.DC,
			NearestDC: r.cfg.DC,
		}, nil
	})
	registerRPC[*tg.HelpGetInviteTextRequest](d, tlprofile.SemanticMethodHelpGetInviteText, func(ctx context.Context, layerRequest *tg.HelpGetInviteTextRequest) (any, error) {
		return &tg.HelpInviteText{Message: "Join me on " + branding.ProductName() + "."}, nil
	})
	registerRPC[*tg.HelpSaveAppLogRequest](d, tlprofile.SemanticMethodHelpSaveAppLog, func(ctx context.Context, _ *tg.HelpSaveAppLogRequest) (any, error) {
		return r.onHelpSaveAppLog(ctx)
	})
	registerRPC[*tg.HelpGetAppUpdateRequest](d, tlprofile.SemanticMethodHelpGetAppUpdate, func(ctx context.Context, layerRequest *tg.HelpGetAppUpdateRequest) (any, error) {
		return r.onHelpGetAppUpdate(ctx, layerRequest.Source)
	})
	registerRPC[*tg.HelpGetAppConfigRequest](d, tlprofile.SemanticMethodHelpGetAppConfig, func(ctx context.Context, layerRequest *tg.HelpGetAppConfigRequest) (any, error) {
		hash := layerRequest.Hash

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
		return tdesktop.TimezonesList(layerRequest.Hash), nil
	})
	registerRPC[*tg.HelpGetPeerColorsRequest](d, tlprofile.SemanticMethodHelpGetPeerColors, func(ctx context.Context, layerRequest *tg.HelpGetPeerColorsRequest) (any, error) {
		return tdesktop.PeerColors(layerRequest.Hash), nil
	})
	registerRPC[*tg.HelpGetPeerProfileColorsRequest](d, tlprofile.SemanticMethodHelpGetPeerProfileColors, func(ctx context.Context, layerRequest *tg.HelpGetPeerProfileColorsRequest) (any, error) {
		return tdesktop.PeerProfileColors(layerRequest.Hash), nil
	})
	registerRPC[*tg.HelpGetPromoDataRequest](d, tlprofile.SemanticMethodHelpGetPromoData, func(ctx context.Context, layerRequest *tg.HelpGetPromoDataRequest) (any, error) {
		return tdesktop.PromoData(r.clock.Now()), nil
	})
	registerRPC[*tg.HelpGetTermsOfServiceUpdateRequest](d, tlprofile.SemanticMethodHelpGetTermsOfServiceUpdate, func(ctx context.Context, layerRequest *tg.HelpGetTermsOfServiceUpdateRequest) (any, error) {
		return tdesktop.TermsOfServiceUpdate(r.clock.Now()), nil
	})
	registerRPC[

	// Clients query help.getDeepLinkInfo when they hit an unrecognized tg:// deep
	// link. telesrv keeps no library of special "app update required" deep-link
	// hints and answers every path with deepLinkInfoEmpty -- the canonical "no
	// special info" response: DrKLO only shows its "please update the app" dialog
	// on a non-empty deepLinkInfo (LaunchActivity.java:5175); on Empty it silently
	// passes the link through as an ordinary one. Registering no handler here
	// previously fell through to a 500 NOT_IMPLEMENTED fallback (log noise and
	// incorrect protocol behavior).
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

// onHelpSaveAppLog provides a bounded compatibility response for the official
// clients' fire-and-forget app telemetry. telesrv does not run a telemetry
// product today, so it never reads, records or persists the event payload; the
// request is already bounded by wire/vector/aggregate/depth limits at exact-layer
// admission. Per the TL access constraints this method allows unauthenticated
// connections to call it, but an authenticated bot must be rejected.
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
	config := tdesktop.BuildConfig(r.cfg.DC, r.cfg.IP, r.cfg.Port, r.clock.Now(), r.cfg.PublicBaseURL, r.cfg.UpdatePublicURL)
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

// onHelpDismissSuggestion provides bounded compatibility for DrKLO's suggestion
// cleanup after a successful number change. Android deletes the suggestion from
// local state first and then sends this RPC, and a generic 500 is retried
// continuously by the connection layer. The server publishes no pending
// suggestions today, so a non-empty dismissal needs no persistence; an
// idempotent BoolTrue is the complete current boundary semantics.
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

// onHelpGetPremiumPromo returns the viewer-specific Premium status, the
// immutable startup-seeded video catalog, and the configured Stars storefront
// bot. All six TL fields are mandatory.
func (r *Router) onHelpGetPremiumPromo(ctx context.Context) (*tg.HelpPremiumPromo, error) {
	promo := &tg.HelpPremiumPromo{
		StatusText:     branding.PremiumName() + " is not active on this account.",
		StatusEntities: []tg.MessageEntityClass{},
		VideoSections:  []string{},
		Videos:         []tg.DocumentClass{},
		PeriodOptions:  []tg.PremiumSubscriptionOption{},
		Users:          []tg.UserClass{},
	}
	userID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !authorized || userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	if r.deps.Users == nil {
		return nil, notImplementedErr()
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	if u.ID != userID {
		return nil, internalErr()
	}
	if u.Bot {
		return nil, botMethodInvalidErr()
	}
	if u.PremiumActiveAt(r.clock.Now().Unix()) {
		until := time.Unix(int64(u.PremiumUntil), 0)
		promo.StatusText = branding.PremiumName() + " is active until " + until.Format("2006-01-02") + "."
	}
	if r.deps.Premium != nil {
		username := strings.TrimPrefix(r.deps.Premium.BotUsername(), "@")
		// premiumSubscriptionOption.currency is an ISO 4217 fiat currency. XTR
		// belongs to invoice/payment forms and is not valid here; advertising it
		// made official clients render duplicate Star glyphs and missing-symbol
		// boxes. This deployment is Stars-only, so the promo page deliberately
		// leaves period_options empty and follows the documented
		// premium_bot_username flow. The bot renders the live Stars catalog.
		botID := r.deps.Premium.BotUserID()
		botUsers := r.domainUsersForIDs(ctx, userID, []int64{botID})
		if len(botUsers) == 0 && botID == domain.PremiumBotConfiguredUserID() {
			bot := domain.PremiumBotUser()
			bot.Username = username
			botUsers = []domain.User{bot}
		}
		promo.Users = tgUsersForViewer(userID, botUsers)
	}
	if r.deps.PremiumPromo != nil {
		catalog, found, err := r.deps.PremiumPromo.PremiumPromo(ctx)
		if err != nil {
			return nil, internalErr()
		}
		if found {
			if len(catalog.VideoSections) != len(catalog.Videos) {
				return nil, internalErr()
			}
			promo.VideoSections = append([]string(nil), catalog.VideoSections...)
			promo.Videos = tgDocuments(catalog.Videos)
		}
	}
	return promo, nil
}
