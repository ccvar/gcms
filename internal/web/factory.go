package web

import (
	"encoding/json"
	"fmt"
	"strings"

	"cms.ccvar.com/internal/store"
)

// 工厂/外贸站 P2：工厂主题族的渲染辅助。
//
// 五个骨架（data-theme-layout）：
//   - factory-catalog     目录型：顶部 hero 条 + 商品栅格 + 弱化文章区，适合 SKU 多的工厂；
//   - factory-showcase    展台型：大图 hero + 精选商品横排 + 「工厂实力」 + 最新动态，适合精品少 SKU；
//   - factory-onepage     单页型：整站一页滚到底（主打产品→实力→流程→FAQ→询盘），页头导航
//     在首页态换成页内锚点（见 partials/header.html），小微工厂 / 单一产品线；
//   - factory-solutions   方案型：应用行业大卡做一级入口 + 定制流程 + 商品作为「案例产出」，OEM/ODM 定制厂；
//   - factory-engineering 技术型：核心产品规格对比表（specs 共有键求交集）+ 认证墙 + 参数分类入口，
//     等宽字体重、密度高，面向工程师采购；
//   - factory-trade       经典外贸：双层页头（顶部联系条 + 主导航）+ 四栏大页脚，
//     门户式首页（横幅 + 左分类栏右商品列表），成熟出口工厂（页头页脚分支见 partials/header|footer.html）；
//   - factory-sidebar     侧栏目录：左侧常驻竖栏（品牌 + 分类树 + 文章入口 + 底部联系按钮）+ 一行极简页脚，
//     数据库感的密集目录，≤920px 竖栏折叠为顶部抽屉（JS class，绝不 :target）；
//   - factory-vision      沉浸展示：全屏大图页头（hero 槽图 / factory.gallery 首图，无图回落主题渐变 + 图纸纹理；
//     导航透明悬浮、滚动加实底——site.js 渐进增强）+ 大留白视觉流 + 页脚 = 获取报价 CTA 通栏；
//   - factory-herofold    门楣：首屏 = 含导航的一体容器（四周留边 + 大圆角，导航行压在 hero 视觉上），
//     滚动离开首屏后导航剥离吸顶变实底（site.js），内容与页脚走常规工厂式。
//
// 皮肤在 web.go Themes 注册（Category 一律 factory），CSS 落在 assets/css/public.css
//（public.css 是实际下发的样式表；style.css 只是保留兜底，不动）。
// 工厂骨架下 product 详情走专属模板 product_detail（见 renderExtDetail），
// 非工厂骨架维持 generic_detail 现状。

// 工厂族三组数据槽（主题 options schema，见 theme_options.go）：
//   - factory.stats   「工厂实力」数字组 JSON [{num,label}]，最多 4 组；
//   - factory.process 「合作流程」四步 JSON [{title,note}]（按位对应询盘/打样/量产/出货，
//     空字段逐项回落 i18n 默认）+ 全局开关 factory.process.enabled（"0"=不渲染流程条）；
//   - factory.cta     CTA 通栏文案 JSON {title,note}，空字段回落 i18n。
//
// 三键均存站点 settings（换主题数据不丢、同骨架族共享），文本键支持 ::lang 按语种覆盖
// （读法同 localizedSetting：非默认语种先读带后缀键）。后台外观页 + AI（site-profile）都可写；
// 解析一律容错，没有数据时模板回落默认（stats 回落「关于我们」拆分块）。
const (
	factoryStatsSettingKey      = "factory.stats"
	factoryProcessSettingKey    = "factory.process"
	factoryProcessEnabledKey    = "factory.process.enabled" // 全局开关（不分语种）："0" 关，其余（含空）开
	factoryCTASettingKey        = "factory.cta"
	factoryFAQSettingKey        = "factory.faq" // FAQ JSON [{q,a}]（按语种；空回落 i18n 四条外贸标配）
	factoryFAQEnabledKey        = "factory.faq.enabled"
	factoryIndustriesSettingKey = "factory.industries" // 应用行业 JSON [{name,note}]（按语种；空回落 i18n 四项）
	factoryIndustriesEnabledKey = "factory.industries.enabled"
	factoryGallerySettingKey    = "factory.gallery"            // 工厂图集 JSON ["url",...]（全局；未配置不渲染，绝不占位假照片）
	factoryCategoriesEnabledKey = "factory.categories.enabled" // 分类入口卡区开关（内容零配置：分类+数量+首个商品封面）
	maxFactoryStats             = 4
	factoryProcessStepCount     = 4
	maxFactoryQA                = 6
	maxFactoryIndustries        = 6
	maxFactoryGallery           = 8
	maxFactoryCatCards          = 8
	factoryProcessDisabledFlag  = "0"
	maxFactoryCompareItems      = 4 // 规格对比表最多对比前 4 个带规格的商品
	maxFactoryCompareRows       = 6 // 规格对比表最多 6 行共有键（再多表就读不动了）
)

// FactoryStat 是「工厂实力」区块的一格数据（如 num="2008" label="工厂成立"）。
// json tag 同时是 settings 存储格式与 site-profile API 的字段名。
type FactoryStat struct {
	Num   string `json:"num"`
	Label string `json:"label"`
}

// FactoryStep 是「合作流程」的一步；Num 是模板展示用序号（01–04，装载时填）。
type FactoryStep struct {
	Num   string `json:"-"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

// FactoryTextPair 是 CTA 通栏文案：{标题, 副文案}。
type FactoryTextPair struct {
	Title string `json:"title"`
	Note  string `json:"note"`
}

// FactoryQA 是 FAQ 区的一条问答。
type FactoryQA struct {
	Q string `json:"q"`
	A string `json:"a"`
}

// FactoryIndustry 是「应用行业」条的一格：{行业名, 一句话}。
type FactoryIndustry struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

// FactoryCatCard 是「分类入口卡区」的一张卡（零配置：分类名 + 商品数 + 首个商品封面）。
type FactoryCatCard struct {
	Name  string
	URL   string // /products/cat/{slug}（模板再套 Tr.U 加语种前缀）
	Cover string // 该分类第一个商品的封面；空则模板渲染首字生成块
	Count int
}

// FactoryCompareRow 是规格对比表的一行：规格键 + 每个参与商品的取值（列序与 Items 对齐）。
type FactoryCompareRow struct {
	Key  string
	Vals []string
}

// FactoryCompare 是技术骨架（factory-engineering）首页的「核心产品规格对比表」：
// 取前几个带 specs 的商品，对共有规格键求交集做对比列。纯派生数据，不新增任何存储概念。
type FactoryCompare struct {
	Items []*store.Post       // 参与对比的商品（2–4 个，列头）
	Rows  []FactoryCompareRow // 共有规格键行（键序跟随第一个商品的 specs 顺序）
}

// factoryProcessDefaults 四步流程的 i18n 默认键（按位对应，五语种齐备）。
var factoryProcessDefaults = [factoryProcessStepCount][2]string{
	{"factory.process_inquiry", "factory.process_inquiry_note"},
	{"factory.process_sample", "factory.process_sample_note"},
	{"factory.process_production", "factory.process_production_note"},
	{"factory.process_shipping", "factory.process_shipping_note"},
}

// factoryFAQDefaults FAQ 的 i18n 默认键（四条外贸标配：起订量/打样/交期/付款方式）。
var factoryFAQDefaults = [4][2]string{
	{"factory.faq_q1", "factory.faq_a1"},
	{"factory.faq_q2", "factory.faq_a2"},
	{"factory.faq_q3", "factory.faq_a3"},
	{"factory.faq_q4", "factory.faq_a4"},
}

// factoryIndustryDefaults 「应用行业」的 i18n 默认键（四个通用行业）。
var factoryIndustryDefaults = [4][2]string{
	{"factory.industry1", "factory.industry1_note"},
	{"factory.industry2", "factory.industry2_note"},
	{"factory.industry3", "factory.industry3_note"},
	{"factory.industry4", "factory.industry4_note"},
}

// isFactoryLayout 判断骨架是否属于工厂主题族。
func isFactoryLayout(layout string) bool { return strings.HasPrefix(layout, "factory-") }

// parseFactoryStats 容错解析 factory.stats：非法 JSON 返回空；num 接受字符串或数字；
// num/label 任一为空的项丢弃；最多保留 4 组。
func parseFactoryStats(raw string) []FactoryStat {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var in []map[string]any
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil
	}
	out := make([]FactoryStat, 0, maxFactoryStats)
	for _, m := range in {
		num := scalarString(m["num"])
		label := scalarString(m["label"])
		if num == "" || label == "" {
			continue
		}
		out = append(out, FactoryStat{Num: num, Label: label})
		if len(out) == maxFactoryStats {
			break
		}
	}
	return out
}

// parseFactorySteps 容错解析 factory.process：非法 JSON 返回空；按位保留（空字段留空，
// 渲染时逐项回落 i18n 默认）；最多 4 步。
func parseFactorySteps(raw string) []FactoryStep {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var in []map[string]any
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil
	}
	out := make([]FactoryStep, 0, factoryProcessStepCount)
	for _, m := range in {
		out = append(out, FactoryStep{Title: scalarString(m["title"]), Note: scalarString(m["note"])})
		if len(out) == factoryProcessStepCount {
			break
		}
	}
	return out
}

// parseFactoryTextPair 容错解析 factory.cta：非法 JSON 返回零值（模板回落 i18n）。
func parseFactoryTextPair(raw string) FactoryTextPair {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return FactoryTextPair{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return FactoryTextPair{}
	}
	return FactoryTextPair{Title: scalarString(m["title"]), Note: scalarString(m["note"])}
}

// parseFactoryQAs 容错解析 factory.faq：q/a 任一为空的条目丢弃；最多 6 条。
func parseFactoryQAs(raw string) []FactoryQA {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var in []map[string]any
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil
	}
	out := make([]FactoryQA, 0, maxFactoryQA)
	for _, m := range in {
		q, a := scalarString(m["q"]), scalarString(m["a"])
		if q == "" || a == "" {
			continue
		}
		out = append(out, FactoryQA{Q: q, A: a})
		if len(out) == maxFactoryQA {
			break
		}
	}
	return out
}

// parseFactoryIndustries 容错解析 factory.industries：name 为空的条目丢弃（note 可空）；最多 6 项。
func parseFactoryIndustries(raw string) []FactoryIndustry {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var in []map[string]any
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil
	}
	out := make([]FactoryIndustry, 0, maxFactoryIndustries)
	for _, m := range in {
		name := scalarString(m["name"])
		if name == "" {
			continue
		}
		out = append(out, FactoryIndustry{Name: name, Note: scalarString(m["note"])})
		if len(out) == maxFactoryIndustries {
			break
		}
	}
	return out
}

// parseFactoryGallery 容错解析 factory.gallery：字符串数组，去空白项；最多 8 张。
func parseFactoryGallery(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var in []any
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil
	}
	out := make([]string, 0, maxFactoryGallery)
	for _, v := range in {
		u := strings.TrimSpace(scalarString(v))
		if u == "" {
			continue
		}
		out = append(out, u)
		if len(out) == maxFactoryGallery {
			break
		}
	}
	return out
}

// factorySectionEnabled 区块开关通用读法：只有显式 "0" 关闭，缺省开。
func (s *Server) factorySectionEnabled(key string) bool {
	return strings.TrimSpace(s.store.Setting(key)) != factoryProcessDisabledFlag
}

// factoryProcessEnabled 「合作流程」整条开关：只有显式 "0" 关闭，缺省开（零配置可渲染）。
func (s *Server) factoryProcessEnabled() bool {
	return s.factorySectionEnabled(factoryProcessEnabledKey)
}

// factoryProcessSteps 装载四步流程：settings（按语种）覆盖，空字段逐项回落 i18n 默认。
func (s *Server) factoryProcessSteps(lang string) []FactoryStep {
	tr := s.i18n.Tr(lang, s.defaultLang())
	custom := parseFactorySteps(s.localizedSetting(factoryProcessSettingKey, lang, ""))
	out := make([]FactoryStep, factoryProcessStepCount)
	for i := range out {
		out[i] = FactoryStep{
			Num:   "0" + string(rune('1'+i)),
			Title: tr.T(factoryProcessDefaults[i][0]),
			Note:  tr.T(factoryProcessDefaults[i][1]),
		}
		if i < len(custom) {
			if t := strings.TrimSpace(custom[i].Title); t != "" {
				out[i].Title = t
			}
			if n := strings.TrimSpace(custom[i].Note); n != "" {
				out[i].Note = n
			}
		}
	}
	return out
}

// factoryCTAText 装载 CTA 通栏文案：settings（按语种）覆盖，空字段回落 i18n。
func (s *Server) factoryCTAText(lang string) FactoryTextPair {
	tr := s.i18n.Tr(lang, s.defaultLang())
	out := parseFactoryTextPair(s.localizedSetting(factoryCTASettingKey, lang, ""))
	if strings.TrimSpace(out.Title) == "" {
		out.Title = tr.T("factory.cta")
	}
	if strings.TrimSpace(out.Note) == "" {
		out.Note = tr.T("factory.contact_note")
	}
	return out
}

// factoryFAQList 装载 FAQ：开关关 → nil；自定义整组替换；空回落 i18n 四条外贸标配。
func (s *Server) factoryFAQList(lang string) []FactoryQA {
	if !s.factorySectionEnabled(factoryFAQEnabledKey) {
		return nil
	}
	if custom := parseFactoryQAs(s.localizedSetting(factoryFAQSettingKey, lang, "")); len(custom) > 0 {
		return custom
	}
	tr := s.i18n.Tr(lang, s.defaultLang())
	out := make([]FactoryQA, 0, len(factoryFAQDefaults))
	for _, keys := range factoryFAQDefaults {
		out = append(out, FactoryQA{Q: tr.T(keys[0]), A: tr.T(keys[1])})
	}
	return out
}

// factoryIndustryList 装载「应用行业」：开关关 → nil；自定义整组替换；空回落 i18n 四项。
func (s *Server) factoryIndustryList(lang string) []FactoryIndustry {
	if !s.factorySectionEnabled(factoryIndustriesEnabledKey) {
		return nil
	}
	if custom := parseFactoryIndustries(s.localizedSetting(factoryIndustriesSettingKey, lang, "")); len(custom) > 0 {
		return custom
	}
	tr := s.i18n.Tr(lang, s.defaultLang())
	out := make([]FactoryIndustry, 0, len(factoryIndustryDefaults))
	for _, keys := range factoryIndustryDefaults {
		out = append(out, FactoryIndustry{Name: tr.T(keys[0]), Note: tr.T(keys[1])})
	}
	return out
}

// factoryCategoryCards 「分类入口卡区」（零配置）：product 分类（有已发布商品的）
// → 卡片{名称, 数量, 首个商品封面}；开关关/无分类/类型未启用 → nil（区块不渲染）。
func (s *Server) factoryCategoryCards(lang string) []FactoryCatCard {
	if !s.factorySectionEnabled(factoryCategoriesEnabledKey) || !s.contentTypeActive("product") {
		return nil
	}
	ct := contentTypeByKey("product")
	if ct == nil {
		return nil
	}
	cats, _ := s.store.ListCategories(lang, "product")
	out := make([]FactoryCatCard, 0, maxFactoryCatCards)
	for _, c := range cats {
		if c.Count <= 0 {
			continue
		}
		card := FactoryCatCard{Name: c.Name, Count: c.Count, URL: "/" + strings.Trim(ct.URLPrefix, "/") + "/cat/" + c.Slug}
		if first, _ := s.store.ListPublishedByType("product", lang, c.ID, 0, 1); len(first) > 0 {
			card.Cover = first[0].CoverImage
		}
		out = append(out, card)
		if len(out) == maxFactoryCatCards {
			break
		}
	}
	return out
}

// factoryHomeProductLimit 首页商品条数：目录/技术/外贸/侧栏型栅格多放些，
// 展台/单页/沉浸/门楣型只放大卡，方案型的「案例产出」次级栅格取中。
func factoryHomeProductLimit(layout string) int {
	switch layout {
	case "factory-showcase", "factory-onepage", "factory-vision", "factory-herofold":
		return 6
	case "factory-solutions":
		return 8
	}
	return 12
}

// productSpecPairs 从商品 extra.specs（repeater）取出全部规格对（去掉键或值为空的项）。
// productChips / productSKU 是按关键词嗅探，这里要的是整表，供规格对比求交集。
func productSpecPairs(p *store.Post) []FieldPair {
	raw := strings.TrimSpace(p.Extra)
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	var out []FieldPair
	for _, kv := range pairsToList(m["specs"]) {
		k, v := strings.TrimSpace(kv.K), strings.TrimSpace(kv.V)
		if k == "" || v == "" {
			continue
		}
		out = append(out, FieldPair{K: k, V: v})
	}
	return out
}

// factorySpecCompare 规格对比表取数（factory-engineering 首页）：
//   - 取参数列表前 maxFactoryCompareItems 个「带 specs」的商品做对比列；
//   - 规格键做交集（键名 TrimSpace 后精确匹配；同键重复取首个值），键序跟随第一个商品；
//   - 带规格的商品不足 2 个、或没有任何共有键 → 返回 nil，模板回落普通商品栅格；
//   - 行数超过 maxFactoryCompareRows 截断。
func factorySpecCompare(products []*store.Post) *FactoryCompare {
	type cand struct {
		p     *store.Post
		vals  map[string]string
		order []string
	}
	cands := make([]cand, 0, maxFactoryCompareItems)
	for _, p := range products {
		if p == nil {
			continue
		}
		pairs := productSpecPairs(p)
		if len(pairs) == 0 {
			continue
		}
		vals := make(map[string]string, len(pairs))
		order := make([]string, 0, len(pairs))
		for _, kv := range pairs {
			if _, ok := vals[kv.K]; ok {
				continue
			}
			vals[kv.K] = kv.V
			order = append(order, kv.K)
		}
		cands = append(cands, cand{p: p, vals: vals, order: order})
		if len(cands) == maxFactoryCompareItems {
			break
		}
	}
	if len(cands) < 2 {
		return nil
	}
	rows := make([]FactoryCompareRow, 0, maxFactoryCompareRows)
	for _, key := range cands[0].order {
		vals := make([]string, 0, len(cands))
		for _, c := range cands {
			v := c.vals[key]
			if v == "" {
				vals = nil
				break
			}
			vals = append(vals, v)
		}
		if vals == nil {
			continue
		}
		rows = append(rows, FactoryCompareRow{Key: key, Vals: vals})
		if len(rows) == maxFactoryCompareRows {
			break
		}
	}
	if len(rows) == 0 {
		return nil
	}
	out := &FactoryCompare{Items: make([]*store.Post, len(cands)), Rows: rows}
	for i, c := range cands {
		out.Items[i] = c.p
	}
	return out
}

// fillFactoryHome 为工厂骨架首页装载数据：分类入口 + 最新商品 + 「工厂实力」+ 合作流程
// + 应用行业 + 工厂图集 + FAQ + CTA 文案。product 类型未对本站启用时商品区留空（模板容错渲染）。
// 注意：必须在 v.Posts 装载之后调用——目录骨架的区块编号要知道 News 是否渲染。
func (s *Server) fillFactoryHome(v *View, lang string) {
	if !isFactoryLayout(v.Layout) {
		return
	}
	if s.contentTypeActive("product") {
		v.FactoryProducts, _ = s.store.ListPublishedByType("product", lang, 0, 0, factoryHomeProductLimit(v.Layout))
	}
	if v.Layout == "factory-engineering" {
		// 技术骨架的规格对比表：纯派生（specs 共有键求交集），失败回落 nil → 模板渲染普通栅格。
		v.FactoryCompare = factorySpecCompare(v.FactoryProducts)
	}
	v.FactoryCats = s.factoryCategoryCards(lang)
	v.FactoryStats = parseFactoryStats(s.localizedSetting(factoryStatsSettingKey, lang, ""))
	v.FactoryProcessOn = s.factoryProcessEnabled()
	if v.FactoryProcessOn {
		v.FactoryProcess = s.factoryProcessSteps(lang)
	}
	v.FactoryIndustries = s.factoryIndustryList(lang)
	v.FactoryGallery = parseFactoryGallery(s.store.Setting(factoryGallerySettingKey)) // 图集全局，不分语种
	v.FactoryQAs = s.factoryFAQList(lang)
	v.FactoryCTA = s.factoryCTAText(lang)

	// 目录骨架的区块编号 eyebrow（01/02/…）：跳过未渲染的区块，编号永远连续。
	num := 0
	v.FactorySectionNum = map[string]string{}
	for _, sec := range []struct {
		key string
		on  bool
	}{
		{"categories", len(v.FactoryCats) > 0},
		{"products", true}, // 商品区永远渲染（空货架有占位文案）
		{"process", v.FactoryProcessOn},
		{"industries", len(v.FactoryIndustries) > 0},
		{"gallery", len(v.FactoryGallery) > 0},
		{"faq", len(v.FactoryQAs) > 0},
		{"news", len(v.Posts) > 0},
	} {
		if !sec.on {
			continue
		}
		num++
		v.FactorySectionNum[sec.key] = fmt.Sprintf("%02d", num)
	}
}

// productChips 从商品 extra.specs（repeater）嗅探最多 2 个「卖点规格」，
// 供首页商品卡渲染成 chip：材质（material/材质）与起订量（MOQ/起订量/min order）。
// 没有命中返回空——模板容错不显示。
func productChips(p *store.Post) []FieldPair {
	raw := strings.TrimSpace(p.Extra)
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	var material, moq *FieldPair
	for _, kv := range pairsToList(m["specs"]) {
		k := strings.ToLower(strings.TrimSpace(kv.K))
		v := strings.TrimSpace(kv.V)
		if v == "" {
			continue
		}
		switch {
		case material == nil && (strings.Contains(k, "材质") || strings.Contains(k, "material")):
			material = &FieldPair{K: kv.K, V: v}
		case moq == nil && (strings.Contains(k, "起订") || strings.Contains(k, "moq") ||
			strings.Contains(k, "min. order") || strings.Contains(k, "minimum order") || strings.Contains(k, "min order")):
			moq = &FieldPair{K: kv.K, V: v}
		}
	}
	out := make([]FieldPair, 0, 2)
	if material != nil {
		out = append(out, *material)
	}
	if moq != nil {
		out = append(out, *moq)
	}
	return out
}

// productSKU 从商品 extra.specs（repeater）里嗅探型号/SKU：
// 键名命中 sku / 型号 / model / 货号 / part no 时取其值，供 Product JSON-LD 的 sku 字段。
func productSKU(p *store.Post) string {
	raw := strings.TrimSpace(p.Extra)
	if raw == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	for _, kv := range pairsToList(m["specs"]) {
		k := strings.ToLower(strings.TrimSpace(kv.K))
		switch {
		case strings.Contains(k, "sku"),
			strings.Contains(k, "型号"),
			strings.Contains(k, "货号"),
			k == "model", strings.HasPrefix(k, "model no"), strings.HasPrefix(k, "part no"):
			if v := strings.TrimSpace(kv.V); v != "" {
				return v
			}
		}
	}
	return ""
}

// relatedProducts 商品详情页「相关商品」：同分类最新（不含自身）最多 4 个；
// 同分类不足时回落全部商品补齐。
func (s *Server) relatedProducts(ct *ContentType, p *store.Post, lang string) []*store.Post {
	const limit = 4
	out := make([]*store.Post, 0, limit)
	seen := map[int64]bool{p.ID: true}
	add := func(list []*store.Post) {
		for _, item := range list {
			if item == nil || seen[item.ID] || len(out) >= limit {
				continue
			}
			seen[item.ID] = true
			out = append(out, item)
		}
	}
	if p.Category != nil && p.Category.ID > 0 {
		list, _ := s.store.ListPublishedByType(ct.Key, lang, p.Category.ID, 0, limit+1)
		add(list)
	}
	if len(out) < limit {
		list, _ := s.store.ListPublishedByType(ct.Key, lang, 0, 0, limit+1)
		add(list)
	}
	return out
}
