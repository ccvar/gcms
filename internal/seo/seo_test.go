package seo

import (
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

// TestSEOOverridesTakePrecedence 单篇 robots/canonical 覆盖：非空时优先于默认值（三种内容类型）。
func TestSEOOverridesTakePrecedence(t *testing.T) {
	site := Site{BaseURL: "https://example.test", Prefix: "/zh", Name: "GCMS"}
	published := time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC)
	base := store.Post{
		Lang: "zh", Title: "Hello", Status: "published", PublishedAt: published,
		RobotsOverride: "noindex, follow", CanonicalOverride: "https://origin.example.com/source-article/",
	}
	build := map[string]func(*store.Post) Meta{"post": site.Article, "page": site.Page, "link": site.Link}
	for typ, fn := range build {
		p := base
		p.Type, p.Slug = typ, "hello"
		meta := fn(&p)
		if meta.Robots != "noindex, follow" {
			t.Fatalf("%s robots = %q, want override", typ, meta.Robots)
		}
		if meta.Canonical != "https://origin.example.com/source-article/" {
			t.Fatalf("%s canonical = %q, want override", typ, meta.Canonical)
		}
	}
	// 空覆盖 → 默认值
	plain := store.Post{Type: "post", Lang: "zh", Slug: "hello", Title: "Hello", Status: "published", PublishedAt: published}
	meta := site.Article(&plain)
	if meta.Robots != defaultRobots || meta.Canonical != "https://example.test/zh/posts/hello/" {
		t.Fatalf("默认值被破坏：robots=%q canonical=%q", meta.Robots, meta.Canonical)
	}
}

func TestContentCanonicalsUseTrailingSlash(t *testing.T) {
	site := Site{BaseURL: "https://example.test", Prefix: "/zh", Name: "GCMS"}
	published := time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		meta Meta
		want string
	}{
		{
			name: "article",
			meta: site.Article(&store.Post{Type: "post", Lang: "zh", Slug: "hello", Title: "Hello", Status: "published", PublishedAt: published}),
			want: "https://example.test/zh/posts/hello/",
		},
		{
			name: "page",
			meta: site.Page(&store.Post{Type: "page", Lang: "zh", Slug: "about", Title: "About", Status: "published", PublishedAt: published}),
			want: "https://example.test/zh/about/",
		},
		{
			name: "link",
			meta: site.Link(&store.Post{Type: "link", Lang: "zh", Slug: "resource", Title: "Resource", Status: "published", PublishedAt: published}),
			want: "https://example.test/zh/links/resource/",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.meta.Canonical != tc.want {
				t.Fatalf("canonical = %q, want %q", tc.meta.Canonical, tc.want)
			}
		})
	}
}

func TestLinkMetaIncludesProductAndFAQStructuredData(t *testing.T) {
	site := Site{BaseURL: "https://example.test", Prefix: "/zh", Name: "GCMS", LangTag: "zh-CN"}
	meta := site.Link(&store.Post{
		Type:       "link",
		Lang:       "zh",
		Slug:       "toolbox",
		Title:      "Toolbox",
		Excerpt:    "工具集合",
		Content:    "## 适合谁？\n答：适合内容运营者。\n\nQ: 是否支持外链？ A: 支持。",
		CoverImage: "/uploads/toolbox.webp",
		LinkURL:    "https://example.com/toolbox",
		Status:     "published",
	})

	product := jsonLDByType(meta.JSONLD, "Product")
	if product == nil {
		t.Fatalf("missing Product JSON-LD: %#v", meta.JSONLD)
	}
	if product["url"] != "https://example.test/zh/links/toolbox/" {
		t.Fatalf("product url = %#v", product["url"])
	}
	if product["sameAs"] != "https://example.com/toolbox" {
		t.Fatalf("product sameAs = %#v", product["sameAs"])
	}

	faq := jsonLDByType(meta.JSONLD, "FAQPage")
	if faq == nil {
		t.Fatalf("missing FAQPage JSON-LD: %#v", meta.JSONLD)
	}
	items, ok := faq["mainEntity"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("faq mainEntity = %#v, want 2 items", faq["mainEntity"])
	}
}

func TestHeroAltUsesHeroTitleFallbacks(t *testing.T) {
	site := Site{Name: "GCMS", Tagline: "内容后台", HeroTitle: "内容发布、\n搜索增长"}
	if got, want := site.HeroAlt(), "内容发布、 搜索增长"; got != want {
		t.Fatalf("HeroAlt = %q, want %q", got, want)
	}
	site.HeroTitle = ""
	if got, want := site.HeroAlt(), "内容后台"; got != want {
		t.Fatalf("HeroAlt fallback = %q, want %q", got, want)
	}
}

func jsonLDByType(items []any, typ string) map[string]any {
	for _, item := range items {
		m, ok := item.(map[string]any)
		if ok && m["@type"] == typ {
			return m
		}
	}
	return nil
}
