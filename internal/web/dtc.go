package web

import (
	"encoding/json"
	"strings"

	"cms.ccvar.com/internal/store"
)

// 外贸独立站（DTC）主题族：跨境卖家自有品牌官网——零售感、大图、故事性，
// 与工厂族的工业感彻底区分；转化仍是询盘/WhatsApp（不做在线交易）。
//
// 三个骨架（data-theme-layout）：
//   - dtc-flagship 品牌旗舰：生活方式大图 hero → 系列入口（商品分类升格为大卡）
//     → 畅销品栅格 → 品牌故事条（factory.stats 复用为「品牌数字」，空回落站点简介）
//     → 用户评价 → FAQ → 页脚前联系 CTA；
//   - dtc-solo     单品爆款：整站长转化流——痛点开场 hero → 卖点分解（gallery 槽逐图
//     左右交替，文案取主打商品 specs）→ 规格表 → 使用场景（gallery 余图）→ 评价
//     → FAQ → 底部大 CTA；主打商品 = 置顶商品优先（ListPublishedByType 本身
//     featured DESC 排序），无置顶取最新；
//   - dtc-lookbook 系列画册：视觉主导——通栏大图墙（gallery 槽为主角，回落商品封面墙）
//     + 系列（分类）为单位的作品陈列，悬停出品名，极少文字。
//
// 数据槽全部复用工厂族的存储键（factory.stats/gallery/faq/cta/categories.enabled，
// 换主题数据不丢），外加独立站专属的 dtc.testimonials（用户评价）。
// 皮肤在 web.go Themes 注册（Category 一律 dtc），CSS 落在 assets/css/public.css 文末。
// 商品详情与工厂骨架同走 product_detail 专属模板（见 renderExtDetail 的前缀判断）。

const (
	// dtc.testimonials 用户评价 JSON [{name,region,quote}]（按语种 ::lang 覆盖；最多 6 条）。
	// 红线：只能录入真实用户评价，绝不编造；未配置时评价区不渲染（绝不放假评价占位）。
	dtcTestimonialsSettingKey = "dtc.testimonials"
	maxDTCTestimonials        = 6
	maxDTCSellingBlocks       = 4  // 单品骨架「卖点分解」最多 4 块图文
	maxDTCLookGroups          = 6  // 画册骨架最多 6 个系列（分类）组
	maxDTCLookGroupItems      = 8  // 每个系列组最多 8 个商品
	maxDTCLookWall            = 12 // 画册封面墙（无分类回落）最多 12 格
)

// DTCTestimonial 用户评价一条：姓名 + 地区（可空）+ 一句话评价。
// json tag 同时是 settings 存储格式与 site-profile API（dtc_testimonials）的字段名。
type DTCTestimonial struct {
	Name   string `json:"name"`
	Region string `json:"region,omitempty"`
	Quote  string `json:"quote"`
}

// DTCSelling 单品骨架「卖点分解」的一块：图（gallery 槽逐图）+ 文（主打商品 specs 对）。
type DTCSelling struct {
	Img   string
	Title string // 规格键（如 材质）
	Note  string // 规格值（如 食品级 304 不锈钢）
}

// DTCLookGroup 画册骨架的一个系列组：分类名 + 入口 + 该分类的商品（封面墙）。
type DTCLookGroup struct {
	Name  string
	URL   string // /products/cat/{slug}（模板再套 Tr.U）
	Items []*store.Post
}

// dtcFAQDefaults 独立站 FAQ 的 i18n 默认键（四条零售外贸标配：下单/物流/退换/质保）。
// 与工厂族的 MOQ/打样问答区分——品牌店的默认口径是零售式的。
var dtcFAQDefaults = [4][2]string{
	{"dtc.faq_q1", "dtc.faq_a1"},
	{"dtc.faq_q2", "dtc.faq_a2"},
	{"dtc.faq_q3", "dtc.faq_a3"},
	{"dtc.faq_q4", "dtc.faq_a4"},
}

// isDTCLayout 判断骨架是否属于外贸独立站主题族。
func isDTCLayout(layout string) bool { return strings.HasPrefix(layout, "dtc-") }

// parseDTCTestimonials 容错解析 dtc.testimonials：非法 JSON 返回空；
// name/quote 任一为空的条目丢弃（region 可空）；最多 6 条。
func parseDTCTestimonials(raw string) []DTCTestimonial {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var in []map[string]any
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil
	}
	out := make([]DTCTestimonial, 0, maxDTCTestimonials)
	for _, m := range in {
		name, quote := scalarString(m["name"]), scalarString(m["quote"])
		if name == "" || quote == "" {
			continue
		}
		out = append(out, DTCTestimonial{Name: name, Region: scalarString(m["region"]), Quote: quote})
		if len(out) == maxDTCTestimonials {
			break
		}
	}
	return out
}

// dtcTestimonialList 装载用户评价：按语种读 settings；没有数据返回 nil（区块不渲染）。
// 刻意没有任何内置默认——评价只能是真实用户说过的话，绝不编造。
func (s *Server) dtcTestimonialList(lang string) []DTCTestimonial {
	return parseDTCTestimonials(s.localizedSetting(dtcTestimonialsSettingKey, lang, ""))
}

// dtcFAQList 装载独立站 FAQ：开关/自定义与工厂族同一存储键，
// 仅空数据时的默认问答换成零售口径（下单/物流/退换/质保）。
func (s *Server) dtcFAQList(lang string) []FactoryQA {
	if !s.factorySectionEnabled(factoryFAQEnabledKey) {
		return nil
	}
	if custom := parseFactoryQAs(s.localizedSetting(factoryFAQSettingKey, lang, "")); len(custom) > 0 {
		return custom
	}
	tr := s.i18n.Tr(lang, s.defaultLang())
	out := make([]FactoryQA, 0, len(dtcFAQDefaults))
	for _, keys := range dtcFAQDefaults {
		out = append(out, FactoryQA{Q: tr.T(keys[0]), A: tr.T(keys[1])})
	}
	return out
}

// dtcCTAText 装载 CTA 通栏文案：同 factory.cta 存储键，空字段回落独立站口径的 i18n
// （「联系我们」而非「获取报价」）。
func (s *Server) dtcCTAText(lang string) FactoryTextPair {
	tr := s.i18n.Tr(lang, s.defaultLang())
	out := parseFactoryTextPair(s.localizedSetting(factoryCTASettingKey, lang, ""))
	if strings.TrimSpace(out.Title) == "" {
		out.Title = tr.T("dtc.cta")
	}
	if strings.TrimSpace(out.Note) == "" {
		out.Note = tr.T("dtc.cta_note")
	}
	return out
}

// dtcHomeProductLimit 首页商品条数：旗舰畅销栅格 8、单品只要主打（多取几条容错）、
// 画册封面墙 12。
func dtcHomeProductLimit(layout string) int {
	switch layout {
	case "dtc-flagship":
		return 8
	case "dtc-solo":
		return 6
	}
	return maxDTCLookWall
}

// dtcSoloSelling 单品骨架「卖点分解」取数：gallery 逐图 × 主打商品 specs 逐对配组，
// 块数 = min(图数, 规格数, 4)——图文都齐才成块（没规格文案就不硬凑）；
// 没用完的 gallery 余图归「使用场景」横带。
func dtcSoloSelling(gallery []string, specs []FieldPair) (selling []DTCSelling, scenes []string) {
	n := len(gallery)
	if len(specs) < n {
		n = len(specs)
	}
	if n > maxDTCSellingBlocks {
		n = maxDTCSellingBlocks
	}
	for i := 0; i < n; i++ {
		selling = append(selling, DTCSelling{Img: gallery[i], Title: specs[i].K, Note: specs[i].V})
	}
	if len(gallery) > n {
		scenes = gallery[n:]
	}
	return selling, scenes
}

// dtcLookGroups 画册骨架的系列组：有已发布商品的分类 → 每组取前几个商品做封面墙；
// 没有任何分类组时回落一组「全部商品」。product 类型未启用返回 nil（模板容错）。
func (s *Server) dtcLookGroups(lang string) []DTCLookGroup {
	if !s.contentTypeActive("product") {
		return nil
	}
	ct := contentTypeByKey("product")
	if ct == nil {
		return nil
	}
	prefix := "/" + strings.Trim(ct.URLPrefix, "/")
	cats, _ := s.store.ListCategories(lang, "product")
	out := make([]DTCLookGroup, 0, maxDTCLookGroups)
	for _, c := range cats {
		if c.Count <= 0 {
			continue
		}
		items, _ := s.store.ListPublishedByType("product", lang, c.ID, 0, maxDTCLookGroupItems)
		if len(items) == 0 {
			continue
		}
		out = append(out, DTCLookGroup{Name: c.Name, URL: prefix + "/cat/" + c.Slug, Items: items})
		if len(out) == maxDTCLookGroups {
			break
		}
	}
	if len(out) == 0 {
		if items, _ := s.store.ListPublishedByType("product", lang, 0, 0, maxDTCLookWall); len(items) > 0 {
			tr := s.i18n.Tr(lang, s.defaultLang())
			out = append(out, DTCLookGroup{Name: tr.T("factory.products"), URL: prefix, Items: items})
		}
	}
	return out
}

// fillDTCHome 为独立站骨架首页装载数据。只装该骨架实际消费的槽（dtcLayoutSlots 同一口径），
// 不消费的槽保持零值（模板不渲染）。product 类型未启用时商品区留空（模板容错渲染）。
func (s *Server) fillDTCHome(v *View, lang string) {
	if !isDTCLayout(v.Layout) {
		return
	}
	consumed := map[string]bool{}
	for _, k := range dtcLayoutSlots[v.Layout] {
		consumed[k] = true
	}
	if s.contentTypeActive("product") {
		v.FactoryProducts, _ = s.store.ListPublishedByType("product", lang, 0, 0, dtcHomeProductLimit(v.Layout))
	}
	if consumed[factoryStatsSettingKey] {
		v.FactoryStats = parseFactoryStats(s.localizedSetting(factoryStatsSettingKey, lang, ""))
	}
	if consumed[factoryGallerySettingKey] {
		v.FactoryGallery = parseFactoryGallery(s.store.Setting(factoryGallerySettingKey)) // 图集全局，不分语种
	}
	if consumed[dtcTestimonialsSettingKey] {
		v.DTCTestimonials = s.dtcTestimonialList(lang)
	}
	if consumed[factoryFAQSettingKey] {
		v.FactoryQAs = s.dtcFAQList(lang)
	}
	if consumed[factoryCTASettingKey] {
		v.FactoryCTA = s.dtcCTAText(lang)
	}
	switch v.Layout {
	case "dtc-flagship":
		v.FactoryCats = s.factoryCategoryCards(lang) // 系列入口（分类大卡；开关同 factory.categories.enabled）
	case "dtc-solo":
		if len(v.FactoryProducts) > 0 {
			v.DTCMain = v.FactoryProducts[0]
			v.DTCMainSpecs = productSpecPairs(v.DTCMain)
		}
		v.DTCSelling, v.DTCScenes = dtcSoloSelling(v.FactoryGallery, v.DTCMainSpecs)
	case "dtc-lookbook":
		v.DTCLookGroups = s.dtcLookGroups(lang)
	}
}
