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
	if meta.Robots != DefaultRobots || meta.Canonical != "https://example.test/zh/posts/hello/" {
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

// TestProductStructuredData 商品详情 Product JSON-LD（工厂/外贸站）：
// name/description/image/brand=站点名/sku 齐备，且绝不输出 offers/price（不做交易，别编造价格）。
func TestProductStructuredData(t *testing.T) {
	site := Site{BaseURL: "https://example.test", Prefix: "/zh", Name: "精工机械", LangTag: "zh-CN"}
	p := &store.Post{
		Type: "product", Lang: "zh", Slug: "bearing-6204", Title: "深沟球轴承 6204",
		Excerpt: "高转速低噪音", CoverImage: "/uploads/bearing.webp", Keywords: "轴承,6204",
		Category: &store.Category{Name: "轴承", Slug: "bearings", Kind: "product"},
		Status:   "published",
	}
	canon := "https://example.test/zh/products/bearing-6204/"
	product := site.Product(p, canon, p.Excerpt, "6204-2RS", nil)

	if product["@type"] != "Product" {
		t.Fatalf("@type = %#v, want Product", product["@type"])
	}
	if product["name"] != "深沟球轴承 6204" || product["url"] != canon {
		t.Fatalf("name/url = %#v / %#v", product["name"], product["url"])
	}
	if product["description"] != "高转速低噪音" {
		t.Fatalf("description = %#v", product["description"])
	}
	if product["image"] != "https://example.test/uploads/bearing.webp" {
		t.Fatalf("image = %#v", product["image"])
	}
	brand, ok := product["brand"].(map[string]any)
	if !ok || brand["name"] != "精工机械" {
		t.Fatalf("brand = %#v, want 站点名", product["brand"])
	}
	if product["sku"] != "6204-2RS" {
		t.Fatalf("sku = %#v", product["sku"])
	}
	if product["category"] != "轴承" {
		t.Fatalf("category = %#v", product["category"])
	}
	for _, banned := range []string{"offers", "price", "priceCurrency"} {
		if _, exists := product[banned]; exists {
			t.Fatalf("Product 不应输出 %q（不做交易）", banned)
		}
	}

	// 图集多图：image 变数组（封面在前、去重、相对路径绝对化），商品富结果建议多图。
	multi := site.Product(p, canon, p.Excerpt, "6204-2RS",
		[]string{"/uploads/bearing.webp", "/uploads/bearing-side.webp", "https://cdn.example.com/b.jpg", " "})
	imgs, ok := multi["image"].([]string)
	if !ok {
		t.Fatalf("多图时 image 应为数组，got %#v", multi["image"])
	}
	want := []string{
		"https://example.test/uploads/bearing.webp",
		"https://example.test/uploads/bearing-side.webp",
		"https://cdn.example.com/b.jpg",
	}
	if len(imgs) != len(want) {
		t.Fatalf("image 数组 = %#v, want %#v（封面去重+去空）", imgs, want)
	}
	for i := range want {
		if imgs[i] != want[i] {
			t.Fatalf("image[%d] = %q, want %q", i, imgs[i], want[i])
		}
	}

	// 无 sku / 无分类 / 无封面：字段省略而非空值，封面回落默认分享图。
	bare := site.Product(&store.Post{Type: "product", Title: "样品", Slug: "sample"}, canon, "", "", nil)
	for _, absent := range []string{"sku", "category", "description", "keywords"} {
		if _, exists := bare[absent]; exists {
			t.Fatalf("空值字段 %q 不应输出", absent)
		}
	}
	if bare["image"] != "https://example.test/assets/og-cover.webp" {
		t.Fatalf("默认图 = %#v", bare["image"])
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
