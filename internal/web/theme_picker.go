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
	"tracklist":           {"唱片集 · Tracklist", "一张唱片的印刷内页：方形专辑封面 + 等宽序号曲目行 + Side 分面 + Credits 页脚"},
	"departure":           {"发车牌 · Departure", "车站告示板：翻牌式行列（时刻｜目的地｜站台）+ 即将发车精选 + 候车厅分类色块"},
	"lexicon":             {"辞书 · Lexicon", "词典词条排版：今日词条大词头（词性 / 义项）+ 词条密排 + 首字母索引竖条"},
	"bistro":              {"菜单 · Bistro", "居中双线框餐牌：分类菜单节 + 虚线引导条目 + 本日特供精选框"},
	"serial":              {"章回 · Serial", "连载章回体：续读大卡 + 汉字序号章回目录 + 分卷页签 + 待续朱印"},
	"verse":               {"诗笺 · Verse", "竖排诗笺：真竖排题签（朱丝栏界格，非中文自动降级）+ 笺条卡两列 + 朱印页脚"},
	"archway":             {"门坊 · Archway", "居中门坊：徽标居上 + 细线居中导航 + 拱门线框精选 + 居中单列条目"},
	"gutter":              {"书缝 · Gutter", "摊开的书：对称书页双栏 + 正中书缝竖排分类导航（圆章 + 页码书口）"},
	"cover":               {"封面 · Cover", "首页即杂志封面：巨型刊名 + 居中细行导航 + 封面故事 + 四角 coverlines"},
	"marquee":             {"戏台 · Marquee", "剧院门头天幕：灯泡边框 + 霓虹站名 + 导航胶囊嵌天幕 + 场次单内容行"},
	"triptych":            {"三联 · Triptych", "楣梁居中承载站名与导航 + 三联祭坛画：中央圆拱精选 + 两翼列表"},
	"stubs":               {"票根 · Stubs", "撕边打孔票根行卡：票号段｜正片段｜日期条码段，精选=放大版票"},
	"cardfile":            {"目录柜 · Cardfile", "卡片目录柜：分类抽屉墙（标签+黄铜拉手）+ 桌面摊开的打字机索引卡"},
	"script":              {"剧本 · Script", "台词本排版：INT. 场景行 + 居中角色名与对白 + SCENE 场号条目"},
	"postmark":            {"邮戳 · Postmark", "明信片墙：齿孔邮票框 + 圆邮戳 + 地址线区，微角度错落"},
	"metro":               {"线网 · Metro", "地铁线网图：分类=彩色线路，文章=沿线站点，换乘大站=精选"},
	"circuit":             {"电路 · Circuit", "PCB 板：焊点网格 + 铜走线 + 芯片卡（丝印编号/引脚行），精选=主控大芯片"},
	"specimen":            {"标本 · Specimen", "博物图鉴：PLATE 图版编号 + 大标本区 + FIG. 针脚小卡"},
	"lockers":             {"储物柜 · Lockers", "一墙编号柜门（百叶+号码牌），精选=打开的柜门，分类=柜区"},
	"auction":             {"拍卖 · Auction", "拍卖图录：LOT 编号拍品行 + 封面拍品大版与落槌签"},
	"lattice":             {"花窗 · Lattice", "园林花窗框景：八角窗精选 + 圆洞门与方窗卡 + 黛瓦横带"},
	"factory-catalog":     {"目录 · Catalog", "工厂目录式首页：hero 条 + 商品栅格 + 弱化文章区，SKU 多的机械 / 零部件 / 轻工工厂"},
	"factory-showcase":    {"展台 · Showcase", "工厂展台式首页：精选商品横排 + 工厂实力 + 最新动态，精品少 SKU 的展示型工厂"},
	"factory-onepage":     {"单页 · Onepage", "工厂单页式首页：主打产品→实力→流程→FAQ→询盘一页滚到底，页头导航即页内锚点，小微工厂 / 单一产品线"},
	"factory-solutions":   {"方案 · Solutions", "工厂方案式首页：应用行业大卡做一级入口 + 定制流程 + 商品作为案例产出，OEM/ODM 定制厂"},
	"factory-engineering": {"技术 · Engineering", "工厂技术式首页：核心产品规格对比表 + 认证墙 + 参数分类入口，等宽高密度，精密制造 / 工程师采购"},
	"factory-trade":       {"经典外贸 · Trade", "工厂门户式首页：双层页头（顶部联系条 + 主导航）+ 横幅与左分类栏右商品列表 + 四栏大页脚，成熟出口工厂"},
	"factory-sidebar":     {"侧栏目录 · Sidebar", "左侧常驻竖栏（品牌 + 分类树 + 联系按钮）+ 右侧密集目录流 + 一行极简页脚，SKU 密集的仓储型工厂"},
	"factory-vision":      {"沉浸展示 · Vision", "全屏大图页头（导航透明悬浮，滚动加实底）+ 大留白视觉流 + 页脚即获取报价通栏，旗舰产品形象站"},
	"factory-herofold":    {"门楣 · Herofold", "导航嵌入 hero 的一体化门楣首屏（四周留边 + 大圆角，滚动后剥离吸顶），内容与页脚走常规工厂式"},
	"factory-andon":       {"安灯板 · Andon", "班次状态灯导航 + LED 大数字屏 hero + 三色状态条工单卡与排产板"},
	"factory-certwall":    {"认证墙 · Certwall", "资质页签导航 + 认证徽章环绕 hero + 合规卡与逐项认证块"},
	"factory-container":   {"集装箱 · Container", "货运单页头 + 箱面大牌 hero + 波纹钢箱面商品卡与堆场图带"},
	"factory-crate":       {"木箱 · Crate", "唛头行导航 + 大木箱面 hero + 箱面商品卡与装箱单参数"},
	"factory-draftdesk":   {"图纸台 · Draftdesk", "蓝图网格底 + 图层标签页导航 + 图签框 hero + 图纸卡与标注引线"},
	"factory-exportmap":   {"航线图 · Exportmap", "时区细条 + 世界航线点阵 hero + 港口代码商品卡，按市场分组"},
	"factory-floorplan":   {"平面图 · Floorplan", "图例条导航 + 车间俯视分区图 hero + 区号色标设备卡，按区分组"},
	"factory-furnace":     {"熔炉 · Furnace", "炉号标签导航 + 炉口橙光 hero + 阶梯色温流程与铸件卡"},
	"factory-gantry":      {"龙门 · Gantry", "龙门架页头（横梁包住导航、标题吊下）+ 横梁铭板 + 吊装件卡"},
	"factory-gauge":       {"仪表墙 · Gauge", "拨杆开关导航 + 指针表盘 hero（数据即仪表）+ 管线流程与控制模块卡"},
	"factory-hazardtape":  {"警示带 · Hazardtape", "安全帽色块导航 + 黄黑警示带 + 安全生产大牌与粗边框卡"},
	"factory-inspection":  {"质检单 · Inspection", "检验流程页头与文档编号章 + 报告头 hero + 规格单行卡与逐项检验表"},
	"factory-line":        {"产线 · Line", "工位标签条导航 + 传送带横带 + 沿带工位卡商品，流程即产线节点串"},
	"factory-nameplate":   {"铭牌 · Nameplate", "蚀刻细字导航 + 金属拉丝大铭牌 hero + 小铭牌商品卡与型号网格"},
	"factory-pipeworks":   {"管廊 · Pipeworks", "阀门圆点导航 + 总管横条 + 表压读数 + 法兰流程与管段卡"},
	"factory-quotation":   {"报价单 · Quotation", "单据步骤导航 + 单头条款行 + 五列报价表行商品与报价参数表"},
	"factory-rackwall":    {"货架 · Rackwall", "通道牌导航 + 承重铭牌 + 托盘位商品卡（上下横梁夹持）与货架分层"},
	"factory-sampleroom":  {"样品间 · Sampleroom", "样品册页签 + 色卡样品条 hero + 样品挂签商品卡与色卡墙"},
	"factory-shutter":     {"卷帘 · Shutter", "门牌号导航 + 档口小牌 + 卷帘门格商品（精选整门卷起）与门市街"},
	"factory-tonnage":     {"吨位 · Tonnage", "粗野大写页头 + 超大数字 hero + 牌号大卡与大字参数块"},
	"dtc-flagship":        {"品牌旗舰 · Flagship", "独立站旗舰首页：生活方式大图 hero + 系列大卡 + 畅销栅格 + 品牌故事 + 用户评价，跨境卖家自有品牌官网"},
	"dtc-solo":            {"单品爆款 · Solo", "独立站单品长页：痛点 hero→卖点分解→规格→使用场景→评价→大 CTA 一滚到底，单一爆品品牌站"},
	"dtc-lookbook":        {"系列画册 · Lookbook", "独立站视觉画册：通栏大图墙 + 分类系列陈列、悬停出品名、极少文字，服饰 / 首饰 / 设计师品牌"},
	"dtc-vitrine":         {"橱窗 · Vitrine", "细顶条导航（右上购物袋）+ 三联橱窗首屏（中窗大图压文案）+ 白底细框竖卡商品橱窗，列表同款竖卡、详情左大图右粘性购买栏"},
	"dtc-journal":         {"刊物店 · Journal", "居中刊头双行导航 + 封面故事跨页（hero 与首件商品图文并排）+「本期选物」三栏条，列表走编辑流、详情是杂志专题页"},
	"dtc-catalogue":       {"型录 · Catalogue", "左侧常驻分类索引竖栏 + 印刷型录编号行（No.NNN + 缩略图 + 规格签 + 右端箭头），详情把规格表提到顶部做双线表头"},
	"dtc-bazaar":          {"集市 · Bazaar", "裁角布条横幅导航 + 开市词首屏 + 木牌胶囊分类与摊位卡（顶部摊棚条、斜放规格签当价签），详情带摊主故事块"},
	"dtc-column":          {"直列 · Column", "无横栏导航（左上站名 + 右缘悬浮圆点节），整页一屏一款：每件商品独占一屏，SHOT 编号由 CSS 计数生成"},
	"dtc-swissgrid":       {"严选格 · Swissgrid", "双层导航（顶部公告细条 + 主导航）+ 瑞士粗黑体大字行 + 1px 网格线陈列（SG-NN 编号），详情按图｜参数｜购买三格拼版"},
	"dtc-runway":          {"秀场 · Runway", "透明悬浮导航压全屏开场 Look + LOOK 编号竖构图横排卡，详情是「Look 页」：左全身大图 + 右单品清单"},
	"dtc-atelier":         {"工坊 · Atelier", "左竖签工牌导航（920px 转顶部）+ 工作台口述叙事 hero + 器物卡带工艺行，详情走制作过程纵向时间线"},
	"dtc-monthbox":        {"月盒 · Monthbox", "居中三段页签导航 +「本月盒」立体大卡（期号 + 首件商品图）+ 往期盒小卡横排，详情是「盒内清单」勾选行"},
	"dtc-booth":           {"展位 · Booth", "展馆指示牌导航（裁角箭头胶囊）+ 品牌墙深色横幅 + A-NN 展位编号卡，详情是参数册 + 预约洽谈大 CTA"},
	"dtc-apothecary":      {"药柜 · Apothecary", "居中瓷标签横排导航 + 药柜题签 hero + 瓶签商品卡（瓶盖装饰 + 编号），详情按成分/用法双表并排"},
	"dtc-grocer":          {"食铺 · Grocer", "悬挂木牌导航 + 黑板价目栏 hero（店名粉笔字 + 点线价目行）+ 纸袋货架商品卡，详情有产地/储存双卡与黑板营养行"},
	"dtc-cellar":          {"酒窖 · Cellar", "暗底鎏金细线导航 + 窖藏题词 hero + 横向酒架行（架板粗线 + 年份签），详情是品鉴笔记 + 年份轴"},
	"dtc-basecamp":        {"营地 · Basecamp", "路标木牌箭头导航 + 等高线底纹 + 装备清单行（缩略图 + 名称 + 右端重量列），详情是参数表 + 场景实拍区"},
	"dtc-toybox":          {"玩具盒 · Toybox", "彩色积木块导航 + 粗描边圆角卡与旋转贴纸徽章，详情拆成年龄段/玩法彩边小卡"},
	"dtc-paperie":         {"手账铺 · Paperie", "索引贴纸页签导航（多色轮换）+ 网格纸底与白纸容器 + 胶带贴角商品卡，详情列用纸规格与搭配示范"},
	"dtc-mono":            {"黑白铺 · Mono", "底部固定黑条导航 + 顶部通栏特大黑体 hero + 1px 黑线格陈列，全站零圆角零阴影，详情=大图 + 一行 + 一颗钮"},
	"dtc-flatlay":         {"平铺 · Flatlay", "角落极简导航 + 俯拍马赛克首屏（大格融合 hero 文案，其余格即商品图 + 角落规格签），详情走平铺细节剪裁"},
	"dtc-flyer":           {"传单 · Flyer", "黄黑警示条 + 粗体链接行导航 + 粗黑框头版（爆炸贴纸取真实规格签）+ 传单格卡，详情有装饰条纹与粗框大图"},
	"dtc-lightbox":        {"灯箱 · Lightbox", "霓虹描边胶囊导航 + 大灯箱 hero（青光晕标题）+ 暗底灯箱格（发光内芯），详情是大灯箱图 + 光晕参数卡"},
	"shelf-index":         {"层架 · Shelf Index", "图文 Hero 与连续知识层架组成的研究型内容索引"},
	"tradeoff-sheet":      {"权衡表 · Tradeoff Sheet", "当前关注、决策透镜与连续文章账簿组成的工作表首页"},
	"progress-bulletin":   {"进度公报 · Progress Bulletin", "按日期组织本期公报、分类索引与文章账簿"},
	"margin-reading-room": {"旁注阅览室 · Margin Reading Room", "三栏导读、阅读视角、伴读清单与注解式索引"},
	"light-table":         {"光桌 · Light Table", "大幅 Hero、精选展签与四列接触印样组成的视觉首页"},
	"counterpoint":        {"复调 · Counterpoint", "主题引子与左右双路径文章流组成的对照阅读首页"},
	"seamless-canvas":     {"融幕 · Seamless Canvas", "Hero、Logo、导航与分类索引共用一张连续媒体首屏"},
	"night-corridor":      {"夜廊 · Night Corridor", "沉浸媒体首屏、融合导航、右侧阅读清单与底部内容轨道"},
	"open-ascent":         {"天阶 · Open Ascent", "明亮媒体首屏、融合导航、三条文章索引与精选阅读展签"},
	"pilot-flight-deck":   {"领航台 · Pilot Flight Deck", "客户端视觉、工作流、能力、案例、资源与下载组成的完整产品发布首页"},
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
	"tracklist":           "A record's printed inner sleeve: square album cover, mono-numbered track rows, side tabs and a credits footer",
	"departure":           "Station departure board: split-flap rows (time | destination | platform), a boarding-next lead and concourse category signs",
	"lexicon":             "Dictionary entry typography: a headword-of-the-day with part-of-speech and senses, dense entry rows and an alphabet rail",
	"bistro":              "Centered double-ruled menu card: category sections, dotted-leader entries and a daily-special box",
	"serial":              "Serialized chapters: a continue-reading card, CJK-numbered chapter list, volume tabs and a to-be-continued seal",
	"verse":               "Vertical verse paper: a true vertical-rl masthead with cinnabar ruled columns (auto-degrades for non-CJK), note-card grid and a seal footer",
	"archway":             "Centered archway: monogram above a hairline-framed centered nav, an arched featured frame and a centered single-column list",
	"gutter":              "An open book: symmetric facing pages with a vertical category nav living in the center gutter (seal mark and folio numbers)",
	"cover":               "The homepage is a magazine cover: giant nameplate, centered hairline nav, a cover story and corner coverlines",
	"marquee":             "A theatre marquee header: bulb borders, neon nameplate and pill nav on the canopy, with a playbill list below",
	"triptych":            "A centered lintel bar holds name and nav above a triptych: arched central feature with list wings",
	"stubs":               "Perforated ticket-stub rows — number stub, feature panel and a dated barcode end; the featured post is a large admit-one",
	"cardfile":            "A library card catalog: category drawers with brass pulls above typewriter index cards pulled onto the desk",
	"script":              "Screenplay typography: INT. slug lines, centered character cues and dialogue, and SCENE-numbered entries",
	"postmark":            "A postcard wall: perforated stamps, round postmarks and address-line areas, slightly rotated",
	"metro":               "A transit network map: categories as colored lines, posts as stations, the featured post an interchange",
	"circuit":             "A PCB board: solder-dot grid, copper traces and chip cards with silkscreen numbers; the featured post is the MCU",
	"specimen":            "A natural-history plate: numbered PLATE header, a large mounted specimen and pinned FIG. cards",
	"lockers":             "A wall of numbered lockers with vents; the featured post is an opened door, categories are locker zones",
	"auction":             "An auction catalogue: LOT-numbered rows, a cover lot with hammer tag and double-ruled masthead",
	"lattice":             "Garden lattice windows: an octagonal featured frame, moon-gate and square window cards under a tile band",
	"factory-catalog":     "Factory catalog homepage: hero strip, product grid and a de-emphasized article section — for SKU-heavy factories",
	"factory-showcase":    "Factory showcase homepage: featured product row, capability stats and latest updates — for low-SKU exhibitors",
	"factory-onepage":     "Single-page factory site: hero, flagship products, stats, workflow, FAQ and inquiry CTA on one scroll, with in-page anchor nav — for micro factories and single product lines",
	"factory-solutions":   "Solutions-first factory homepage: large industry/application cards as the primary entry, custom workflow and products as case output — for OEM/ODM manufacturers",
	"factory-engineering": "Engineering-grade factory homepage: spec comparison table of core products, certification wall and parameter category entries, dense and monospace-heavy — for engineer buyers",
	"factory-trade":       "Classic trade-portal factory site: double-deck header (utility contact strip + main nav), banner homepage with a category rail beside the product grid, and a four-column mega footer — for established exporters",
	"factory-sidebar":     "Persistent left rail (brand, category tree and a contact button) with a dense directory stream and a one-line footer — for SKU-heavy warehouse factories",
	"factory-vision":      "Immersive full-screen hero header with a floating transparent nav that solidifies on scroll, generous whitespace and a footer that doubles as the quote CTA — for flagship product showcases",
	"factory-herofold":    "A hero fold that embeds the nav inside an inset rounded container; the nav detaches and sticks solid after scrolling, with conventional factory content and footer below",
	"factory-andon":       "Andon board: shift status lights and an LED numeric hero over work-order cards",
	"factory-certwall":    "Certification wall: credential tabs and a badge-ring hero over compliance cards",
	"factory-container":   "Shipping container: manifest bar nav, corrugated container hero and box-face product cards",
	"factory-crate":       "Export crate: shipping-mark nav and a big timber crate-face hero",
	"factory-draftdesk":   "Drafting table: blueprint grid, layer-tab nav, title-block hero and drawing cards",
	"factory-exportmap":   "Shipping routes: timezone strip, world route-dot hero and port-code product cards",
	"factory-floorplan":   "Floor plan: legend-bar nav and a top-down zoned workshop hero",
	"factory-furnace":     "Foundry furnace: furnace-number nav, glowing hearth hero and casting cards",
	"factory-gantry":      "Gantry crane: beam header wrapping the nav with the title hung from cables",
	"factory-gauge":       "Instrument wall: toggle nav and dial-gauge hero where stats become needles",
	"factory-hazardtape":  "Hazard tape: hard-hat block nav, yellow-black tape bands and safety board hero",
	"factory-inspection":  "Inspection report: process-step nav with a numbered document stamp, report header and specification rows",
	"factory-line":        "Production line: station-tag nav, conveyor band and station cards along the belt",
	"factory-nameplate":   "Machine nameplate: etched nav and a brushed-metal plate hero with spec grid",
	"factory-pipeworks":   "Pipework: valve-dot nav, header pipe and pressure-gauge stats with flanged flow",
	"factory-quotation":   "Quotation sheet: document-step nav, terms header and a five-column price table",
	"factory-rackwall":    "Warehouse racking: aisle-sign nav, load plate and pallet-position cards between beams",
	"factory-sampleroom":  "Sample room: swatch-book tabs, colour-strip hero and hang-tag product cards",
	"factory-shutter":     "Roller shutter: door-number nav and shutter-grid products with the featured door rolled up",
	"factory-tonnage":     "Tonnage: brutalist header with an oversized stat hero and grade cards",
	"dtc-flagship":        "Brand-store flagship homepage: lifestyle hero, collection cards, bestseller grid, brand-story numbers and customer reviews — for cross-border DTC brand sites",
	"dtc-solo":            "Single-product long-scroll funnel: pain-point hero, alternating selling points, spec table, in-use scenes, reviews and a big closing CTA — for one-hero-product brands",
	"dtc-lookbook":        "Visual-first lookbook: full-bleed image wall and collection-by-collection product walls with hover-revealed names and minimal copy — for fashion, jewellery and designer brands",
	"dtc-vitrine":         "A shopfront window: slim top bar with a bag link, a three-pane vitrine hero with copy on the center pane, and tall white product cards; the detail page keeps a sticky buy rail",
	"dtc-journal":         "A shop that reads like a magazine: centered two-line masthead nav, a cover-story spread pairing the hero with the first product, and a three-column selects strip; entries flow like editorial features",
	"dtc-catalogue":       "A printed catalogue: a standing category index rail beside numbered No.NNN rows (thumb, title, spec chips, end arrow); the detail page leads with a double-ruled spec table",
	"dtc-bazaar":          "A market stall row: a clipped cloth-banner nav, an opening line hero, wooden-sign category pills and stall cards with awning tops and tilted spec tags",
	"dtc-column":          "No top bar at all — a corner wordmark plus a floating dot rail; every product owns a full viewport section with CSS-counted SHOT numbers",
	"dtc-swissgrid":       "Swiss grid discipline: a two-tier header with an announcement strip, one heavy display line, and a 1px-seam product grid numbered SG-NN; the detail page splits into image / spec / buy panels",
	"dtc-runway":          "A fashion show: a transparent header over a full-bleed opening look, LOOK-numbered 3/4 portrait cards, and a look page pairing a full-length shot with an item list",
	"dtc-atelier":         "A maker’s atelier: a vertical wooden name-tag rail (folding to the top at 920px), a spoken-word workbench hero, craft-row product cards and a vertical process timeline on detail",
	"dtc-monthbox":        "A subscription box: a centered three-segment tab nav, a raised “this month’s box” card carrying the issue line and first product, past-box cards below, and a checklist-style detail page",
	"dtc-booth":           "A trade-show booth: arrow-clipped wayfinding pills, a dark brand-wall banner with hall/booth line, A-NN numbered stand cards and a spec-booklet detail page",
	"dtc-apothecary":      "An apothecary cabinet: centered porcelain-label nav, a serif dispensary title block, bottle-label cards with cap accents, and a paired ingredients / directions table on detail",
	"dtc-grocer":          "A grocer’s shop: hanging wooden signs, a chalkboard hero listing dotted-leader lines from real products, paper-bag shelf cards, and origin / storage cards on detail",
	"dtc-cellar":          "A wine cellar: a gilt hairline nav, an engraved cellar title, horizontal rack rows with plank rules and vintage tags, and tasting notes with a vintage axis on detail",
	"dtc-basecamp":        "A trail basecamp: arrow-clipped signpost nav over contour-line texture, gear-list rows with a monospace weight column, and a spec table beside in-the-field shots",
	"dtc-toybox":          "A toy box: building-block nav in rotating colors, thick-outlined rounded cards with tilted sticker badges, and colour-edged age / play cards on detail",
	"dtc-paperie":         "A stationery shop: index-tab nav in rotating colours over grid paper, a white sheet container, washi-taped cards, and paper-spec plus pairing notes on detail",
	"dtc-mono":            "Pure monochrome: a fixed bottom black bar as navigation, a full-width ultra-bold hero, a 1px-rule product grid with zero radius and zero shadow, and a one-line, one-button detail page",
	"dtc-flatlay":         "An overhead flat lay: corner-only navigation above an uneven mosaic where the big tile carries the hero copy and the rest are product tiles with corner spec tags",
	"dtc-flyer":           "A promo flyer: a hazard-stripe bar over bold link rows, a thick-ruled headline sheet with a starburst sticker fed by real spec chips, and heavy-framed flyer cards",
	"dtc-lightbox":        "Backlit display cases: neon-outlined pill nav, a glowing lightbox hero, dark cards with radial-lit image wells, and a haloed spec card on detail",
	"shelf-index":         "Research-oriented content index built from an image-led hero and continuous knowledge shelves",
	"tradeoff-sheet":      "Worksheet homepage with a current focus, decision lenses and a continuous article ledger",
	"progress-bulletin":   "Date-led bulletin with a current issue, category index and chronological article ledger",
	"margin-reading-room": "Three-column introduction, reading lenses, companion reading and an annotated content index",
	"light-table":         "Visual homepage with a large hero, featured wall label and four-column contact sheet",
	"counterpoint":        "Paired-reading homepage with a premise band and two neutral article paths",
	"seamless-canvas":     "One continuous media-led first screen shared by the hero, logo, navigation and category index",
	"night-corridor":      "Immersive media hero with integrated navigation, a reading guide and a bottom content track",
	"open-ascent":         "Bright media-led first screen with integrated navigation, three article rows and a featured reading label",
	"pilot-flight-deck":   "A complete desktop-client launch page with product media, workflow, capabilities, cases, resources and downloads",
}

// secondBatchFactoryThemeDescEN keeps every concrete skin in the second factory
// batch independently localizable in the English appearance picker. The family
// card still uses themeSkeletonDescEN, while direct ThemeOption rendering reads
// themeDescEN by skin ID.
var secondBatchFactoryThemeDescEN = map[string]string{
	"andon":       "Andon board: shift status lights and an LED numeric hero over work-order cards",
	"shopfloor":   "Andon board: shift status lights and an LED numeric hero over work-order cards",
	"andon-white": "Andon board: shift status lights and an LED numeric hero over work-order cards",

	"certwall":       "Certification wall: credential tabs and a badge-ring hero over compliance cards",
	"attest":         "Certification wall: credential tabs and a badge-ring hero over compliance cards",
	"certwall-white": "Certification wall: credential tabs and a badge-ring hero over compliance cards",

	"container":       "Shipping container: manifest bar nav, corrugated container hero and box-face product cards",
	"seafreight":      "Shipping container: manifest bar nav, corrugated container hero and box-face product cards",
	"container-white": "Shipping container: manifest bar nav, corrugated container hero and box-face product cards",

	"crate":       "Export crate: shipping-mark nav and a big timber crate-face hero",
	"stencil":     "Export crate: shipping-mark nav and a big timber crate-face hero",
	"crate-white": "Export crate: shipping-mark nav and a big timber crate-face hero",

	"draftdesk":       "Drafting table: blueprint grid, layer-tab nav, title-block hero and drawing cards",
	"blueline":        "Drafting table: blueprint grid, layer-tab nav, title-block hero and drawing cards",
	"draftdesk-white": "Drafting table: blueprint grid, layer-tab nav, title-block hero and drawing cards",

	"exportmap":       "Shipping routes: timezone strip, world route-dot hero and port-code product cards",
	"nightport":       "Shipping routes: timezone strip, world route-dot hero and port-code product cards",
	"exportmap-white": "Shipping routes: timezone strip, world route-dot hero and port-code product cards",

	"floorplan":       "Floor plan: legend-bar nav and a top-down zoned workshop hero",
	"zoning":          "Floor plan: legend-bar nav and a top-down zoned workshop hero",
	"floorplan-white": "Floor plan: legend-bar nav and a top-down zoned workshop hero",

	"furnace":       "Foundry furnace: furnace-number nav, glowing hearth hero and casting cards",
	"emberdark":     "Foundry furnace: furnace-number nav, glowing hearth hero and casting cards",
	"furnace-white": "Foundry furnace: furnace-number nav, glowing hearth hero and casting cards",

	"gantry":       "Gantry crane: beam header wrapping the nav with the title hung from cables",
	"beamline":     "Gantry crane: beam header wrapping the nav with the title hung from cables",
	"gantry-white": "Gantry crane: beam header wrapping the nav with the title hung from cables",

	"gauge":       "Instrument wall: toggle nav and dial-gauge hero where stats become needles",
	"dialface":    "Instrument wall: toggle nav and dial-gauge hero where stats become needles",
	"gauge-white": "Instrument wall: toggle nav and dial-gauge hero where stats become needles",

	"hazardtape":       "Hazard tape: hard-hat block nav, yellow-black tape bands and safety board hero",
	"graveyard":        "Hazard tape: hard-hat block nav, yellow-black tape bands and safety board hero",
	"hazardtape-white": "Hazard tape: hard-hat block nav, yellow-black tape bands and safety board hero",

	"inspection":       "Inspection report: process-step nav with a numbered document marker, report header and specification rows",
	"passmark":         "Inspection report: process-step nav with a numbered document marker, report header and specification rows",
	"inspection-white": "Inspection report: process-step nav with a numbered document marker, report header and specification rows",

	"line":       "Production line: station-tag nav, conveyor band and station cards along the belt",
	"conveyor":   "Production line: station-tag nav, conveyor band and station cards along the belt",
	"line-white": "Production line: station-tag nav, conveyor band and station cards along the belt",

	"nameplate":       "Machine nameplate: etched nav and a brushed-metal plate hero with spec grid",
	"etchplate":       "Machine nameplate: etched nav and a brushed-metal plate hero with spec grid",
	"nameplate-white": "Machine nameplate: etched nav and a brushed-metal plate hero with spec grid",

	"pipeworks":       "Pipework: valve-dot nav, header pipe and pressure-gauge stats with flanged flow",
	"flowline":        "Pipework: valve-dot nav, header pipe and pressure-gauge stats with flanged flow",
	"pipeworks-white": "Pipework: valve-dot nav, header pipe and pressure-gauge stats with flanged flow",

	"quotation":       "Quotation sheet: document-step nav, terms header and a five-column price table",
	"proforma":        "Quotation sheet: document-step nav, terms header and a five-column price table",
	"quotation-white": "Quotation sheet: document-step nav, terms header and a five-column price table",

	"rackwall":       "Warehouse racking: aisle-sign nav, load plate and pallet-position cards between beams",
	"aisle":          "Warehouse racking: aisle-sign nav, load plate and pallet-position cards between beams",
	"rackwall-white": "Warehouse racking: aisle-sign nav, load plate and pallet-position cards between beams",

	"sampleroom":       "Sample room: swatch-book tabs, colour-strip hero and hang-tag product cards",
	"swatchbook":       "Sample room: swatch-book tabs, colour-strip hero and hang-tag product cards",
	"sampleroom-white": "Sample room: swatch-book tabs, colour-strip hero and hang-tag product cards",

	"shutter":       "Roller shutter: door-number nav and shutter-grid products with the featured door rolled up",
	"stallfront":    "Roller shutter: door-number nav and shutter-grid products with the featured door rolled up",
	"shutter-white": "Roller shutter: door-number nav and shutter-grid products with the featured door rolled up",

	"tonnage":       "Tonnage: brutalist header with an oversized stat hero and grade cards",
	"millscale":     "Tonnage: brutalist header with an oversized stat hero and grade cards",
	"tonnage-white": "Tonnage: brutalist header with an oversized stat hero and grade cards",
}

func init() {
	for id, desc := range secondBatchFactoryThemeDescEN {
		themeDescEN[id] = desc
	}
}

// themeBgDefault 是每个皮肤的底色（public.css 里该皮 :root 变量块的 --bg），
// 色卡「主色 + 底色」双色呈现用；缺省（editorial / sidebar 等骑默认调色板的皮）
// 回落基础纸色 themeBgFallback。与 themeAccentDefault 同一套维护方式。
const themeBgFallback = "#fbfaf7"

var themeBgDefault = map[string]string{
	"field-ledger": "#f7f5f0", "field-ledger-graphite": "#101216", "field-ledger-ocean": "#eef5f4", "field-ledger-plum": "#f5f0f4", "field-ledger-amber": "#fbf4e8",
	"signal-archive": "#f8f6f0", "signal-archive-ink": "#111315", "signal-archive-copper": "#f6eee7", "signal-archive-cobalt": "#eef3fb",
	"paper-current": "#f4f1e9", "paper-current-sage": "#eff3ed", "paper-current-rose": "#fbefee", "paper-current-indigo": "#f0f0f8",
	"night-watch": "#090c0d", "night-watch-cyan": "#091314", "night-watch-amber": "#11100b", "night-watch-violet": "#100d17",
	"orbit-index": "#f7f5ef", "orbit-index-coral": "#fff3ed", "orbit-index-forest": "#eef4ef", "orbit-index-violet": "#f3eff9",
	"column-stage": "#f4f0e7", "column-stage-citrus": "#fff2df", "column-stage-mineral": "#eaf2f1", "column-stage-noir": "#101315",
	"type-cascade": "#ffffff", "type-cascade-cobalt": "#f2f5ff", "type-cascade-coral": "#fff3ef", "type-cascade-cyan": "#edf8f8",
	"briefing-desk": "#f7f3eb", "briefing-desk-white": "#ffffff", "briefing-desk-sage": "#eef2ed", "briefing-desk-ink": "#111723",
	"decision-wall": "#f7f2e9", "decision-wall-white": "#ffffff", "decision-wall-mint": "#e9f4ef", "decision-wall-carbon": "#11141a",
	"route-atlas": "#f3eadb", "route-atlas-white": "#ffffff", "route-atlas-indigo": "#eef1fa", "route-atlas-moss": "#edf1e8",
	"answer-desk": "#fbfaf7", "answer-desk-white": "#ffffff", "answer-desk-dark": "#0c111b",
	"portrait-journal": "#f6f7f4", "portrait-journal-white": "#ffffff", "portrait-journal-dark": "#0d120f",
	"casebook": "#fbfbfa", "casebook-white": "#ffffff", "casebook-dark": "#11100f",
	"shelf-index": "#f4f1e9", "shelf-index-white": "#ffffff", "shelf-index-dark": "#0d1420",
	"tradeoff-sheet": "#f5f1eb", "tradeoff-sheet-white": "#ffffff", "tradeoff-sheet-dark": "#17120f",
	"progress-bulletin": "#f3f0e8", "progress-bulletin-white": "#ffffff", "progress-bulletin-dark": "#151310",
	"margin-reading-room": "#edf1ef", "margin-reading-room-white": "#ffffff", "margin-reading-room-dark": "#0f1714",
	"light-table": "#f2f0ea", "light-table-white": "#ffffff", "light-table-dark": "#111110",
	"counterpoint": "#f4f1e8", "counterpoint-white": "#ffffff", "counterpoint-dark": "#111621",
	"seamless-canvas": "#f2f0eb", "seamless-canvas-white": "#ffffff", "seamless-canvas-dark": "#121416",
	"night-corridor": "#f4f3f0", "night-corridor-white": "#ffffff", "night-corridor-dark": "#080a0d",
	"open-ascent": "#f5f5f2", "open-ascent-white": "#ffffff", "open-ascent-dark": "#0c1420",
	"pilot-flight-deck": "#f5f7fb", "pilot-flight-deck-white": "#ffffff", "pilot-flight-deck-dark": "#0b111b",
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
	// 工厂第二批 20 骨架：每个骨架均登记原生、反差与纯白三张色卡，
	// 避免未登记皮肤错误回落到统一纸色，导致卡片预览和真实页面不一致。
	"andon": "#0c0f0d", "shopfloor": "#eff1ef", "andon-white": "#ffffff",
	"certwall": "#eef3f7", "attest": "#0a111a", "certwall-white": "#ffffff",
	"container": "#edf0f2", "seafreight": "#0a0e14", "container-white": "#ffffff",
	"crate": "#f2ead9", "stencil": "#14100a", "crate-white": "#ffffff",
	"draftdesk": "#eef3f9", "blueline": "#10365c", "draftdesk-white": "#ffffff",
	"exportmap": "#0a1826", "nightport": "#eef5fb", "exportmap-white": "#ffffff",
	"floorplan": "#eef0eb", "zoning": "#0e1216", "floorplan-white": "#ffffff",
	"furnace": "#17100a", "emberdark": "#f1eee7", "furnace-white": "#ffffff",
	"gantry": "#f1f1ee", "beamline": "#16181a", "gantry-white": "#ffffff",
	"gauge": "#171b1d", "dialface": "#eef1f0", "gauge-white": "#ffffff",
	"hazardtape": "#f2f1ec", "graveyard": "#0b0c08", "hazardtape-white": "#ffffff",
	"inspection": "#f1f5f2", "passmark": "#0b1310", "inspection-white": "#ffffff",
	"line": "#eff1ef", "conveyor": "#0d0f10", "line-white": "#ffffff",
	"nameplate": "#e8eaec", "etchplate": "#0e1113", "nameplate-white": "#ffffff",
	"pipeworks": "#eef2f1", "flowline": "#0b1014", "pipeworks-white": "#ffffff",
	"quotation": "#eef2ee", "proforma": "#0c0f0d", "quotation-white": "#ffffff",
	"rackwall": "#e7e5e1", "aisle": "#101215", "rackwall-white": "#ffffff",
	"sampleroom": "#f4efe4", "swatchbook": "#1c1510", "sampleroom-white": "#ffffff",
	"shutter": "#f0efea", "stallfront": "#121110", "shutter-white": "#ffffff",
	"tonnage": "#eef0f1", "millscale": "#0d0c0b", "tonnage-white": "#ffffff",
	// 外贸独立站主题族
	"cream": "#fdfbf6", "amberglow": "#faf3e8", "inknavy": "#0e1420", "oliveleaf": "#f4f4ec",
	"dawnfair":  "#ffffff",
	"solowhite": "#ffffff", "charcoal": "#17181a", "coralpop": "#fff6f2", "limewash": "#f2f6ee",
	"galleria": "#fafafa", "blackbox": "#0c0c0e", "flaxen": "#f6f2e9", "fogblue": "#eef1f4",
	// 外贸独立站第二批 20 骨架 ×3 皮
	"vitrine": "#f6f4ef", "vitrine-noir": "#151009", "vitrine-white": "#ffffff",
	"journalshop": "#fbfaf6", "journalshop-noir": "#161411", "journalshop-white": "#ffffff",
	"catalogue": "#f4f2ec", "catalogue-noir": "#15130e", "catalogue-white": "#ffffff",
	"bazaar": "#f1e8d8", "bazaar-noir": "#221712", "bazaar-white": "#ffffff",
	"column": "#101113", "column-day": "#faf9f5", "column-white": "#ffffff",
	"swissgrid": "#f3f0e7", "swissgrid-noir": "#0e0e0e", "swissgrid-white": "#ffffff",
	"catwalk": "#0d0c0e", "catwalk-day": "#faf9f6", "catwalk-white": "#ffffff",
	"handcraft": "#efe9dd", "handcraft-noir": "#18130e", "handcraft-white": "#ffffff",
	"monthbox": "#f7f3ec", "monthbox-noir": "#0f1524", "monthbox-white": "#ffffff",
	"booth": "#eef0f2", "booth-noir": "#0d1218", "booth-white": "#ffffff",
	"herbary": "#f2f4ef", "herbary-noir": "#131c15", "herbary-white": "#ffffff",
	"grocer": "#f5efe2", "grocer-noir": "#16221b", "grocer-white": "#ffffff",
	"cellar": "#17130e", "cellar-day": "#f7f1e2", "cellar-white": "#ffffff",
	"basecamp": "#eef0e8", "basecamp-noir": "#151c10", "basecamp-white": "#ffffff",
	"toybox": "#fdf7ec", "toybox-noir": "#120f1d", "toybox-white": "#ffffff",
	"paperie": "#f6f4ee", "paperie-noir": "#10141f", "paperie-white": "#ffffff",
	"mono": "#f0f0ec", "mono-noir": "#0d0d0d", "mono-white": "#ffffff",
	"flatlay": "#efeae2", "flatlay-noir": "#171310", "flatlay-white": "#ffffff",
	"flyer": "#fff8e8", "flyer-noir": "#16110b", "flyer-white": "#ffffff",
	"lightbox": "#0e0f12", "lightbox-day": "#f2f6f9", "lightbox-white": "#ffffff",
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
	"answer-desk":            "answer-desk",
	"answer-desk-white":      "answer-desk",
	"answer-desk-dark":       "answer-desk",
	"portrait-journal":       "portrait-journal",
	"portrait-journal-white": "portrait-journal",
	"portrait-journal-dark":  "portrait-journal",
	"casebook":               "casebook",
	"casebook-white":         "casebook",
	"casebook-dark":          "casebook",
	"field-ledger":           "field-ledger",
	"field-ledger-graphite":  "field-ledger",
	"field-ledger-ocean":     "field-ledger",
	"field-ledger-plum":      "field-ledger",
	"field-ledger-amber":     "field-ledger",
	"signal-archive":         "signal-archive",
	"signal-archive-ink":     "signal-archive",
	"signal-archive-copper":  "signal-archive",
	"signal-archive-cobalt":  "signal-archive",
	"paper-current":          "paper-current",
	"paper-current-sage":     "paper-current",
	"paper-current-rose":     "paper-current",
	"paper-current-indigo":   "paper-current",
	"night-watch":            "night-watch",
	"night-watch-cyan":       "night-watch",
	"night-watch-amber":      "night-watch",
	"night-watch-violet":     "night-watch",
	"orbit-index":            "orbit-index",
	"orbit-index-coral":      "orbit-index",
	"orbit-index-forest":     "orbit-index",
	"orbit-index-violet":     "orbit-index",
	"column-stage":           "column-stage",
	"column-stage-citrus":    "column-stage",
	"column-stage-mineral":   "column-stage",
	"column-stage-noir":      "column-stage",
	"type-cascade":           "type-cascade",
	"type-cascade-cobalt":    "type-cascade",
	"type-cascade-coral":     "type-cascade",
	"type-cascade-cyan":      "type-cascade",
	"briefing-desk":          "briefing-desk",
	"briefing-desk-white":    "briefing-desk",
	"briefing-desk-sage":     "briefing-desk",
	"briefing-desk-ink":      "briefing-desk",
	"decision-wall":          "decision-wall",
	"decision-wall-white":    "decision-wall",
	"decision-wall-mint":     "decision-wall",
	"decision-wall-carbon":   "decision-wall",
	"route-atlas":            "route-atlas",
	"route-atlas-white":      "route-atlas",
	"route-atlas-indigo":     "route-atlas",
	"route-atlas-moss":       "route-atlas",
	"paperwhite":             "editorial",
	"citrus":                 "editorial",
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
