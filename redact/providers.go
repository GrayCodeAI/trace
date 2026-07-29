package redact

import "regexp"

var providerTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sb_secret_[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`sbp_[a-z0-9_-]{20,}`),
}

func detectProviderTokens(s string) []taggedRegion {
	var regions []taggedRegion
	for _, pat := range providerTokenPatterns {
		for _, loc := range pat.FindAllStringIndex(s, -1) {
			regions = append(regions, taggedRegion{region: region{loc[0], loc[1]}})
		}
	}
	return regions
}
