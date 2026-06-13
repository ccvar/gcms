package web

import (
	"testing"

	"cms.ccvar.com/internal/i18n"
)

func TestNegotiateAcceptLanguage(t *testing.T) {
	locales := []i18n.Locale{
		{Code: "zh", Tag: "zh-CN"},
		{Code: "en", Tag: "en-US"},
		{Code: "ja", Tag: "ja-JP"},
		{Code: "pt-br", Tag: "pt-BR"},
	}

	tests := []struct {
		name     string
		header   string
		fallback string
		want     string
	}{
		{
			name:     "empty header falls back",
			header:   "",
			fallback: "zh",
			want:     "zh",
		},
		{
			name:     "exact tag match",
			header:   "en-US,en;q=0.9,zh-CN;q=0.8",
			fallback: "zh",
			want:     "en",
		},
		{
			name:     "q value wins over order",
			header:   "en-US;q=0.4,ja-JP;q=0.9,zh-CN;q=0.8",
			fallback: "zh",
			want:     "ja",
		},
		{
			name:     "regional variant matches primary language",
			header:   "en-GB,en;q=0.9",
			fallback: "zh",
			want:     "en",
		},
		{
			name:     "primary language matches custom regional code",
			header:   "pt;q=0.9,en;q=0.8",
			fallback: "zh",
			want:     "pt-br",
		},
		{
			name:     "unsupported language falls back",
			header:   "de-DE,de;q=0.9",
			fallback: "zh",
			want:     "zh",
		},
		{
			name:     "wildcard uses fallback",
			header:   "de-DE;q=0.9,*;q=0.8",
			fallback: "zh",
			want:     "zh",
		},
		{
			name:     "q zero is ignored",
			header:   "en-US;q=0,ja-JP;q=0.7",
			fallback: "zh",
			want:     "ja",
		},
		{
			name:     "invalid q is ignored",
			header:   "en-US;q=nope,zh-CN;q=0.7",
			fallback: "ja",
			want:     "zh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := negotiateAcceptLanguage(tt.header, locales, tt.fallback)
			if got != tt.want {
				t.Fatalf("negotiateAcceptLanguage(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}
