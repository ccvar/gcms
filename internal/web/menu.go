package web

import (
	"encoding/json"
	"strings"
)

// MenuRow 是导航菜单的一条「原始」配置：URL + 各语种标签（供后台编辑与存储）。
type MenuRow struct {
	URL    string            `json:"url"`
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
	case u == "/search":
		return "search"
	}
	return ""
}
