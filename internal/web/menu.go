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
		if strings.TrimSpace(m.URL) != "" {
			out = append(out, m)
		}
	}
	return out
}

// buildMenuJSON 把后台表单的并列数组（nav_url[] + nav_label_<lang>[]）压成 JSON。
func buildMenuJSON(urls []string, labelsByLang map[string][]string) string {
	var list []MenuRow
	for i, u := range urls {
		u = strings.TrimSpace(u)
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
		return ""
	}
	b, _ := json.Marshal(list)
	return string(b)
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
		if strings.TrimSpace(opt.URL) != "" {
			byURL[opt.URL] = opt
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
