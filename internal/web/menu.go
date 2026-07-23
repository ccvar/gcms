package web

import (
	"encoding/json"
	"net/url"
	"strings"
)

// MenuRow 是导航菜单的一条「原始」配置：URL + 各语种标签（供后台编辑与存储）。
type MenuRow struct {
	URL          string            `json:"url"`
	Labels       map[string]string `json:"labels"`
	TargetValue  string            `json:"-"`
	TargetLabel  string            `json:"-"`
	TargetKind   string            `json:"-"`
	CustomTarget bool              `json:"-"`
}

// MenuTargetOption 是后台导航编辑器里的「指向哪里」选项。
type MenuTargetOption struct {
	Value  string            `json:"value"`
	Label  string            `json:"label"`
	URL    string            `json:"url"`
	Kind   string            `json:"kind"`
	Labels map[string]string `json:"labels"`
}

// MenuItem 是前台页眉渲染用的一项（已按当前语种解析好）。
type MenuItem struct {
	Href     string
	Label    string
	Active   bool
	External bool
	Index    int
}

// parseMenuRows 解析 settings.nav_menu（JSON 数组），过滤空 URL。
func parseMenuRows(s string) []MenuRow {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var rows []MenuRow
	if json.Unmarshal([]byte(s), &rows) != nil {
		return nil
	}
	out := rows[:0]
	for _, m := range rows {
		m.URL = cleanMenuURLValue(m.URL)
		if m.URL != "" {
			out = append(out, m)
		}
	}
	return out
}

// menuRowsConfigured 区分“从未配置导航”和用户明确保存的空数组。
// 前者仍可回落默认导航；后者表示用户确实要隐藏全部导航入口。
func menuRowsConfigured(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") {
		return false
	}
	var rows []MenuRow
	return json.Unmarshal([]byte(s), &rows) == nil
}

// effectiveMenuRows 返回前台当前实际可见、可由 Pilot 管理的导航。
// nav_menu 未配置时，前台会显示默认项，因此管理 API 也必须返回同一组默认项，
// 否则默认入口既看不见也无法走受控删除。
func (s *Server) effectiveMenuRows() (rows []MenuRow, raw string, configured bool) {
	raw = s.store.Setting("nav_menu")
	rows = parseMenuRows(raw)
	configured = menuRowsConfigured(raw)
	if len(rows) > 0 || configured {
		return rows, raw, configured
	}
	return s.menuEditRows(), raw, false
}

// buildMenuJSON 把后台表单的并列数组（nav_url[] + nav_label_<lang>[]）压成 JSON。
func buildMenuJSON(urls []string, labelsByLang map[string][]string) string {
	var list []MenuRow
	for i, u := range urls {
		u = cleanMenuURLValue(u)
		if u == "" {
			continue
		}
		labels := map[string]string{}
		for lang, arr := range labelsByLang {
			if i < len(arr) {
				if v := strings.TrimSpace(arr[i]); v != "" {
					labels[lang] = v
				}
			}
		}
		list = append(list, MenuRow{URL: u, Labels: labels})
	}
	if len(list) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(list)
	return string(b)
}

func cleanMenuURLValue(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	decoded := u
	if v, err := url.PathUnescape(u); err == nil {
		decoded = strings.TrimSpace(v)
	}
	placeholders := map[string]bool{
		"自定义地址":                        true,
		"/docs 或 https://example.com":  true,
		"/docs or https://example.com": true,
	}
	if placeholders[u] || placeholders[decoded] {
		return ""
	}
	if !isExternalURL(u) && !strings.HasPrefix(u, "/") {
		return ""
	}
	return u
}

func isExternalURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "mailto:")
}

func menuURLParts(raw string) (path, full string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || isExternalURL(raw) {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	path = strings.TrimSpace(u.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	full = path
	if u.RawQuery != "" {
		full += "?" + u.RawQuery
	}
	return path, full, true
}

func menuURLMatchesCurrent(raw, currentPath, currentFull string) bool {
	path, full, ok := menuURLParts(raw)
	if !ok {
		return false
	}
	if strings.Contains(raw, "?") {
		return full == currentFull
	}
	return path == currentPath
}

func menuOptionLabels() map[string]string {
	return map[string]string{"__custom__": "自定义站内路径", "__external__": "外部链接"}
}

func decorateMenuRows(rows []MenuRow, targets []MenuTargetOption) []MenuRow {
	byURL := map[string]MenuTargetOption{}
	for _, opt := range targets {
		targetURL := strings.TrimSpace(opt.URL)
		if targetURL != "" {
			byURL[targetURL] = opt
			if slug := strings.TrimPrefix(targetURL, "/links/cat/"); slug != targetURL && slug != "" {
				byURL["/links?cat="+url.QueryEscape(slug)] = opt
			}
		}
	}
	fallback := menuOptionLabels()
	for _, opt := range targets {
		if opt.Value == "__custom__" || opt.Value == "__external__" {
			fallback[opt.Value] = opt.Label
		}
	}
	for i := range rows {
		rows[i].URL = strings.TrimSpace(rows[i].URL)
		if opt, ok := byURL[rows[i].URL]; ok {
			rows[i].URL = opt.URL
			rows[i].TargetValue = opt.Value
			rows[i].TargetLabel = opt.Label
			rows[i].TargetKind = opt.Kind
			continue
		}
		rows[i].CustomTarget = true
		if isExternalURL(rows[i].URL) {
			rows[i].TargetValue = "__external__"
			rows[i].TargetLabel = fallback["__external__"]
			rows[i].TargetKind = "external"
		} else {
			rows[i].TargetValue = "__custom__"
			rows[i].TargetLabel = fallback["__custom__"]
			rows[i].TargetKind = "custom"
		}
	}
	return rows
}

// navKeyOf 把菜单 URL 映射到现有的「当前导航」键，用于高亮当前项。
func navKeyOf(u string) string {
	switch {
	case u == "/":
		return "home"
	case strings.HasPrefix(u, "/category"):
		return "category"
	case strings.HasPrefix(u, "/links"):
		return "links"
	case u == "/about":
		return "about"
	case u == "/start":
		return "start"
	case u == "/search":
		return "search"
	case strings.HasPrefix(u, "/") && !strings.Contains(strings.TrimPrefix(u, "/"), "/"):
		return strings.TrimPrefix(u, "/")
	}
	return ""
}
