package geoip

import (
	"encoding/json"
	"strings"
)

// This file fixes the response fields of the four free backends. The field
// shapes all come from measured 2026-09 response bodies, not from documentation
// -- these services' actual fields and their docs do not always agree (for
// example HackMyIP's location.country is a two-letter country code while
// country_name is the actual country name).
//
// Common convention: an empty country_name counts as "not found"
// (parseNotFound) and the caller continues to the next backend. No other field
// is stretched into a country name: a timezone cannot imply a country, and any
// such guess would only be wrong information.

// reallyFreeGeoIPResponse is the response of reallyfreegeoip.org /json/{ip}.
//
// An empty country_name/city is the norm, not an anomaly: measured 1.1.1.1 has
// not even a country_name.
type reallyFreeGeoIPResponse struct {
	IP          string  `json:"ip"`
	CountryCode string  `json:"country_code"`
	CountryName string  `json:"country_name"`
	RegionCode  string  `json:"region_code"`
	RegionName  string  `json:"region_name"`
	City        string  `json:"city"`
	ZipCode     string  `json:"zip_code"`
	TimeZone    string  `json:"time_zone"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	MetroCode   int     `json:"metro_code"`
}

func parseReallyFreeGeoIP(body []byte) (Location, parseOutcome) {
	var payload reallyFreeGeoIPResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return Location{}, parseFailed
	}
	country := trimmedOrEmpty(payload.CountryName)
	if country == "" {
		return Location{}, parseNotFound
	}
	// The city is closest to the "where" a user sees, then state/province, and
	// finally the timezone. Russian IPs often have lat/long but no city; there
	// Europe/Moscow at least indicates the general region.
	return Location{
		Country: country,
		Region:  firstNonEmpty(payload.City, payload.RegionName, payload.TimeZone),
	}, parseOK
}

// geoJSResponse is the response of get.geojs.io /v1/ip/geo/{ip}.json.
//
// It only has ASN-database coverage: 8.8.8.8 has country and timezone, while
// 1.1.1.1 has only asn and organization, not even a country. So it is usually a
// poor primary in the chain but a good fallback. latitude/longitude are strings,
// and the literal value "nil" when there is no data.
type geoJSResponse struct {
	Accuracy         any    `json:"accuracy"`
	AreaCode         string `json:"area_code"`
	ASN              int    `json:"asn"`
	ContinentCode    string `json:"continent_code"`
	Country          string `json:"country"`
	CountryCode      string `json:"country_code"`
	CountryCode3     string `json:"country_code3"`
	IP               string `json:"ip"`
	Latitude         string `json:"latitude"`
	Longitude        string `json:"longitude"`
	Organization     string `json:"organization"`
	OrganizationName string `json:"organization_name"`
	Timezone         string `json:"timezone"`
}

func parseGeoJS(body []byte) (Location, parseOutcome) {
	var payload geoJSResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return Location{}, parseFailed
	}
	country := trimmedOrEmpty(payload.Country)
	if country == "" {
		return Location{}, parseNotFound
	}
	// There are no administrative-division fields, so the timezone is the only
	// thing that can provide a "finer location".
	return Location{Country: country, Region: trimmedOrEmpty(payload.Timezone)}, parseOK
}

// hackMyIPResponse is the response of hackmyip.com /api/lookup?ip={ip}.
//
// An outer success=false is a self-reported service failure (measured:
// intermittent 400 + "IP lookup service temporarily unavailable"), which is an
// unreachability case rather than "no such IP". Rate-limit headers:
// x-ratelimit-limit: 100 / x-ratelimit-window: 60.
type hackMyIPResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Data    struct {
		IP       string `json:"ip"`
		Location struct {
			City        string  `json:"city"`
			Region      string  `json:"region"`
			Country     string  `json:"country"`
			CountryName string  `json:"country_name"`
			Latitude    float64 `json:"latitude"`
			Longitude   float64 `json:"longitude"`
			Timezone    string  `json:"timezone"`
			PostalCode  string  `json:"postal_code"`
		} `json:"location"`
	} `json:"data"`
}

func parseHackMyIP(body []byte) (Location, parseOutcome) {
	var payload hackMyIPResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return Location{}, parseFailed
	}
	if !payload.Success {
		return Location{}, parseFailed
	}
	// location.country is a two-letter code like "AU", so country_name must be used.
	country := firstNonEmpty(payload.Data.Location.CountryName, payload.Data.Location.Country)
	if country == "" {
		return Location{}, parseNotFound
	}
	return Location{
		Country: country,
		Region: firstNonEmpty(
			payload.Data.Location.City,
			payload.Data.Location.Region,
			payload.Data.Location.Timezone,
		),
	}, parseOK
}

// ipapiISResponse covers both documented tiers: anonymous responses are flat;
// keyed responses put geolocation under location and company/asn are objects.
// https://ipapi.is/developers.html
//
// is_bogon=true means the address has no geographic identity at all (measured
// 203.0.113.7: every field is null). That is a definite "none", and the caller
// should not keep asking the next backend, much less record anything beyond a
// negative cache entry.
type ipapiISResponse struct {
	IsBogon  bool   `json:"is_bogon"`
	Error    string `json:"error"`
	City     string `json:"city"`
	Region   string `json:"region"`
	Country  string `json:"country"`
	Timezone string `json:"timezone"`
	Location *struct {
		Country  string `json:"country"`
		City     string `json:"city"`
		State    string `json:"state"`
		Timezone string `json:"timezone"`
	} `json:"location"`
}

func parseIPAPIS(body []byte) (Location, parseOutcome) {
	var payload ipapiISResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return Location{}, parseFailed
	}
	if payload.Error != "" {
		return Location{}, parseFailed
	}
	if payload.IsBogon {
		return Location{}, parseNotFound
	}
	loc := Location{
		Country: trimmedOrEmpty(payload.Country),
		Region:  firstNonEmpty(payload.City, payload.Region, payload.Timezone),
	}
	if payload.Location != nil {
		loc = Location{
			Country: trimmedOrEmpty(payload.Location.Country),
			Region:  firstNonEmpty(payload.Location.City, payload.Location.State, payload.Location.Timezone),
		}
	}
	if loc.Country == "" {
		return Location{}, parseNotFound
	}
	return loc, parseOK
}

// genericJSONFieldAliases are the field names generic parsing tries in priority
// order. They cover the four backends above plus the field-naming habits of
// common self-hosted proxies (such as various MaxMind/GeoIP2 HTTP proxies).
var (
	genericCountryFields = []string{
		"country_name", "countryName", "country", "nation", "country_code",
	}
	genericRegionFields = []string{
		"city", "city_name", "cityName", "region", "region_name", "regionName",
		"state", "state_name", "province", "timezone", "time_zone", "timeZone",
	}
	// genericNestedKeys are the common wrapper layers. Drilling down one level
	// before reading fields lets generic parsing recognize structures like
	// HackMyIP's {"data":{"location":{...}}}.
	genericNestedKeys = []string{"data", "location", "result", "ip"}
)

// parseGenericJSON targets unrecognized hosts (self-hosted MaxMind proxies,
// private mirrors, etc.).
//
// Strategy: flatten the JSON into a field table and take the first non-empty
// country name by priority; if none is found, return parseNotFound explicitly and
// let the caller ask the next backend instead of guessing here.
func parseGenericJSON(body []byte) (Location, parseOutcome) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return Location{}, parseFailed
	}
	fields := flattenJSONFields(root)
	country := ""
	for _, key := range genericCountryFields {
		if value := trimmedOrEmpty(genericScalar(lookupField(fields, key))); value != "" {
			country = value
			break
		}
	}
	if country == "" {
		return Location{}, parseNotFound
	}
	region := ""
	for _, key := range genericRegionFields {
		if value := trimmedOrEmpty(genericScalar(lookupField(fields, key))); value != "" {
			region = value
			break
		}
	}
	return Location{Country: country, Region: region}, parseOK
}

// genericScalar converts a flattened scalar into a string for generic field-name matching.
func genericScalar(value any) string {
	return trimmedOrEmpty(jsonScalarToString(value))
}

// flattenJSONFields flattens nested objects into a "dotted path -> raw value"
// table, drilling down only a limited number of levels. The depth cap prevents
// crafted deep JSON from dragging parsing into deep stack recursion.
func flattenJSONFields(root map[string]any) map[string]any {
	out := make(map[string]any, len(root))
	var walk func(prefix string, node map[string]any, depth int)
	walk = func(prefix string, node map[string]any, depth int) {
		for key, value := range node {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			out[path] = value
			if child, ok := value.(map[string]any); ok && depth < 2 {
				walk(path, child, depth+1)
			}
		}
	}
	walk("", root, 0)
	return out
}

// lookupField reads a field value, additionally supporting dotted-suffix matching (exact first, then the last segment).
func lookupField(fields map[string]any, key string) any {
	if value, ok := fields[key]; ok {
		return value
	}
	for path, value := range fields {
		if strings.HasSuffix(path, "."+key) {
			return value
		}
	}
	return nil
}

// jsonScalarToString converts any flattened scalar into a displayable string.
//
// geojs's latitude is the string "nil" (on a miss) and ipapi.is's fields are
// null; these must all be treated as "absent", otherwise the UI would literally
// display "nil".
func jsonScalarToString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64, bool, int, int64:
		return strings.TrimSpace(strings.Trim(jsonNumber(typed), `"`))
	default:
		return ""
	}
}

func jsonNumber(value any) string {
	switch typed := value.(type) {
	case float64:
		return strings.TrimRight(strings.TrimRight(formatFloat(typed), "0"), ".")
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func formatFloat(value float64) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func trimmedOrEmpty(value string) string {
	// Upstreams express absence as "nil"/"null"/"N/A"; these must not be shown to users as place names.
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "nil", "null", "n/a", "na", "none", "unknown":
		return ""
	default:
		return trimmed
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := trimmedOrEmpty(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
