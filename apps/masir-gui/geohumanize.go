package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

var geoAcronyms = map[string]string{
	"ai": "AI", "vpn": "VPN", "dns": "DNS", "doh": "DoH", "ddns": "DDNS",
	"cdn": "CDN", "ntp": "NTP", "tld": "TLD", "api": "API", "ip": "IP",
	"ipfs": "IPFS", "p2p": "P2P", "tv": "TV", "vr": "VR", "gfw": "GFW",
	"aws": "AWS", "sms": "SMS",
}

var geoBrands = map[string]string{
	"google": "Google", "apple": "Apple", "github": "GitHub", "gitlab": "GitLab",
	"youtube": "YouTube", "openai": "OpenAI", "tiktok": "TikTok", "whatsapp": "WhatsApp",
	"paypal": "PayPal", "linkedin": "LinkedIn", "microsoft": "Microsoft", "netflix": "Netflix",
	"spotify": "Spotify", "telegram": "Telegram", "twitter": "X (Twitter)",
}

// countryName returns the localized name of a two-letter country code.
func countryName(code string) string {
	return countryNameIn(code, appLanguage())
}

func countryNameIn(code, lang string) string {
	code = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(code), "!"))
	if code == "uk" {
		code = "gb"
	}
	if len(code) != 2 {
		return ""
	}
	region, err := language.ParseRegion(code)
	if err != nil {
		return ""
	}
	names := display.English.Regions()
	if lang == "ru" {
		names = display.Russian.Regions()
	}
	name := names.Name(region)
	if name == "" {
		return ""
	}
	return name
}

// formatTopic formats a topic/category name (e.g. "bank" -> "Banks", "ai" -> "AI").
func formatTopic(topic string) string {
	tLower := strings.ToLower(topic)
	if a, ok := geoAcronyms[tLower]; ok {
		return a
	}
	if b, ok := geoBrands[tLower]; ok {
		return b
	}
	switch tLower {
	case "bank":
		return tr("Banks")
	case "game":
		return tr("Games")
	case "shop", "shopping":
		return tr("Shopping")
	case "news":
		return tr("News")
	case "scholar":
		return tr("Scholar")
	case "finance":
		return tr("Finance")
	case "ads", "advertisement":
		return tr("Ads")
	case "porn":
		return tr("Porn websites")
	case "forum":
		return tr("Forums")
	case "video":
		return tr("Video")
	case "music":
		return tr("Music")
	case "social":
		return tr("Social")
	default:
		parts := strings.Split(topic, "-")
		for i, p := range parts {
			pLow := strings.ToLower(p)
			if a, ok := geoAcronyms[pLow]; ok {
				parts[i] = a
			} else if b, ok := geoBrands[pLow]; ok {
				parts[i] = b
			} else if len(p) > 0 {
				parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
			}
		}
		return strings.Join(parts, " ")
	}
}

// humanizeGeosite extracts human-readable title, flag, and icon for geosite codes.
func humanizeGeosite(code string) (title string, flag string, icon *ui.SVG, ok bool) {
	raw := strings.TrimPrefix(code, "geosite:")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil, false
	}

	// 1. Special geosite entries
	switch raw {
	case "category-ads-all":
		return tr("Ads & Trackers"), "", iconGlobe, true
	case "category-ads":
		return tr("Ads"), "", iconGlobe, true
	case "category-porn":
		return tr("Porn Websites"), "", iconGlobe, true
	case "gfw":
		return tr("Blocked in China (GFW)"), "", iconGlobe, true
	case "greatfire":
		return tr("Blocked in China (GreatFire)"), "", iconGlobe, true
	case "win-update":
		return tr("Windows Update"), "", iconGlobe, true
	case "win-spy":
		return tr("Windows Telemetry"), "", iconGlobe, true
	case "win-extra":
		return tr("Windows Extras"), "", iconGlobe, true
	case "private":
		return tr("Local Network Names"), "", iconGlobe, true
	}

	// 2. Direct country code: e.g. "ir", "cn", "us"
	if len(raw) == 2 || raw == "uk" {
		if cName := countryName(raw); cName != "" {
			return cName + tr(" Websites"), flagFor(raw), iconGlobe, true
		}
	}

	// 3. geolocation-...
	if strings.HasPrefix(raw, "geolocation-") {
		countryPart := strings.TrimPrefix(raw, "geolocation-")
		if strings.HasPrefix(countryPart, "!") {
			if cName := countryName(countryPart); cName != "" {
				return tr("Websites (Excluding ") + cName + ")", "", iconGlobe, true
			}
		} else {
			if cName := countryName(countryPart); cName != "" {
				return tr("Websites In ") + cName, flagFor(countryPart), iconGlobe, true
			}
		}
	}

	// 4. category-...
	if strings.HasPrefix(raw, "category-") {
		rest := strings.TrimPrefix(raw, "category-")

		// 4a. Direct country category: category-ir, category-ru, category-cn
		if cName := countryName(rest); cName != "" {
			return cName + tr(" Websites"), flagFor(rest), iconGlobe, true
		}

		// 4b. Negated country suffix: category-<topic>-!<country> (e.g. category-ai-!cn, category-scholar-!cn)
		if strings.Contains(rest, "-!") {
			topicPart, countryPart, _ := strings.Cut(rest, "-!")
			if cName := countryName(countryPart); cName != "" {
				topic := formatTopic(topicPart)
				return topic + tr(" (Excluding ") + cName + ")", "", iconGlobe, true
			}
		}

		// 4c. Positive country suffix: category-<topic>-<country> (e.g. category-ai-cn, category-bank-ir)
		if idx := strings.LastIndex(rest, "-"); idx > 0 {
			topicPart := rest[:idx]
			countryPart := rest[idx+1:]
			if cName := countryName(countryPart); cName != "" {
				// Acronyms (e.g. AI): category-ai-cn -> "AI China"
				if _, isAcronym := geoAcronyms[strings.ToLower(topicPart)]; isAcronym {
					return geoAcronyms[strings.ToLower(topicPart)] + " " + cName, flagFor(countryPart), iconGlobe, true
				}
				// Other topics: e.g. "Iran Banks"
				topic := formatTopic(topicPart)
				return cName + " " + topic, flagFor(countryPart), iconGlobe, true
			}
		}
	}

	// 5. Brand/Acronym + country (e.g. google-cn, apple-cn, tld-!cn)
	if idx := strings.LastIndex(raw, "-"); idx > 0 {
		prefix := raw[:idx]
		countryPart := raw[idx+1:]
		if strings.HasPrefix(countryPart, "!") {
			if cName := countryName(countryPart); cName != "" {
				if brand, ok := geoBrands[strings.ToLower(prefix)]; ok {
					return brand + tr(" (Excluding ") + cName + ")", "", iconGlobe, true
				}
				if acr, ok := geoAcronyms[strings.ToLower(prefix)]; ok {
					return acr + tr(" (Excluding ") + cName + ")", "", iconGlobe, true
				}
			}
		} else {
			if cName := countryName(countryPart); cName != "" {
				if brand, ok := geoBrands[strings.ToLower(prefix)]; ok {
					return brand + " (" + cName + ")", flagFor(countryPart), iconGlobe, true
				}
				if acr, ok := geoAcronyms[strings.ToLower(prefix)]; ok {
					return acr + " " + cName, flagFor(countryPart), iconGlobe, true
				}
			}
		}
	}

	return "", "", nil, false
}

// humanizeGeoIP extracts human-readable title, flag, and icon for geoip codes.
func humanizeGeoIP(code string) (title string, flag string, icon *ui.SVG, ok bool) {
	raw := strings.TrimPrefix(code, "geoip:")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil, false
	}

	// Special IP identifiers
	switch strings.ToLower(raw) {
	case "private":
		return tr("Private IPs"), "", iconLayers, true
	case "telegram":
		return tr("Telegram Servers"), "", iconLayers, true
	case "twitter":
		return "X (Twitter)", "", iconLayers, true
	case "cloudfront":
		return "Amazon CloudFront", "", iconLayers, true
	case "tor":
		return tr("Tor Relays"), "", iconLayers, true
	}

	// Negated country IP: e.g. !cn
	if strings.HasPrefix(raw, "!") {
		if cName := countryName(raw); cName != "" {
			return tr("IPs (Excluding ") + cName + ")", "", iconLayers, true
		}
	}

	// Country code: e.g. ir, cn, de, us, gb
	if cName := countryName(raw); cName != "" {
		return cName, flagFor(raw), iconLayers, true
	}

	return "", "", nil, false
}
