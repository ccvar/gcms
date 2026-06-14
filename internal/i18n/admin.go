package i18n

import (
	"embed"
	"encoding/json"
	"sort"
	"strings"
)

//go:embed admin/*.json
var adminFS embed.FS

// AdminTr 是后台界面翻译助手。调用方传入 key 和中文兜底文案：
//
//	{{.Admin.T "admin.nav.posts" "文章"}}
//
// 解析顺序：用户覆盖 -> 当前语种 JSON -> 中文 JSON -> 调用处中文兜底 -> key。
// 因此某个后台语种 JSON 不存在或不完整时，页面仍显示现有中文文案。
type AdminTr struct {
	Lang     Locale
	cat      map[string]string
	zh       map[string]string
	override map[string]string
}

func loadAdminCatalogs() map[string]map[string]string {
	out := map[string]map[string]string{}
	entries, _ := adminFS.ReadDir("admin")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		code := strings.TrimSuffix(e.Name(), ".json")
		b, err := adminFS.ReadFile("admin/" + e.Name())
		if err != nil {
			continue
		}
		var kv map[string]string
		if json.Unmarshal(b, &kv) == nil {
			out[code] = kv
		}
	}
	return out
}

// AdminTr 构建后台界面翻译助手。override 仅包含用户覆盖的 key。
func (m *Manager) AdminTr(code string, override map[string]string) *AdminTr {
	loc := m.Locale(code)
	return &AdminTr{
		Lang:     loc,
		cat:      m.adminCats[loc.Code],
		zh:       m.adminCats["zh"],
		override: override,
	}
}

// AdminLocales 返回内置后台翻译 JSON 已提供的语种列表。中文固定在第一位。
func (m *Manager) AdminLocales() []Locale {
	m.mu.RLock()
	codes := make([]string, 0, len(m.adminCats))
	for code := range m.adminCats {
		codes = append(codes, code)
	}
	m.mu.RUnlock()
	sort.Strings(codes)
	var out []Locale
	add := func(code string) {
		for _, l := range out {
			if l.Code == code {
				return
			}
		}
		if loc, ok := m.meta(code); ok {
			out = append(out, loc)
		}
	}
	add("zh")
	for _, code := range codes {
		add(code)
	}
	if len(out) == 0 {
		loc, _ := builtinMeta("zh")
		out = []Locale{loc}
	}
	return out
}

func (t *AdminTr) T(key, fallback string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return fallback
	}
	if t.override != nil {
		if v := strings.TrimSpace(t.override[key]); v != "" {
			return v
		}
	}
	if t.cat != nil {
		if v := strings.TrimSpace(t.cat[key]); v != "" {
			return v
		}
	}
	if t.zh != nil {
		if v := strings.TrimSpace(t.zh[key]); v != "" {
			return v
		}
	}
	if fallback != "" {
		return fallback
	}
	return key
}

// ParseAdminOverrides 解析用户覆盖的后台翻译 JSON。只保留字符串值，方便白盒维护。
func ParseAdminOverrides(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var kv map[string]string
	if json.Unmarshal([]byte(raw), &kv) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range kv {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}

func MarshalAdminOverrides(kv map[string]string) string {
	if len(kv) == 0 {
		return ""
	}
	b, _ := json.MarshalIndent(kv, "", "  ")
	return string(b)
}
