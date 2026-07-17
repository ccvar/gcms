package web

import (
	"sort"
	"strings"
)

// 「扩展」内容类型引擎 —— 注册表。
//
// 设计要点（详见多站点安全版方案）：
//   - 类型「定义」是全局的：写在代码里，随单一二进制被所有站点共享。
//   - 类型「启用」是每站独立的：存在各站点库的 settings(enabled_content_types) 里，
//     与 settings.theme 同构。扩展类型默认全部关闭，需在后台按站点开启。
//   - 内置 post/page/link 标记 Builtin：仅作为枚举元数据，仍走各自既有的
//     handler/模板，不经过通用机制（不改动现网行为）。
//
// 这份字段 schema 是「单一事实源」：同时驱动后台动态表单、静态导出/搜索、
// API 校验，以及交给 AI 运营时的工具契约。

// FieldType 是扩展内容类型自定义字段的数据类型。
type FieldType string

const (
	FieldText     FieldType = "text"
	FieldTextarea FieldType = "textarea"
	FieldMarkdown FieldType = "markdown"
	FieldNumber   FieldType = "number"
	FieldDatetime FieldType = "datetime"
	FieldURL      FieldType = "url"
	FieldSelect   FieldType = "select"
	FieldBool     FieldType = "bool"
	FieldImage    FieldType = "image"    // 单图（存 URL）
	FieldGallery  FieldType = "gallery"  // 多图（存 URL 数组）
	FieldRelation FieldType = "relation" // 关联其它内容（如文档上级）——渲染在 Phase 2
	FieldRepeater FieldType = "repeater" // 键值重复块（如商品规格）——渲染在 Phase 2
)

// FieldOption 是 select 字段的单个选项。
type FieldOption struct {
	Value  string
	Labels map[string]string
}

// Field 描述一个自定义字段（存入 posts.extra JSON 的一个键）。
type Field struct {
	Key        string            // extra JSON 中的键名
	Labels     map[string]string // 各语种显示名 {zh:..,en:..}
	Type       FieldType
	Required   bool
	Options    []FieldOption     // 仅 select
	Localized  bool              // true=按语种各填；false=语义上各语种共用（价格/图集/时间等）——注意：非持续同步，仅在「翻译为 X」创建译文时复制一份初始值，之后各语种独立维护
	InList     bool              // 是否在后台列表/归档卡片作为一列展示
	Structural bool              // 结构性字段（如层级父级/排序），不在前台作为内容字段展示
	Help       map[string]string // 帮助文本（各语种）
}

// Label 返回字段在指定语种下的显示名（回退 zh → en → key）。
func (f Field) Label(lang string) string { return pickLabel(f.Labels, lang, f.Key) }

// ContentType 是一种内容类型的完整定义。
type ContentType struct {
	Key            string            // 唯一标识，对应 posts.type，如 "product"
	Names          map[string]string // 各语种名称
	Icon           string            // tabler 图标名（后台菜单/列表用）
	URLPrefix      string            // 公开路由前缀，如 "products" → /products/{slug}
	Fields         []Field           // 自定义字段（内置 title/正文/封面等不在此列）
	HasCategory    bool              // 是否启用分类（kind=Key 的分类树）
	Multilingual   bool              // 是否多语言（复用 trans_group 机制）
	Searchable     bool              // 是否进站内搜索索引
	Hierarchical   bool              // 是否树形（文档上级）
	ListTemplate   string            // 列表/归档模板名；空则回退 generic_list
	DetailTemplate string            // 详情模板名；空则回退 generic_detail
	Builtin        bool              // 内置类型：仅枚举元数据，不可禁用，不走通用机制
	DefaultOn      bool              // 新建站点是否默认启用（扩展类型默认 false）
	Custom         bool              // 数据库自定义类型（可在设计器中编辑/删除）；代码注册类型恒为 false

	// Primary：站点启用该类型后，它在后台左侧上浮为一级菜单（与文章/页面平级），
	// 而不是只藏在「扩展」hub 里。机制是通用的（任何标记 Primary 的启用类型都会上浮），
	// P1 只给 product 打标；未来可放开为按站点配置（如 settings 里存 primary 类型集合）。
	Primary bool
}

// Name 返回类型在指定语种下的名称（回退 zh → en → key）。
func (ct *ContentType) Name(lang string) string { return pickLabel(ct.Names, lang, ct.Key) }

// FieldByKey 按 key 取字段定义，未找到返回 nil。
func (ct *ContentType) FieldByKey(key string) *Field {
	for i := range ct.Fields {
		if ct.Fields[i].Key == key {
			return &ct.Fields[i]
		}
	}
	return nil
}

// FieldValue 是渲染期一个自定义字段的展示值（供通用模板 generic_* 使用）。
// 由 Phase 1 的处理器从 posts.extra 解析后填充。
type FieldValue struct {
	Key   string
	Label string
	Type  string      // 字段类型字符串（text/number/url/gallery…），便于模板比较
	Text  string      // 标量展示值
	List  []string    // 多值（gallery/images）
	Pairs []FieldPair // 键值重复块（repeater，如商品规格）
}

// FieldPair 是 repeater 字段里的一项键值对。
type FieldPair struct {
	K string
	V string
}

// contentTypes 是引擎内置注册的全部类型。
var contentTypes = []*ContentType{
	{
		Key: "post", Names: map[string]string{"zh": "文章", "en": "Articles"},
		Icon: "ti-article", URLPrefix: "posts",
		HasCategory: true, Multilingual: true, Searchable: true,
		Builtin: true, DefaultOn: true,
	},
	{
		Key: "page", Names: map[string]string{"zh": "页面", "en": "Pages"},
		Icon: "ti-file", URLPrefix: "",
		Multilingual: true, Searchable: true,
		Builtin: true, DefaultOn: true,
	},
	{
		// 链接的目标网址用专用列 link_url，不在 extra 里；这里仅作枚举登记。
		Key: "link", Names: map[string]string{"zh": "链接", "en": "Links"},
		Icon: "ti-link", URLPrefix: "links",
		HasCategory: true, Multilingual: true, Searchable: true,
		Builtin: true, DefaultOn: true,
	},

	// ---- 扩展类型（默认对站点关闭，需在后台「扩展」中启用）----
	{
		Key: "product", Names: map[string]string{"zh": "商品", "en": "Products"},
		Icon: "ti-shopping-bag", URLPrefix: "products",
		HasCategory: true, Multilingual: true, Searchable: true,
		Primary: true, // 工厂/外贸站方向：商品与文章平级，启用后上浮为后台一级菜单
		Fields: []Field{
			// 价格是自由文本：外贸报价离不开币种/单位/「面议」（US$12.5/pc、Negotiable…），
			// 裸数字反而误导。留空 = 前台不显示价格行。历史 number 数据经 scalarString 原样渲染，兼容。
			{Key: "price", Labels: map[string]string{"zh": "价格", "en": "Price"}, Type: FieldText, InList: true,
				Help: map[string]string{
					"zh": "自由文本，如「US$ 12.5/pc」「面议」；留空则前台不显示价格。",
					"en": "Free text, e.g. \"US$ 12.5/pc\" or \"Negotiable\"; leave empty to hide.",
				}},
			{Key: "gallery", Labels: map[string]string{"zh": "图集", "en": "Gallery"}, Type: FieldGallery},
			{Key: "specs", Labels: map[string]string{"zh": "规格参数", "en": "Specs"}, Type: FieldRepeater, Localized: true},
		},
	},
	{
		Key: "doc", Names: map[string]string{"zh": "文档", "en": "Docs"},
		Icon: "ti-book", URLPrefix: "docs",
		Multilingual: true, Searchable: true, Hierarchical: true,
		Fields: []Field{
			{Key: "parent", Labels: map[string]string{"zh": "上级文档", "en": "Parent"}, Type: FieldRelation, Structural: true},
			{Key: "order", Labels: map[string]string{"zh": "排序", "en": "Order"}, Type: FieldNumber, Structural: true},
		},
	},
	{
		Key: "event", Names: map[string]string{"zh": "活动", "en": "Events"},
		Icon: "ti-calendar-event", URLPrefix: "events",
		HasCategory: true, Multilingual: true, Searchable: true,
		Fields: []Field{
			{Key: "start_at", Labels: map[string]string{"zh": "开始时间", "en": "Start"}, Type: FieldDatetime, InList: true},
			{Key: "end_at", Labels: map[string]string{"zh": "结束时间", "en": "End"}, Type: FieldDatetime},
			{Key: "location", Labels: map[string]string{"zh": "地点", "en": "Location"}, Type: FieldText, Localized: true, InList: true},
			{Key: "signup_url", Labels: map[string]string{"zh": "报名链接", "en": "Sign-up URL"}, Type: FieldURL},
		},
	},
	{
		Key: "gallery", Names: map[string]string{"zh": "相册", "en": "Galleries"},
		Icon: "ti-photo", URLPrefix: "gallery",
		HasCategory: true, Multilingual: true, Searchable: true,
		Fields: []Field{
			{Key: "images", Labels: map[string]string{"zh": "图片集", "en": "Images"}, Type: FieldGallery},
		},
	},
}

var contentTypeIndex = func() map[string]*ContentType {
	m := make(map[string]*ContentType, len(contentTypes))
	for _, ct := range contentTypes {
		m[ct.Key] = ct
	}
	return m
}()

// contentTypeByKey 按 key 查注册表，未注册返回 nil。
func contentTypeByKey(key string) *ContentType { return contentTypeIndex[strings.TrimSpace(key)] }

// extContentTypes 返回全部「扩展」类型（不含内置 post/page/link）。
func extContentTypes() []*ContentType {
	out := make([]*ContentType, 0, len(contentTypes))
	for _, ct := range contentTypes {
		if !ct.Builtin {
			out = append(out, ct)
		}
	}
	return out
}

// ---------- 每站启用 ----------

const enabledContentTypesKey = "enabled_content_types"

// parseEnabledTypes 解析逗号分隔的启用列表。
func parseEnabledTypes(raw string) map[string]bool {
	out := map[string]bool{}
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = true
		}
	}
	return out
}

// enabledTypeSet 返回当前站点（s.store 为每站独立）已启用的扩展类型键集合。
func (s *Server) enabledTypeSet() map[string]bool {
	return parseEnabledTypes(s.store.Setting(enabledContentTypesKey))
}

// activeExtContentTypes 返回当前站点「已注册且已启用」的扩展内容类型。
// 供 Phase 1+ 的路由、静态导出、搜索索引与 API 枚举使用。
func (s *Server) activeExtContentTypes() []*ContentType {
	enabled := s.enabledTypeSet()
	all := s.allExtTypes()
	out := make([]*ContentType, 0, len(all))
	for _, ct := range all {
		if enabled[ct.Key] {
			out = append(out, ct)
		}
	}
	return out
}

// contentTypeActive 判断某扩展类型是否对当前站点启用。
// 内置类型恒返回 false：它们不经此启用机制（始终可用、走既有路径）。
func (s *Server) contentTypeActive(key string) bool {
	ct := s.lookupType(key)
	if ct == nil || ct.Builtin {
		return false
	}
	return s.enabledTypeSet()[key]
}

// pickLabel 按 lang → zh → en → fallback 的顺序取一个非空标签。
func pickLabel(m map[string]string, lang, fallback string) string {
	if m != nil {
		if v := strings.TrimSpace(m[lang]); v != "" {
			return v
		}
		if v := strings.TrimSpace(m["zh"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(m["en"]); v != "" {
			return v
		}
	}
	return fallback
}

// ExtTypeRow 是后台「扩展」hub 里的一行：类型 + 启用状态 + 内容数。
type ExtTypeRow struct {
	Key          string
	Name         string
	Icon         string
	URLPrefix    string
	Enabled      bool
	Primary      bool // 标记 Primary 的类型：启用后已上浮为一级菜单，整卡迁出 hub（见 hubExtTypeRows）
	Count        int
	Custom       bool              // 数据库自定义类型（可编辑/删除）
	ArchiveTitle map[string]string // 归档页自定义标题（按语种），供 hub 弹窗回填
	ArchiveIntro map[string]string // 归档页自定义简介（按语种）
}

// extTypeRows 构建当前站点的扩展类型行（含启用状态与内容数），供后台 hub 渲染。
func (s *Server) extTypeRows(lang string) []ExtTypeRow {
	enabled := s.enabledTypeSet()
	all := s.allExtTypes()
	meta := s.extArchiveMetaAll()
	rows := make([]ExtTypeRow, 0, len(all))
	for _, ct := range all {
		n := 0
		if list, _ := s.store.ListAllByType(ct.Key, lang); list != nil {
			n = len(list)
		}
		row := ExtTypeRow{
			Key: ct.Key, Name: ct.Name(lang), Icon: ct.Icon,
			URLPrefix: ct.URLPrefix, Enabled: enabled[ct.Key], Primary: ct.Primary, Count: n,
			Custom:       ct.Custom,
			ArchiveTitle: map[string]string{},
			ArchiveIntro: map[string]string{},
		}
		if m, ok := meta[ct.Key]; ok {
			if m.Title != nil {
				row.ArchiveTitle = m.Title
			}
			if m.Intro != nil {
				row.ArchiveIntro = m.Intro
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// hubExtTypeRows 返回扩展 hub 实际展示的行。已启用且标记 Primary 的类型已整体迁出
// hub：内容入口在左侧一级菜单，类型管理（归档页文案/停用/编辑类型）在其列表页的
// 设置菜单——hub 里不再出现任何卡；停用后（或非 Primary）照旧显示。机制泛化，不硬编码类型。
func (s *Server) hubExtTypeRows(lang string) []ExtTypeRow {
	all := s.extTypeRows(lang)
	out := make([]ExtTypeRow, 0, len(all))
	for _, row := range all {
		if row.Enabled && row.Primary {
			continue
		}
		out = append(out, row)
	}
	return out
}

// ExtNavItem 是后台左侧一级菜单里一个上浮的扩展类型入口（如「商品」）。
type ExtNavItem struct {
	Key  string
	Name string
}

// primaryExtNav 返回当前站点「已启用且标记 Primary」的扩展类型，按注册顺序，
// 供后台布局把它们渲染成与文章/页面平级的一级菜单。
func (s *Server) primaryExtNav(adminLang string) []ExtNavItem {
	var out []ExtNavItem
	for _, ct := range s.activeExtContentTypes() {
		if ct.Primary {
			out = append(out, ExtNavItem{Key: ct.Key, Name: ct.Name(adminLang)})
		}
	}
	return out
}

// extCategoryKinds 返回「已启用且支持分类」的扩展类型（含数据库自定义类型），
// 供分类设置页在文章/链接之外泛化出各自的分类管理小节（如「商品分类」）。
func (s *Server) extCategoryKinds(adminLang string) []ExtNavItem {
	var out []ExtNavItem
	for _, ct := range s.activeExtContentTypes() {
		if ct.HasCategory {
			out = append(out, ExtNavItem{Key: ct.Key, Name: ct.Name(adminLang)})
		}
	}
	return out
}

// joinEnabledTypes 把启用集合拼成稳定的逗号串（保留全部已启用键，含数据库自定义类型）。
func joinEnabledTypes(enabled map[string]bool) string {
	keys := make([]string, 0, len(enabled))
	for k, on := range enabled {
		if on {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
