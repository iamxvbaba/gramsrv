package rpc

import (
	"encoding/json"
	"telesrv/internal/domain"

	"github.com/iamxvbaba/td/tg"
	"sort"
)

func tgLangPackLanguages(items []domain.LangPackLanguage) []tg.LangPackLanguage {
	out := make([]tg.LangPackLanguage, 0, len(items))
	for _, item := range items {
		out = append(out, tg.LangPackLanguage{
			Official:        item.Official,
			Rtl:             item.Rtl,
			Beta:            item.Beta,
			Name:            item.Name,
			NativeName:      item.NativeName,
			LangCode:        item.LangCode,
			BaseLangCode:    item.BaseLangCode,
			PluralCode:      item.PluralCode,
			StringsCount:    item.StringsCount,
			TranslatedCount: item.TranslatedCount,
			TranslationsURL: item.TranslationsURL,
		})
	}
	return out
}

func tgLangPackStrings(items []domain.LangPackString) []tg.LangPackStringClass {
	out := make([]tg.LangPackStringClass, 0, len(items))
	for _, item := range items {
		if item.Deleted {
			out = append(out, &tg.LangPackStringDeleted{Key: item.Key})
			continue
		}
		if item.Pluralized {
			out = append(out, &tg.LangPackStringPluralized{
				Key:        item.Key,
				ZeroValue:  item.ZeroValue,
				OneValue:   item.OneValue,
				TwoValue:   item.TwoValue,
				FewValue:   item.FewValue,
				ManyValue:  item.ManyValue,
				OtherValue: item.OtherValue,
			})
			continue
		}
		out = append(out, &tg.LangPackString{Key: item.Key, Value: item.Value})
	}
	return out
}

func tgPassword(settings domain.PasswordSettings) *tg.AccountPassword {
	if len(settings.SecureRandom) == 0 {
		settings.SecureRandom = []byte("telesrv-tdesktop-dev-secure-rand")
	}
	out := &tg.AccountPassword{
		HasRecovery:             settings.HasRecovery,
		HasSecureValues:         settings.HasSecureValues,
		HasPassword:             settings.HasPassword,
		Hint:                    settings.Hint,
		EmailUnconfirmedPattern: settings.EmailUnconfirmedPattern,
		NewAlgo:                 tgPasswordAlgo(settings.NewAlgo),
		NewSecureAlgo:           tgSecurePasswordAlgo(settings.NewSecureAlgo),
		SecureRandom:            settings.SecureRandom,
		LoginEmailPattern:       settings.LoginEmailPattern,
	}
	if settings.HasPassword && settings.CurrentAlgo != nil {
		out.CurrentAlgo = tgPasswordAlgo(*settings.CurrentAlgo)
		out.SRPB = append([]byte(nil), settings.SRPB...)
		out.SRPID = settings.SRPID
	}
	if settings.PendingResetDate != 0 {
		out.PendingResetDate = settings.PendingResetDate
	}
	return out
}

func tgPasswordAlgo(algo domain.PasswordKDFAlgo) tg.PasswordKdfAlgoClass {
	if len(algo.P) == 0 || algo.G == 0 {
		return &tg.PasswordKdfAlgoUnknown{}
	}
	return &tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow{
		Salt1: append([]byte(nil), algo.Salt1...),
		Salt2: append([]byte(nil), algo.Salt2...),
		G:     algo.G,
		P:     append([]byte(nil), algo.P...),
	}
}

func domainPasswordAlgo(in tg.PasswordKdfAlgoClass) (*domain.PasswordKDFAlgo, bool) {
	if algo, ok := in.(*tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow); ok {
		return &domain.PasswordKDFAlgo{
			Salt1: append([]byte(nil), algo.Salt1...),
			Salt2: append([]byte(nil), algo.Salt2...),
			G:     algo.G,
			P:     append([]byte(nil), algo.P...),
		}, true
	}
	return nil, false
}

func tgSecurePasswordAlgo(algo domain.SecurePasswordKDFAlgo) tg.SecurePasswordKdfAlgoClass {
	if algo.Kind == "pbkdf2_hmac_sha512_iter100000" {
		return &tg.SecurePasswordKdfAlgoPBKDF2HMACSHA512iter100000{Salt: append([]byte(nil), algo.Salt...)}
	}
	return &tg.SecurePasswordKdfAlgoUnknown{}
}

func domainPasswordCheck(in tg.InputCheckPasswordSRPClass) domain.PasswordCheck {
	if srp, ok := in.(*tg.InputCheckPasswordSRP); ok {
		return domain.PasswordCheck{
			SRPID: srp.SRPID,
			A:     append([]byte(nil), srp.A...),
			M1:    append([]byte(nil), srp.M1...),
		}
	}
	return domain.PasswordCheck{Empty: true}
}

func domainPasswordInputSettings(in tg.AccountPasswordInputSettings) (domain.PasswordInputSettings, error) {
	out := domain.PasswordInputSettings{}
	if algo, ok := in.GetNewAlgo(); ok {
		domainAlgo, ok := domainPasswordAlgo(algo)
		if !ok {
			return out, passwordHashInvalidErr()
		}
		out.NewAlgo = domainAlgo
		out.NewPasswordHash = append([]byte(nil), in.NewPasswordHash...)
		out.Hint = in.Hint
		out.HasHint = true
	}
	if email, ok := in.GetEmail(); ok {
		out.Email = email
		out.HasEmail = true
	}
	return out, nil
}

func tgPasswordSettings(settings domain.PrivatePasswordSettings) *tg.AccountPasswordSettings {
	out := &tg.AccountPasswordSettings{}
	if settings.Email != "" {
		out.Email = settings.Email
	}
	return out
}

func tgCountriesList(list domain.CountriesList) tg.HelpCountriesListClass {
	out := &tg.HelpCountriesList{
		Hash:      list.Hash,
		Countries: make([]tg.HelpCountry, 0, len(list.Countries)),
	}
	for _, country := range list.Countries {
		item := tg.HelpCountry{
			Hidden:       country.Hidden,
			ISO2:         country.ISO2,
			DefaultName:  country.DefaultName,
			Name:         country.Name,
			CountryCodes: make([]tg.HelpCountryCode, 0, len(country.CountryCodes)),
		}
		for _, code := range country.CountryCodes {
			item.CountryCodes = append(item.CountryCodes, tg.HelpCountryCode{
				CountryCode: code.CountryCode,
				Prefixes:    code.Prefixes,
				Patterns:    code.Patterns,
			})
		}
		out.Countries = append(out.Countries, item)
	}
	return out
}

func tgJSONValue(data []byte) tg.JSONValueClass {
	if len(data) == 0 {
		return &tg.JSONObject{}
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return &tg.JSONObject{}
	}
	return tgJSON(v)
}

func tgJSON(v any) tg.JSONValueClass {
	switch x := v.(type) {
	case nil:
		return &tg.JSONNull{}
	case bool:
		return &tg.JSONBool{Value: x}
	case float64:
		return &tg.JSONNumber{Value: x}
	case string:
		return &tg.JSONString{Value: x}
	case []any:
		arr := &tg.JSONArray{Value: make([]tg.JSONValueClass, 0, len(x))}
		for _, item := range x {
			arr.Value = append(arr.Value, tgJSON(item))
		}
		return arr
	case map[string]any:
		obj := &tg.JSONObject{Value: make([]tg.JSONObjectValue, 0, len(x))}
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			obj.Value = append(obj.Value, tg.JSONObjectValue{Key: key, Value: tgJSON(x[key])})
		}
		return obj
	default:
		return &tg.JSONString{Value: ""}
	}
}
