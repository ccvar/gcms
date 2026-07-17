package web

import (
	"html/template"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
)

// 联系方式与询盘按钮（工厂/外贸站 P1）。
//
// 站点设置新增「联系方式」分区（做法参照 Telegram 分区）：WhatsApp / 邮箱 / 电话 /
// 微信二维码 + 浮动按钮开关。前台两处消费：
//   - product 详情页（generic_detail）「询盘」区块：WhatsApp（wa.me 预填产品名+页面 URL）、
//     Email（mailto 带 subject）、电话（tel:）、微信（点开二维码弹层，JS class 开关，不用 :target）。
//   - 全站右下角浮动联系按钮：配置了任一联系方式且 contact.float 开才渲染。
//
// 询盘表单/留言箱是 P3，此处不涉及。

const (
	contactWhatsAppSetting = "contact.whatsapp"  // WhatsApp 号码（intl 格式，含国家码）
	contactEmailSetting    = "contact.email"     // 联系邮箱
	contactPhoneSetting    = "contact.phone"     // 联系电话
	contactWeChatQRSetting = "contact.wechat_qr" // 微信二维码图 URL（复用上传机制）
	contactFloatSetting    = "contact.float"     // 浮动按钮开关（"0" 关；空/其它 = 开，默认开）
)

// ContactView 是前台渲染用的联系方式集合（仅含已配置项）。
type ContactView struct {
	WhatsApp string // 原样展示用（intl 格式）
	WaMe     string // wa.me 链接用的纯数字号码
	Email    string
	Phone    string
	TelURL   template.URL // tel: 链接（html/template 的 URL 过滤器不认 tel:，需显式标记安全；内容仅由数字与 + 构成）
	WeChatQR string
	Float    bool // 浮动按钮开关（默认开）
}

// Any 是否配置了任一联系方式（浮动按钮/询盘区块的渲染门槛）。
func (c *ContactView) Any() bool {
	if c == nil {
		return false
	}
	return c.WhatsApp != "" || c.Email != "" || c.Phone != "" || c.WeChatQR != ""
}

// waDigits 把号码收敛为 wa.me 可用的纯数字（丢弃 +、空格、连字符、括号）。
func waDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// telHref 生成 tel: 链接：保留前导 + 与数字，去掉展示用分隔符。
// 返回 template.URL：值只可能由 "tel:"、"+"、数字构成，标记安全没有注入面。
func telHref(s string) template.URL {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	digits := waDigits(s)
	if digits == "" {
		return ""
	}
	if strings.HasPrefix(s, "+") {
		return template.URL("tel:+" + digits)
	}
	return template.URL("tel:" + digits)
}

// contactView 从本站 settings 构建前台联系方式视图；一项都没配置返回 nil。
func (s *Server) contactView() *ContactView {
	c := &ContactView{
		WhatsApp: strings.TrimSpace(s.store.Setting(contactWhatsAppSetting)),
		Email:    strings.TrimSpace(s.store.Setting(contactEmailSetting)),
		Phone:    strings.TrimSpace(s.store.Setting(contactPhoneSetting)),
		WeChatQR: strings.TrimSpace(s.store.Setting(contactWeChatQRSetting)),
		Float:    s.store.Setting(contactFloatSetting) != "0", // 默认开
	}
	if !c.Any() {
		return nil
	}
	c.WaMe = waDigits(c.WhatsApp)
	c.TelURL = telHref(c.Phone)
	return c
}

// InquiryView 是 product 详情页「询盘」区块的数据（只含已配置项的目标链接）。
type InquiryView struct {
	WhatsAppURL string // wa.me/{号码}?text=预填（产品名 + 页面 URL）
	MailtoURL   string // mailto:{email}?subject=产品名
	Phone       string
	TelURL      template.URL // 见 telHref：tel: 需显式标记安全
	WeChatQR    string
}

// mailtoEscape 按 mailto / wa.me 查询串的习惯转义（空格用 %20 而非 +）：
// WhatsApp click-to-chat 对 + 的解码在移动端 deep link 上不可靠，商品名几乎必含空格，
// 用 QueryEscape 的 + 会让买家看到满屏加号。
func mailtoEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// inquiryView 为一条内容构建询盘区块数据；未配置任何联系方式返回 nil。
func inquiryView(c *ContactView, prefillText, subject string) *InquiryView {
	if !c.Any() {
		return nil
	}
	iv := &InquiryView{Phone: c.Phone, TelURL: c.TelURL, WeChatQR: c.WeChatQR}
	if c.WaMe != "" {
		iv.WhatsAppURL = "https://wa.me/" + c.WaMe + "?text=" + mailtoEscape(prefillText)
	}
	if c.Email != "" {
		iv.MailtoURL = "mailto:" + c.Email + "?subject=" + mailtoEscape(subject)
	}
	return iv
}

// ---------- 后台设置 ----------

// applyContactSettingsForm 校验并落库联系方式设置。返回非空字符串 = 表单错误（不写库）。
func (s *Server) applyContactSettingsForm(r *http.Request) string {
	whatsapp := strings.TrimSpace(r.FormValue("contact_whatsapp"))
	if whatsapp != "" {
		if d := waDigits(whatsapp); len(d) < 5 || len(d) > 15 {
			return "WhatsApp 号码格式不正确：请填写含国家码的国际格式，例如 +86 138 0013 8000。"
		}
	}
	email := strings.TrimSpace(r.FormValue("contact_email"))
	if email != "" {
		if _, err := mail.ParseAddress(email); err != nil {
			return "联系邮箱格式不正确，例如 sales@example.com。"
		}
	}
	phone := strings.TrimSpace(r.FormValue("contact_phone"))
	if phone != "" && waDigits(phone) == "" {
		return "联系电话需要包含数字。"
	}
	qr := strings.TrimSpace(r.FormValue("contact_wechat_qr"))
	if qr != "" && !strings.HasPrefix(qr, "/") && !strings.HasPrefix(qr, "http://") && !strings.HasPrefix(qr, "https://") {
		return "微信二维码需要是上传后的图片地址或 http(s) 绝对地址。"
	}
	float := "0"
	if r.FormValue("contact_float") == "1" {
		float = "1"
	}
	_ = s.store.SetSetting(contactWhatsAppSetting, whatsapp)
	_ = s.store.SetSetting(contactEmailSetting, email)
	_ = s.store.SetSetting(contactPhoneSetting, phone)
	_ = s.store.SetSetting(contactWeChatQRSetting, qr)
	_ = s.store.SetSetting(contactFloatSetting, float)
	s.clearGeneratedCaches()
	return ""
}

// adminSaveContact POST /admin/settings/contact：站点设置页保存联系方式。
func (s *Server) adminSaveContact(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if msg := s.applyContactSettingsForm(r); msg != "" {
		s.showSettings(w, r, "contact", "", msg)
		return
	}
	s.redirectSettings(w, r, "contact", "联系方式已保存。")
}
