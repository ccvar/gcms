package web

import "testing"

func TestMenuURLMatchesCurrent(t *testing.T) {
	tests := []struct {
		name        string
		menuURL     string
		currentPath string
		currentFull string
		want        bool
	}{
		{"category exact", "/category/features", "/category/features", "/category/features", true},
		{"category sibling", "/category/start", "/category/features", "/category/features", false},
		{"root", "/", "/", "/", true},
		{"query exact", "/links?cat=tools", "/links", "/links?cat=tools", true},
		{"query differs", "/links?cat=tools", "/links", "/links?cat=design", false},
		{"external ignored", "https://example.com", "/category/features", "/category/features", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := menuURLMatchesCurrent(tt.menuURL, tt.currentPath, tt.currentFull); got != tt.want {
				t.Fatalf("menuURLMatchesCurrent(%q, %q, %q) = %v, want %v", tt.menuURL, tt.currentPath, tt.currentFull, got, tt.want)
			}
		})
	}
}
