package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

func TestWaDigitsAndTelHref(t *testing.T) {
	if got := waDigits("+86 138-0013 (8000)"); got != "8613800138000" {
		t.Fatalf("waDigits = %q", got)
	}
	if got := telHref("+86 138 0013 8000"); got != "tel:+8613800138000" {
		t.Fatalf("telHref = %q", got)
	}
	if got := telHref("0755 1234"); got != "tel:07551234" {
		t.Fatalf("telHref (no plus) = %q", got)
	}
	if got := telHref("  "); got != "" {
		t.Fatalf("telHref empty = %q", got)
	}
}

func setContact(t *testing.T, s *Server, whatsapp, email, phone, qr, float string) {
	t.Helper()
	for k, v := range map[string]string{
		contactWhatsAppSetting: whatsapp,
		contactEmailSetting:    email,
		contactPhoneSetting:    phone,
		contactWeChatQRSetting: qr,
		contactFloatSetting:    float,
	} {
		if err := s.store.SetSetting(k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	s.clearGeneratedCaches()
}

// 未配置任何联系方式：contactView 为 nil，前台不渲染浮动按钮与询盘。
func TestContactViewUnconfigured(t *testing.T) {
	s := newTestPublicServer(t, "")
	if s.contactView() != nil {
		t.Fatalf("unconfigured contactView should be nil")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zh/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("home status = %d", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "contact-float") || strings.Contains(body, "wechat-modal") {
		t.Fatalf("unconfigured site should not render contact widgets")
	}
}

// 配置齐全：浮动按钮渲染各联系方式 + 微信弹层；关掉 contact.float 后浮动按钮消失。
func TestContactFloatRendering(t *testing.T) {
	s := newTestPublicServer(t, "")
	setContact(t, s, "+86 138 0013 8000", "sales@example.com", "+86 755 1234", "/uploads/wechat.webp", "1")

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zh/", nil))
	body := w.Body.String()
	for _, want := range []string{
		"contact-float",
		"https://wa.me/8613800138000",
		"mailto:sales@example.com",
		"tel:&#43;867551234", // html/template 把属性里的 + 转义为 &#43;，浏览器照常解码
		"data-wechat-open",
		"/uploads/wechat.webp",
		"联系我们",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("home body missing %q", want)
		}
	}

	// 浮动开关关闭：按钮消失，但微信弹层（供询盘用）与配置无关的部分不再输出。
	setContact(t, s, "+86 138 0013 8000", "sales@example.com", "", "", "0")
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/zh/", nil))
	if strings.Contains(w2.Body.String(), "contact-float") {
		t.Fatalf("contact.float=0 should hide the floating button")
	}
}

// 只配置部分渠道：浮动面板只列已配置项。
func TestContactFloatPartialChannels(t *testing.T) {
	s := newTestPublicServer(t, "")
	setContact(t, s, "", "sales@example.com", "", "", "")

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zh/", nil))
	body := w.Body.String()
	if !strings.Contains(body, "contact-float") || !strings.Contains(body, "mailto:sales@example.com") {
		t.Fatalf("email-only float button missing")
	}
	for _, absent := range []string{"wa.me/", "data-wechat-open", "tel:"} {
		if strings.Contains(body, absent) {
			t.Fatalf("unconfigured channel %q leaked into float panel", absent)
		}
	}
}

// product 详情页询盘区块：WhatsApp 预填产品名+页面 URL、mailto 带 subject、tel、微信弹层。
func TestProductInquiryBlock(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "inq-prod", Title: "询盘商品",
		Excerpt: "简介", Status: "published",
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}
	setContact(t, s, "+86 138 0013 8000", "sales@example.com", "+86 755 1234", "/uploads/wechat.webp", "1")

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zh/products/inq-prod", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "询盘") {
		t.Fatalf("detail missing inquiry block")
	}
	// WhatsApp 预填包含产品名与页面 URL（query 转义后）。
	if !strings.Contains(body, "https://wa.me/8613800138000?text=") {
		t.Fatalf("detail missing wa.me inquiry link")
	}
	if !strings.Contains(body, url.QueryEscape("询盘商品")) {
		t.Fatalf("wa.me prefill missing product title")
	}
	if !strings.Contains(body, url.QueryEscape("https://example.test/zh/products/inq-prod/")) {
		t.Fatalf("wa.me prefill missing canonical URL; body has %q", body[strings.Index(body, "wa.me"):strings.Index(body, "wa.me")+200])
	}
	// mailto subject=产品名（mailto 用 %20 不用 +）。
	if !strings.Contains(body, "mailto:sales@example.com?subject=") || !strings.Contains(body, mailtoEscape("询盘商品")) {
		t.Fatalf("detail missing mailto inquiry link")
	}
	if !strings.Contains(body, "tel:&#43;867551234") { // + 在属性里被转义为 &#43;
		t.Fatalf("detail missing tel link")
	}
	if !strings.Contains(body, "data-wechat-open") || !strings.Contains(body, "wechat-modal") {
		t.Fatalf("detail missing wechat button/modal")
	}

	// 非 product 扩展类型不出询盘区块。
	if err := s.store.SetSetting(enabledContentTypesKey, "product,event"); err != nil {
		t.Fatalf("enable event: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "event", Lang: "zh", Slug: "inq-event", Title: "活动一", Status: "published",
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	s.clearGeneratedCaches()
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/zh/events/inq-event", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("event detail status = %d", w2.Code)
	}
	if strings.Contains(w2.Body.String(), "inquiry-actions") {
		t.Fatalf("inquiry block should be product-only")
	}
}

// 未配置联系方式时，product 详情页不出询盘区块。
func TestProductInquiryAbsentWithoutContact(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "bare-prod", Title: "无联系商品", Status: "published",
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zh/products/bare-prod", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("detail status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "inquiry-actions") {
		t.Fatalf("inquiry block should not render without contact settings")
	}
}

// 表单校验：坏号码/坏邮箱拒绝；合法输入落库且 float 开关持久化。
func TestApplyContactSettingsForm(t *testing.T) {
	s := newTestPublicServer(t, "")
	mkReq := func(vals url.Values) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/admin/settings/contact", strings.NewReader(vals.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}

	if msg := s.applyContactSettingsForm(mkReq(url.Values{"contact_whatsapp": {"abc"}})); msg == "" {
		t.Fatalf("invalid whatsapp should be rejected")
	}
	if msg := s.applyContactSettingsForm(mkReq(url.Values{"contact_email": {"not-an-email"}})); msg == "" {
		t.Fatalf("invalid email should be rejected")
	}
	if msg := s.applyContactSettingsForm(mkReq(url.Values{"contact_wechat_qr": {"uploads/x.png"}})); msg == "" {
		t.Fatalf("relative QR path should be rejected")
	}

	msg := s.applyContactSettingsForm(mkReq(url.Values{
		"contact_whatsapp":  {"+86 138 0013 8000"},
		"contact_email":     {"sales@example.com"},
		"contact_phone":     {"+86 755 1234"},
		"contact_wechat_qr": {"/uploads/wechat.webp"},
		"contact_float":     {"1"},
	}))
	if msg != "" {
		t.Fatalf("valid form rejected: %s", msg)
	}
	c := s.contactView()
	if c == nil || c.WaMe != "8613800138000" || c.Email != "sales@example.com" || !c.Float {
		t.Fatalf("contactView after save = %+v", c)
	}

	// 不勾选 float → 存 0（显式关闭，区别于默认开）。
	if msg := s.applyContactSettingsForm(mkReq(url.Values{"contact_email": {"sales@example.com"}})); msg != "" {
		t.Fatalf("save without float: %s", msg)
	}
	if got := s.store.Setting(contactFloatSetting); got != "0" {
		t.Fatalf("contact.float = %q, want 0", got)
	}
	if c := s.contactView(); c == nil || c.Float {
		t.Fatalf("float should be off, got %+v", c)
	}
}
