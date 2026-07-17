package web

import "strings"

// 后台主题选择器（外观设置页）：卡片=配色族（同一设计的纯配色变体共享一张卡），
// 卡底色卡排=族内皮肤（data-theme，配色）。本文件只管展示层的聚合与配色采样，
// 存储零变化：选中/保存的仍是皮肤 id（settings 键 "theme"）。
//
// 分组规则（合并的唯一合法理由=「同一设计的纯配色变体」）：
//   - 非 topbar 骨架的皮肤组生来就是按配色变体设计的一族（工厂五骨架 ×4 皮、
//     各骨架的原生皮+反差皮+两套浅色皮）→ 族 = 骨架 id，沿用骨架的中英对名与定位；
//   - topbar 的元老皮肤是各自独立的设计（字体/纹理/装饰/布局手感各异，骨架相同
//     只是必要条件不是充分条件）→ 族 = 皮肤自身，独立成卡、恢复自己的名字与描述；
//   - 例外经逐皮审定后登记在 themeFamilies（见该表注释）。

// ThemeSkeletonInfo 是一个骨架的展示信息：中英对名 + 一句话定位（从原生皮描述提炼）。
type ThemeSkeletonInfo struct{ Name, Desc string }

// themeSkeletons 给每个骨架（layoutForTheme 的取值）起中英对名与定位描述，
// 供「族=骨架」的多皮族卡命名用。命名尽量沿用原生皮的名字；皮味太重的
// （夜幕/千禧/喧哗…）换成结构中性的叫法。topbar 不在此表：顶栏元老皮各自成族，
// 卡片直接用皮肤自己的名字与描述。
var themeSkeletons = map[string]ThemeSkeletonInfo{
	"sidebar":             {"侧栏 · Sidebar", "左侧常驻竖栏（品牌 + 导航）+ 右侧阅读流，个人站 / 文档站气质"},
	"bento":               {"拼贴 · Bento", "错落卡片网格首页（顶栏照旧），作品集 / 个人主页气质"},
	"index":               {"索引 · Index", "首页是一张排版化内容索引表：等宽编号 + 发丝线 + 大留白"},
	"split":               {"分屏 · Split", "满屏左右分栏：左侧巨型标题 + 右侧整块精选"},
	"axis":                {"中线 · Axis", "全居中宣言式：巨型居中标题 + 中线分隔的居中列表"},
	"bands":               {"光带 · Bands", "全宽交替色带分区：一屏一段的纵向叙事带"},
	"ticker":              {"行情 · Ticker", "顶部滚动跑马灯 + 下方实时信息流，行情 / 数据面板质感"},
	"liftoff":             {"起飞 · Liftoff", "单一 CTA 发射页：巨型标题 + 供给进度条 + 主按钮"},
	"board":               {"看板 · Board", "多列看板 / 路线图：全宽横幅精选 + 横向泳道列与迷你卡片"},
	"timeline":            {"时间线 · Timeline", "居中竖脊时间线：单发丝线主轴 + 圆点节点 + 等宽日期"},
	"deck":                {"横卷 · Deck", "横向滚动影卷：整屏卡片侧滑 + 锚点翻页，作品集 / lookbook"},
	"poster":              {"封幕 · Poster", "整屏封面图 + 压图大字 + 纵向 scroll-snap 分屏折叠"},
	"uptime":              {"状态 · Uptime", "状态页：总状态横幅 + 组件在线率格条 + 事件时间线"},
	"profile":             {"名帖 · Profile", "无导航个人页：头像 + 可点大按钮链接栈"},
	"bloom":               {"草木 · Bloom", "有机曲面：blob 裁剪 hero + 左右交错叶卡 + 波浪分隔"},
	"desktop":             {"桌面 · Desktop", "仿 OS 桌面：窗口容器 + 文件夹散布 + 任务栏"},
	"cinema":              {"荧幕 · Cinema", "宽荧幕影格：2.39:1 黑边 + 整屏场景 + 时间码"},
	"collage":             {"剪贴 · Collage", "反网格拼贴：叠错旋转卡片 + 便签胶带 + 涂鸦箭头"},
	"constellation":       {"星图 · Constellation", "可筛选生态名录：分类芯片 + 实时搜索过滤项目卡片网格"},
	"masonry":             {"瀑布 · Masonry", "多列瀑布流：变高卡片沿 2–4 列自然下落 + 宽幅精选卡"},
	"feed":                {"动态 · Feed", "社交动态流：左侧常驻名片栏 + 居中单列帖子卡"},
	"gazette":             {"头版 · Gazette", "对开报纸头版：巨型报头 + 粗细双线 + 分栏线多栏正文"},
	"manual":              {"手册 · Manual", "三栏手册页：左侧章节目录 + 中间编号小节 + 右侧速览卡"},
	"almanac":             {"月历 · Almanac", "月历首页：七列月格把文章钉成日子 + 点线议程清单"},
	"inbox":               {"收件箱 · Inbox", "邮件客户端三栏：文件夹栏 + 邮件列表 + 精选阅读窗格"},
	"catalog":             {"货架 · Shelves", "资源目录首页：品牌导语 + 精选橱窗 + 分类货架与紧凑商品卡"},
	"broadcast":           {"广播 · Broadcast", "广播节目首页：大幅主节目播放器 + 频道刻度 + 编号节目单"},
	"exhibit":             {"展厅 · Exhibit", "策展式首页：展签标题 + 非对称作品墙 + 展厅分类导览"},
	"factory-catalog":     {"目录 · Catalog", "工厂目录式首页：hero 条 + 商品栅格 + 弱化文章区，SKU 多的机械 / 零部件 / 轻工工厂"},
	"factory-showcase":    {"展台 · Showcase", "工厂展台式首页：精选商品横排 + 工厂实力 + 最新动态，精品少 SKU 的展示型工厂"},
	"factory-onepage":     {"单页 · Onepage", "工厂单页式首页：主打产品→实力→流程→FAQ→询盘一页滚到底，页头导航即页内锚点，小微工厂 / 单一产品线"},
	"factory-solutions":   {"方案 · Solutions", "工厂方案式首页：应用行业大卡做一级入口 + 定制流程 + 商品作为案例产出，OEM/ODM 定制厂"},
	"factory-engineering": {"技术 · Engineering", "工厂技术式首页：核心产品规格对比表 + 认证墙 + 参数分类入口，等宽高密度，精密制造 / 工程师采购"},
	"factory-trade":       {"经典外贸 · Trade", "工厂门户式首页：双层页头（顶部联系条 + 主导航）+ 横幅与左分类栏右商品列表 + 四栏大页脚，成熟出口工厂"},
	"factory-sidebar":     {"侧栏目录 · Sidebar", "左侧常驻竖栏（品牌 + 分类树 + 联系按钮）+ 右侧密集目录流 + 一行极简页脚，SKU 密集的仓储型工厂"},
	"factory-vision":      {"沉浸展示 · Vision", "全屏大图页头（导航透明悬浮，滚动加实底）+ 大留白视觉流 + 页脚即获取报价通栏，旗舰产品形象站"},
	"factory-herofold":    {"门楣 · Herofold", "导航嵌入 hero 的一体化门楣首屏（四周留边 + 大圆角，滚动后剥离吸顶），内容与页脚走常规工厂式"},
	"dtc-flagship":        {"品牌旗舰 · Flagship", "独立站旗舰首页：生活方式大图 hero + 系列大卡 + 畅销栅格 + 品牌故事 + 用户评价，跨境卖家自有品牌官网"},
	"dtc-solo":            {"单品爆款 · Solo", "独立站单品长页：痛点 hero→卖点分解→规格→使用场景→评价→大 CTA 一滚到底，单一爆品品牌站"},
	"dtc-lookbook":        {"系列画册 · Lookbook", "独立站视觉画册：通栏大图墙 + 分类系列陈列、悬停出品名、极少文字，服饰 / 首饰 / 设计师品牌"},
}

// themeSkeletonDescEN 是骨架定位描述的英文版（英文后台用，对应 themeDescEN 的做法）。
var themeSkeletonDescEN = map[string]string{
	"sidebar":             "Persistent left rail (brand + nav) with a reading stream on the right — personal sites and docs",
	"bento":               "Staggered bento card grid homepage — portfolios and personal pages",
	"index":               "The homepage as a typographic index table: monospaced numbering, hairlines and generous whitespace",
	"split":               "Full-screen split: a giant title on the left, a featured block on the right",
	"axis":                "Centered manifesto: a huge centered title and a centerline-divided list",
	"bands":               "Full-width alternating bands — one screen, one chapter of vertical storytelling",
	"ticker":              "A scrolling ticker on top and a live feed below — market and data-panel feel",
	"liftoff":             "Single-CTA launch page: giant title, supply progress bar and one primary button",
	"board":               "Multi-column board/roadmap: full-width featured banner plus horizontal swimlanes of compact cards",
	"timeline":            "Centered spine timeline: one hairline axis, dot nodes and monospaced dates",
	"deck":                "Horizontal scrolling deck: full-screen cards with snap paging — portfolios and lookbooks",
	"poster":              "Full-screen covers with oversized type and vertical scroll-snap folds",
	"uptime":              "Status page: overall banner, component uptime bars and an incident timeline",
	"profile":             "Nav-free bio page: avatar plus a stack of large link buttons",
	"bloom":               "Organic curves: blob-clipped hero, alternating leaf cards and wavy dividers",
	"desktop":             "OS-style desktop: window chrome, scattered folders and a taskbar",
	"cinema":              "Widescreen frames: 2.39:1 letterboxing, full-screen scenes and timecodes",
	"collage":             "Anti-grid collage: overlapping rotated cards, sticky notes, tape and doodle arrows",
	"constellation":       "Filterable directory: category chips and live search over a grid of project cards",
	"masonry":             "Multi-column masonry: variable-height cards flowing down 2–4 columns with a wide featured card",
	"feed":                "Social feed: a persistent profile rail and a centered single-column stream of post cards",
	"gazette":             "Broadsheet front page: giant masthead, double rules and multi-column body text",
	"manual":              "Three-pane handbook: chapter nav, numbered sections and quick-reference cards",
	"almanac":             "Calendar homepage: a seven-column month grid pinning posts to days, plus a dotted agenda list",
	"inbox":               "Three-pane mail client: folder rail, message list and a featured reading pane",
	"catalog":             "Resource directory: brand intro, featured showcase and categorized shelves of compact cards",
	"broadcast":           "Radio-show homepage: a large featured player, channel dial and numbered programme list",
	"exhibit":             "Curated exhibition: wall labels, an asymmetric works wall and gallery-style category tours",
	"factory-catalog":     "Factory catalog homepage: hero strip, product grid and a de-emphasized article section — for SKU-heavy factories",
	"factory-showcase":    "Factory showcase homepage: featured product row, capability stats and latest updates — for low-SKU exhibitors",
	"factory-onepage":     "Single-page factory site: hero, flagship products, stats, workflow, FAQ and inquiry CTA on one scroll, with in-page anchor nav — for micro factories and single product lines",
	"factory-solutions":   "Solutions-first factory homepage: large industry/application cards as the primary entry, custom workflow and products as case output — for OEM/ODM manufacturers",
	"factory-engineering": "Engineering-grade factory homepage: spec comparison table of core products, certification wall and parameter category entries, dense and monospace-heavy — for engineer buyers",
	"factory-trade":       "Classic trade-portal factory site: double-deck header (utility contact strip + main nav), banner homepage with a category rail beside the product grid, and a four-column mega footer — for established exporters",
	"factory-sidebar":     "Persistent left rail (brand, category tree and a contact button) with a dense directory stream and a one-line footer — for SKU-heavy warehouse factories",
	"factory-vision":      "Immersive full-screen hero header with a floating transparent nav that solidifies on scroll, generous whitespace and a footer that doubles as the quote CTA — for flagship product showcases",
	"factory-herofold":    "A hero fold that embeds the nav inside an inset rounded container; the nav detaches and sticks solid after scrolling, with conventional factory content and footer below",
	"dtc-flagship":        "Brand-store flagship homepage: lifestyle hero, collection cards, bestseller grid, brand-story numbers and customer reviews — for cross-border DTC brand sites",
	"dtc-solo":            "Single-product long-scroll funnel: pain-point hero, alternating selling points, spec table, in-use scenes, reviews and a big closing CTA — for one-hero-product brands",
	"dtc-lookbook":        "Visual-first lookbook: full-bleed image wall and collection-by-collection product walls with hover-revealed names and minimal copy — for fashion, jewellery and designer brands",
}

// themeBgDefault 是每个皮肤的底色（public.css 里该皮 :root 变量块的 --bg），
// 色卡「主色 + 底色」双色呈现用；缺省（editorial / sidebar 等骑默认调色板的皮）
// 回落基础纸色 themeBgFallback。与 themeAccentDefault 同一套维护方式。
const themeBgFallback = "#fbfaf7"

var themeBgDefault = map[string]string{
	"magazine": "#ffffff", "terminal": "#0b0f14", "brutalist": "#f3f3ee", "notebook": "#fbf7ec",
	"swiss": "#ffffff", "pastel": "#faf7ff", "newspaper": "#f6f4ee", "darkpro": "#0e1016",
	"landing": "#fbfcff", "product": "#f8fafc", "prism": "#09090b", "exchange": "#05080d",
	"academy": "#f6f9ff", "garment": "#f7f8f5", "institution": "#f7f4ef", "studio": "#f7f7f2",
	"lifestyle": "#fbf7ef", "knowledge": "#ffffff", "bento": "#f3f3f6", "nocturne": "#0d0f14",
	"terra": "#f6f1ea", "porcelain": "#f4f6f5", "index": "#ffffff", "split": "#f6f5f1",
	"axis": "#ffffff", "journal": "#faf8f3", "blueprint": "#eef1f5", "riso": "#f2efe6",
	"quiet": "#f7f5f0", "lucid": "#f5f5f2", "aurora": "#f4f6fc", "bands": "#ffffff",
	"ticker": "#fbfcfb", "liftoff": "#f6f6f4", "board": "#eef1f6", "timeline": "#f7f3ea",
	"deck": "#f3f1ec", "poster": "#f4f2ec", "uptime": "#f6f8fa", "profile": "#fff7f1",
	"bloom": "#f5f1e6", "desktop": "#cfeaf7", "cinema": "#0a0b0d", "collage": "#fdf3e3",
	"constellation": "#fafbff", "gilded": "#151110", "grove": "#f7f4ec", "obsidian": "#0d0f10",
	"codex": "#f8f4ea", "gilt": "#17120f", "zenith": "#0b0f1c", "fir": "#f7f5ec",
	"ember": "#131215", "ignition": "#0b0d12", "cork": "#f1e9d8", "orbit": "#0a0f19",
	"runway": "#0d0b0c", "velvet": "#171114", "pulse": "#0a0f16", "onyx": "#0e1013",
	"lotus": "#edf3f1", "vapor": "#171130", "matinee": "#f6f2ea", "rave": "#131312",
	"astrolabe": "#0c1120", "masonry": "#fafaf8", "darkroom": "#101013", "feed": "#eef2f6",
	"noir": "#000000", "gazette": "#f6f1e4", "tabloid": "#0c0c0d", "manual": "#f5f7fa",
	"kernel": "#0f1319", "almanac": "#f7f3e8", "nightshift": "#0a0d16", "inbox": "#f2f4f8",
	"midnight": "#0b101b", "catalog": "#f4f1eb", "nightmarket": "#07110f", "broadcast": "#f7f5f2",
	"airwave": "#090b19", "exhibit": "#f2f0eb", "afterhours": "#11100f", "industrial": "#f2f4f6",
	"machinist": "#eceef0", "tradewind": "#f7f6f0", "foundry": "#141110", "showroom": "#fafbfc",
	"assembly": "#f0f1f3", "harbor": "#f1f6f6", "gunmetal": "#101418", "paperwhite": "#f5f7fb",
	"citrus": "#fff9de", "bookshop": "#f7f8fc", "canal": "#eaf7f5", "confetti": "#f7f7fb",
	"icebox": "#eaf4ff", "ledger": "#f7faf8", "signal": "#f2f3f5", "gallery": "#f7f8fb",
	"coast": "#e8f6f7", "monument": "#f5f7fa", "petal": "#fff3f5", "market": "#eef7ff",
	"seaside": "#e6f7f7", "daytrade": "#f3f6f4", "mintwire": "#e8f8f1", "sunrise": "#fff1df",
	"horizon": "#eaf5ff", "workshop": "#f1f5fa", "playbook": "#eef5f1", "chronicle": "#f7f8fc",
	"gardenpath": "#edf6ec", "portfolio": "#f5f5f4", "postcard": "#eaf5ff", "atelier": "#f7f7f5",
	"festival": "#fff6c9", "daywatch": "#f1f6fa", "clinic": "#eaf7f6", "peach": "#fff0ed",
	"skyline": "#eef7ff", "herbarium": "#edf3e9", "coralreef": "#e8f7f6", "cloudos": "#dfefff",
	"candyglass": "#ffeaf4", "paperfilm": "#f7f7f4", "azurefilm": "#eaf3fb", "cutpaper": "#f7f7f3",
	"primary": "#eeeeec", "atlas": "#f2f6fb", "mintmap": "#e8f7f0", "pinboard": "#f7f8fb",
	"spectrum": "#f1f2f5", "daybook": "#f0f5fa", "civic": "#f2f3f5", "broadsheet": "#f6f7f8",
	"salmonpress": "#fbe7df", "fieldguide": "#f2f6f1", "bluebook": "#eaf2fb", "sunclock": "#fff7cf",
	"seedcalendar": "#edf4e8", "postbox": "#f3f5f8", "airmail": "#e9f4fb", "apothecary": "#eaf5ef",
	"toolroom": "#eef1f5", "publicradio": "#f5f6f8", "morningfm": "#eaf6fb", "whitecube": "#f7f7f6",
	"botanical": "#edf3ea",
	"packline":  "#f7f5ef", "carbon": "#141414", "linen": "#f8f6f1", "redline": "#f7f7f6",
	"drafting": "#eef3f4", "flagship": "#0d1626", "concrete": "#efeeec", "amberpress": "#faf6ed",
	"phosphor": "#0b100d", "schematic": "#f3f7fb", "titanium": "#eef0f2", "hazard": "#f5f4f1",
	"navigator": "#f4f6f9", "cargo": "#f8f4ee", "mistblue": "#eef2f6", "malachite": "#0f1613",
	"steelrack": "#eff1f2", "depot": "#f6f3ea", "nightbay": "#101317", "plateblue": "#f2f6fa",
	"eclipse": "#0b0b0d", "haze": "#f5f5f2", "copperglow": "#16100c", "indigo": "#f2f3fa",
	"glaze": "#f7f7f4", "carbonblue": "#eef1f4", "warmsand": "#f7f2ea", "nightfall": "#101019",
	// 净白系：纯白底（色卡上的「白格」，一眼可辨）
	"purewhite": "#ffffff", "gallerywhite": "#ffffff", "pagewhite": "#ffffff",
	"planwhite": "#ffffff", "specwhite": "#ffffff", "portwhite": "#ffffff",
	"rackwhite": "#ffffff", "whitehall": "#ffffff", "archwhite": "#ffffff",
	// 外贸独立站主题族
	"cream": "#fdfbf6", "amberglow": "#faf3e8", "inknavy": "#0e1420", "oliveleaf": "#f4f4ec",
	"dawnfair":  "#ffffff",
	"solowhite": "#ffffff", "charcoal": "#17181a", "coralpop": "#fff6f2", "limewash": "#f2f6ee",
	"galleria": "#fafafa", "blackbox": "#0c0c0e", "flaxen": "#f6f2e9", "fogblue": "#eef1f4",
}

// themeBg 返回皮肤色卡的底色，未登记的回落基础纸色。
func themeBg(id string) string {
	if v := themeBgDefault[id]; v != "" {
		return v
	}
	return themeBgFallback
}

// themeFamilies 是皮肤 → 配色族的显式登记，只收「缺省规则之外」的归属；
// 族 id 必须是注册表里更早出现的皮肤 id（该皮肤即族的门面，卡片用它的名字与描述）。
//
// 逐皮审定结论（2026-07 通读 public.css，判据=除色值变量外排版/纹理/组件样式是否一致）：
// topbar 的 29 个元老皮里只有 paperwhite / citrus 是默认设计（editorial 骑默认调色板）
// 的纯配色变体——两者仅覆写 :root 色值变量（外加 citrus 两条纯色彩的组件覆写），
// 字体、纹理、组件结构零改动，并入 editorial 族；其余 26 皮各有独有的排版/纹理/装饰
// （terminal 等宽终端、brutalist 粗框硬阴影、riso 网点套印偏移、quiet 和风留白、
// gilded 细字重宽字距、porcelain 巨字 hero……），互不为配色变体，独立成卡。
var themeFamilies = map[string]string{
	"paperwhite": "editorial",
	"citrus":     "editorial",
	// dawnfair 挂 dtc-flagship 骨架,但覆写了字重/按钮/卡片结构(Shopify Dawn 设计身份),
	// 按「合并唯一合法理由=纯配色变体」的铁律独立成卡(用户实测也找不到它)。
	"dawnfair": "dawnfair",
}

// familyForTheme 返回皮肤所属的配色族 id：
// 显式登记优先；非 topbar 骨架 → 骨架 id；topbar 元老皮 → 皮肤自身。
func familyForTheme(id string) string {
	if f, ok := themeFamilies[id]; ok {
		return f
	}
	if l := layoutForTheme(id); l != "topbar" {
		return l
	}
	return id
}

// ThemeFamilyCard 是设置页一张配色族卡：多皮族卡底是色卡排（点色卡切皮肤）；
// 独立皮族只有一张卡、不渲染色卡排。
type ThemeFamilyCard struct {
	Family     string      // 族 id（骨架族=骨架 id；独立皮族=皮肤 id；兜底卡为 "custom"）
	Name, Desc string      // 骨架族用骨架中英对名+定位；独立皮族用皮肤自己的名字与描述
	Categories string      // 皮肤分类去重集合（空格分隔，注册表顺序），分类 chips 过滤用
	Active     ThemeCard   // 当前激活皮肤：站点选中皮肤属于本族时为它，否则第一个皮肤
	Selected   bool        // 站点当前选中的皮肤是否在本族下
	Skins      []ThemeCard // 该族下全部皮肤（注册表顺序）
}

// themeSkeletonName 按后台语言取骨架名：英文后台只留「·」后的英文半段（同 themeOptionForAdmin）。
func themeSkeletonName(name, lang string) string {
	if !strings.HasPrefix(strings.ToLower(lang), "en") {
		return name
	}
	if i := strings.LastIndex(name, " · "); i >= 0 {
		return strings.TrimSpace(name[i+len(" · "):])
	}
	return name
}

// themeFamilyCards 把皮肤卡按配色族聚合成族卡：卡序=族在注册表里首次出现的顺序，
// 皮序=注册表顺序。cards 与 Themes 注册表一一对应（含各自微调值与 Bg，已按后台语言本地化）。
// 命名：骨架族取 themeSkeletons 的中英对名与定位（英文后台转英文）；
// 独立皮族（族 id=皮肤 id）直接用带头皮肤的名字与描述（cards 已本地化，无需再转）。
// selected 是站点当前选中的皮肤 id；若它不在注册表（注册表收缩后的残留值），
// 追加一张「自定义 · Custom」兜底卡保住选中态入口（保存时 validTheme 照旧把关）。
func themeFamilyCards(cards []ThemeCard, selected, adminLang string) []ThemeFamilyCard {
	var order []string
	byFamily := map[string]*ThemeFamilyCard{}
	selectedFound := false
	for _, c := range cards {
		family := familyForTheme(c.ID)
		fc, ok := byFamily[family]
		if !ok {
			var name, desc string
			if info, isSkeleton := themeSkeletons[family]; isSkeleton {
				name, desc = info.Name, info.Desc
				if strings.HasPrefix(strings.ToLower(adminLang), "en") {
					name = themeSkeletonName(name, adminLang)
					if en := themeSkeletonDescEN[family]; en != "" {
						desc = en
					}
				}
			} else {
				// 独立皮族 / 皮肤 id 命名的族：门面=注册表里最先出现的皮肤。
				name, desc = c.Name, c.Desc
			}
			if name == "" { // 兜底：别让卡空标题
				name = family
			}
			fc = &ThemeFamilyCard{Family: family, Name: name, Desc: desc}
			byFamily[family] = fc
			order = append(order, family)
		}
		fc.Skins = append(fc.Skins, c)
		if !strings.Contains(" "+fc.Categories+" ", " "+c.Category+" ") {
			if fc.Categories != "" {
				fc.Categories += " "
			}
			fc.Categories += c.Category
		}
		if c.ID == selected {
			fc.Selected = true
			fc.Active = c
			selectedFound = true
		}
	}
	out := make([]ThemeFamilyCard, 0, len(order)+1)
	for _, family := range order {
		fc := byFamily[family]
		if !fc.Selected {
			fc.Active = fc.Skins[0]
		}
		out = append(out, *fc)
	}
	if !selectedFound && selected != "" {
		// 注册表外的残留选中值：单独一张「自定义」卡，别丢选中态入口。
		name := "自定义 · Custom"
		desc := "当前保存的主题不在内置注册表中（可能来自旧版本）；改选任意主题保存后此卡消失。"
		if strings.HasPrefix(strings.ToLower(adminLang), "en") {
			name = "Custom"
			desc = "The saved theme is not in the built-in registry (possibly from an older version); pick and save any theme to dismiss this card."
		}
		c := ThemeCard{ID: selected, Name: selected, Desc: desc, Accent: themeAccentDefault["editorial"], Radius: "10", Bg: themeBgFallback}
		out = append(out, ThemeFamilyCard{Family: "custom", Name: name, Desc: desc, Active: c, Selected: true, Skins: []ThemeCard{c}})
	}
	return out
}
