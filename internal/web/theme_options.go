package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// 主题 options schema（工厂/外贸站 P3）。
//
// 设计共识：**入口挂在主题上，数据存在站点上**——
//   - 主题/骨架族在这里声明「消费哪些数据槽」（options schema）；
//   - 外观页按当前主题的 schema 渲染动态表单，保存进站点 settings 的语义键
//     （换主题数据不丢、同骨架族共享；留空 = 回落 i18n/内置默认）；
//   - 文本槽支持 ::lang 按语种覆盖（同 copyKey/localizedSetting 约定）；
//   - AI 经既有 settings 写路径（PATCH /site-profile 的 factory_* 字段，见 api.go）
//     写同一批键，automation_docs.go 由本注册表生成文档。
//
// 既有「首页 Hero 右侧视觉」归并进同一机制：作为所有主题共有的 hero 槽声明，
// 外观页由 schema 循环渲染（不再是独立硬编码区块），存储键维持 hero.visual/hero.image/hero.svg 不变。

// 槽类型：决定外观页渲染哪种编辑控件与 settings 值的格式。
const (
	themeOptHero     = "hero"     // 既有 Hero 右侧视觉控件（hero.visual/hero.image/hero.svg，全局）
	themeOptStats    = "stats"    // 数字组 JSON [{num,label}]，最多 Max 组
	themeOptSteps    = "steps"    // 步骤组 JSON [{title,note}] ×Max + 全局开关 EnabledKey
	themeOptTextPair = "textpair" // 文本对 JSON {title,note}
	themeOptToggle   = "toggle"   // 纯开关区块（内容零配置，如分类入口卡区），只有 EnabledKey
	themeOptPairs    = "pairs"    // 名称+一句话组 JSON [{name,note}] ×Max + 开关（如应用行业）
	themeOptQAList   = "qalist"   // 问答组 JSON [{q,a}] ×Max + 开关（如 FAQ）
	themeOptGallery  = "gallery"  // 图片 URL 列表 JSON ["url",...]（全局；空 = 区块不渲染）
	// 评价组 JSON [{name,region,quote}] ×Max（独立站族）。没有开关也没有默认：
	// 未配置区块不渲染——评价只能录真实用户的话，绝不编造占位。
	themeOptTestimonials = "testimonials"
)

// ThemeOptionSpec 是主题声明的一个可配置数据槽。
type ThemeOptionSpec struct {
	Key        string // settings 语义键（如 factory.stats）；hero 槽记主键 hero.visual
	Type       string // themeOpt* 之一
	LabelKey   string // 后台 i18n 键（模板 {{Admin.T LabelKey Label}}）
	Label      string // 中文兜底文案
	DescKey    string
	Desc       string
	Localized  bool   // 文本按语种覆盖（key::lang）
	Max        int    // 组类槽（stats/steps）的条目上限
	EnabledKey string // steps 槽的整条开关键（全局，"0"=关）
	Example    string // AI 文档用的格式示例（JSON）
}

// heroThemeOption 所有主题共有的「首页 Hero 视觉」槽（既有功能，归并进 schema）。
// 默认动画按骨架分流：内容/通用主题渲染科技感动画（templates/home.html 的 hv/hv2），
// 工厂骨架（含沉浸 vision / 门楣 herofold 全屏形态——文案右侧视觉位）渲染
// 图纸机械动画变体（factory_hero_anim：齿轮 + 流水线）；
// 独立站骨架（dtc-*）渲染 DTC 风轻动画（dtc_hero_anim：柔和渐变光斑漂移 + 浮动产品卡，
// 克制零 JS，prefers-reduced-motion 静止）——工厂齿轮的工业感与品牌店气质不合；
// vision/herofold 若配了 hero 图或 factory.gallery，仍优先全幅铺底图（既有行为不变）。
var heroThemeOption = ThemeOptionSpec{
	Key:      "hero.visual",
	Type:     themeOptHero,
	LabelKey: "admin.settings.appearance.hero_visual",
	Label:    "首页 Hero 右侧视觉",
	DescKey:  "admin.settings.appearance.hero_visual_desc",
	Desc:     "默认是随主题的动画视觉（内容主题为科技感动画，工厂骨架为图纸机械动画，独立站骨架为渐变光斑动画），可替换为图片、SVG 文件或直接粘贴 SVG 代码。",
}

// factoryThemeOptions 工厂主题族的全量槽定义（spec 唯一事实源；声明顺序即表单/文档顺序）。
// 每个骨架实际消费哪个子集见 factoryLayoutSlots——spec 共用一份，映射只挑子集。
var factoryThemeOptions = []ThemeOptionSpec{
	{
		Key:       factoryStatsSettingKey,
		Type:      themeOptStats,
		LabelKey:  "admin.settings.appearance.factory_stats",
		Label:     "工厂实力（数字组）",
		DescKey:   "admin.settings.appearance.factory_stats_desc",
		Desc:      "首页「工厂实力」的 4 组数字与标签（如 2008 / 工厂成立）。留空则回落渲染站点简介。",
		Localized: true,
		Max:       maxFactoryStats,
		Example:   `[{"num":"2008","label":"工厂成立"},{"num":"12,000㎡","label":"自有厂房"}]`,
	},
	{
		Key:        factoryProcessSettingKey,
		Type:       themeOptSteps,
		LabelKey:   "admin.settings.appearance.factory_process",
		Label:      "合作流程（四步）",
		DescKey:    "admin.settings.appearance.factory_process_desc",
		Desc:       "首页流程条的四步文案（询盘→打样→量产→出货）。留空的字段回落内置默认；取消勾选可整条隐藏。",
		Localized:  true,
		Max:        factoryProcessStepCount,
		EnabledKey: factoryProcessEnabledKey,
		Example:    `[{"title":"询盘","note":"告知产品与数量，24 小时内报价。"},{"title":"打样","note":""}]`,
	},
	{
		Key:        factoryCategoriesEnabledKey,
		Type:       themeOptToggle,
		LabelKey:   "admin.settings.appearance.factory_categories",
		Label:      "分类入口卡区",
		DescKey:    "admin.settings.appearance.factory_categories_desc",
		Desc:       "首页商品分类卡片（分类名 + 商品数 + 该分类首个商品封面），内容零配置自动生成；没有分类时不渲染。",
		EnabledKey: factoryCategoriesEnabledKey,
		Example:    `{"enabled":true}`,
	},
	{
		Key:        factoryIndustriesSettingKey,
		Type:       themeOptPairs,
		LabelKey:   "admin.settings.appearance.factory_industries",
		Label:      "应用行业条",
		DescKey:    "admin.settings.appearance.factory_industries_desc",
		Desc:       "「服务哪些行业」的小卡（行业名 + 一句话）。留空回落四个通用行业；取消勾选整条隐藏。",
		Localized:  true,
		Max:        maxFactoryIndustries,
		EnabledKey: factoryIndustriesEnabledKey,
		Example:    `[{"name":"机械制造","note":"零部件与装配件成套供应"},{"name":"零售与品牌","note":"OEM/ODM 贴牌定制"}]`,
	},
	{
		Key:      factoryGallerySettingKey,
		Type:     themeOptGallery,
		LabelKey: "admin.settings.appearance.factory_gallery",
		Label:    "工厂图集（车间与设备）",
		DescKey:  "admin.settings.appearance.factory_gallery_desc",
		Desc:     "横向图集的图片 URL（每行一个，最多 8 张，全站共用不分语种）。未配置时区块不渲染，绝不放占位假照片。",
		Max:      maxFactoryGallery,
		Example:  `["/uploads/workshop-1.webp","/uploads/workshop-2.webp"]`,
	},
	{
		Key:        factoryFAQSettingKey,
		Type:       themeOptQAList,
		LabelKey:   "admin.settings.appearance.factory_faq",
		Label:      "常见问题（FAQ）",
		DescKey:    "admin.settings.appearance.factory_faq_desc",
		Desc:       "首页 FAQ 手风琴（问 + 答，最多 6 条）。留空回落四条外贸标配问答；取消勾选整条隐藏。",
		Localized:  true,
		Max:        maxFactoryQA,
		EnabledKey: factoryFAQEnabledKey,
		Example:    `[{"q":"起订量怎么算？","a":"每款产品的 MOQ 标注在详情页，样品单与拼单可以再谈。"}]`,
	},
	{
		Key:       factoryCTASettingKey,
		Type:      themeOptTextPair,
		LabelKey:  "admin.settings.appearance.factory_cta",
		Label:     "CTA 通栏（获取报价）",
		DescKey:   "admin.settings.appearance.factory_cta_desc",
		Desc:      "页脚前询盘通栏的大标题与副文案。留空回落内置默认。",
		Localized: true,
		Example:   `{"title":"获取报价","note":"告诉我们您的需求，我们会尽快回复报价与交期。"}`,
	},
}

// factoryLayoutSlots 骨架 → 消费槽键（盘点自 templates/partials/home_factory_*.html 与
// header/footer 分支的实际渲染，含间接消费）：
//   - onepage 不渲染分类卡/行业/图集（页头锚点跟随 process/faq 开关，属同槽消费）；
//   - solutions 的行业槽升格为「解决方案」编号大卡（一级入口），不渲染分类卡/图集/FAQ；
//   - engineering 首页是规格对比表 + 认证墙 + 参数分类入口，只吃 stats/categories/cta；
//   - trade/sidebar 的分类树在双层页头/侧栏竖栏（header.html/footer.html 分支）消费 categories；
//   - vision 页脚 = factory_cta 通栏（footer.html 分支），且 hero 无图时回落 gallery 首图；
//   - herofold 的 hero 同样回落 gallery 首图。
//
// 新骨架未登记时回落全量（宁可多渲染配置项，绝不让数据槽失联）。
var factoryLayoutSlots = map[string][]string{
	"factory-catalog":     {factoryStatsSettingKey, factoryProcessSettingKey, factoryCategoriesEnabledKey, factoryIndustriesSettingKey, factoryGallerySettingKey, factoryFAQSettingKey, factoryCTASettingKey},
	"factory-showcase":    {factoryStatsSettingKey, factoryProcessSettingKey, factoryCategoriesEnabledKey, factoryIndustriesSettingKey, factoryGallerySettingKey, factoryFAQSettingKey, factoryCTASettingKey},
	"factory-onepage":     {factoryStatsSettingKey, factoryProcessSettingKey, factoryFAQSettingKey, factoryCTASettingKey},
	"factory-solutions":   {factoryStatsSettingKey, factoryProcessSettingKey, factoryIndustriesSettingKey, factoryCTASettingKey},
	"factory-engineering": {factoryStatsSettingKey, factoryCategoriesEnabledKey, factoryCTASettingKey},
	"factory-trade":       {factoryStatsSettingKey, factoryProcessSettingKey, factoryCategoriesEnabledKey, factoryIndustriesSettingKey, factoryGallerySettingKey, factoryFAQSettingKey, factoryCTASettingKey},
	"factory-sidebar":     {factoryStatsSettingKey, factoryProcessSettingKey, factoryCategoriesEnabledKey, factoryIndustriesSettingKey, factoryGallerySettingKey, factoryFAQSettingKey, factoryCTASettingKey},
	"factory-vision":      {factoryStatsSettingKey, factoryProcessSettingKey, factoryCategoriesEnabledKey, factoryIndustriesSettingKey, factoryGallerySettingKey, factoryFAQSettingKey, factoryCTASettingKey},
	"factory-herofold":    {factoryStatsSettingKey, factoryProcessSettingKey, factoryCategoriesEnabledKey, factoryIndustriesSettingKey, factoryGallerySettingKey, factoryFAQSettingKey, factoryCTASettingKey},
}

// factoryLayoutThemeOptions 按骨架挑出消费的槽子集（保持 factoryThemeOptions 声明顺序）；
// 未登记的工厂骨架回落全量。
func factoryLayoutThemeOptions(layout string) []ThemeOptionSpec {
	keys, ok := factoryLayoutSlots[layout]
	if !ok {
		return factoryThemeOptions
	}
	consumed := make(map[string]bool, len(keys))
	for _, k := range keys {
		consumed[k] = true
	}
	out := make([]ThemeOptionSpec, 0, len(keys))
	for _, spec := range factoryThemeOptions {
		if consumed[spec.Key] {
			out = append(out, spec)
		}
	}
	return out
}

// factoryOptionSpec 按 settings 键名取工厂槽的 spec（注册表即唯一事实源；
// dtc 族复用 FAQ/CTA 等同键槽时经这里取，automation_docs.go 也用同一读法）。
func factoryOptionSpec(key string) ThemeOptionSpec {
	for _, spec := range factoryThemeOptions {
		if spec.Key == key {
			return spec
		}
	}
	return ThemeOptionSpec{}
}

// dtcThemeOptions 外贸独立站主题族的全量槽定义（声明顺序即表单/文档顺序）。
// stats/categories/gallery 复用工厂族存储键（换主题数据不丢），但按品牌店语义
// 重挂标签与说明；testimonials 为独立站专属新槽；FAQ/CTA 与工厂族同一 spec。
var dtcThemeOptions = []ThemeOptionSpec{
	{
		Key:       factoryStatsSettingKey,
		Type:      themeOptStats,
		LabelKey:  "admin.settings.appearance.dtc_stats",
		Label:     "品牌数字（数字组）",
		DescKey:   "admin.settings.appearance.dtc_stats_desc",
		Desc:      "首页「品牌故事」条的数字与标签（如 2018 / 品牌创立）。留空则回落渲染站点简介。与工厂主题共用同一存储（factory.stats），换主题数据不丢。",
		Localized: true,
		Max:       maxFactoryStats,
		Example:   `[{"num":"2018","label":"品牌创立"},{"num":"30+","label":"发货国家与地区"}]`,
	},
	{
		Key:        factoryCategoriesEnabledKey,
		Type:       themeOptToggle,
		LabelKey:   "admin.settings.appearance.dtc_categories",
		Label:      "系列入口（分类大卡）",
		DescKey:    "admin.settings.appearance.dtc_categories_desc",
		Desc:       "首页「系列」大卡（商品分类升格为系列入口：分类名 + 商品数 + 首个商品封面），内容零配置自动生成；没有分类时不渲染。",
		EnabledKey: factoryCategoriesEnabledKey,
		Example:    `{"enabled":true}`,
	},
	{
		Key:      factoryGallerySettingKey,
		Type:     themeOptGallery,
		LabelKey: "admin.settings.appearance.dtc_gallery",
		Label:    "品牌图集（生活方式大图）",
		DescKey:  "admin.settings.appearance.dtc_gallery_desc",
		Desc:     "品牌/产品生活方式图片 URL（每行一个，最多 8 张，全站共用不分语种）。旗舰骨架渲染横向图带；单品骨架逐图吃进「卖点分解」与「使用场景」；画册骨架作为首页大图墙主角（未配置回落商品封面墙）。",
		Max:      maxFactoryGallery,
		Example:  `["/uploads/lifestyle-1.webp","/uploads/lifestyle-2.webp"]`,
	},
	{
		Key:       dtcTestimonialsSettingKey,
		Type:      themeOptTestimonials,
		LabelKey:  "admin.settings.appearance.dtc_testimonials",
		Label:     "用户评价",
		DescKey:   "admin.settings.appearance.dtc_testimonials_desc",
		Desc:      "首页评价区（姓名 + 地区 + 一句话评价，最多 6 条，按语种保存）。只能录入真实用户评价，绝不编造；未配置时评价区不渲染，绝不放假评价占位。",
		Localized: true,
		Max:       maxDTCTestimonials,
		Example:   `[{"name":"Sarah M.","region":"US","quote":"质感远超预期，已经回购了第二套。"}]`,
	},
	dtcFAQOptionSpec(),
	dtcCTAOptionSpec(),
}

// dtcFAQOptionSpec / dtcCTAOptionSpec：与工厂族同键同格式，仅换独立站语义的标签与说明
// （FAQ 默认问答走零售口径，见 dtcFAQDefaults；CTA 默认文案走 dtc.cta*）。
func dtcFAQOptionSpec() ThemeOptionSpec {
	spec := factoryOptionSpec(factoryFAQSettingKey)
	spec.LabelKey, spec.Label = "admin.settings.appearance.dtc_faq", "常见问题（FAQ）"
	spec.DescKey, spec.Desc = "admin.settings.appearance.dtc_faq_desc", "首页 FAQ 手风琴（问 + 答，最多 6 条）。留空回落四条零售外贸标配（下单/物流/退换/质保）；取消勾选整条隐藏。"
	return spec
}

func dtcCTAOptionSpec() ThemeOptionSpec {
	spec := factoryOptionSpec(factoryCTASettingKey)
	spec.LabelKey, spec.Label = "admin.settings.appearance.dtc_cta", "CTA 通栏（联系我们）"
	spec.DescKey, spec.Desc = "admin.settings.appearance.dtc_cta_desc", "页脚前联系通栏的大标题与副文案。留空回落内置默认（联系我们）。"
	spec.Example = `{"title":"联系我们","note":"有任何产品或订购问题，欢迎随时联系——我们通常在 24 小时内回复。"}`
	return spec
}

// dtcLayoutSlots 独立站骨架 → 消费槽键（盘点自 templates/partials/home_dtc_*.html 的实际渲染）：
//   - flagship 全量：系列入口（categories.enabled）+ 畅销栅格 + 品牌数字（stats）
//   - 生活方式图带（gallery）+ 评价 + FAQ + CTA；
//   - solo 不渲染系列入口（单品长页没有分类概念），其余同 flagship
//     （gallery 逐图吃进卖点分解/使用场景，stats 是信任数字带）；
//   - lookbook 视觉主导：只吃 gallery（大图墙）与 CTA，系列陈列直接吃分类数据
//     （非槽、零配置），评价/FAQ/数字带刻意不上——画册要极少文字。
//
// 新骨架未登记时回落全量（同 factoryLayoutSlots 的约定）。
var dtcLayoutSlots = map[string][]string{
	"dtc-flagship": {factoryStatsSettingKey, factoryCategoriesEnabledKey, factoryGallerySettingKey, dtcTestimonialsSettingKey, factoryFAQSettingKey, factoryCTASettingKey},
	"dtc-solo":     {factoryStatsSettingKey, factoryGallerySettingKey, dtcTestimonialsSettingKey, factoryFAQSettingKey, factoryCTASettingKey},
	"dtc-lookbook": {factoryGallerySettingKey, factoryCTASettingKey},
}

// dtcLayoutThemeOptions 按独立站骨架挑槽子集（保持 dtcThemeOptions 声明顺序）；
// 未登记的骨架回落全量。
func dtcLayoutThemeOptions(layout string) []ThemeOptionSpec {
	keys, ok := dtcLayoutSlots[layout]
	if !ok {
		return dtcThemeOptions
	}
	consumed := make(map[string]bool, len(keys))
	for _, k := range keys {
		consumed[k] = true
	}
	out := make([]ThemeOptionSpec, 0, len(keys))
	for _, spec := range dtcThemeOptions {
		if consumed[spec.Key] {
			out = append(out, spec)
		}
	}
	return out
}

// themeOptionSpecs 返回主题声明的 options schema：所有主题都有 hero 槽；
// 工厂骨架族（经 layoutForTheme 归族）追加该骨架实际消费的工厂槽子集；
// 独立站骨架族同理追加 dtc 槽子集。
func themeOptionSpecs(theme string) []ThemeOptionSpec {
	specs := []ThemeOptionSpec{heroThemeOption}
	switch layout := layoutForTheme(theme); {
	case isFactoryLayout(layout):
		specs = append(specs, factoryLayoutThemeOptions(layout)...)
	case isDTCLayout(layout):
		specs = append(specs, dtcLayoutThemeOptions(layout)...)
	}
	return specs
}

// ---------- 外观页动态表单：视图装配 ----------

// ThemeStepField 是 steps 槽的一行表单值（含该位的默认值占位）。
type ThemeStepField struct {
	Title, Note       string
	TitleDef, NoteDef string
}

// ThemeQAField 是 qalist 槽的一行表单值（FAQ 问答 + 默认占位）。
type ThemeQAField struct {
	Q, A       string
	QDef, ADef string
}

// ThemePairField 是 pairs 槽的一行表单值（名称 + 一句话 + 默认占位）。
type ThemePairField struct {
	Name, Note       string
	NameDef, NoteDef string
}

// ThemeTestiField 是 testimonials 槽的一行表单值（评价没有默认占位——绝不编造）。
type ThemeTestiField struct {
	Name, Region, Quote string
}

// ThemeOptionView 是外观页渲染一个数据槽所需的视图数据（值按当前编辑语种的实际存储值，
// 未设置即为空，便于看出回落；Def* 是该语种的默认值占位）。
type ThemeOptionView struct {
	ThemeOptionSpec
	Enabled           bool          // steps/toggle/pairs/qalist 槽的整条开关
	Stats             []FactoryStat // stats 槽，补齐到 Max 行
	Steps             []ThemeStepField
	QAs               []ThemeQAField    // qalist 槽，补齐到 Max 行
	Pairs             []ThemePairField  // pairs 槽，补齐到 Max 行
	Testimonials      []ThemeTestiField // testimonials 槽，补齐到 Max 行（无默认占位）
	GalleryText       string            // gallery 槽：每行一个 URL 的 textarea 值
	Title, Note       string            // textpair 槽
	TitleDef, NoteDef string
}

// themeOptionViews 按主题 schema 装配外观页表单数据；lang 是编辑语种（读裸键或 ::lang 键）。
// FAQ / CTA 的默认占位按主题族分流：独立站骨架显示零售口径默认（dtcFAQDefaults / dtc.cta*）。
func (s *Server) themeOptionViews(theme, lang string) []ThemeOptionView {
	tr := s.i18n.Tr(lang, s.defaultLang())
	dtcFamily := isDTCLayout(layoutForTheme(theme))
	var out []ThemeOptionView
	for _, spec := range themeOptionSpecs(theme) {
		v := ThemeOptionView{ThemeOptionSpec: spec}
		raw := s.store.Setting(s.copyKey(spec.Key, lang))
		switch spec.Type {
		case themeOptStats:
			v.Stats = parseFactoryStats(raw)
			for len(v.Stats) < spec.Max {
				v.Stats = append(v.Stats, FactoryStat{})
			}
		case themeOptSteps:
			v.Enabled = strings.TrimSpace(s.store.Setting(spec.EnabledKey)) != factoryProcessDisabledFlag
			custom := parseFactorySteps(raw)
			for i := 0; i < spec.Max; i++ {
				f := ThemeStepField{
					TitleDef: tr.T(factoryProcessDefaults[i][0]),
					NoteDef:  tr.T(factoryProcessDefaults[i][1]),
				}
				if i < len(custom) {
					f.Title, f.Note = custom[i].Title, custom[i].Note
				}
				v.Steps = append(v.Steps, f)
			}
		case themeOptTextPair:
			pair := parseFactoryTextPair(raw)
			v.Title, v.Note = pair.Title, pair.Note
			if dtcFamily {
				v.TitleDef, v.NoteDef = tr.T("dtc.cta"), tr.T("dtc.cta_note")
			} else {
				v.TitleDef, v.NoteDef = tr.T("factory.cta"), tr.T("factory.contact_note")
			}
		case themeOptToggle:
			v.Enabled = s.factorySectionEnabled(spec.EnabledKey)
		case themeOptPairs:
			v.Enabled = s.factorySectionEnabled(spec.EnabledKey)
			custom := parseFactoryIndustries(raw)
			for i := 0; i < spec.Max; i++ {
				f := ThemePairField{}
				if i < len(factoryIndustryDefaults) {
					f.NameDef, f.NoteDef = tr.T(factoryIndustryDefaults[i][0]), tr.T(factoryIndustryDefaults[i][1])
				}
				if i < len(custom) {
					f.Name, f.Note = custom[i].Name, custom[i].Note
				}
				v.Pairs = append(v.Pairs, f)
			}
		case themeOptQAList:
			v.Enabled = s.factorySectionEnabled(spec.EnabledKey)
			custom := parseFactoryQAs(raw)
			faqDefaults := factoryFAQDefaults
			if dtcFamily {
				faqDefaults = dtcFAQDefaults
			}
			for i := 0; i < spec.Max; i++ {
				f := ThemeQAField{}
				if i < len(faqDefaults) {
					f.QDef, f.ADef = tr.T(faqDefaults[i][0]), tr.T(faqDefaults[i][1])
				}
				if i < len(custom) {
					f.Q, f.A = custom[i].Q, custom[i].A
				}
				v.QAs = append(v.QAs, f)
			}
		case themeOptTestimonials:
			// 评价没有默认占位（绝不编造）：只回填已存的真实评价。
			custom := parseDTCTestimonials(raw)
			for i := 0; i < spec.Max; i++ {
				f := ThemeTestiField{}
				if i < len(custom) {
					f.Name, f.Region, f.Quote = custom[i].Name, custom[i].Region, custom[i].Quote
				}
				v.Testimonials = append(v.Testimonials, f)
			}
		case themeOptGallery:
			// 图集全局（不分语种）：读裸键。
			v.GalleryText = strings.Join(parseFactoryGallery(s.store.Setting(spec.Key)), "\n")
		}
		out = append(out, v)
	}
	return out
}

// themeOptionsLocalized 外观页是否需要语种切换条（当前主题声明了按语种的槽）。
func themeOptionsLocalized(theme string) bool {
	for _, spec := range themeOptionSpecs(theme) {
		if spec.Localized {
			return true
		}
	}
	return false
}

// ---------- 外观页保存 ----------

// themeOptsFormMarker 外观表单的槽标记字段：只有表单实际渲染了工厂槽（值 "factory"）
// 才写这批键——防止在内容主题下保存外观时把工厂数据误清。
const themeOptsFormMarker = "theme_opts"

// themeOptSlotField 每个实际渲染的工厂槽在表单里的登记字段（值 = 槽键，如 factory.stats）。
// 保存只写登记过的槽：骨架按子集渲染后，没渲染的槽不出现 ≠ 数据被清。
// 旧表单没带登记字段时回落「全部槽」（维持既有语义，不回归）。
const themeOptSlotField = "theme_opt_slot"

// postedThemeOptionSlots 从表单读出本次实际渲染（因此可安全落库）的槽键集合。
// 旧表单（无登记字段）的回落刻意只含工厂槽：老表单从未渲染过 dtc.testimonials，
// 若把它并进回落集合，等于用空表单值清掉评价数据。
func postedThemeOptionSlots(r *http.Request) map[string]bool {
	posted := map[string]bool{}
	for _, k := range r.Form[themeOptSlotField] {
		if k = strings.TrimSpace(k); k != "" {
			posted[k] = true
		}
	}
	if len(posted) == 0 {
		for _, spec := range factoryThemeOptions {
			posted[spec.Key] = true
		}
	}
	return posted
}

// marshalThemeOptionJSON 组类/文本对槽的统一落库：空数据存 ""（= 回落默认），否则存规范 JSON。
func marshalThemeOptionJSON(v any, empty bool) string {
	if empty {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// saveThemeOptionsFromForm 从外观表单读取工厂槽并写入 settings（按编辑语种）。
// 仅当表单带槽标记时调用方才应进入这里；每个槽再看槽登记（theme_opt_slot）——
// 只写本次表单实际渲染的槽，没渲染的槽绝不触碰（骨架子集渲染下换主题数据保留）。
func (s *Server) saveThemeOptionsFromForm(r *http.Request, lang string) {
	posted := postedThemeOptionSlots(r)

	if posted[factoryStatsSettingKey] {
		// factory.stats：num/label 都非空才算一组（与 parseFactoryStats 口径一致）。
		stats := make([]FactoryStat, 0, maxFactoryStats)
		for i := 0; i < maxFactoryStats; i++ {
			num := strings.TrimSpace(r.FormValue(fmt.Sprintf("factory_stat_num_%d", i)))
			label := strings.TrimSpace(r.FormValue(fmt.Sprintf("factory_stat_label_%d", i)))
			if num == "" || label == "" {
				continue
			}
			stats = append(stats, FactoryStat{Num: num, Label: label})
		}
		_ = s.store.SetSetting(s.copyKey(factoryStatsSettingKey, lang), marshalThemeOptionJSON(stats, len(stats) == 0))
	}

	if posted[factoryProcessSettingKey] {
		// factory.process：四步按位存（空字段保留，渲染时逐项回落）；全空存 ""。
		steps := make([]FactoryStep, factoryProcessStepCount)
		stepsAny := false
		for i := 0; i < factoryProcessStepCount; i++ {
			steps[i] = FactoryStep{
				Title: strings.TrimSpace(r.FormValue(fmt.Sprintf("factory_step_title_%d", i))),
				Note:  strings.TrimSpace(r.FormValue(fmt.Sprintf("factory_step_note_%d", i))),
			}
			if steps[i].Title != "" || steps[i].Note != "" {
				stepsAny = true
			}
		}
		_ = s.store.SetSetting(s.copyKey(factoryProcessSettingKey, lang), marshalThemeOptionJSON(steps, !stepsAny))

		// 整条开关（全局，不分语种）：勾选=开（存空保持默认），取消=存 "0"。
		_ = s.store.SetSetting(factoryProcessEnabledKey, formToggleFlag(r, "factory_process_on"))
	}

	if posted[factoryCTASettingKey] {
		// factory.cta：{title,note}；全空存 ""。
		cta := FactoryTextPair{
			Title: strings.TrimSpace(r.FormValue("factory_cta_title")),
			Note:  strings.TrimSpace(r.FormValue("factory_cta_note")),
		}
		_ = s.store.SetSetting(s.copyKey(factoryCTASettingKey, lang), marshalThemeOptionJSON(cta, cta.Title == "" && cta.Note == ""))
	}

	if posted[factoryCategoriesEnabledKey] {
		// 分类入口卡区：纯开关（内容零配置）。
		_ = s.store.SetSetting(factoryCategoriesEnabledKey, formToggleFlag(r, "factory_categories_on"))
	}

	if posted[factoryIndustriesSettingKey] {
		// factory.industries：name 非空才算一项（note 可空）；全空存 ""（回落 i18n 默认）。
		industries := make([]FactoryIndustry, 0, maxFactoryIndustries)
		for i := 0; i < maxFactoryIndustries; i++ {
			name := strings.TrimSpace(r.FormValue(fmt.Sprintf("factory_industry_name_%d", i)))
			if name == "" {
				continue
			}
			industries = append(industries, FactoryIndustry{Name: name, Note: strings.TrimSpace(r.FormValue(fmt.Sprintf("factory_industry_note_%d", i)))})
		}
		_ = s.store.SetSetting(s.copyKey(factoryIndustriesSettingKey, lang), marshalThemeOptionJSON(industries, len(industries) == 0))
		_ = s.store.SetSetting(factoryIndustriesEnabledKey, formToggleFlag(r, "factory_industries_on"))
	}

	if posted[factoryGallerySettingKey] {
		// factory.gallery（全局，不分语种）：textarea 每行一个 URL；空存 ""（区块不渲染）。
		gallery := make([]string, 0, maxFactoryGallery)
		for _, line := range strings.Split(r.FormValue("factory_gallery"), "\n") {
			u := strings.TrimSpace(line)
			if u == "" {
				continue
			}
			gallery = append(gallery, u)
			if len(gallery) == maxFactoryGallery {
				break
			}
		}
		_ = s.store.SetSetting(factoryGallerySettingKey, marshalThemeOptionJSON(gallery, len(gallery) == 0))
	}

	if posted[factoryFAQSettingKey] {
		// factory.faq：q/a 都非空才算一条；全空存 ""（回落 i18n 四条外贸标配）。
		qas := make([]FactoryQA, 0, maxFactoryQA)
		for i := 0; i < maxFactoryQA; i++ {
			q := strings.TrimSpace(r.FormValue(fmt.Sprintf("factory_faq_q_%d", i)))
			a := strings.TrimSpace(r.FormValue(fmt.Sprintf("factory_faq_a_%d", i)))
			if q == "" || a == "" {
				continue
			}
			qas = append(qas, FactoryQA{Q: q, A: a})
		}
		_ = s.store.SetSetting(s.copyKey(factoryFAQSettingKey, lang), marshalThemeOptionJSON(qas, len(qas) == 0))
		_ = s.store.SetSetting(factoryFAQEnabledKey, formToggleFlag(r, "factory_faq_on"))
	}

	if posted[dtcTestimonialsSettingKey] {
		// dtc.testimonials：name/quote 都非空才算一条（region 可空）；全空存 ""（区块不渲染）。
		// 表单只是录入口——评价必须是真实用户反馈，红线写在表单说明与 SKILL 文档里。
		rows := make([]DTCTestimonial, 0, maxDTCTestimonials)
		for i := 0; i < maxDTCTestimonials; i++ {
			name := strings.TrimSpace(r.FormValue(fmt.Sprintf("dtc_testi_name_%d", i)))
			quote := strings.TrimSpace(r.FormValue(fmt.Sprintf("dtc_testi_quote_%d", i)))
			if name == "" || quote == "" {
				continue
			}
			rows = append(rows, DTCTestimonial{Name: name, Region: strings.TrimSpace(r.FormValue(fmt.Sprintf("dtc_testi_region_%d", i))), Quote: quote})
		}
		_ = s.store.SetSetting(s.copyKey(dtcTestimonialsSettingKey, lang), marshalThemeOptionJSON(rows, len(rows) == 0))
	}
}

// formToggleFlag 区块开关表单值 → settings 值：勾选存 ""（缺省=开，settings 保持干净），
// 取消存 "0"。
func formToggleFlag(r *http.Request, field string) string {
	if r.FormValue(field) == "1" {
		return ""
	}
	return factoryProcessDisabledFlag
}
