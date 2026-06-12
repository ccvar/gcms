package web

import (
	"encoding/json"
	"html/template"
	"net/url"
	"strings"
)

// SocialLink 是页脚「关注」栏的一条社交链接。
type SocialLink struct {
	URL   string        // 链接地址（含 mailto:）
	Label string        // 用户填写的原始名称（可空，供后台表单回填）
	Name  string        // 展示名（为空时按平台/域名自动命名）
	Icon  template.HTML // 按域名识别的图标（内联 SVG）
	Blank bool          // 是否新窗口打开（mailto 不开新窗口）
}

// 品牌 / 通用图标（18×18，currentColor）。后台录入，内联渲染。
const (
	svgGitHub    = `<svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12 2C6.48 2 2 6.58 2 12.26c0 4.52 2.87 8.36 6.84 9.72.5.1.68-.22.68-.49l-.01-1.9c-2.78.62-3.37-1.2-3.37-1.2-.46-1.18-1.11-1.49-1.11-1.49-.91-.63.07-.62.07-.62 1 .07 1.53 1.05 1.53 1.05.9 1.56 2.36 1.11 2.94.85.09-.66.35-1.11.63-1.36-2.22-.26-4.56-1.14-4.56-5.07 0-1.12.39-2.03 1.03-2.75-.1-.26-.45-1.3.1-2.71 0 0 .84-.27 2.75 1.05a9.4 9.4 0 0 1 5 0c1.91-1.32 2.75-1.05 2.75-1.05.55 1.41.2 2.45.1 2.71.64.72 1.03 1.63 1.03 2.75 0 3.94-2.34 4.81-4.57 5.06.36.32.68.94.68 1.9l-.01 2.82c0 .27.18.59.69.49A10.04 10.04 0 0 0 22 12.26C22 6.58 17.52 2 12 2Z"/></svg>`
	svgX         = `<svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M18.24 2.25h3.31l-7.23 8.26 8.5 11.24h-6.66l-5.21-6.82-5.97 6.82H1.68l7.73-8.84L1.25 2.25h6.83l4.71 6.23 5.45-6.23Zm-1.16 17.52h1.83L7.08 4.13H5.12L17.08 19.77Z"/></svg>`
	svgYouTube   = `<svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M23 12s0-3.5-.45-5.18a2.6 2.6 0 0 0-1.83-1.84C18.96 4.5 12 4.5 12 4.5s-6.96 0-8.72.48A2.6 2.6 0 0 0 1.45 6.82 27.4 27.4 0 0 0 1 12c0 1.7.45 5.18.45 5.18a2.6 2.6 0 0 0 1.83 1.84C5.04 19.5 12 19.5 12 19.5s6.96 0 8.72-.48a2.6 2.6 0 0 0 1.83-1.84C23 15.5 23 12 23 12Zm-13 3.27V8.73L15.5 12 10 15.27Z"/></svg>`
	svgTelegram  = `<svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M9.78 18.65l.28-4.23 7.68-6.92c.34-.31-.07-.46-.52-.19L7.74 13.3 3.64 12c-.88-.25-.89-.86.2-1.3l15.97-6.16c.73-.33 1.43.18 1.15 1.3l-2.72 12.81c-.19.91-.74 1.13-1.5.71L12.6 16.3l-1.99 1.93c-.23.23-.42.42-.83.42z"/></svg>`
	svgLinkedIn  = `<svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M19 3a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h14ZM8.34 9.5H6v8.16h2.34V9.5Zm-1.17-3.7a1.36 1.36 0 1 0 0 2.72 1.36 1.36 0 0 0 0-2.72ZM18 13.6c0-2.17-1.16-3.18-2.7-3.18-1.25 0-1.8.69-2.12 1.17V9.5h-2.34v8.16h2.34v-4.3c0-.23.02-.46.08-.62.18-.45.6-.92 1.28-.92.9 0 1.26.69 1.26 1.69v4.15H18V13.6Z"/></svg>`
	svgInstagram = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="18" height="18" rx="5"/><circle cx="12" cy="12" r="3.8"/><circle cx="17.3" cy="6.7" r="1.1" fill="currentColor" stroke="none"/></svg>`
	svgMail      = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/></svg>`
	svgRSS       = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 11a9 9 0 0 1 9 9"/><path d="M4 4a16 16 0 0 1 16 16"/><circle cx="5" cy="19" r="1.4" fill="currentColor" stroke="none"/></svg>`
	svgLink      = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1.5 1.5"/><path d="M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1.5-1.5"/></svg>`
)

// socialMeta 按链接识别平台名与图标。
func socialMeta(raw string) (string, template.HTML) {
	low := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(low, "mailto:") {
		return "Email", template.HTML(svgMail)
	}
	host := ""
	if u, err := url.Parse(raw); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	host = strings.TrimPrefix(host, "www.")
	switch {
	case strings.Contains(host, "github.com"):
		return "GitHub", template.HTML(svgGitHub)
	case strings.Contains(host, "x.com"), strings.Contains(host, "twitter.com"):
		return "X", template.HTML(svgX)
	case strings.Contains(host, "youtube."), strings.Contains(host, "youtu.be"):
		return "YouTube", template.HTML(svgYouTube)
	case strings.Contains(host, "t.me"), strings.Contains(host, "telegram"):
		return "Telegram", template.HTML(svgTelegram)
	case strings.Contains(host, "linkedin."):
		return "LinkedIn", template.HTML(svgLinkedIn)
	case strings.Contains(host, "weibo."):
		return "微博", template.HTML(svgLink)
	case strings.Contains(host, "zhihu."):
		return "知乎", template.HTML(svgLink)
	case strings.Contains(host, "bilibili."):
		return "哔哩哔哩", template.HTML(svgLink)
	case strings.Contains(host, "instagram."):
		return "Instagram", template.HTML(svgInstagram)
	case strings.Contains(host, "facebook."):
		return "Facebook", template.HTML(svgLink)
	case strings.Contains(host, "discord"):
		return "Discord", template.HTML(svgLink)
	case strings.Contains(host, "mastodon"), strings.Contains(host, "@"):
		return "Mastodon", template.HTML(svgLink)
	}
	if host != "" {
		return host, template.HTML(svgLink)
	}
	return "链接", template.HTML(svgLink)
}

// parseSocialLinks 解析 settings.social_links（JSON 数组）。
func parseSocialLinks(s string) []SocialLink {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var raw []struct {
		URL   string `json:"url"`
		Label string `json:"label"`
	}
	if json.Unmarshal([]byte(s), &raw) != nil {
		return nil
	}
	var out []SocialLink
	for _, r := range raw {
		u := strings.TrimSpace(r.URL)
		if u == "" {
			continue
		}
		platform, icon := socialMeta(u)
		name := strings.TrimSpace(r.Label)
		if name == "" {
			name = platform
		}
		out = append(out, SocialLink{
			URL:   u,
			Label: strings.TrimSpace(r.Label),
			Name:  name,
			Icon:  icon,
			Blank: !strings.HasPrefix(strings.ToLower(u), "mailto:"),
		})
	}
	return out
}

// buildSocialJSON 把后台表单的并列数组（url[] / label[]）压成 JSON 存储。
func buildSocialJSON(urls, labels []string) string {
	type row struct {
		URL   string `json:"url"`
		Label string `json:"label"`
	}
	var list []row
	for i, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		lbl := ""
		if i < len(labels) {
			lbl = strings.TrimSpace(labels[i])
		}
		list = append(list, row{URL: u, Label: lbl})
	}
	if len(list) == 0 {
		return ""
	}
	b, _ := json.Marshal(list)
	return string(b)
}
