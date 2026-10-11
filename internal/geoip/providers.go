package geoip

import (
	"encoding/json"
	"strings"
)

// 本文件固定了四个免费后端的响应字段。字段形状都来自 2026-09 的实测响应体,不是
// 文档抄来的——这几个服务的实际字段和文档并不总是一致(例如 HackMyIP 的
// location.country 是两位国家码,country_name 才是国名)。
//
// 共同约定:country_name 为空就算"没查到"(parseNotFound),由上层继续问下一个后端。
// 不拿别的字段硬凑一个国家名:时区推不出国家,拼出来的只会是错的信息。

// reallyFreeGeoIPResponse 是 reallyfreegeoip.org /json/{ip} 的响应。
//
// country_name/city 为空是常态而非异常:实测 1.1.1.1 连 country_name 都是空的。
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
	// 城市最贴近用户看到的"哪里",其次州/省,最后退到时区。俄罗斯 IP 常有经纬度却
	// 没有 city,这时 Europe/Moscow 至少指出了大致区域。
	return Location{
		Country: country,
		Region:  firstNonEmpty(payload.City, payload.RegionName, payload.TimeZone),
	}, parseOK
}

// geoJSResponse 是 get.geojs.io /v1/ip/geo/{ip}.json 的响应。
//
// 它只有 ASN 库级别的覆盖:8.8.8.8 有 country 和 timezone,1.1.1.1 只有 asn 和
// organization,连 country 都没有。所以它排在链路里当主力通常不合适,当兜底很合适。
// latitude/longitude 是字符串,没数据时值是字面量 "nil"。
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
	// 没有任何行政区字段,时区是唯一能给"更细一级位置"的东西。
	return Location{Country: country, Region: trimmedOrEmpty(payload.Timezone)}, parseOK
}

// hackMyIPResponse 是 hackmyip.com /api/lookup?ip={ip} 的响应。
//
// 外层 success=false 是服务自报故障(实测间歇性出现 400 +
// "IP lookup service temporarily unavailable"),那属于不可达而不是"没这条 IP"。
// 限流头:x-ratelimit-limit: 100 / x-ratelimit-window: 60。
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
	// location.country 是 "AU" 这样的两位码,必须用 country_name。
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

// ipapiISResponse 是 api.ipapi.is /?q={ip} 的响应。免费额度下不带 key 也能用。
//
// is_bogon=true 表示这个地址根本没有地理归属(实测 203.0.113.7 就是:全字段为 null)。
// 那属于确定的"没有",不该继续问下一个后端,更不该记负缓存之外的东西。
type ipapiISResponse struct {
	IP       string   `json:"ip"`
	IsBogon  bool     `json:"is_bogon"`
	Company  *string  `json:"company"`
	ASN      *string  `json:"asn"`
	City     *string  `json:"city"`
	Region   *string  `json:"region"`
	Country  *string  `json:"country"`
	Lat      *float64 `json:"lat"`
	Lon      *float64 `json:"lon"`
	Timezone *string  `json:"timezone"`
}

func parseIPAPIS(body []byte) (Location, parseOutcome) {
	var payload ipapiISResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return Location{}, parseFailed
	}
	if payload.IsBogon {
		return Location{}, parseNotFound
	}
	country := trimmedOrEmpty(derefString(payload.Country))
	if country == "" {
		return Location{}, parseNotFound
	}
	return Location{
		Country: country,
		Region: firstNonEmpty(
			derefString(payload.City),
			derefString(payload.Region),
			derefString(payload.Timezone),
		),
	}, parseOK
}

// genericJSONFieldAliases 是通用解析按优先级尝试的字段名。覆盖上面四个后端以及常见
// 的自建代理(如各类 MaxMind/GeoIP2 HTTP 代理)的字段命名习惯。
var (
	genericCountryFields = []string{
		"country_name", "countryName", "country", "nation", "country_code",
	}
	genericRegionFields = []string{
		"city", "city_name", "cityName", "region", "region_name", "regionName",
		"state", "state_name", "province", "timezone", "time_zone", "timeZone",
	}
	// genericNestedKeys 是常见的包裹层。先下钻一层再取字段,这样 HackMyIP 那类
	// {"data":{"location":{...}}} 的结构也能被通用解析认出来。
	genericNestedKeys = []string{"data", "location", "result", "ip"}
)

// parseGenericJSON 面向识别不出的主机(自建 MaxMind 代理、私有镜像等)。
//
// 策略:把 JSON 摊平成一张字段表,按优先级取第一个非空的国家名;找不到就明确
// parseNotFound,让上层去问下一个后端,而不是在这里猜。
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

// genericScalar 把摊平后的标量转成字符串,供通用字段名匹配使用。
func genericScalar(value any) string {
	return trimmedOrEmpty(jsonScalarToString(value))
}

// flattenJSONFields 把嵌套对象摊平成一张"点号路径 -> 原始值"的表,只下钻有限层数。
// 深度上限是为了防住构造出来的深层 JSON 把解析拖成栈深递归。
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

// lookupField 取字段值,并额外支持按点号后缀匹配(先精确,再取最后一段)。
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

// jsonScalarToString 把摊平后的任意标量转成可展示的字符串。
//
// geojs 的 latitude 是字符串 "nil"(未命中时),ipapi.is 的字段是 null,这些都必须
// 当成"没有",否则界面上会直接显示 "nil"。
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
	// 上游用 "nil"/"null"/"N/A" 表达缺失,这些不能被当成地名显示给用户。
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

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
