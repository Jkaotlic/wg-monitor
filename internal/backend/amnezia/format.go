package amnezia

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func FormatAccountSummary(nickname string, info *AccountInfo) string {
	if info == nil {
		return "Amnezia Premium: нет данных кабинета"
	}
	free := info.MaxDeviceCount - info.ActiveDeviceCount
	if free < 0 {
		free = 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Amnezia Premium - %s\n\n", nickname)
	fmt.Fprintf(&b, "Статус: %s\n", humanStatus(info.SubscriptionStatus))
	if end := formatDate(info.SubscriptionEndDate); end != "" {
		fmt.Fprintf(&b, "Оплачен до: %s\n", end)
	}
	fmt.Fprintf(&b, "Устройства: занято %d/%d, свободно %d\n", info.ActiveDeviceCount, info.MaxDeviceCount, free)
	fmt.Fprintf(&b, "Локаций: %d\n", len(info.AvailableCountries))

	countryConfigs, devices := splitIssued(info.IssuedConfigs)
	fmt.Fprintf(&b, "\nКонфиги по странам: %d\n", len(countryConfigs))
	for _, c := range countryConfigs {
		fmt.Fprintf(&b, "- %s (%s), скачан %s\n", countryName(c.CountryCode, c.CountryName), strings.ToUpper(c.CountryCode), fallbackDate(c.LastDownloaded))
	}
	fmt.Fprintf(&b, "\nУстройства по ключу: %d\n", len(devices))
	for _, d := range devices {
		fmt.Fprintf(&b, "- %s / %s, обновлено %s\n", countryName(d.CountryCode, d.CountryName), d.OSVersion, fallbackDate(d.LastDownloaded))
	}
	return strings.TrimSpace(b.String())
}

func FreeCountries(info *AccountInfo) []Country {
	if info == nil {
		return nil
	}
	issued := map[string]bool{}
	for _, c := range info.IssuedConfigs {
		if c.SourceType == "country_config" {
			issued[strings.ToLower(c.CountryCode)] = true
		}
	}
	var out []Country
	for _, c := range info.AvailableCountries {
		code := strings.ToLower(c.Code)
		if code == "" || issued[code] {
			continue
		}
		out = append(out, Country{Code: code, Name: c.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

func splitIssued(items []IssuedConfig) (countryConfigs, devices []IssuedConfig) {
	for _, item := range items {
		switch item.SourceType {
		case "country_config":
			countryConfigs = append(countryConfigs, item)
		case "gateway_account":
			devices = append(devices, item)
		}
	}
	return countryConfigs, devices
}

func humanStatus(s string) string {
	switch s {
	case "active":
		return "активна"
	case "expired":
		return "истекла"
	case "expireSoon":
		return "скоро истечет"
	case "":
		return "неизвестно"
	default:
		return s
	}
}

func fallbackDate(s string) string {
	if got := formatDate(s); got != "" {
		return got
	}
	return "неизвестно"
}

func formatDate(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Format("2006-01-02 15:04 UTC")
	}
	return s
}

func countryName(code, name string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return strings.ToUpper(code)
}
