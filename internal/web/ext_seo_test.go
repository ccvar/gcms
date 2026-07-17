package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

// ---------- 工厂站「发现通道」审计修复的回归测试 ----------
//
// 覆盖：sitemap/RSS 收录扩展类型、og:image 绝对化与兜底、robots 默认值与单篇覆盖、
// 归档 hreflang、BreadcrumbList/CollectionPage、分页 canonical 自指与越界 404、
// 空态/搜索文案、wa.me 预填编码、to-top 全站脚本。

func extSEOTestServer(t *testing.T, enabled string) *Server {
	t.Helper()
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("locales", "zh,en"); err != nil {
		t.Fatalf("set locales: %v", err)
	}
	if enabled != "" {
		if err := s.store.SetSetting(enabledContentTypesKey, enabled); err != nil {
			t.Fatalf("enable types: %v", err)
		}
	}
	return s
}

func getBody(t *testing.T, h http.Handler, path string, wantStatus int) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != wantStatus {
		t.Fatalf("GET %s status = %d, want %d; body = %s", path, w.Code, wantStatus, w.Body.String())
	}
	return w.Body.String()
}

// sitemap 收录已启用扩展类型：/{prefix} 归档（全语种）、分类页、按 TransGroup
// 配对 hreflang 的详情页，且商品详情带 image:image（封面+图集）；未启用类型缺席。
func TestSitemapIncludesExtContentTypes(t *testing.T) {
	s := extSEOTestServer(t, "product")
	catID, err := s.store.CreateCategory(&store.Category{Slug: "bearings", Name: "轴承", Lang: "zh", Kind: "product"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	pub := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	zh := &store.Post{
		Type: "product", Lang: "zh", Slug: "bearing-6204", Title: "深沟球轴承",
		Status: "published", PublishedAt: pub, TransGroup: "g:bearing",
		CoverImage: "/uploads/bearing.webp",
		Extra:      `{"gallery":["/uploads/bearing-side.webp","/uploads/bearing.webp"]}`,
		CategoryID: sql.NullInt64{Int64: catID, Valid: true},
	}
	if _, err := s.store.CreatePost(zh); err != nil {
		t.Fatalf("create zh product: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "en", Slug: "bearing-6204-en", Title: "Deep Groove Bearing",
		Status: "published", PublishedAt: pub, TransGroup: "g:bearing",
	}); err != nil {
		t.Fatalf("create en product: %v", err)
	}

	body := getBody(t, s.Handler(), "/sitemap.xml", http.StatusOK)
	for _, want := range []string{
		`xmlns:image="http://www.google.com/schemas/sitemap-image/1.1"`,
		"<loc>https://example.test/zh/products</loc>",
		"<loc>https://example.test/en/products</loc>",
		"<loc>https://example.test/zh/products/cat/bearings</loc>",
		"<loc>https://example.test/zh/products/bearing-6204/</loc>",
		"<loc>https://example.test/en/products/bearing-6204-en/</loc>",
		`hreflang="en-US" href="https://example.test/en/products/bearing-6204-en/"`,
		`hreflang="x-default" href="https://example.test/zh/products/bearing-6204/"`,
		"<image:image><image:loc>https://example.test/uploads/bearing.webp</image:loc></image:image>",
		"<image:image><image:loc>https://example.test/uploads/bearing-side.webp</image:loc></image:image>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sitemap missing %q", want)
		}
	}
	// 封面与图集重复的图只出现一次。
	if n := strings.Count(body, "<image:loc>https://example.test/uploads/bearing.webp</image:loc>"); n != 1 {
		t.Errorf("cover image should appear once in image:image, got %d", n)
	}
	// 未启用的扩展类型（event/docs/gallery）不进 sitemap。
	for _, absent := range []string{"/zh/events", "/zh/docs", "/zh/gallery"} {
		if strings.Contains(body, "<loc>https://example.test"+absent) {
			t.Errorf("sitemap should not list disabled type %s", absent)
		}
	}
}

// RSS 与文章合流：已启用且可搜索的扩展类型进 feed，按发布时间倒序混排。
func TestRSSIncludesExtContent(t *testing.T) {
	s := extSEOTestServer(t, "product")
	older := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	if _, err := s.store.CreatePost(&store.Post{
		Type: "post", Lang: "zh", Slug: "old-post", Title: "旧文章", Status: "published", PublishedAt: older,
	}); err != nil {
		t.Fatalf("create post: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "new-product", Title: "新品轴承", Excerpt: "上新", Status: "published", PublishedAt: newer,
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}

	body := getBody(t, s.Handler(), "/zh/rss.xml", http.StatusOK)
	prodIdx := strings.Index(body, "https://example.test/zh/products/new-product/")
	postIdx := strings.Index(body, "https://example.test/zh/posts/old-post/")
	if prodIdx < 0 {
		t.Fatalf("rss missing product item; body = %s", body)
	}
	if postIdx < 0 {
		t.Fatalf("rss missing post item")
	}
	if prodIdx > postIdx {
		t.Errorf("rss items should be time-sorted: newer product should precede older post")
	}
}

// 商品详情分享元数据：og:image/twitter:image 绝对化，空封面回落图集→站点默认图；
// og:type=product；robots 输出默认值而非空串。
func TestExtDetailShareMetaAbsoluteAndFallback(t *testing.T) {
	s := extSEOTestServer(t, "product,event")
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "with-cover", Title: "带封面", Status: "published",
		CoverImage: "/uploads/cover.webp",
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "gallery-only", Title: "只有图集", Status: "published",
		Extra: `{"gallery":["/uploads/g1.webp"]}`,
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "bare", Title: "无图", Status: "published",
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}
	h := s.Handler()

	body := getBody(t, h, "/zh/products/with-cover/", http.StatusOK)
	for _, want := range []string{
		`<meta property="og:image" content="https://example.test/uploads/cover.webp">`,
		`<meta name="twitter:image" content="https://example.test/uploads/cover.webp">`,
		`<meta property="og:type" content="product">`,
		`<meta name="robots" content="index, follow, max-image-preview:large">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("product detail missing %q", want)
		}
	}
	if strings.Contains(body, `<meta name="robots" content="">`) {
		t.Errorf("product detail must not output empty robots meta")
	}

	// 无封面 → 图集第一张兜底。
	if b := getBody(t, h, "/zh/products/gallery-only/", http.StatusOK); !strings.Contains(b,
		`<meta property="og:image" content="https://example.test/uploads/g1.webp">`) {
		t.Errorf("gallery-only product should fall back to first gallery image for og:image")
	}
	// 全无 → 站点默认分享图。
	if b := getBody(t, h, "/zh/products/bare/", http.StatusOK); !strings.Contains(b,
		`<meta property="og:image" content="https://example.test/assets/og-cover.webp">`) {
		t.Errorf("bare product should fall back to the default share image")
	}

	// 非商品扩展类型维持 og:type=article。
	if _, err := s.store.CreatePost(&store.Post{
		Type: "event", Lang: "zh", Slug: "expo", Title: "展会", Status: "published",
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	s.clearGeneratedCaches()
	if b := getBody(t, h, "/zh/events/expo/", http.StatusOK); !strings.Contains(b, `<meta property="og:type" content="article">`) {
		t.Errorf("non-product ext detail should keep og:type=article")
	}
}

// v1.3.24 的单篇 SEO 覆盖字段在扩展详情前台生效（此前被渲染忽略）。
func TestExtDetailRobotsAndCanonicalOverrides(t *testing.T) {
	s := extSEOTestServer(t, "product")
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "overridden", Title: "转载商品", Status: "published",
		RobotsOverride:    "noindex, follow",
		CanonicalOverride: "https://origin.example.com/source-product/",
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}
	body := getBody(t, s.Handler(), "/zh/products/overridden/", http.StatusOK)
	if !strings.Contains(body, `<meta name="robots" content="noindex, follow">`) {
		t.Errorf("robots_override not applied on ext detail")
	}
	if !strings.Contains(body, `rel="canonical" href="https://origin.example.com/source-product/"`) {
		t.Errorf("canonical_override not applied on ext detail")
	}
}

// 商品详情输出 BreadcrumbList（首页→商品→分类→本品），Product JSON-LD 的 image 含图集多图。
func TestProductDetailJSONLDBreadcrumbAndImages(t *testing.T) {
	s := extSEOTestServer(t, "product")
	catID, err := s.store.CreateCategory(&store.Category{Slug: "cnc", Name: "CNC 加工", Lang: "zh", Kind: "product"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "cnc-part", Title: "精密件", Status: "published",
		CoverImage: "/uploads/p1.webp",
		Extra:      `{"gallery":["/uploads/p2.webp","/uploads/p1.webp"]}`,
		CategoryID: sql.NullInt64{Int64: catID, Valid: true},
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}
	body := getBody(t, s.Handler(), "/zh/products/cnc-part/", http.StatusOK)
	for _, want := range []string{
		`"@type":"BreadcrumbList"`,
		`"name":"CNC 加工"`,
		`"item":"https://example.test/zh/products/cat/cnc"`,
		// image 数组：封面 + 图集（与封面重复的去重）。
		`"image":["https://example.test/uploads/p1.webp","https://example.test/uploads/p2.webp"]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("product detail JSON-LD missing %q", want)
		}
	}
}

// /{prefix} 归档（无分类分支）补齐 hreflang；列表页输出 CollectionPage+BreadcrumbList。
func TestExtArchiveHreflangAndJSONLD(t *testing.T) {
	s := extSEOTestServer(t, "product")
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "p1", Title: "商品一", Status: "published",
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}
	body := getBody(t, s.Handler(), "/zh/products", http.StatusOK)
	for _, want := range []string{
		`hreflang="en-US" href="https://example.test/en/products"`,
		`hreflang="x-default" href="https://example.test/zh/products"`,
		`"@type":"CollectionPage"`,
		`"@type":"BreadcrumbList"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ext archive missing %q", want)
		}
	}
}

// 分页 canonical 自指页码；越界页码返回 404（不再产出无限量 soft-404）。
func TestExtArchivePaginationCanonicalAndOutOfRange(t *testing.T) {
	s := extSEOTestServer(t, "product")
	for i := 0; i < 13; i++ { // 每页 12 条 → 共 2 页
		if _, err := s.store.CreatePost(&store.Post{
			Type: "product", Lang: "zh", Slug: "sku-" + string(rune('a'+i)), Title: "商品", Status: "published",
		}); err != nil {
			t.Fatalf("create product %d: %v", i, err)
		}
	}
	h := s.Handler()

	if b := getBody(t, h, "/zh/products", http.StatusOK); !strings.Contains(b,
		`rel="canonical" href="https://example.test/zh/products"`) {
		t.Errorf("page 1 canonical should be the archive base")
	}
	if b := getBody(t, h, "/zh/products/page/2", http.StatusOK); !strings.Contains(b,
		`rel="canonical" href="https://example.test/zh/products/page/2/"`) {
		t.Errorf("page 2 canonical should be self-referential")
	}
	getBody(t, h, "/zh/products/page/3", http.StatusNotFound)
	getBody(t, h, "/zh/products/page/99", http.StatusNotFound)
}

// 商品列表空态不再说「还没有链接。」：product 用询盘引导文案，其余扩展类型用中性空态。
func TestExtArchiveEmptyStateText(t *testing.T) {
	s := extSEOTestServer(t, "product,event")
	h := s.Handler()

	products := getBody(t, h, "/zh/products", http.StatusOK)
	if strings.Contains(products, "还没有链接") {
		t.Errorf("product empty state must not borrow the links copy")
	}
	if !strings.Contains(products, "商品资料整理中") {
		t.Errorf("product empty state should use factory.no_products")
	}
	events := getBody(t, h, "/zh/events", http.StatusOK)
	if strings.Contains(events, "还没有链接") {
		t.Errorf("event empty state must not borrow the links copy")
	}
	if !strings.Contains(events, "这里还没有内容。") {
		t.Errorf("event empty state should use archive.empty")
	}
}

// 站内搜索：占位符改中性文案；商品结果带类型徽标与分类标识。
func TestSearchNeutralPlaceholderAndTypeBadge(t *testing.T) {
	s := extSEOTestServer(t, "product")
	catID, err := s.store.CreateCategory(&store.Category{Slug: "bearings", Name: "轴承类", Lang: "zh", Kind: "product"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "search-prod", Title: "精密轴承 6204", Excerpt: "低噪音", Status: "published",
		CategoryID: sql.NullInt64{Int64: catID, Valid: true},
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}
	body := getBody(t, s.Handler(), "/zh/search?q=%E8%BD%B4%E6%89%BF", http.StatusOK) // q=轴承
	if !strings.Contains(body, `placeholder="搜索站内内容…"`) {
		t.Errorf("search placeholder should be neutral, not article-only")
	}
	if !strings.Contains(body, "/zh/products/search-prod/") {
		t.Fatalf("search should hit the product; body = %s", body)
	}
	if !strings.Contains(body, `<span class="tag">商品</span>`) {
		t.Errorf("product search result should carry a type badge")
	}
	if !strings.Contains(body, `/zh/products/cat/bearings`) || !strings.Contains(body, "轴承类") {
		t.Errorf("product search result should link its category")
	}
}

// wa.me 预填编码：空格用 %20（QueryEscape 的 + 在 WhatsApp 端可能原样显示加号）。
func TestInquiryWhatsAppPrefillUsesPercent20(t *testing.T) {
	c := &ContactView{WhatsApp: "+86 138 0013 8000", WaMe: "8613800138000"}
	iv := inquiryView(c, "精密 CNC 铝合金加工件 https://example.test/zh/products/cnc/", "精密 CNC 铝合金加工件")
	if iv == nil || iv.WhatsAppURL == "" {
		t.Fatalf("inquiryView = %+v", iv)
	}
	if strings.Contains(iv.WhatsAppURL, "+") {
		t.Errorf("wa.me prefill must not contain '+': %s", iv.WhatsAppURL)
	}
	if !strings.Contains(iv.WhatsAppURL, "%20") {
		t.Errorf("wa.me prefill should encode spaces as %%20: %s", iv.WhatsAppURL)
	}
}

// 灯箱/微信弹层的关闭按钮 aria-label 是「关闭」，不再是「回到顶部」或裸 ×。
func TestLightboxAndWeChatCloseAria(t *testing.T) {
	s := extSEOTestServer(t, "product")
	if err := s.store.SetSetting("theme", "industrial"); err != nil { // factory-catalog 骨架 → product_detail 模板
		t.Fatalf("set theme: %v", err)
	}
	setContact(t, s, "", "", "", "/uploads/wechat.webp", "1")
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "aria-prod", Title: "带图商品", Status: "published",
		CoverImage: "/uploads/cover.webp",
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}
	body := getBody(t, s.Handler(), "/zh/products/aria-prod/", http.StatusOK)
	if !strings.Contains(body, `data-lightbox-close aria-label="关闭"`) {
		t.Errorf("lightbox close buttons should be labelled 关闭")
	}
	if strings.Contains(body, `data-lightbox-close aria-label="回到顶部"`) {
		t.Errorf("lightbox backdrop must not be labelled 回到顶部")
	}
	if !strings.Contains(body, `data-wechat-close aria-label="关闭"`) {
		t.Errorf("wechat modal close buttons should be labelled 关闭")
	}
}

// to-top 全站生效且不吞点击：逻辑在 site.js（每页都载）、toc.js 不再重复绑定；
// CSS 上未显示的 .to-top 禁点、浮动询盘按钮层级高于 to-top。
func TestToTopSiteWideAndContactFloatLayering(t *testing.T) {
	s := extSEOTestServer(t, "")
	h := s.Handler()

	site := getBody(t, h, "/assets/js/site.js", http.StatusOK)
	if !strings.Contains(site, `querySelector(".to-top")`) {
		t.Errorf("site.js should own the to-top behaviour")
	}
	toc := getBody(t, h, "/assets/js/toc.js", http.StatusOK)
	if strings.Contains(toc, `querySelector(".to-top")`) {
		t.Errorf("toc.js must not double-bind to-top")
	}
	css := getBody(t, h, "/assets/css/public.css", http.StatusOK)
	start := strings.Index(css, ".to-top {")
	if start < 0 || !strings.Contains(css[start:start+600], "pointer-events: none") {
		t.Errorf(".to-top should be pointer-events:none until shown")
	}
	if !strings.Contains(css, ".to-top.show { opacity: 1; transform: translateY(0); pointer-events: auto; }") {
		t.Errorf(".to-top.show should restore pointer-events")
	}
	float := strings.Index(css, ".contact-float {")
	if float < 0 || !strings.Contains(css[float:float+300], "z-index: 91") {
		t.Errorf(".contact-float should sit above .to-top (z-index 91 > 90)")
	}
}
