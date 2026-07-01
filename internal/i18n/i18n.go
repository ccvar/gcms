// Package i18n 提供前台多语种支持：语种注册表、界面文案目录与每请求的翻译助手 Tr。
//
// 设计要点：
//   - 模板集合在启动时解析一次、全站共享，因此「当前语种」不能放进 FuncMap，
//     而是通过传入模板数据里的 *Tr 携带——模板用 {{.Tr.T "key"}}、{{.Tr.U "/path"}}、
//     {{.Tr.Date .Time}} 访问，语种随请求流动而不需要重解析模板。
//   - 文案目录从 embed 的 locales/<code>.json 加载，新增语言只需加一个 JSON。
//   - 除内置语种外，站点可在后台「语言」分区新增「自定义预设」（存 settings），
//     由 Manager 合并进有效语种表；自定义语种没有界面文案目录，UI 文案回落默认语种。
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed locales/*.json
var localesFS embed.FS

// Locale 描述一个语种的元信息。
type Locale struct {
	Code   string // 路径前缀与 posts.lang 列值，如 zh / en
	Name   string // 原生显示名，如 中文 / English
	Tag    string // BCP47 语言标记，用于 <html lang> / hreflang / inLanguage
	OG     string // Open Graph locale，如 zh_CN
	Custom bool   // 是否为后台新增的自定义预设
	dateFn func(time.Time) string
}

func dateZH(t time.Time) string { return t.Format("2006 年 1 月 2 日") }
func dateEN(t time.Time) string { return t.Format("Jan 2, 2006") }

// registry 是内置可用语种。具体启用哪些、默认哪个，由站点设置决定。
var registry = []Locale{
	{Code: "zh", Name: "中文", Tag: "zh-CN", OG: "zh_CN", dateFn: dateZH},
	{Code: "en", Name: "English", Tag: "en-US", OG: "en_US", dateFn: dateEN},
	{Code: "ja", Name: "日本語", Tag: "ja-JP", OG: "ja_JP", dateFn: dateEN},
	{Code: "ko", Name: "한국어", Tag: "ko-KR", OG: "ko_KR", dateFn: dateEN},
	{Code: "fr", Name: "Français", Tag: "fr-FR", OG: "fr_FR", dateFn: dateEN},
	{Code: "de", Name: "Deutsch", Tag: "de-DE", OG: "de_DE", dateFn: dateEN},
	{Code: "es", Name: "Español", Tag: "es-ES", OG: "es_ES", dateFn: dateEN},
	{Code: "vi", Name: "Tiếng Việt", Tag: "vi-VN", OG: "vi_VN", dateFn: dateEN},
	{Code: "id", Name: "Bahasa Indonesia", Tag: "id-ID", OG: "id_ID", dateFn: dateEN},
	{Code: "th", Name: "ไทย", Tag: "th-TH", OG: "th_TH", dateFn: dateEN},
}

func builtinMeta(code string) (Locale, bool) {
	for _, l := range registry {
		if l.Code == code {
			return l, true
		}
	}
	return Locale{}, false
}

// ValidCode 校验语种码：2–12 位的小写字母/数字/连字符（用作 URL 前缀）。
func ValidCode(code string) bool {
	if len(code) < 2 || len(code) > 12 {
		return false
	}
	for _, r := range code {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

// Manager 持有各语种的文案目录与「自定义预设」。一次构建、全站共享。
type Manager struct {
	cats      map[string]map[string]string // code -> key -> text
	adminCats map[string]map[string]string // code -> admin key -> text
	mu        sync.RWMutex
	custom    []Locale                     // 后台新增的自定义预设
	overrides map[string]map[string]string // code -> key -> text，站点级前台字典覆盖
}

// New 加载 embed 的文案目录。
func New() *Manager {
	m := &Manager{cats: map[string]map[string]string{}, adminCats: loadAdminCatalogs(), overrides: map[string]map[string]string{}}
	entries, _ := localesFS.ReadDir("locales")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		code := strings.TrimSuffix(e.Name(), ".json")
		b, err := localesFS.ReadFile("locales/" + e.Name())
		if err != nil {
			continue
		}
		var kv map[string]string
		if json.Unmarshal(b, &kv) == nil {
			m.cats[code] = kv
		}
	}
	return m
}

// meta 解析某语种码的元信息：先内置后自定义。
func (m *Manager) meta(code string) (Locale, bool) {
	if l, ok := builtinMeta(code); ok {
		return l, true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, l := range m.custom {
		if l.Code == code {
			return l, true
		}
	}
	return Locale{}, false
}

// SetCustom 用解析好的自定义预设替换当前集合（线程安全）。
func (m *Manager) SetCustom(ls []Locale) {
	m.mu.Lock()
	m.custom = ls
	m.mu.Unlock()
}

// LoadCustom 从 settings 里的 JSON 字符串解析并设置自定义预设。
func (m *Manager) LoadCustom(jsonStr string) { m.SetCustom(ParseCustom(jsonStr)) }

// Custom 返回当前自定义预设的拷贝。
func (m *Manager) Custom() []Locale {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Locale, len(m.custom))
	copy(out, m.custom)
	return out
}

type customRaw struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Tag  string `json:"tag"`
	OG   string `json:"og"`
}

// ParseCustom 解析自定义预设 JSON（数组）。过滤非法码、与内置重复的码、重复项。
func ParseCustom(jsonStr string) []Locale {
	if strings.TrimSpace(jsonStr) == "" {
		return nil
	}
	var raw []customRaw
	if json.Unmarshal([]byte(jsonStr), &raw) != nil {
		return nil
	}
	var out []Locale
	seen := map[string]bool{}
	for _, c := range raw {
		c.Code = strings.TrimSpace(strings.ToLower(c.Code))
		if !ValidCode(c.Code) || seen[c.Code] {
			continue
		}
		if _, isBuiltin := builtinMeta(c.Code); isBuiltin {
			continue
		}
		seen[c.Code] = true
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = c.Code
		}
		tag := strings.TrimSpace(c.Tag)
		if tag == "" {
			tag = c.Code
		}
		og := strings.TrimSpace(c.OG)
		if og == "" {
			og = strings.ReplaceAll(tag, "-", "_")
		}
		out = append(out, Locale{Code: c.Code, Name: name, Tag: tag, OG: og, Custom: true, dateFn: dateEN})
	}
	return out
}

// MarshalCustom 把自定义预设序列化回 JSON（存 settings）。
func MarshalCustom(ls []Locale) string {
	raw := make([]customRaw, 0, len(ls))
	for _, l := range ls {
		raw = append(raw, customRaw{Code: l.Code, Name: l.Name, Tag: l.Tag, OG: l.OG})
	}
	b, _ := json.Marshal(raw)
	return string(b)
}

// ParseCatalog 解析单个语种的前台字典 JSON。空输入或空对象表示清空覆盖。
func ParseCatalog(jsonStr string) (map[string]string, error) {
	jsonStr = strings.TrimSpace(jsonStr)
	if jsonStr == "" {
		return nil, nil
	}
	var kv map[string]string
	if err := json.Unmarshal([]byte(jsonStr), &kv); err != nil {
		return nil, err
	}
	return SanitizeCatalog(kv), nil
}

// SanitizeCatalog 规范化前台字典：去掉空 key，保留空 value 用于显式覆盖为空文案。
func SanitizeCatalog(kv map[string]string) map[string]string {
	if len(kv) == 0 {
		return nil
	}
	out := make(map[string]string, len(kv))
	for k, v := range kv {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MarshalCatalog 把单个语种字典格式化为便于后台编辑的 JSON。
func MarshalCatalog(kv map[string]string) string {
	if kv == nil {
		kv = map[string]string{}
	}
	b, _ := json.MarshalIndent(kv, "", "  ")
	return string(b)
}

// ParseCatalogOverrides 解析站点级前台字典覆盖（settings.locale_catalogs）。
func ParseCatalogOverrides(jsonStr string) map[string]map[string]string {
	if strings.TrimSpace(jsonStr) == "" {
		return nil
	}
	var raw map[string]map[string]string
	if json.Unmarshal([]byte(jsonStr), &raw) != nil {
		return nil
	}
	out := map[string]map[string]string{}
	for code, cat := range raw {
		code = strings.ToLower(strings.TrimSpace(code))
		if !ValidCode(code) {
			continue
		}
		if clean := SanitizeCatalog(cat); len(clean) > 0 {
			out[code] = clean
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MarshalCatalogOverrides 把站点级前台字典覆盖序列化回 settings。
func MarshalCatalogOverrides(cats map[string]map[string]string) string {
	if len(cats) == 0 {
		return "{}"
	}
	clean := map[string]map[string]string{}
	for code, cat := range cats {
		code = strings.ToLower(strings.TrimSpace(code))
		if !ValidCode(code) {
			continue
		}
		if c := SanitizeCatalog(cat); len(c) > 0 {
			clean[code] = c
		}
	}
	if len(clean) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(clean)
	return string(b)
}

// SetCatalogOverrides 用解析好的站点级前台字典覆盖替换当前集合（线程安全）。
func (m *Manager) SetCatalogOverrides(cats map[string]map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overrides = cloneNestedCatalog(cats)
}

// LoadCatalogOverrides 从 settings JSON 中加载站点级前台字典覆盖。
func (m *Manager) LoadCatalogOverrides(jsonStr string) {
	m.SetCatalogOverrides(ParseCatalogOverrides(jsonStr))
}

// CatalogOverride 返回某语种的覆盖字典拷贝。
func (m *Manager) CatalogOverride(code string) map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneCatalog(m.overrides[strings.ToLower(strings.TrimSpace(code))])
}

// CatalogSource 返回某语种前台字典的主要来源：custom 表示存在站点覆盖。
func (m *Manager) CatalogSource(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	m.mu.RLock()
	if len(m.overrides[code]) > 0 {
		m.mu.RUnlock()
		return "custom"
	}
	m.mu.RUnlock()
	if len(m.cats[code]) > 0 {
		return "builtin"
	}
	return "fallback"
}

// Catalog 返回某语种可生效的完整前台字典：英文/中文兜底、默认语种、目标语种、站点覆盖依次叠加。
func (m *Manager) Catalog(code, defaultCode string) map[string]string {
	code = m.Locale(code).Code
	defaultCode = strings.ToLower(strings.TrimSpace(defaultCode))
	out := map[string]string{}
	mergeCatalog(out, m.cats["en"])
	mergeCatalog(out, m.cats["zh"])
	mergeCatalog(out, m.cats[defaultCode])
	mergeCatalog(out, m.CatalogOverride(defaultCode))
	mergeCatalog(out, m.cats[code])
	mergeCatalog(out, m.CatalogOverride(code))
	return out
}

// CatalogKeyCount 返回某语种可生效的前台字典 key 数。
func (m *Manager) CatalogKeyCount(code, defaultCode string) int {
	return len(m.Catalog(code, defaultCode))
}

func cloneCatalog(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneNestedCatalog(in map[string]map[string]string) map[string]map[string]string {
	if len(in) == 0 {
		return map[string]map[string]string{}
	}
	out := make(map[string]map[string]string, len(in))
	for code, cat := range in {
		if clean := cloneCatalog(cat); len(clean) > 0 {
			out[code] = clean
		}
	}
	return out
}

func mergeCatalog(dst, src map[string]string) {
	for k, v := range src {
		if k != "" {
			dst[k] = v
		}
	}
}

// Known 报告某语种码是否可用（内置或自定义）。
func (m *Manager) Known(code string) bool { _, ok := m.meta(code); return ok }

// Locale 取某语种的元信息（未知时回退默认 zh）。
func (m *Manager) Locale(code string) Locale {
	if l, ok := m.meta(code); ok {
		return l
	}
	l, _ := builtinMeta("zh")
	return l
}

// All 返回内置 + 自定义的全部语种（供后台「语言」勾选）。
func (m *Manager) All() []Locale {
	out := make([]Locale, len(registry))
	copy(out, registry)
	return append(out, m.Custom()...)
}

// Active 解析启用的语种列表（逗号分隔，首个为默认）。过滤未知项；为空时回退 [zh]。
func (m *Manager) Active(conf string) []Locale {
	var out []Locale
	seen := map[string]bool{}
	for _, c := range strings.Split(conf, ",") {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if l, ok := m.meta(c); ok {
			out = append(out, l)
			seen[c] = true
		}
	}
	if len(out) == 0 {
		l, _ := builtinMeta("zh")
		out = []Locale{l}
	}
	return out
}

// Default 返回启用列表里的默认语种码（首个）。
func (m *Manager) Default(conf string) string { return m.Active(conf)[0].Code }

// Tr 是「绑定到某语种」的翻译助手，随每个请求构建并放进模板数据。
type Tr struct {
	Loc              Locale
	prefix           string
	cat              map[string]string
	override         map[string]string
	fallback         map[string]string
	fallbackOverride map[string]string
	zh               map[string]string
	en               map[string]string
}

// Tr 构建某语种的助手；defaultCode 用作文案回退。
func (m *Manager) Tr(code, defaultCode string) *Tr {
	loc := m.Locale(code)
	return &Tr{
		Loc:              loc,
		prefix:           "/" + loc.Code,
		cat:              m.cats[loc.Code],
		override:         m.CatalogOverride(loc.Code),
		fallback:         m.cats[defaultCode],
		fallbackOverride: m.CatalogOverride(defaultCode),
		zh:               m.cats["zh"],
		en:               m.cats["en"],
	}
}

// 下列方法供模板直接调用。

func (t *Tr) Lang() string   { return t.Loc.Code }
func (t *Tr) Name() string   { return t.Loc.Name }
func (t *Tr) Tag() string    { return t.Loc.Tag }
func (t *Tr) OG() string     { return t.Loc.OG }
func (t *Tr) Prefix() string { return t.prefix }

// WithPrefix 返回一个使用自定义 URL 前缀的翻译助手，供站点预览等带壳路由复用。
func (t *Tr) WithPrefix(prefix string) *Tr {
	if t == nil {
		return nil
	}
	cp := *t
	cp.prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
	if cp.prefix == "" {
		cp.prefix = "/" + cp.Loc.Code
	}
	return &cp
}

// T 取一条界面文案：先查本语种，缺失回退默认语种，再回退内置中/英文，最后返回 key 本身。
func (t *Tr) T(key string) string {
	if t.override != nil {
		if v, ok := t.override[key]; ok {
			return v
		}
	}
	if t.cat != nil {
		if v, ok := t.cat[key]; ok && v != "" {
			return v
		}
	}
	if t.fallbackOverride != nil {
		if v, ok := t.fallbackOverride[key]; ok {
			return v
		}
	}
	if t.fallback != nil {
		if v, ok := t.fallback[key]; ok && v != "" {
			return v
		}
	}
	if t.zh != nil {
		if v, ok := t.zh[key]; ok && v != "" {
			return v
		}
	}
	if t.en != nil {
		if v, ok := t.en[key]; ok && v != "" {
			return v
		}
	}
	return key
}

// Tf 取一条带 fmt 占位符的文案并格式化（如 "找到 %d 篇"）。
func (t *Tr) Tf(key string, args ...any) string { return fmt.Sprintf(t.T(key), args...) }

// U 把站内路径加上语种前缀：U("/") -> "/zh/"，U("/posts/x") -> "/zh/posts/x"。
func (t *Tr) U(p string) string {
	if p == "" || p == "/" {
		return t.prefix + "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return t.prefix + p
}

// Date 按语种格式化日期。
func (t *Tr) Date(tm time.Time) string {
	if tm.IsZero() {
		return ""
	}
	if t.Loc.dateFn != nil {
		return t.Loc.dateFn(tm)
	}
	return tm.Format("2006-01-02")
}

// ISODate 机器可读日期（与语种无关）。
func (t *Tr) ISODate(tm time.Time) string {
	if tm.IsZero() {
		return ""
	}
	return tm.Format("2006-01-02")
}

// SortLocales 按内置注册顺序排序一组语种码（自定义码排在末尾，稳定保序）。
func SortLocales(codes []string) []string {
	order := map[string]int{}
	for i, l := range registry {
		order[l.Code] = i
	}
	ord := func(c string) int {
		if v, ok := order[c]; ok {
			return v
		}
		return 1000
	}
	out := append([]string{}, codes...)
	sort.SliceStable(out, func(i, j int) bool { return ord(out[i]) < ord(out[j]) })
	return out
}
