package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed locales/*.json
var localeFS embed.FS

const DefaultLocale = "en"

type LocaleInfo struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	NativeName string `json:"nativeName"`
}

var supported = []LocaleInfo{
	{Code: "en", Name: "English", NativeName: "English"},
	{Code: "pt", Name: "Portuguese", NativeName: "Português"},
	{Code: "es", Name: "Spanish", NativeName: "Español"},
}

var catalogs = map[string]map[string]string{}

func init() {
	for _, info := range supported {
		raw, err := localeFS.ReadFile("locales/" + info.Code + ".json")
		if err != nil {
			panic("missing locale catalog " + info.Code + ": " + err.Error())
		}
		var nested map[string]any
		if err := json.Unmarshal(raw, &nested); err != nil {
			panic("invalid locale catalog " + info.Code + ": " + err.Error())
		}
		flat := map[string]string{}
		flatten("", nested, flat)
		catalogs[info.Code] = flat
	}
}

func flatten(prefix string, src map[string]any, dst map[string]string) {
	for k, v := range src {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case string:
			dst[key] = val
		case map[string]any:
			flatten(key, val, dst)
		}
	}
}

func SupportedLocales() []LocaleInfo {
	out := make([]LocaleInfo, len(supported))
	copy(out, supported)
	return out
}

func Normalize(tag string) (string, bool) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", true
	}
	normalized := strings.ToLower(strings.ReplaceAll(tag, "_", "-"))
	if _, ok := catalogs[normalized]; ok {
		return normalized, true
	}
	primary, _, _ := strings.Cut(normalized, "-")
	if _, ok := catalogs[primary]; ok {
		return primary, true
	}
	return "", false
}

func Resolve(candidates ...string) string {
	for _, candidate := range candidates {
		if loc, ok := Normalize(candidate); ok && loc != "" {
			return loc
		}
	}
	return DefaultLocale
}

func FromAcceptLanguage(header string) string {
	type weighted struct {
		tag   string
		q     float64
		order int
	}
	var items []weighted
	for i, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, params, _ := strings.Cut(part, ";")
		tag = strings.TrimSpace(tag)
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		for _, param := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = parsed
				}
			}
		}
		if q <= 0 {
			continue
		}
		items = append(items, weighted{tag: tag, q: q, order: i})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].q != items[j].q {
			return items[i].q > items[j].q
		}
		return items[i].order < items[j].order
	})
	for _, item := range items {
		if loc, ok := Normalize(item.tag); ok && loc != "" {
			return loc
		}
	}
	return ""
}

func T(locale, key string, vars ...map[string]string) string {
	loc := Resolve(locale)
	msg, ok := catalogs[loc][key]
	if !ok {
		msg, ok = catalogs[DefaultLocale][key]
	}
	if !ok {
		return key
	}
	if len(vars) == 0 || vars[0] == nil {
		return msg
	}
	pairs := make([]string, 0, len(vars[0])*2)
	for k, v := range vars[0] {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(msg)
}

func FormatDate(locale string, t time.Time) string {
	loc := Resolve(locale)
	weekday := T(loc, fmt.Sprintf("dates.weekdays.%d", int(t.Weekday())))
	month := T(loc, fmt.Sprintf("dates.months.%d", int(t.Month())))
	return T(loc, "dates.dateFormat", map[string]string{
		"weekday": weekday,
		"day":     strconv.Itoa(t.Day()),
		"month":   month,
		"year":    strconv.Itoa(t.Year()),
	})
}

func FormatDateTimeRange(locale string, start, end time.Time) string {
	loc := Resolve(locale)
	weekday := T(loc, fmt.Sprintf("dates.weekdays.%d", int(start.Weekday())))
	month := T(loc, fmt.Sprintf("dates.months.%d", int(start.Month())))
	return T(loc, "dates.dateTimeRangeFormat", map[string]string{
		"weekday":   weekday,
		"day":       strconv.Itoa(start.Day()),
		"month":     month,
		"year":      strconv.Itoa(start.Year()),
		"startTime": start.Format("15:04"),
		"endTime":   end.Format("15:04"),
		"zone":      end.Format("MST"),
	})
}

type EmailMessage struct {
	Subject string
	Text    string
}

func RenderEmail(locale, template string, data map[string]string) EmailMessage {
	loc := Resolve(locale)
	prefix := "email." + template
	return EmailMessage{
		Subject: T(loc, prefix+".subject", data),
		Text:    T(loc, prefix+".body", data),
	}
}
