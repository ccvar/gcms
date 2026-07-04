// Package web 提供 HTTP 处理器：公开站点、动态 SEO 端点与后台管理。
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/fnv"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/backup"
	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/seo"
	"cms.ccvar.com/internal/store"
)

type Server struct {
	store           *store.Store
	platform        *platform.Store
	rnd             *Renderer
	baseURL         string
	platformBaseURL string
	platformSiteID  int64
	uploadDir       string
	sess            *sessions
	login           *loginLimiter
	apiLimiter      *apiRateLimiter
	i18n            *i18n.Manager
	mux             *http.ServeMux
	assetsFS        fs.FS
	assetVer        string // 静态资源内容指纹，用作 ?v= 破缓存（资源变更即失效旧缓存）
	imageSizes      map[string]ImageSize
	cacheMu         sync.RWMutex
	content         map[string]contentCacheEntry
	endpoints       map[string]endpointCacheEntry
	pages           map[string]pageCacheEntry

	cloudflareMu         sync.Mutex
	cloudflareTimer      *time.Timer
	cloudflareStatusFile string

	runtimeMu sync.RWMutex
	runtimes  *SiteRuntimePool
}

// SiteDomainForm pre-fills the domain-binding modal for one site.
type SiteDomainForm struct {
	PrimaryHost     string // 主域名（host，可空）
	AliasText       string // 别名域名，一行一个
	RedirectAliases bool   // 别名是否 301 跳转到主域名
}

type SiteRuntime struct {
	Site      *platform.Site
	Store     *store.Store
	BaseURL   string
	UploadDir string
	server    *Server
}

type SiteRuntimePool struct {
	byID          map[int64]*SiteRuntime
	byHost        map[string]*SiteRuntime
	redirects     map[string]string // 别名 Host -> 主域名基址（scheme://host），命中即 301
	defaultSite   *SiteRuntime
	platformHost  string
	localPlatform bool
}

// redirectTarget returns the primary base URL an alias host should 301 to, or "".
func (p *SiteRuntimePool) redirectTarget(rawHost string) string {
	if p == nil || len(p.redirects) == 0 {
		return ""
	}
	host := normalizeRuntimeHost(rawHost)
	if host == "" {
		return ""
	}
	return p.redirects[host]
}

type contentCacheEntry struct {
	updatedAt time.Time
	html      template.HTML
	toc       []Heading
}

type endpointCacheEntry struct {
	body        []byte
	contentType string
	expires     time.Time
}

type pageCacheEntry struct {
	body        []byte
	contentType string
	etag        string
	expires     time.Time
}

const (
	generatedEndpointCacheControl = "public, max-age=1800"
	generatedEndpointCacheTTL     = 30 * time.Minute
	publicPageCacheControl        = "public, max-age=0, must-revalidate"
	publicPageCacheTTL            = 5 * time.Minute
	publicPageCacheLimit          = 512
	uploadCacheControl            = "public, max-age=31536000, immutable"
)

// assetVersion 取关键静态资源内容的短指纹：内容变了指纹就变，配合长缓存做缓存破坏。
func assetVersion(fsys fs.FS) string {
	h := fnv.New64a()
	for _, p := range []string{"assets/css/style.css", "assets/css/public.css", "assets/css/admin.css", "assets/js/admin.js", "assets/js/site.js", "assets/js/toc.js"} {
		if b, err := fs.ReadFile(fsys, p); err == nil {
			_, _ = h.Write(b)
		}
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// ThemeOption 用于后台设置页的主题下拉。
type ThemeOption struct{ ID, Name, Desc string }

// Themes 是可选的前台主题（布局风格各不相同）。
var Themes = []ThemeOption{
	{"editorial", "编辑 · Editorial", "暖色衬线、单列大字号列表（默认）"},
	{"magazine", "杂志 · Magazine", "无衬线、居中刊头、三列卡片网格"},
	{"terminal", "极客 · Terminal", "等宽字体、深色、终端清单式布局"},
	{"brutalist", "粗野 · Brutalist", "黑白高对比、粗黑边框、硬阴影、无圆角"},
	{"notebook", "手账 · Notebook", "米黄纸张、衬线、横格背景、暖橙强调"},
	{"swiss", "瑞士 · Swiss", "国际主义网格、红黑配色、巨号编号"},
	{"pastel", "柔彩 · Pastel", "浅彩渐变、大圆角卡片、友好柔和"},
	{"newspaper", "报纸 · Newspaper", "多栏排版、衬线、首字下沉、黑白"},
	{"darkpro", "暗夜 · Dark Pro", "现代暗色、靛蓝霓虹、卡片网格"},
	{"landing", "官网 · Landing", "产品/项目官网风：大 hero + CTA 按钮 + 特性卡片"},
	{"product", "产品 · Product", "开源项目/互联网产品官网、文档入口、更新日志"},
	{"prism", "光谱 · Prism", "深色海报感、多色信号线、错层内容卡"},
	{"exchange", "交易所 · Exchange", "Web3 增长页、行情仪表盘、强转化按钮"},
	{"academy", "智课 · Academy", "AI 教材科普、课程目录、阅读友好卡片"},
	{"garment", "制衣 · Garment", "外贸服装工厂、样衣目录、B2B 展示感"},
	{"institution", "机构 · Institution", "专业服务/咨询/律所/协会官网、可信背书"},
	{"studio", "作品 · Studio", "设计/摄影/建筑/品牌工作室、图像主导作品集"},
	{"lifestyle", "生活 · Lifestyle", "咖啡/民宿/餐厅/买手店、小品牌温度感官网"},
	{"knowledge", "知识库 · Knowledge Hub", "搜索优先、分类导航、推荐阅读和更新时间线"},
	{"sidebar", "侧栏 · Sidebar", "左侧常驻竖栏（品牌+导航）+ 右侧阅读流，个人站 / 文档站气质"},
	{"bento", "拼贴 · Bento", "错落卡片网格首页（顶栏照旧），靛蓝强调、大圆角，作品集 / 个人主页气质"},
	{"nocturne", "夜栏 · Nocturne", "深色左栏 + 阅读流，薄荷青强调、等宽点缀，开发者夜间博客 / 暗色文档"},
	{"terra", "暖砂 · Terra", "暖砂拼贴网格 + 衬线标题，陶土橙强调，创作者作品集 / 个人主页"},
	{"porcelain", "月白 · Porcelain", "极简留白顶栏 + 文学衬线，青瓷绿强调，写作 / 品牌 / 期刊式站点"},
	{"index", "索引 · Index", "首页是一张排版化内容索引表：等宽编号 + 发丝线 + 大留白，克制精密的科技感"},
	{"split", "分屏 · Split", "满屏左右分栏：左侧巨型标题 + 右侧整块精选，黑白克制、大气留白"},
	{"axis", "中线 · Axis", "全居中宣言式：巨型居中标题 + 中线分隔的居中列表，极致对称留白"},
	{"journal", "文选 · Journal", "学刊小开本：窄栏衬线、文本优先、克制留白，学术 / 文学气质"},
	{"blueprint", "蓝图 · Blueprint", "工程制图：方格纸底纹 + 墨线 + 等宽技术标注 + 角落标题栏"},
	{"riso", "孔版 · Risograph", "独立孔版印刷：双专色叠印、网点质感、套印偏移、硬阴影"},
	{"quiet", "和敬 · Quiet", "和风留白：极阔间距、竖向节奏、发丝线、一点朱印强调"},
	{"lucid", "明快 · Lucid", "亮白极简 + 暖橙强调：现代无衬线、扁平圆角卡片、胶囊按钮、宽松留白——科普 / 金融式的清晰亲和"},
	{"aurora", "霓白 · Aurora", "浅色玻璃拟态：柔和渐变网格底 + 磨砂半透卡片 + 渐变标题，靛紫强调——Web3 发布页 / L2·DeFi 的高级科技感"},
	{"bands", "光带 · Bands", "全宽交替色带分区：一屏一段的纵向叙事带，电光蓝强调、超大无衬线——现代营销 / Web3 推广落地页"},
	{"ticker", "流光 · Ticker", "顶部滚动行情/生态跑马灯 + 下方实时信息流：等宽数字、翠绿涨色强调——Web3 实时感信息站"},
	{"liftoff", "起飞 · Liftoff", "单一 CTA 的 MINT/DROP 发射页：巨型标题 + 供给进度条 + 主按钮，右侧大方形艺术框——NFT 铸造 / 代币发售落地页"},
	{"board", "看板 · Board", "多列看板/路线图：全宽横幅精选 + 横向泳道列（分类分组）与紧凑迷你卡片，靛蓝强调——产品工具 / 产品路线图"},
	{"timeline", "时间线 · Timeline", "居中竖脊时间线：单发丝线主轴 + 圆点节点 + 等宽日期，衬线标题、暖纸墨色——档案编年 / 更新日志 / 路线图"},
	{"deck", "横卷 · Deck", "横向滚动影卷：整屏卡片 scroll-snap 侧滑 + 锚点翻页，作品集 / 时装 lookbook / 案例集（纯 CSS 横向）"},
	{"poster", "封幕 · Poster", "整屏封面图 + 压图大字 + 纵向 scroll-snap 分屏折叠：杂志封面 / 品牌发布会 / mint 落地页"},
	{"uptime", "健康 · Uptime", "状态页：总状态横幅 + 组件在线率格条 + 事件时间线，RPC / 节点 / 协议状态页"},
	{"profile", "名帖 · Profile", "无导航个人页：头像 + 可点大按钮链接栈，Linktree 式创作者 / 项目 bio 页"},
	{"bloom", "草木 · Bloom", "有机曲面：blob 裁剪 hero + 藤蔓脊左右交错叶卡 + 波浪分隔，养生 / 有机 / 手作品牌"},
	{"desktop", "千禧 · Desktop", "仿千禧 OS 桌面：窗口容器 + 文件夹散布 + 任务栏，frutiger-aero 光泽玩味"},
	{"cinema", "夜幕 · Cinema", "宽荧幕影格：2.39:1 黑边 + 灰度整屏场景 + 时间码，影像 / 摄影 / 电影质感"},
	{"collage", "喧哗 · Collage", "反网格拼贴：叠错旋转卡片 + 便签胶带 + 涂鸦箭头，音乐节 / 潮牌 / zine"},
	{"constellation", "星图 · Constellation", "可筛选生态名录：分类芯片 + 实时搜索过滤项目卡片网格（渐进增强，需前端 JS），Web3 生态 / 项目墙 / dApp 目录"},
	{"gilded", "鎏金 · Gilded", "浓咖黑底 + 鎏金强调、轻字重衬线大标题，威士忌 / 珠宝 / 精品工作室的暗夜奢华官网"},
	{"grove", "松涧 · Grove", "奶油纸底 + 深松绿侧栏、蜜色高亮，衬线标题，茶园 / 花艺 / 自然生活博客"},
	{"obsidian", "曜石 · Obsidian", "石墨黑拼贴卡片 + 荧光青柠强调、等宽数字，开发者夜间主页 / 科技作品集"},
	{"codex", "典藏 · Codex", "缃色纸底 + 牛血红强调、衬线条目与双细线表头，文博收藏 / 人文档案的目录页"},
	{"gilt", "乌金 · Gilt", "满屏分屏换上乌木鎏金深色皮：衬线巨题 + 黄铜金强调，画廊 / 高端品牌 / 收藏站的夜场气质"},
	{"zenith", "苍穹 · Zenith", "全居中宣言浸入星夜深蓝：衬线巨题、星光淡蓝强调、极致对称留白，宣言 / 诗集 / 概念项目的静谧夜航感"},
	{"fir", "杉野 · Fir", "全宽色带换成米纸松绿：衬线大标题 + 胶囊按钮、自然系配色，有机食品 / 手作 / 户外品牌落地页"},
	{"ember", "琥珀 · Ember", "行情跑马灯切入夜盘：石墨深底 + 琥珀信号色、等宽时间戳微光，加密行情 / 数据面板 / 夜间信息站的终端质感"},
	{"ignition", "点火 · Ignition", "深空发射控制台：暗夜蓝黑底 + 琥珀→火焰渐变进度与 CTA，遥测仪表气质——夜间 mint / 代币发售 / 硬核项目发射页"},
	{"cork", "软木 · Cork", "软木告示板：牛皮纸暖底 + 蜡印红图钉、衬线标题的泳道看板——编辑部选题板 / 社区公告栏 / 个人项目路线图"},
	{"orbit", "星轨 · Orbit", "深空观测夜志：蓝黑夜幕 + 星点辉光节点、冰蓝等宽刻度——太空/天文主题编年、黑客松 build log、暗色版本路线图"},
	{"runway", "秀场 · Runway", "午夜秀场影卷：黑幕整屏横滑 + 绯红舞台光、紧排无衬线大字——时装 lookbook / 暗调摄影集 / 品牌大片发布"},
	{"velvet", "丝绒 · Velvet", "整屏封面折叠改走深夜高定：丝绒暗底、香槟金衬线大字——时装屋 / 香氛美妆 / 画廊发布会"},
	{"pulse", "脉冲 · Pulse", "状态页换深色指挥台：冰青读数、等宽标题、暗夜机房气质——节点监控 / 基础设施 / API 状态页"},
	{"onyx", "玄玉 · Onyx", "链接栈 bio 页换黑曜暗卡：荧光青柠强调、等宽名字——开发者 / 极客创作者 / 工具作者主页"},
	{"lotus", "青荷 · Lotus", "草木曲面转冷调水岸：青雾纸底、荷粉 + 湖水青点缀、圆润无衬线——SPA / 花艺 / 疗愈生活品牌"},
	{"vapor", "幻夜 · Vapor", "千禧桌面开进午夜：暗紫夜幕 + 霓粉窗格、荧光绿高光，蒸汽波气质的电玩 / 复古社区 / 玩乐个人站"},
	{"matinee", "日场 · Matinee", "宽荧幕影格翻成日场画册：暖纸白 + 酒红字幕卡、衬线大标题、银盐灰阶影像，摄影集 / 展览画册 / 品牌影像站"},
	{"rave", "夜电 · Rave", "反网格拼贴开进深夜：黑纸白墨硬阴影、只留荧光黄绿一击、等宽打字机标题，地下演出 / 电子厂牌 / 夜行 zine"},
	{"astrolabe", "浑天 · Astrolabe", "星图名录换上真正的夜空：墨蓝底 + 星辉琥珀、衬线标题、细星点底纹，天文馆气质的生态目录 / 项目墙 / 收藏索引"},
	{"masonry", "瀑布 · Masonry", "Pinterest 式多列瀑布流：居中题签 + 分类胶囊 + 宽幅精选卡，变高卡片沿 2–4 列自然下落，亮白画廊大留白——图库 / 灵感墙 / 设计收藏"},
	{"darkroom", "暗房 · Darkroom", "瀑布流坠入暗房夜场：近黑展墙 + 安全灯红强调，灰阶封面悬停显色、首字母红印生成块——摄影作品集 / 夜间画廊 / 视觉档案"},
	{"feed", "动态 · Feed", "社交微博式动态流：左侧常驻名片栏 + 居中单列帖子卡（置顶精选、头像缩略），天蓝清爽——个人动态 / build-in-public / 短内容"},
	{"noir", "夜航 · Noir", "动态流熄灯开夜航：纯黑 AMOLED 底 + 霓虹紫强调、等宽时间戳的暗夜卡片流——深夜开发者动态 / 暗色个人微博"},
	{"gazette", "头版 · Gazette", "对开报纸头版：巨型衬线报头 + 粗细双线 + 分栏线多栏正文与双线简报框，象牙纸底、牛血红眉题——新闻 / 评论 / 编辑部站点"},
	{"tabloid", "街报 · Tabloid", "头版骨架换上街头小报皮：黑底大写无衬线巨题 + 猩红色块眉题、高反差白墨双线——潮流资讯 / 音乐现场 / 争议话题评论"},
	{"manual", "手册 · Manual", "三栏手册页：左侧章节目录 + 中间编号小节 + 右侧速览卡，青灰蓝冷静克制——文档站 / 产品手册 / API 参考"},
	{"kernel", "内核 · Kernel", "手册三栏换上石墨夜色工程皮：等宽标题 + 钢蓝强调、man page 气质——开发者文档 / 运维手册 / API 参考"},
	{"almanac", "月历 · Almanac", "月历首页：双线刊头 + 七列月格把文章钉成日子 + 点线议程清单，暖手帐纸配朱红图钉——活动日程 / 更新日志 / 期刊连载"},
	{"nightshift", "夜班 · Nightshift", "月历骨架值夜班：墨蓝夜底、霓虹紫发光图钉与等宽日号，月格与议程改走排程台质感——夜场演出 / 电竞赛程 / 链上活动日历"},
	{"inbox", "收件箱 · Inbox", "邮件客户端三栏：左侧文件夹栏（分类+标签）+ 中间邮件列表（未读点/发件人/摘要）+ 右侧精选阅读窗格，经典浅色邮件质感——通讯归档 / 新闻简报 / 站内公告"},
	{"midnight", "午夜 · Midnight", "三栏邮件客户端熄灯夜读：墨蓝夜幕 + 冰蓝强调、未读点辉光、星标鎏金——开发者简报 / 夜间通讯 / 更新公告"},
}

// themeLayouts 登记“非默认骨架”的主题；未登记者一律 "topbar"（= 现有基础骨架）。
// 皮（data-theme）与骨（data-theme-layout）解耦：同一套骨架可被多套皮复用，
// 结构性差异在模板里按 .Layout 分支（骨架数量级），而非按主题名分支（主题数量级）。
var themeLayouts = map[string]string{
	"sidebar":       "sidebar",
	"bento":         "bento",
	"nocturne":      "sidebar",
	"terra":         "bento",
	"index":         "index",
	"split":         "split",
	"axis":          "axis",
	"bands":         "bands",
	"ticker":        "ticker",
	"liftoff":       "liftoff",
	"board":         "board",
	"timeline":      "timeline",
	"deck":          "deck",
	"poster":        "poster",
	"uptime":        "uptime",
	"profile":       "profile",
	"bloom":         "bloom",
	"desktop":       "desktop",
	"cinema":        "cinema",
	"collage":       "collage",
	"constellation": "constellation",
	// 皮肤复用骨架（新皮 → 既有骨）
	"grove":     "sidebar",
	"obsidian":  "bento",
	"codex":     "index",
	"gilt":      "split",
	"zenith":    "axis",
	"fir":       "bands",
	"ember":     "ticker",
	"ignition":  "liftoff",
	"cork":      "board",
	"orbit":     "timeline",
	"runway":    "deck",
	"velvet":    "poster",
	"pulse":     "uptime",
	"onyx":      "profile",
	"lotus":     "bloom",
	"vapor":     "desktop",
	"matinee":   "cinema",
	"rave":      "collage",
	"astrolabe": "constellation",
	// 新骨架（原生皮 + 反差皮）
	"masonry": "masonry", "darkroom": "masonry",
	"feed": "feed", "noir": "feed",
	"gazette": "gazette", "tabloid": "gazette",
	"manual": "manual", "kernel": "manual",
	"almanac": "almanac", "nightshift": "almanac",
	"inbox": "inbox", "midnight": "inbox",
}

// layoutForTheme 返回主题对应的布局骨架，缺省 "topbar"。
func layoutForTheme(theme string) string {
	if l, ok := themeLayouts[theme]; ok {
		return l
	}
	return "topbar"
}

func validTheme(id string) bool {
	for _, t := range Themes {
		if t.ID == id {
			return true
		}
	}
	return false
}

// 各主题的默认主色 / 圆角（用于微调控件的初值与未自定义时的展示）。
var themeAccentDefault = map[string]string{
	"editorial": "#9a3b2f", "magazine": "#1f5fff", "terminal": "#3fb950",
	"brutalist": "#1f23ff", "notebook": "#c2691f", "swiss": "#e30613",
	"pastel": "#8b5cf6", "newspaper": "#8b0000", "darkpro": "#7c7cf8", "landing": "#4f46e5", "product": "#0f7cff", "prism": "#d7ff4a",
	"exchange": "#00f5a0", "academy": "#2563eb", "garment": "#0f766e",
	"institution": "#8a1f2d", "studio": "#ff4f5e", "lifestyle": "#2f7d57",
	"knowledge": "#0f766e",
	"liftoff":   "#e5157a", "board": "#4f46e5", "timeline": "#9a5b1e", "deck": "#b0742c",
	"poster": "#e8402a", "uptime": "#16a34a", "profile": "#f0653c", "bloom": "#5c7a4a",
	"desktop": "#1e7fe0", "cinema": "#7fb4d8", "collage": "#e5343a",
	"constellation": "#5b6cf0",
	"gilded":        "#c9a24b",
	"grove":         "#3e6b4f", "obsidian": "#b8e34c", "codex": "#8c2f2b", "gilt": "#d4a24e",
	"zenith": "#a8b8ff", "fir": "#3e6b4f", "ember": "#e8a13d", "ignition": "#ffb224",
	"cork": "#bf3b2b", "orbit": "#62d0ff", "runway": "#e13558", "velvet": "#c9a25c",
	"pulse": "#38cfe0", "onyx": "#c3f53c", "lotus": "#c2517b", "vapor": "#ff5fc1",
	"matinee": "#a4343a", "rave": "#cff24d", "astrolabe": "#e5b04e",
	"masonry": "#d6335c", "darkroom": "#e6483c", "feed": "#1d9bf0", "noir": "#8b5cf6",
	"gazette": "#7d1d12", "tabloid": "#f5142e", "manual": "#3a6ea5", "kernel": "#7aa2f7",
	"almanac": "#bf4229", "nightshift": "#8f6bff", "inbox": "#2563eb", "midnight": "#7aa2ff",
}
var themeRadiusDefault = map[string]string{
	"editorial": "10", "magazine": "12", "terminal": "6", "brutalist": "0",
	"notebook": "8", "swiss": "0", "pastel": "18", "newspaper": "0", "darkpro": "14", "landing": "14", "product": "14", "prism": "18",
	"exchange": "16", "academy": "16", "garment": "12",
	"institution": "8", "studio": "4", "lifestyle": "18",
	"knowledge": "8",
	"liftoff":   "20", "board": "10", "timeline": "8", "deck": "2",
	"poster": "0", "uptime": "8", "profile": "20", "bloom": "24",
	"desktop": "6", "cinema": "0", "collage": "4",
	"constellation": "14",
	"gilded":        "6",
	"grove":         "10", "obsidian": "12", "codex": "0", "gilt": "0",
	"zenith": "0", "fir": "6", "ember": "10", "ignition": "12",
	"cork": "8", "orbit": "8", "runway": "0", "velvet": "0",
	"pulse": "6", "onyx": "14", "lotus": "22", "vapor": "14",
	"matinee": "2", "rave": "4", "astrolabe": "16",
	"masonry": "14", "darkroom": "10", "feed": "16", "noir": "16",
	"gazette": "0", "tabloid": "0", "manual": "8", "kernel": "6",
	"almanac": "10", "nightshift": "10", "inbox": "10", "midnight": "10",
}

const (
	homeLinksLimitKey       = "home.links_limit"
	homePostsPerPageKey     = "home.posts_per_page"
	layoutWidthKey          = "layout.width"
	postDefaultAuthorKey    = "content.post_author"
	linkDefaultAuthorKey    = "content.link_author"
	defaultHomeLinksLimit   = 8
	defaultHomePostsPerPage = 6
	minHomeLinksLimit       = 0
	maxHomeLinksLimit       = 24
	minHomePostsPerPage     = 1
	maxHomePostsPerPage     = 50
)

const (
	homeSectionsKey = "home.sections" // 首页版块顺序 + 开关（JSON）
	homeHeroKey     = "home.hero"     // 首页 Hero 开关（"0" 关，其余为开）
)

// HomeSection 是默认首页布局里一个可编排的内容版块。
type HomeSection struct {
	Key string `json:"key"`
	On  bool   `json:"on"`
}

// homeSectionKeys 是允许的内容版块键（Hero 单独用开关控制，不入此列表）。
var homeSectionKeys = map[string]bool{"featured": true, "links": true, "latest": true, "categories": true}

// defaultHomeSections 是默认顺序：分类默认关闭以保持现有首页外观不变。
var defaultHomeSections = []HomeSection{
	{"featured", true}, {"links", true}, {"latest", true}, {"categories", false},
}

// normalizeHomeSections 校验/补齐版块列表：只保留已知键、去重、缺失的补默认，保证 4 个内容版块都在。
func normalizeHomeSections(in []HomeSection) []HomeSection {
	seen := map[string]bool{}
	out := make([]HomeSection, 0, len(defaultHomeSections))
	for _, sec := range in {
		if homeSectionKeys[sec.Key] && !seen[sec.Key] {
			out = append(out, HomeSection{sec.Key, sec.On})
			seen[sec.Key] = true
		}
	}
	for _, d := range defaultHomeSections {
		if !seen[d.Key] {
			out = append(out, d)
		}
	}
	return out
}

// homeSectionConfig 读取并规整首页版块配置，返回（有序版块, Hero 是否显示）。
func (s *Server) homeSectionConfig() ([]HomeSection, bool) {
	heroOn := s.store.Setting(homeHeroKey) != "0" // 缺省为开
	raw := strings.TrimSpace(s.store.Setting(homeSectionsKey))
	if raw == "" {
		return normalizeHomeSections(nil), heroOn
	}
	var stored []HomeSection
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return normalizeHomeSections(nil), heroOn
	}
	return normalizeHomeSections(stored), heroOn
}

// sanitizeHomeSectionsJSON 校验后台提交的版块 JSON 并重新序列化（绝不原样存用户输入）。
func sanitizeHomeSectionsJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	var in []HomeSection
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &in)
	}
	b, _ := json.Marshal(normalizeHomeSections(in))
	return string(b)
}

func normalizeLayoutWidth(v string) string {
	switch strings.TrimSpace(v) {
	case "1080", "1200", "1240", "1360", "1440":
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

// ThemeCard 是设置页每个主题卡片的状态（含该主题自己的微调值）。
type ThemeCard struct {
	ID, Name, Desc string
	Accent, Radius string
	Custom         bool
}

// themeTweak 读取某主题的微调（按主题独立存储），未设置时回落到该主题默认。
func (s *Server) themeTweak(id string) (custom bool, accent, radius string) {
	if v, _ := s.store.GetSetting("theme." + id + ".custom"); v == "1" {
		custom = true
	}
	accent, _ = s.store.GetSetting("theme." + id + ".accent")
	if !hexColor(accent) {
		if d := themeAccentDefault[id]; d != "" {
			accent = d
		} else {
			accent = "#9a3b2f"
		}
	}
	radius, _ = s.store.GetSetting("theme." + id + ".radius")
	if radius == "" {
		if d := themeRadiusDefault[id]; d != "" {
			radius = d
		} else {
			radius = "10"
		}
	}
	return
}

// LangLink 是页眉语言切换器里的一项。
type LangLink struct {
	Code, Name, URL string
	Active          bool
}

// ArchiveConfig 描述文章分类/链接列表的「全部」入口配置。
// 它不是 categories 表里的真实分类，只用于列表页标题、筛选按钮、SEO 和默认导航。
type ArchiveConfig struct {
	Title       string
	Label       string
	Description string
	Slug        string
	Path        string
}

type KnowledgeGroup struct {
	Key         string
	Title       string
	Description string
	Path        string
	Count       int
	Posts       []*store.Post
}

// View 是传给模板的统一数据载体。
type View struct {
	Site         seo.Site
	SEO          seo.Meta
	ForceNoindex bool
	Nav          string
	Year         int
	Theme        string
	Layout       string
	ThemeStyle   template.CSS
	AssetVer     string

	// 多语种（前台）
	Tr                      *i18n.Tr
	Lang                    string
	Langs                   []LangLink
	ExternalLinks           ExternalLinkPolicy
	RootLangRedirect        bool
	RootLangRedirectLocales template.JS
	RootLangRedirectDefault template.JS
	SitemapURL              string
	RobotsURL               string
	Admin                   *i18n.AdminTr
	AdminLang               string
	AdminLangs              []i18n.Locale
	AdminReturn             string

	Posts           []*store.Post
	Featured        *store.Post
	FeaturedMore    []*store.Post
	FeatLinks       []*store.Post
	HomeSections    []HomeSection // 首页可编排版块（默认布局；有序 + 开关）
	HomeHero        bool          // 首页是否显示 Hero 版块
	Post            *store.Post
	Page            *store.Post
	Categories      []*store.Category
	KnowledgeGroups []*KnowledgeGroup
	Category        *store.Category
	CategoryAll     ArchiveConfig
	LinksAll        ArchiveConfig
	Prev            *store.Post
	Next            *store.Post
	Related         []*store.Post
	Giscus          *GiscusView

	ContentHTML template.HTML
	TOC         []Heading

	// 扩展内容类型（通用兜底模板 generic_* 用）
	CT     *ContentType
	Fields []FieldValue

	// 后台「扩展」分区
	ExtTypes      []ExtTypeRow
	ExtType       *ContentType
	ExtPosts      []*store.Post
	ExtDepth      map[int64]int   // 层级类型列表：每条的缩进层级（按 parent 排好序）
	ExtParent     map[int64]int64 // 层级类型列表：每条的上级 ID（用于同级拖动排序）
	ExtEdit       *store.Post
	ExtValues     map[string]string
	ExtRelOptions []*store.Post // 关联字段（如文档上级）的候选项
	DocTree       []DocNode     // 文档左侧导航树（GitBook 式：分类 → 章节，当前页高亮）
	DocChildren   []DocNode     // 当前文档的下级章节（详情页底部「本节」卡片）
	TypeForm      *TypeFormView // 可视化类型设计器表单
	ArchiveTitle  string        // 扩展归档页：后台自定义标题（覆盖类型名）
	ArchiveIntro  string        // 扩展归档页：后台自定义简介

	PageNum    int
	TotalPages int
	HasPrev    bool
	HasNext    bool
	PrevPage   int
	NextPage   int
	BasePath   string

	Query   string
	Results int

	// 后台
	AllPosts                     []*store.Post
	ListTotal                    int
	StatusFilter                 string
	CategoryFilter               string
	CategoryFilterName           string
	AdminListPath                string
	DefaultAuthor                string
	Edit                         *store.Post
	IsPage                       bool
	IsLink                       bool
	EditBase                     string // 编辑表单的后台路径基：posts | pages | links
	EditListURL                  string // 返回列表的后台 URL
	EditTypeLabel                string // 文章 | 页面 | 链接
	Authed                       bool
	PlatformMode                 bool // 当前实例启用了平台级多站点
	PlatformAdminView            bool // 平台级管理页，不显示当前站点后台导航
	ShowPwWarn                   bool // 仍为默认密码且本会话未关闭提示
	CSRF                         string
	Flash                        string
	FormErr                      string
	Settings                     *SettingsForm
	Themes                       []ThemeOption
	Cards                        []ThemeCard
	Section                      string
	CatKind                      string // 分类管理当前类型：post | link
	EditCat                      *store.Category
	FormVals                     map[string]string // 表单回填（分类新增/编辑出错时）
	Update                       *UpdateInfo       // 系统更新检查
	Upgrade                      *UpgradeStatus    // 系统升级任务状态
	Cloudflare                   *CloudflareView   // Cloudflare Worker 部署配置与状态
	AutomationKeys               []*store.AutomationKey
	AutomationLogs               []*store.AutomationLog
	NewAPISecret                 string
	NewAPIName                   string
	NewAPIScopes                 string
	NewAIBrief                   string
	NewAPIKeyID                  int64
	APIBaseURL                   string
	OpenAPIURL                   string
	APIDocsURL                   string
	SkillPackageURL              string
	StarterPackageURL            string
	EditLang                     string        // 后台当前操作的内容语种
	Locales                      []i18n.Locale // 已启用语种
	AllLocales                   []i18n.Locale // 全部可选语种（内置 + 自定义，语言设置勾选）
	CustomLocales                []i18n.Locale // 自定义预设（可删除）
	LocaleCatalogs               map[string]LocaleCatalogView
	AdminI18NJSON                string                                 // 当前后台语种的用户覆盖翻译 JSON
	Trans                        []*store.Post                          // 当前编辑文章的互译版本
	Social                       []SocialLink                           // 页脚社交链接（前台渲染 / 后台回填）
	Menu                         []MenuItem                             // 前台页眉导航（按当前语种解析）
	MenuEdit                     []MenuRow                              // 后台导航菜单编辑（URL + 各语种标签）
	MenuTargets                  []MenuTargetOption                     // 后台导航菜单可选入口
	VisualEdit                   bool                                   // 前台 iframe 可视化编辑模式
	VisualPreviewURL             string                                 // 后台可视化编辑 iframe 地址
	AdminSiteURL                 string                                 // 后台顶部“查看站点”入口
	AdminPreviewPrefix           string                                 // 平台多站点下当前站点的前台预览前缀
	VisualFields                 []VisualField                          // 可视化编辑侧栏字段
	VisualGroups                 []VisualGroup                          // 可视化编辑侧栏字段分组
	VisualHistory                []VisualLog                            // 可视化编辑最近修改
	LayoutWidth                  string                                 // 前台内容最大宽度预设（空=跟随主题）
	OverviewStats                []OverviewStat                         // 后台概览：内容状态
	OverviewTasks                []OverviewTask                         // 后台概览：待处理事项
	OverviewRecent               []*store.Post                          // 后台概览：最近更新
	OverviewStatus               []OverviewStatus                       // 后台概览：系统状态
	PlatformSites                []*platform.Site                       // 平台综合后台：站点列表
	PlatformKeys                 []*platform.PlatformAutomationKey      // 平台自动化密钥（多站 AI 管理）
	PlatformKeyLogs              []*platform.PlatformAutomationLogEntry // 跨站审计时间线
	PlatformSkillURL             string                                 // 平台薄包下载入口（reveal 弹窗内嵌）
	NewPlatformSecret            string                                 // 新建/重生成后一次性显示的平台密钥明文
	NewPlatformName              string
	NewPlatformScopes            string
	NewPlatformKeyID             int64
	NewPlatformMembership        string
	NewPlatformBrief             string // 给 AI 的接入说明（平台版）
	PlatformDomains              map[int64][]*platform.SiteDomain
	PlatformDomainForms          map[int64]SiteDomainForm // 每站点：绑定域名弹窗的预填数据
	PlatformSiteIcons            map[int64]string
	PlatformPreviewURLs          map[int64]string // 平台站点页：按各站点默认语种生成的预览入口
	PlatformOfficialURLs         map[int64]string // 已发布到 Cloudflare 的正式站点入口
	PlatformOfficialHosts        map[int64]string // 正式站点入口展示域名
	PlatformCFDeployAt           map[int64]string // 每站点：Cloudflare 最近部署时间（RFC3339，空=未记录）
	PlatformCFStatus             map[int64]string // 每站点：Cloudflare 部署状态（running/success/failed/空）
	PlatformLocaleCounts         map[int64]int    // 每站点：启用语种数
	PlatformContentCounts        map[int64]int    // 每站点：主语种内容条数（含草稿）
	PlatformContentUpdatedAt     map[int64]string // 每站点：对外可见内容上次更新时间（RFC3339，空=无已发布内容）；服务器托管站展示"服务器 · X 前"
	PlatformCurrentSiteID        int64            // 平台会话中当前选择的站点
	GoogleOAuthConfigured        bool             // 平台级 Google OAuth 客户端已配置
	GoogleOAuthClientID          string           // 平台级 Google OAuth Client ID（后台表单回填）
	GoogleOAuthRedirectURL       string           // 平台级 Google OAuth 回调地址（后台表单回填）
	GoogleOAuthSecretSet         bool             // 平台级 Google OAuth Client Secret 已配置
	GoogleOAuthProjectID         string           // 从 OAuth Client ID 推断出的 Google Cloud 项目号（展示/跳转用）
	GoogleAnalyticsAdminAPIURL   string           // Analytics Admin API 启用入口
	GoogleAnalyticsDataAPIURL    string           // Analytics Data API 启用入口
	GoogleSearchConsoleAPIURL    string           // Search Console API 启用入口
	GoogleAccounts               []*platform.GoogleAccount
	GoogleAnalyticsAccounts      []*platform.GoogleAccount
	GoogleSearchConsoleAccounts  []*platform.GoogleAccount
	SiteGoogleIntegrations       map[int64]map[string]*platform.SiteGoogleIntegration
	SiteGoogleAnalyticsSummaries map[int64]*platform.SiteGoogleAnalyticsSummary
	SiteGoogleSearchSummaries    map[int64]*platform.SiteGoogleSearchConsoleSummary
	PlatformGoogleDefaultURIs    map[int64]string // 每站点自动创建 GA/匹配 Search Console 时优先使用的网站地址
	ArchivedSites                []*platform.ArchivedSite
	ArchivedSiteIcons            map[int64]string
	BackupConfig                 backup.Config
	BackupRecords                []*backup.BackupRecord
	BackupDir                    string
	ServerHealth                 ServerHealth // 平台站点页：服务器负载 / 内存 / 磁盘快照
	CaddyOnDemand                bool         // 已启用 Caddy 按需签发（决定域名绑定指引显示自动/手动）
	CFDNSTokenSet                bool         // 平台级 Cloudflare DNS 令牌已授权
	CFDNSFingerprint             string       // 已授权令牌指纹（展示用）
	CFServerIPv4                 string       // 记住的服务器 IPv4（DNS A 记录目标）
	CFServerIPv6                 string       // 记住的服务器 IPv6（DNS AAAA 记录目标，可选）
	CFAuthorizeURL               string       // Cloudflare 授权模板链接
	CFProxied                    bool         // 「橙云代理」开关的记忆状态（勾选=写代理记录）
}

type OverviewStat struct {
	Label     string
	Href      string
	Icon      string
	Total     int
	Published int
	Draft     int
	Scheduled int
}

type OverviewTask struct {
	Label string
	Hint  string
	Href  string
	Icon  string
	Count int
	Tone  string
}

type OverviewStatus struct {
	Label string
	Value string
	Hint  string
	Href  string
	Icon  string
	Tone  string
}

type VisualField struct {
	Key        string
	Label      string
	Value      string
	Meta       string // 侧栏卡片的辅助展示值，例如导航 URL
	Group      string
	Kind       string // text | image
	Hint       string
	Multiline  bool
	Draggable  bool // 是否允许在可视化编辑侧栏拖动排序
	Contextual bool // 是否只在当前预览页出现对应元素时显示
	Localized  bool // 是否按语种保存
	Inherited  bool // 当前语种是否沿用默认语种
}

type VisualGroup struct {
	ID     string
	Title  string
	Fields []VisualField
}

type VisualLog struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Label string `json:"label"`
	Lang  string `json:"lang"`
	Kind  string `json:"kind"`
	Old   string `json:"old"`
	New   string `json:"new"`
	At    string `json:"at"`
}

type LocaleCatalogView struct {
	Code          string
	JSON          string
	Source        string
	SourceLabel   string
	OverrideCount int
	KeyCount      int
}

// GiscusView 是前台文章页渲染 giscus 所需的受控配置。
type GiscusView struct {
	Repo          string
	RepoID        string
	Category      string
	CategoryID    string
	Mapping       string
	Strict        string
	Reactions     string
	InputPosition string
	Theme         string
	Lang          string
}

// SettingsForm 承载后台设置页的可编辑字段。
type SettingsForm struct {
	Name               string
	NameDef            string
	Tagline            string
	TaglineDef         string
	Description        string
	DescriptionDef     string
	Keywords           string
	KeywordsDef        string
	PostAuthor         string
	PostAuthorDef      string
	LinkAuthor         string
	LinkAuthorDef      string
	Favicon            string
	Logo               string
	ShareImage         string
	Brand              string
	Theme              string
	Custom             bool   // 是否启用主题微调
	Accent             string // 自定义主色 #rrggbb
	Radius             string // 自定义圆角 px
	HeroEyebrow        string
	HeroTitle          string
	HeroDescription    string
	HeroDescriptionDef string
	HeroVisual         string // ""(默认动画) | image | svg
	HeroImage          string
	HeroImageDef       string
	HeroImageMode      string
	HeroSVG            string
	FooterNote         string
	// 首页栏目标题（可自定义，空则前台回落语种默认）
	HomeFeatured string
	HomeLinks    string
	HomeLatest   string
	// 首页显示数量（站点信息）
	HomeLinksLimit   string
	HomePostsPerPage string
	// 首页版块编排（默认布局：有序 + 开关）
	HomeSections []HomeSection
	HomeHero     bool
	// 各栏目标题的语种默认值（作为输入框 placeholder 提示）
	HomeFeaturedDef string
	HomeLinksDef    string
	HomeLatestDef   string
	// 代码注入（统计/广告等；头部进 <head> 末尾，尾部进 </body> 前）
	InjectHead string
	InjectBody string
	// 第三方评论（giscus）
	CommentsProvider    string
	GiscusRepo          string
	GiscusRepoID        string
	GiscusCategory      string
	GiscusCategoryID    string
	GiscusMapping       string
	GiscusStrict        bool
	GiscusReactions     bool
	GiscusInputPosition string
	GiscusTheme         string
	// 分类/链接列表的「全部」入口（设置 - 分类）。
	AllTitle       string
	AllLabel       string
	AllSlug        string
	AllPath        string
	AllDescription string
	ExternalLinks  ExternalLinkPolicyForm
}

const (
	defaultFaviconPath   = "/assets/favicon.svg"
	defaultLogoPath      = "/assets/logo.svg"
	defaultLogoENPath    = "/assets/logo-en.svg"
	defaultShareImageURL = "/assets/og-cover.webp"
)

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultAuthorFallback(kind, lang string) string {
	if lang == "en" {
		if kind == "link" {
			return "GCMS Picks"
		}
		return "GCMS Team"
	}
	if kind == "link" {
		return "gcms 推荐"
	}
	return "gcms 团队"
}

func defaultAuthorKey(kind string) string {
	if kind == "link" {
		return linkDefaultAuthorKey
	}
	return postDefaultAuthorKey
}

func (s *Server) defaultContentAuthor(kind, lang string) string {
	if lang == "" || !s.langEnabled(lang) {
		lang = s.defaultLang()
	}
	if v := strings.TrimSpace(s.store.Setting(s.copyKey(defaultAuthorKey(kind), lang))); v != "" {
		return v
	}
	if lang != "en" && lang != s.defaultLang() {
		if v := strings.TrimSpace(s.store.Setting(defaultAuthorKey(kind))); v != "" {
			return v
		}
	}
	return defaultAuthorFallback(kind, lang)
}

func (s *Server) fillDefaultAuthor(p *store.Post) {
	if p == nil || strings.TrimSpace(p.Author) != "" {
		return
	}
	switch p.Type {
	case "post", "link":
		p.Author = s.defaultContentAuthor(p.Type, p.Lang)
	}
}

func New(st *store.Store, baseURL, uploadDir string, tplFS, assetsFS fs.FS) (*Server, error) {
	return NewWithPlatform(st, nil, baseURL, uploadDir, tplFS, assetsFS)
}

func NewWithPlatform(st *store.Store, ps *platform.Store, baseURL, uploadDir string, tplFS, assetsFS fs.FS) (*Server, error) {
	imageSizes := scanAssetImageSizes(assetsFS)
	rnd, err := NewRenderer(tplFS, imageSizes)
	if err != nil {
		return nil, err
	}
	if uploadDir != "" {
		_ = os.MkdirAll(uploadDir, 0o755)
	}
	sessionStore := adminSessionStore(st)
	if ps != nil {
		sessionStore = ps
	}
	s := &Server{
		store: st, platform: ps, rnd: rnd, baseURL: baseURL, platformBaseURL: baseURL, uploadDir: uploadDir, assetsFS: assetsFS,
		sess: newSessions(sessionStore), login: newLoginLimiter(), apiLimiter: newAPIRateLimiter(), i18n: i18n.New(), assetVer: assetVersion(assetsFS), imageSizes: imageSizes,
		content: map[string]contentCacheEntry{}, endpoints: map[string]endpointCacheEntry{}, pages: map[string]pageCacheEntry{},
		cloudflareStatusFile: cloudflareStatusPath(),
	}
	s.i18n.LoadCustom(st.Setting("custom_locales")) // 合并后台新增的自定义语种预设
	s.i18n.LoadCatalogOverrides(st.Setting("locale_catalogs"))
	s.migratePlatformAdminI18N()
	s.routes(assetsFS)
	if ps != nil {
		if err := s.reloadRuntimePool(); err != nil {
			return nil, err
		}
	}
	s.resumeCloudflareSync()
	return s, nil
}

func (s *Server) Handler() http.Handler {
	if s.runtimePool() != nil {
		return http.HandlerFunc(s.serveWithRuntime)
	}
	return s.siteHandler()
}

func (s *Server) siteHandler() http.Handler {
	return s.securityHeaders(s.withCloudflareCanonicalFrontend(s.withLocale(s.publicPageCache(s.contentTypeRouter(s.mux)))))
}

func (s *Server) runtimePool() *SiteRuntimePool {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.runtimes
}

func (s *Server) setRuntimePool(pool *SiteRuntimePool) {
	s.runtimeMu.Lock()
	s.runtimes = pool
	s.runtimeMu.Unlock()
}

func (s *Server) detachSiteRuntime(siteID int64) {
	if siteID <= 0 {
		return
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimes == nil {
		return
	}
	rt := s.runtimes.byID[siteID]
	delete(s.runtimes.byID, siteID)
	for host, candidate := range s.runtimes.byHost {
		if candidate == rt {
			delete(s.runtimes.byHost, host)
		}
	}
	if rt != nil && rt.Store != nil && rt.Store != s.store && (rt.Site == nil || !rt.Site.IsDefault) {
		_ = rt.Store.Close()
	}
}

func (s *Server) reloadRuntimePool() error {
	if s.platform == nil {
		return nil
	}
	sites, err := s.platform.Sites()
	if err != nil {
		return err
	}
	domains, err := s.platform.SiteDomains()
	if err != nil {
		return err
	}
	domainsBySite := map[int64][]*platform.SiteDomain{}
	for _, d := range domains {
		if d == nil || !d.Enabled {
			continue
		}
		domainsBySite[d.SiteID] = append(domainsBySite[d.SiteID], d)
	}
	pool := &SiteRuntimePool{
		byID:          map[int64]*SiteRuntime{},
		byHost:        map[string]*SiteRuntime{},
		redirects:     map[string]string{},
		platformHost:  normalizeRuntimeHost(baseURLHost(s.baseURL)),
		localPlatform: isLocalBaseURL(s.baseURL),
	}
	for _, site := range sites {
		if site == nil {
			continue
		}
		rt, err := s.runtimeForSite(site, domainsBySite[site.ID])
		if err != nil {
			return err
		}
		pool.byID[site.ID] = rt
		if site.IsDefault || pool.defaultSite == nil {
			pool.defaultSite = rt
		}
		if site.Status != "enabled" {
			continue
		}
		if site.IsDefault {
			continue
		}
		siteDomains := domainsBySite[site.ID]
		primaryBase := ""
		for _, d := range siteDomains {
			if d.IsPrimary {
				if normalizeRuntimeHost(d.Host) != "" {
					primaryBase = d.Scheme + "://" + d.Host
				}
				break
			}
		}
		for _, d := range siteDomains {
			host := normalizeRuntimeHost(d.Host)
			if host == "" {
				continue
			}
			pool.byHost[host] = rt
			// 别名域名（标记跳转、且不是主域名）在有主域名时映射为 301 目标。
			if d.RedirectToPrimary && !d.IsPrimary && primaryBase != "" {
				pool.redirects[host] = primaryBase
			}
		}
	}
	if pool.defaultSite == nil {
		return fmt.Errorf("平台数据库缺少启用的默认站点")
	}
	if pool.platformHost != "" {
		pool.byHost[pool.platformHost] = pool.defaultSite
	}
	s.setRuntimePool(pool)
	return nil
}

func (s *Server) runtimeForSite(site *platform.Site, domains []*platform.SiteDomain) (*SiteRuntime, error) {
	baseURL := s.siteBaseURL(site, domains)
	uploadDir := strings.TrimSpace(site.UploadDir)
	if uploadDir == "" && site.IsDefault {
		uploadDir = s.uploadDir
	}
	st := s.store
	if !site.IsDefault && strings.TrimSpace(site.DBPath) != "" {
		opened, err := store.Open(site.DBPath)
		if err != nil {
			return nil, fmt.Errorf("打开站点 %s 数据库失败: %w", site.Slug, err)
		}
		st = opened
	}
	rt := &SiteRuntime{Site: site, Store: st, BaseURL: baseURL, UploadDir: uploadDir}
	if site.IsDefault {
		s.baseURL = baseURL
		s.uploadDir = uploadDir
		s.platformSiteID = site.ID
		rt.server = s
		return rt, nil
	}
	rt.server = s.cloneForRuntime(rt)
	return rt, nil
}

func (s *Server) cloneForRuntime(rt *SiteRuntime) *Server {
	if strings.TrimSpace(rt.UploadDir) != "" {
		_ = os.MkdirAll(rt.UploadDir, 0o755)
	}
	clone := &Server{
		store:                rt.Store,
		platform:             s.platform,
		rnd:                  s.rnd,
		baseURL:              rt.BaseURL,
		platformBaseURL:      s.platformBaseURL,
		platformSiteID:       rt.Site.ID,
		uploadDir:            rt.UploadDir,
		sess:                 s.sess,
		login:                s.login,
		apiLimiter:           s.apiLimiter,
		i18n:                 i18n.New(),
		assetsFS:             s.assetsFS,
		assetVer:             s.assetVer,
		imageSizes:           s.imageSizes,
		content:              map[string]contentCacheEntry{},
		endpoints:            map[string]endpointCacheEntry{},
		pages:                map[string]pageCacheEntry{},
		cloudflareStatusFile: cloudflareStatusPathForRuntime(rt),
	}
	clone.i18n.LoadCustom(rt.Store.Setting("custom_locales"))
	clone.i18n.LoadCatalogOverrides(rt.Store.Setting("locale_catalogs"))
	clone.routes(s.assetsFS)
	return clone
}

func (s *Server) siteBaseURL(site *platform.Site, domains []*platform.SiteDomain) string {
	if site != nil && site.IsDefault {
		return strings.TrimRight(strings.TrimSpace(s.baseURL), "/")
	}
	for _, d := range domains {
		if d != nil && d.Enabled && d.IsPrimary {
			if host := normalizeRuntimeHost(d.Host); host != "" {
				scheme := strings.TrimSpace(d.Scheme)
				if scheme != "http" && scheme != "https" {
					scheme = "https"
				}
				return scheme + "://" + host
			}
		}
	}
	for _, d := range domains {
		if d != nil && d.Enabled {
			if host := normalizeRuntimeHost(d.Host); host != "" {
				scheme := strings.TrimSpace(d.Scheme)
				if scheme != "http" && scheme != "https" {
					scheme = "https"
				}
				return scheme + "://" + host
			}
		}
	}
	return strings.TrimRight(strings.TrimSpace(s.baseURL), "/")
}

func baseURLHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	return u.Host
}

func normalizeRuntimeHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimSuffix(raw, "/")
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return ""
	}
	return raw
}

func (p *SiteRuntimePool) runtimeByHost(rawHost string) (*SiteRuntime, bool) {
	if p == nil {
		return nil, false
	}
	host := normalizeRuntimeHost(rawHost)
	if host != "" {
		if rt := p.byHost[host]; rt != nil {
			return rt, true
		}
	}
	if host == "" || (p.localPlatform && isLocalHostOnly(host)) {
		if p.defaultSite != nil {
			return p.defaultSite, true
		}
	}
	return nil, false
}

func (p *SiteRuntimePool) runtimeByID(id int64) (*SiteRuntime, bool) {
	if p == nil {
		return nil, false
	}
	if id > 0 {
		if rt := p.byID[id]; rt != nil {
			return rt, true
		}
	}
	return nil, false
}

func isLocalHostOnly(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	default:
		return false
	}
}

func (s *Server) serveWithRuntime(w http.ResponseWriter, r *http.Request) {
	// Identity marker so the domain-bind wizard's verify step can confirm a domain actually
	// reaches THIS gcms instance (read from the response header, cheap, unconditional).
	w.Header().Set("X-Gcms", "1")
	// Caddy on-demand TLS ask — must answer regardless of Host (Caddy calls it on
	// 127.0.0.1) and before any host-based routing.
	if r.URL.Path == "/internal/caddy/ask" {
		s.caddyAsk(w, r)
		return
	}
	// Loopback-only: the sudo sync script fetches the rendered Caddy domain config here.
	if r.URL.Path == "/internal/caddy/config" {
		s.caddyConfigHandler(w, r)
		return
	}
	pool := s.runtimePool()
	if pool == nil {
		s.siteHandler().ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/platform/") {
		s.servePlatformAPI(w, r, pool)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/sites/") && strings.Contains(r.URL.Path, "/preview") {
		s.serveSitePreview(w, r, pool)
		return
	}
	if siteID, target, ok := prefixedSiteAdminTarget(r.URL.Path); ok {
		s.servePrefixedSiteAdmin(w, r, pool, siteID, target)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/uploads/") && s.serveAdminScopedUpload(w, r, pool) {
		return
	}
	if platformOnlyPath(r.URL.Path) {
		if !s.platformHostAllowed(r, pool) {
			http.NotFound(w, r)
			return
		}
		s.siteHandler().ServeHTTP(w, r)
		return
	}
	if siteAdminPath(r.URL.Path) {
		sess, _ := s.currentSession(r)
		rt, ok := pool.runtimeByID(sess.currentSiteID)
		if !ok || rt == nil || rt.server == nil {
			http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
			return
		}
		rt.server.siteHandler().ServeHTTP(w, r)
		return
	}
	reqHost := requestHost(r)
	if target := pool.redirectTarget(reqHost); target != "" {
		http.Redirect(w, r, target+r.URL.RequestURI(), http.StatusMovedPermanently)
		return
	}
	rt, ok := pool.runtimeByHost(reqHost)
	if !ok || rt == nil || rt.server == nil {
		http.NotFound(w, r)
		return
	}
	rt.server.siteHandler().ServeHTTP(w, r)
}

func (s *Server) serveAdminScopedUpload(w http.ResponseWriter, r *http.Request, pool *SiteRuntimePool) bool {
	if !s.platformHostAllowed(r, pool) {
		return false
	}
	siteID := s.uploadSiteIDFromAdminContext(r)
	if siteID <= 0 {
		return false
	}
	rt, ok := pool.runtimeByID(siteID)
	if !ok || rt == nil || rt.server == nil || rt.Site == nil || rt.Site.IsDefault {
		return false
	}
	rt.server.siteHandler().ServeHTTP(w, r)
	return true
}

func (s *Server) uploadSiteIDFromAdminContext(r *http.Request) int64 {
	ref := strings.TrimSpace(r.Referer())
	if ref == "" {
		return 0
	}
	u, err := url.Parse(ref)
	if err != nil {
		return 0
	}
	if id, _, ok := sitePreviewTarget(u.Path); ok {
		return id
	}
	if !strings.HasPrefix(u.Path, "/admin") {
		return 0
	}
	sess, ok := s.currentSession(r)
	if !ok {
		return 0
	}
	return sess.currentSiteID
}

func (s *Server) serveSitePreview(w http.ResponseWriter, r *http.Request, pool *SiteRuntimePool) {
	if !s.platformHostAllowed(r, pool) {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.currentSession(r); !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	siteID, rest, ok := sitePreviewTarget(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rt, ok := pool.runtimeByID(siteID)
	if !ok || rt == nil || rt.server == nil {
		http.NotFound(w, r)
		return
	}
	if rest == "" || rest == "/" {
		rest = "/" + rt.server.defaultLang() + "/"
	}
	nextURL := *r.URL
	nextURL.Path = rest
	previewPrefix := "/admin/sites/" + strconv.FormatInt(siteID, 10) + "/preview"
	ctx := withPreviewRoutePrefix(withPreviewNoindex(r.Context()), previewPrefix)
	req := r.Clone(ctx)
	req.URL = &nextURL
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "no-store")
	rt.server.siteHandler().ServeHTTP(w, req)
}

func (s *Server) servePrefixedSiteAdmin(w http.ResponseWriter, r *http.Request, pool *SiteRuntimePool, siteID int64, target string) {
	if !s.platformHostAllowed(r, pool) {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.currentSession(r); !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	rt, ok := pool.runtimeByID(siteID)
	if !ok || rt == nil || rt.server == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.sess.setCurrentSite(sessionToken(r), siteID); err != nil {
		s.serverError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func prefixedSiteAdminTarget(path string) (int64, string, bool) {
	const prefix = "/admin/sites/"
	if !strings.HasPrefix(path, prefix) {
		return 0, "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	sitePart, tail, ok := strings.Cut(rest, "/")
	if !ok {
		return 0, "", false
	}
	id, err := strconv.ParseInt(sitePart, 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	tail = "/" + strings.TrimPrefix(tail, "/")
	head := strings.Trim(strings.SplitN(strings.TrimPrefix(tail, "/"), "/", 2)[0], "/")
	switch head {
	case "", "posts", "links", "pages", "settings", "visual":
		if head == "" {
			return id, "/admin", true
		}
		return id, "/admin" + tail, true
	default:
		return 0, "", false
	}
}

func sitePreviewTarget(path string) (int64, string, bool) {
	const prefix = "/admin/sites/"
	if !strings.HasPrefix(path, prefix) {
		return 0, "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	sitePart, tail, ok := strings.Cut(rest, "/preview")
	if !ok {
		return 0, "", false
	}
	id, err := strconv.ParseInt(strings.Trim(sitePart, "/"), 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	if tail == "" {
		tail = "/"
	}
	if !strings.HasPrefix(tail, "/") {
		tail = "/" + tail
	}
	return id, tail, true
}

// platformAutomationKillSwitchKey 是全局急停开关（平台设置）。置为 "1" 时，所有平台密钥请求一律 403，
// 用于事故一键封禁（不影响站点密钥与后台）。
const platformAutomationKillSwitchKey = "platform_automation_disabled"

func (s *Server) platformAutomationKilled() bool {
	return s.platform != nil && s.platform.Setting(platformAutomationKillSwitchKey) == "1"
}

func (s *Server) servePlatformAPI(w http.ResponseWriter, r *http.Request, pool *SiteRuntimePool) {
	if !s.platformHostAllowed(r, pool) {
		http.NotFound(w, r)
		return
	}
	// 发现端点：GET /api/platform/v1/sites（含结尾斜杠），在解析数字 siteID 之前拦截。
	if p := r.URL.Path; p == "/api/platform/v1/sites" || p == "/api/platform/v1/sites/" {
		s.servePlatformDiscovery(w, r, pool)
		return
	}
	siteID, ok := platformAPISiteID(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// 平台密钥鉴权：若 token 命中 platform_automation_keys，则在此处完成鉴权 + 成员/开关校验，
	// 再把合成身份注入 context，交由站点 handler 原样处理。
	// 若 token 不是平台密钥（未命中），**不要 401**——回退到站点路径，保证既有站点密钥（gcms_）
	// 通过平台命名空间调用时仍然可用（向后兼容 / invariant 4）。
	token := apiTokenFromRequest(r)
	if s.platform != nil && token != "" {
		key, isPlat, err := s.platform.GetPlatformKeyByToken(token)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "auth_error", err.Error())
			return
		}
		if isPlat {
			s.servePlatformKeyRequest(w, r, pool, key, siteID)
			return
		}
	}
	// 站点密钥路径（保持原逻辑不变）。
	rt, ok := pool.runtimeByID(siteID)
	if !ok || rt == nil || rt.server == nil || rt.Site == nil {
		http.NotFound(w, r)
		return
	}
	if !rt.Site.ManagementAutomationEnabled {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "platform_api_disabled", "message": "该站点未开启平台自动化入口。"})
		return
	}
	rt.server.siteHandler().ServeHTTP(w, r)
}

// servePlatformKeyRequest 处理一把平台密钥对某个站点的调用：
// 限流（仅此一次）→ 全局急停 → 实时成员/站点开关校验（读平台库，非缓存 rt.Site）→ 注入身份 → 交站点 handler。
func (s *Server) servePlatformKeyRequest(w http.ResponseWriter, r *http.Request, pool *SiteRuntimePool, key *platform.PlatformAutomationKey, siteID int64) {
	token := apiTokenFromRequest(r)
	// 平台层限流一次；requireAutomationToken 在平台身份下会跳过限流，避免二次计数。
	if !s.checkAPIRateLimit(w, r, token) {
		return
	}
	if s.platformAutomationKilled() {
		apiError(w, http.StatusForbidden, "platform_automation_disabled", "平台自动化已被全局关闭。")
		return
	}
	allowed, err := s.platform.PlatformKeyCanAccessSite(key, siteID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "auth_error", err.Error())
		return
	}
	if !allowed {
		apiError(w, http.StatusForbidden, "site_forbidden", "该密钥无权管理此站点，或站点未开启自动化。")
		return
	}
	rt, ok := pool.runtimeByID(siteID)
	if !ok || rt == nil || rt.server == nil || rt.Site == nil {
		apiError(w, http.StatusNotFound, "site_not_found", "站点不存在。")
		return
	}
	_ = s.platform.TouchPlatformKey(key.ID)
	ctx := withPlatformIdentity(r.Context(), &platformIdentity{keyID: key.ID, scopes: apiScopeMap(key.Scopes)})
	rt.server.siteHandler().ServeHTTP(w, r.WithContext(ctx))
}

// servePlatformDiscovery 回应 GET /api/platform/v1/sites：仅平台密钥可用，返回该密钥当前**实际可管**的站点集。
// 契约（已冻结，CLI 是硬消费方）：{"items":[{"id","slug","name","capabilities","api_base"}],"all_sites":bool}
// 附加字段（可选、只增不改，供 gcms Pilot 等客户端展示）："url" 站点公开地址（无法确定时为空）、"logo" 站点 Logo 绝对地址（未设置时为空）。
func (s *Server) servePlatformDiscovery(w http.ResponseWriter, r *http.Request, pool *SiteRuntimePool) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET。")
		return
	}
	if s.platform == nil {
		apiError(w, http.StatusNotFound, "platform_api_disabled", "未启用平台模式。")
		return
	}
	token := apiTokenFromRequest(r)
	if !s.checkAPIRateLimit(w, r, token) {
		return
	}
	if token == "" {
		apiError(w, http.StatusUnauthorized, "missing_token", "缺少访问密钥。")
		return
	}
	key, isPlat, err := s.platform.GetPlatformKeyByToken(token)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "auth_error", err.Error())
		return
	}
	if !isPlat {
		// 仅平台密钥可发现（站点密钥不暴露跨站清单）。
		apiError(w, http.StatusUnauthorized, "invalid_token", "访问密钥无效或不是平台密钥。")
		return
	}
	if s.platformAutomationKilled() {
		apiError(w, http.StatusForbidden, "platform_automation_disabled", "平台自动化已被全局关闭。")
		return
	}
	sites, err := s.platform.ManageableSites(key)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	caps := key.ScopeList()
	if caps == nil {
		caps = []string{}
	}
	base := strings.TrimRight(s.platformPublicBaseURL(r), "/")
	allDomains, _ := s.platform.SiteDomains()
	domainsBySite := map[int64][]*platform.SiteDomain{}
	for _, d := range allDomains {
		if d != nil {
			domainsBySite[d.SiteID] = append(domainsBySite[d.SiteID], d)
		}
	}
	items := make([]map[string]any, 0, len(sites))
	for _, site := range sites {
		items = append(items, map[string]any{
			"id":           site.ID,
			"slug":         site.Slug,
			"name":         site.Name,
			"capabilities": caps,
			"api_base":     fmt.Sprintf("%s/api/platform/v1/sites/%d", base, site.ID),
			"url":          s.discoverySiteURL(site, domainsBySite[site.ID]),
			"logo":         s.discoverySiteLogo(pool, site, domainsBySite[site.ID]),
		})
	}
	_ = s.platform.TouchPlatformKey(key.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"all_sites": key.MembershipMode == platform.KeyMembershipAll,
	})
}

// discoverySiteURL 给出站点对外可访问的公开地址；非默认站且没有已启用域名时返回空（避免误导到平台地址）。
func (s *Server) discoverySiteURL(site *platform.Site, domains []*platform.SiteDomain) string {
	if site == nil {
		return ""
	}
	if !site.IsDefault {
		hasEnabled := false
		for _, d := range domains {
			if d != nil && d.Enabled {
				hasEnabled = true
				break
			}
		}
		if !hasEnabled {
			return ""
		}
	}
	return s.siteBaseURL(site, domains)
}

// discoverySiteLogo 读取站点的 site.logo 设置并转成绝对地址；拿不到公开地址的相对 Logo 返回空。
func (s *Server) discoverySiteLogo(pool *SiteRuntimePool, site *platform.Site, domains []*platform.SiteDomain) string {
	if pool == nil || site == nil {
		return ""
	}
	rt, ok := pool.runtimeByID(site.ID)
	if !ok || rt == nil || rt.Store == nil {
		return ""
	}
	logo := strings.TrimSpace(rt.Store.Setting("site.logo"))
	if logo == "" {
		return ""
	}
	if strings.HasPrefix(logo, "http://") || strings.HasPrefix(logo, "https://") {
		return logo
	}
	base := s.discoverySiteURL(site, domains)
	if base == "" {
		return ""
	}
	return base + "/" + strings.TrimLeft(logo, "/")
}

func platformAPISiteID(path string) (int64, bool) {
	const prefix = "/api/platform/v1/sites/"
	if !strings.HasPrefix(path, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(path, prefix)
	part, _, _ := strings.Cut(rest, "/")
	id, err := strconv.ParseInt(part, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func (s *Server) platformHostAllowed(r *http.Request, pool *SiteRuntimePool) bool {
	if pool == nil || pool.platformHost == "" {
		return true
	}
	host := normalizeRuntimeHost(requestHost(r))
	if host == pool.platformHost {
		return true
	}
	return pool.localPlatform && isLocalHostOnly(host)
}

func platformOnlyPath(path string) bool {
	switch {
	case path == "/admin/login", path == "/admin/language", path == "/admin/logout", path == "/admin/dismiss-pw":
		return true
	case path == "/admin/security":
		return true
	case path == "/admin/platform/settings":
		return true
	case path == "/admin/server-health":
		return true
	case strings.HasPrefix(path, "/admin/google/oauth/") || strings.HasPrefix(path, "/admin/google/accounts/") || strings.HasPrefix(path, "/admin/google/analytics/") || strings.HasPrefix(path, "/admin/google/search-console/"):
		return true
	case path == "/admin/backups" || strings.HasPrefix(path, "/admin/backups/"):
		return true
	case path == "/admin/archived-sites" || strings.HasPrefix(path, "/admin/archived-sites/"):
		return true
	case path == "/admin/updates" || strings.HasPrefix(path, "/admin/updates/"):
		return true
	case path == "/admin/admin-i18n":
		return true
	case path == "/admin/settings/updates" || strings.HasPrefix(path, "/admin/settings/updates/"):
		return true
	case path == "/admin/settings/admin-i18n":
		return true
	case path == "/admin/sites" || strings.HasPrefix(path, "/admin/sites/"):
		return true
	case path == "/admin/automation" || strings.HasPrefix(path, "/admin/automation/"):
		return true
	case strings.HasPrefix(path, "/api/platform/"):
		return true
	default:
		return false
	}
}

func siteAdminPath(path string) bool {
	return path == "/admin" || strings.HasPrefix(path, "/admin/")
}

func (s *Server) withCloudflareCanonicalFrontend(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if previewNoindexFrom(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		action := s.cloudflareSourceFrontendAction(r)
		if target := action.redirectURL; target != "" {
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		if action.noindex {
			w.Header().Set("X-Robots-Tag", "noindex, follow")
		}
		next.ServeHTTP(w, r)
	})
}

type cloudflareSourceFrontendAction struct {
	redirectURL string
	noindex     bool
}

func (s *Server) cloudflareCanonicalFrontendRedirect(r *http.Request) string {
	return s.cloudflareSourceFrontendAction(r).redirectURL
}

func (s *Server) cloudflareCanonicalFrontendNoindex(r *http.Request) bool {
	return s.cloudflareSourceFrontendAction(r).noindex
}

func (s *Server) cloudflareSourceFrontendAction(r *http.Request) cloudflareSourceFrontendAction {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return cloudflareSourceFrontendAction{}
	}
	if previewNoindexFrom(r.Context()) {
		return cloudflareSourceFrontendAction{}
	}
	if cloudflareCanonicalFrontendExemptPath(r.URL.Path) {
		return cloudflareSourceFrontendAction{}
	}
	mode := s.cloudflareSourceFrontendMode()
	if mode == cloudflareSourceModeNone {
		return cloudflareSourceFrontendAction{}
	}
	primary := s.cloudflarePublishedPrimaryHost()
	if primary == "" {
		return cloudflareSourceFrontendAction{}
	}
	host := normalizeCloudflareDomainHost(requestHost(r))
	if host == "" || sameCloudflareDNSName(host, primary) {
		return cloudflareSourceFrontendAction{}
	}
	if mode == cloudflareSourceModeNoindex {
		return cloudflareSourceFrontendAction{noindex: true}
	}
	next := *r.URL
	next.Scheme = "https"
	next.Host = primary
	return cloudflareSourceFrontendAction{redirectURL: next.String()}
}

func (s *Server) cloudflareSourceFrontendMode() string {
	if s == nil || s.store == nil {
		return cloudflareSourceModeRedirect
	}
	return normalizeCloudflareSourceMode(s.store.Setting(cloudflareSourceModeKey))
}

func (s *Server) cloudflarePublishedPrimaryHost() string {
	st := s.readCloudflareStatus()
	if !cloudflareStatusPublished(st) {
		return ""
	}
	if host := normalizeCloudflareDomainHost(st.PrimaryDomain); host != "" {
		return host
	}
	return s.cloudflareConfig().primaryHost()
}

func cloudflareCanonicalFrontendExemptPath(path string) bool {
	for _, prefix := range []string{"/admin", "/api", "/preview", "/assets", "/uploads", "/.well-known"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return path == "/favicon.ico"
}

func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request) {
	name, ok := uploadNameFromPath(r.URL.EscapedPath())
	if !ok {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(s.uploadDir, name)
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

func uploadNameFromPath(escapedPath string) (string, bool) {
	raw := strings.TrimPrefix(escapedPath, "/uploads/")
	if raw == "" {
		return "", false
	}
	name, err := url.PathUnescape(raw)
	if err != nil || !validUploadFilename(name) {
		return "", false
	}
	return name, true
}

func validUploadFilename(name string) bool {
	if name == "" || name == "." || strings.HasPrefix(name, ".") || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return false
	}
	if !allowedUploadExt[strings.ToLower(filepath.Ext(name))] {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func (s *Server) assetCacheControl(r *http.Request) string {
	if s.assetVer != "" && r.URL.Query().Get("v") == s.assetVer {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=86400"
}

// securityHeaders 给所有响应加上基础安全头；并为静态资源/上传文件加缓存，
// 特别地对 /uploads/（尤其用户上传的 SVG）施加 CSP，杜绝直链访问触发脚本执行（XSS）。
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		p := r.URL.Path
		// 后台禁止被任意站点内嵌（防点击劫持）；公开页不限制，便于被正常嵌入/预览
		if strings.HasPrefix(p, "/admin") {
			h.Set("X-Frame-Options", "SAMEORIGIN")
		}
		switch {
		case strings.HasPrefix(p, "/uploads/"):
			// 用户上传内容：禁脚本与插件，并禁止被嵌入为顶层文档执行（SVG XSS 防护）
			h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'none'; object-src 'none'; base-uri 'none'; sandbox")
			h.Set("Cache-Control", uploadCacheControl)
		case strings.HasPrefix(p, "/assets/"):
			h.Set("Cache-Control", s.assetCacheControl(r))
		default:
			h.Set("Content-Security-Policy-Report-Only", cspReportOnly(p))
		}
		next.ServeHTTP(w, r)
	})
}

func cspReportOnly(path string) string {
	common := "default-src 'self'; base-uri 'self'; object-src 'none'; form-action 'self'; img-src 'self' data: blob: https:; media-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; font-src 'self' data:;"
	if strings.HasPrefix(path, "/admin") {
		return common + " script-src 'self' 'unsafe-inline'; connect-src 'self' https://api.github.com https://github.com; frame-src 'self'; frame-ancestors 'self'"
	}
	return common + " script-src 'self' 'unsafe-inline' https://giscus.app https://www.googletagmanager.com; connect-src 'self' https://giscus.app https://api.github.com https://github.com https://www.google-analytics.com https://region1.google-analytics.com; frame-src 'self' https://giscus.app; frame-ancestors 'self'"
}

// ---------- 多语种基础设施 ----------

type ctxKey int

const langKey ctxKey = 0
const publicBaseKey ctxKey = 1
const previewNoindexKey ctxKey = 2
const previewRoutePrefixKey ctxKey = 3
const previewThemeKey ctxKey = 4

func withLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, langKey, lang)
}

func withPublicBase(ctx context.Context, baseURL string) context.Context {
	return context.WithValue(ctx, publicBaseKey, strings.TrimRight(strings.TrimSpace(baseURL), "/"))
}

func withPreviewNoindex(ctx context.Context) context.Context {
	return context.WithValue(ctx, previewNoindexKey, true)
}

func withPreviewRoutePrefix(ctx context.Context, prefix string) context.Context {
	prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return ctx
	}
	return context.WithValue(ctx, previewRoutePrefixKey, prefix)
}

func previewNoindexFrom(ctx context.Context) bool {
	v, _ := ctx.Value(previewNoindexKey).(bool)
	return v
}

func previewRoutePrefixFrom(ctx context.Context) string {
	v, _ := ctx.Value(previewRoutePrefixKey).(string)
	return v
}

// withPreviewTheme / previewThemeFrom：主题试穿预览——在不改站点设置的前提下，
// 让本次请求树里的所有前台页面按候选主题渲染（viewForLang 统一读取）。
func withPreviewTheme(ctx context.Context, theme string) context.Context {
	if strings.TrimSpace(theme) == "" {
		return ctx
	}
	return context.WithValue(ctx, previewThemeKey, theme)
}

func previewThemeFrom(ctx context.Context) string {
	v, _ := ctx.Value(previewThemeKey).(string)
	return v
}

func langFrom(r *http.Request) string {
	if v, ok := r.Context().Value(langKey).(string); ok && v != "" {
		return v
	}
	return "zh"
}

// locales 返回已启用语种（首个为默认）。
func (s *Server) locales() []i18n.Locale { return s.i18n.Active(s.store.Setting("locales")) }

func (s *Server) defaultLang() string { return s.locales()[0].Code }

func (s *Server) langEnabled(code string) bool {
	for _, l := range s.locales() {
		if l.Code == code {
			return true
		}
	}
	return false
}

type langPreference struct {
	value string
	q     float64
	order int
}

func normalizeLangToken(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.ReplaceAll(v, "_", "-")
	return v
}

func parseAcceptLanguage(header string) []langPreference {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	parts := strings.Split(header, ",")
	prefs := make([]langPreference, 0, len(parts))
	for i, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		segments := strings.Split(raw, ";")
		value := normalizeLangToken(segments[0])
		if value == "" {
			continue
		}
		q := 1.0
		for _, seg := range segments[1:] {
			seg = strings.TrimSpace(seg)
			if strings.HasPrefix(seg, "q=") {
				v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(seg, "q=")), 64)
				if err != nil {
					q = 0
					break
				}
				q = v
			}
		}
		if q <= 0 {
			continue
		}
		if q > 1 {
			q = 1
		}
		prefs = append(prefs, langPreference{value: value, q: q, order: i})
	}
	sort.SliceStable(prefs, func(i, j int) bool {
		if prefs[i].q == prefs[j].q {
			return prefs[i].order < prefs[j].order
		}
		return prefs[i].q > prefs[j].q
	})
	return prefs
}

func negotiateAcceptLanguage(header string, locales []i18n.Locale, fallback string) string {
	if len(locales) == 0 {
		return fallback
	}
	code := map[string]string{}
	tag := map[string]string{}
	for _, l := range locales {
		code[normalizeLangToken(l.Code)] = l.Code
		tag[normalizeLangToken(l.Tag)] = l.Code
	}
	if fallback == "" {
		fallback = locales[0].Code
	}
	for _, pref := range parseAcceptLanguage(header) {
		if pref.value == "*" {
			return fallback
		}
		if v, ok := code[pref.value]; ok {
			return v
		}
		if v, ok := tag[pref.value]; ok {
			return v
		}
		primary := pref.value
		if i := strings.IndexByte(primary, '-'); i >= 0 {
			primary = primary[:i]
		}
		if v, ok := code[primary]; ok {
			return v
		}
		if v, ok := tag[primary]; ok {
			return v
		}
		for _, l := range locales {
			c := normalizeLangToken(l.Code)
			t := normalizeLangToken(l.Tag)
			if strings.HasPrefix(c, primary+"-") || strings.HasPrefix(t, primary+"-") {
				return l.Code
			}
		}
	}
	return fallback
}

func (s *Server) preferredLang(r *http.Request, fallback string) string {
	return negotiateAcceptLanguage(r.Header.Get("Accept-Language"), s.locales(), fallback)
}

func (s *Server) abs(path string) string { return absWithBase(s.baseURL, path) }

func absWithBase(baseURL, path string) string { return strings.TrimRight(baseURL, "/") + path }

func (s *Server) absForRequest(r *http.Request, path string) string {
	return absWithBase(s.publicBaseURL(r), path)
}

func (s *Server) absForPlatformRequest(r *http.Request, path string) string {
	return absWithBase(s.platformPublicBaseURL(r), path)
}

func (s *Server) platformPublicBaseURL(r *http.Request) string {
	configured := strings.TrimRight(strings.TrimSpace(s.platformBaseURL), "/")
	if configured == "" {
		configured = strings.TrimRight(strings.TrimSpace(s.baseURL), "/")
	}
	if configured != "" && !isLocalBaseURL(configured) {
		return configured
	}
	if host := requestHost(r); host != "" {
		return requestScheme(r) + "://" + host
	}
	if configured != "" {
		return configured
	}
	return s.publicBaseURL(r)
}

func (s *Server) publicBaseURL(r *http.Request) string {
	if r != nil {
		if v, ok := r.Context().Value(publicBaseKey).(string); ok && v != "" {
			return v
		}
	}
	configured := strings.TrimRight(strings.TrimSpace(s.baseURL), "/")
	if configured != "" && !isLocalBaseURL(configured) {
		return configured
	}
	if host := requestHost(r); host != "" {
		return requestScheme(r) + "://" + host
	}
	if configured != "" {
		return configured
	}
	return "http://localhost:8080"
}

func requestScheme(r *http.Request) string {
	if r == nil {
		return "http"
	}
	if proto := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); proto == "http" || proto == "https" {
		return proto
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Ssl"), "on") || r.TLS != nil {
		return "https"
	}
	return "http"
}

func requestHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, raw := range []string{r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Original-Host"), r.Host} {
		host := firstHeaderValue(raw)
		if host != "" && !strings.ContainsAny(host, " \t\r\n") {
			return host
		}
	}
	return ""
}

func firstHeaderValue(raw string) string {
	if raw == "" {
		return ""
	}
	if i := strings.IndexByte(raw, ','); i >= 0 {
		raw = raw[:i]
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

func isLocalBaseURL(raw string) bool {
	host := ""
	if u, err := url.Parse(raw); err == nil {
		host = u.Hostname()
	}
	if host == "" {
		host = strings.TrimSpace(raw)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
	}
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	default:
		return false
	}
}

// 这些路径不参与语种前缀：后台、静态资源、上传、临时预览、全局 SEO 端点。
func isReservedPath(p string) bool {
	switch {
	case strings.HasPrefix(p, "/admin"), strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, "/assets/"), strings.HasPrefix(p, "/uploads/"), strings.HasPrefix(p, "/preview/"):
		return true
	case p == "/robots.txt", p == "/sitemap.xml", p == "/favicon.ico":
		return true
	}
	return false
}

func shiftPath(p string) (head, tail string) {
	p = strings.TrimPrefix(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i:]
	}
	return p, "/"
}

// withLocale 是放在 mux 前的中间件：识别并剥掉路径里的语种前缀写入 context；
// 无前缀的公开路径 302 跳到默认语种；保留路径原样透传。
func (s *Server) withLocale(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		def := s.defaultLang()
		if isReservedPath(r.URL.Path) {
			next.ServeHTTP(w, r.WithContext(withLang(r.Context(), def)))
			return
		}
		head, tail := shiftPath(r.URL.Path)
		if s.langEnabled(head) {
			r.URL.Path = tail
			next.ServeHTTP(w, r.WithContext(withLang(r.Context(), head)))
			return
		}
		// 无语种前缀 → 根路径按 Accept-Language 协商，其它路径仍跳兜底语种，避免旧链接被跳到不存在的译文。
		targetLang := def
		if r.URL.Path == "/" {
			targetLang = s.preferredLang(r, def)
			w.Header().Add("Vary", "Accept-Language")
			w.Header().Set("Cache-Control", "private, no-store")
		}
		target := "/" + targetLang
		if r.URL.Path == "/" {
			target += "/"
		} else {
			target += r.URL.Path
		}
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
}

func localizedPrefix(routePrefix, lang string) string {
	routePrefix = strings.TrimRight(strings.TrimSpace(routePrefix), "/")
	if routePrefix != "" {
		return routePrefix + "/" + lang
	}
	return "/" + lang
}

func localizedPath(routePrefix, lang, p string) string {
	prefix := localizedPrefix(routePrefix, lang)
	if p == "" || p == "/" {
		return prefix + "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return prefix + p
}

func previewLocalizedPath(r *http.Request, lang, p string) string {
	if r == nil {
		return localizedPath("", lang, p)
	}
	return localizedPath(previewRoutePrefixFrom(r.Context()), lang, p)
}

func previewRootPath(r *http.Request, p string) string {
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if r == nil {
		return p
	}
	if prefix := previewRoutePrefixFrom(r.Context()); prefix != "" {
		return strings.TrimRight(prefix, "/") + p
	}
	return p
}

// langSwitch 构建「仅切换器」语言链接（不输出 hreflang）：每个语种走 fallback 路径。
func (s *Server) langSwitch(cur string, pathByLang map[string]string, fallback string) []LangLink {
	return s.langSwitchForRequest(nil, cur, pathByLang, fallback)
}

func (s *Server) langSwitchForRequest(r *http.Request, cur string, pathByLang map[string]string, fallback string) []LangLink {
	var out []LangLink
	for _, l := range s.locales() {
		p := fallback
		if pathByLang != nil {
			if v, ok := pathByLang[l.Code]; ok {
				p = v
			}
		}
		out = append(out, LangLink{Code: l.Code, Name: l.Name, URL: previewLocalizedPath(r, l.Code, p), Active: l.Code == cur})
	}
	return out
}

// i18nLinks 给定「该页在各语种的相对路径」，同时构建语言切换器与 hreflang 备份链接。
// pathByLang 仅包含真实存在译文的语种；缺失语种的切换器回退到该语种首页，且不输出其 hreflang。
func (s *Server) i18nLinks(baseURL, cur string, pathByLang map[string]string) (langs []LangLink, alts []seo.Alternate) {
	return s.i18nLinksForRequest(nil, baseURL, cur, pathByLang)
}

func (s *Server) i18nLinksForRequest(r *http.Request, baseURL, cur string, pathByLang map[string]string) (langs []LangLink, alts []seo.Alternate) {
	def := s.defaultLang()
	for _, l := range s.locales() {
		if p, ok := pathByLang[l.Code]; ok {
			langs = append(langs, LangLink{Code: l.Code, Name: l.Name, URL: previewLocalizedPath(r, l.Code, p), Active: l.Code == cur})
			alts = append(alts, seo.Alternate{Hreflang: l.Tag, Href: absWithBase(baseURL, localizedPath("", l.Code, p))})
		} else {
			langs = append(langs, LangLink{Code: l.Code, Name: l.Name, URL: previewLocalizedPath(r, l.Code, "/"), Active: l.Code == cur})
		}
	}
	if p, ok := pathByLang[def]; ok {
		alts = append(alts, seo.Alternate{Hreflang: "x-default", Href: absWithBase(baseURL, localizedPath("", def, p))})
	} else {
		alts = append(alts, seo.Alternate{Hreflang: "x-default", Href: absWithBase(baseURL, localizedPath("", def, "/"))})
	}
	return
}

func (s *Server) localizedSetting(base, lang, dflt string) string {
	def := s.defaultLang()
	if lang != def {
		if v := strings.TrimSpace(s.store.Setting(base + "::" + lang)); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(s.store.Setting(base)); v != "" {
		return v
	}
	return dflt
}

func archivePrefix(kind string) string {
	if kind == "link" {
		return "links.all."
	}
	return "category.all."
}

func (s *Server) archiveStoreKey(kind, field, lang string) string {
	return s.copyKey(archivePrefix(kind)+field, lang)
}

func normalizeArchiveSlug(slug, fallback string) string {
	slug = slugify(slug)
	if slug == "" {
		return fallback
	}
	return slug
}

func archivePath(kind, slug string) string {
	switch kind {
	case "link":
		if slug == "" || slug == "links" {
			return "/links"
		}
		return "/links/" + slug
	default:
		if slug == "" || slug == "category" {
			return "/category"
		}
		return "/category/" + slug
	}
}

func (s *Server) archiveConfig(lang, kind string) ArchiveConfig {
	def := s.defaultLang()
	tr := s.i18n.Tr(lang, def)
	prefix := archivePrefix(kind)
	siteDesc := s.localizedSetting("site.description", lang, "用 Go 与 SQLite 构建的轻量内容站，关注后端工程、极简设计与搜索引擎优化。")
	titleDef, labelDef, slugDef := tr.T("nav.category"), tr.T("links.all"), "category"
	descDef := tr.T("archive.post_description")
	if kind == "link" {
		titleDef = tr.T("nav.links")
		slugDef = "links"
		descDef = tr.T("archive.link_description")
	}
	title := s.localizedSetting(prefix+"title", lang, titleDef)
	label := s.localizedSetting(prefix+"label", lang, labelDef)
	desc := s.localizedSetting(prefix+"description", lang, nonEmpty(descDef, siteDesc))
	slug := normalizeArchiveSlug(s.localizedSetting(prefix+"slug", lang, slugDef), slugDef)
	return ArchiveConfig{Title: title, Label: label, Description: desc, Slug: slug, Path: archivePath(kind, slug)}
}

// site 每请求构建站点配置（含当前语种的文案、前缀、OG/hreflang 元信息）。
func (s *Server) site(lang string) seo.Site {
	loc := s.i18n.Locale(lang)
	def := s.defaultLang()
	tr := s.i18n.Tr(lang, def)
	// 文案：优先 key::lang，回落默认语种 bare key，再回落硬编码默认。
	get := func(base, dflt string) string {
		if lang != def {
			if v := s.store.Setting(base + "::" + lang); v != "" {
				return v
			}
		}
		if v := s.store.Setting(base); v != "" {
			return v
		}
		return dflt
	}
	getAsset := func(base string) string {
		if lang != def {
			if v := s.store.Setting(base + "::" + lang); v != "" {
				return v
			}
		}
		return s.store.Setting(base)
	}
	theme := s.store.Setting("theme")
	if !validTheme(theme) {
		theme = "editorial"
	}
	var ogAlt []string
	for _, l := range s.locales() {
		if l.Code != lang {
			ogAlt = append(ogAlt, l.OG)
		}
	}
	brand := s.store.Setting("site.brand")
	if brand == "" {
		brand = "logo"
	}
	logo := getAsset("site.logo")
	if logo == "" {
		logo = defaultLogoPath
	}
	if lang == "en" && logo == defaultLogoPath {
		logo = defaultLogoENPath
	}
	defaultSiteDescription := "用 Go 与 SQLite 构建的轻量内容站，关注后端工程、极简设计与搜索引擎优化。"
	defaultSiteKeywords := "Go,SQLite,CMS,内容管理系统,服务端渲染,SEO,极简设计,后端工程"
	if lang == "en" {
		defaultSiteDescription = "A lightweight content site built with Go and SQLite — focused on backend engineering, minimal design and SEO."
		defaultSiteKeywords = "Go,SQLite,CMS,content management,server-side rendering,SEO,minimal design,backend engineering"
	}
	siteDescription := get("site.description", defaultSiteDescription)
	heroDescription := get("site.hero_description", siteDescription)
	linkAll := s.archiveConfig(lang, "link")
	return seo.Site{
		Name:             get("site.name", "CCVAR 简记"),
		Tagline:          get("site.tagline", "记录技术、工具与思考"),
		Description:      siteDescription,
		Keywords:         get("site.keywords", defaultSiteKeywords),
		BaseURL:          s.baseURL,
		Locale:           loc.OG,
		LangTag:          loc.Tag,
		Prefix:           "/" + loc.Code,
		Author:           s.defaultContentAuthor("post", lang),
		Theme:            theme,
		Favicon:          s.store.Setting("site.favicon"),
		Logo:             logo,
		ShareImage:       getAsset("site.share_image"),
		Brand:            brand,
		HeroEyebrow:      get("site.hero_eyebrow", "Go · SQLite · SEO"),
		HeroTitle:        get("site.hero_title", "把复杂留给后端，\n把简单留给读者。"),
		HeroDescription:  heroDescription,
		HeroVisual:       getAsset("hero.visual"),
		HeroImage:        getAsset("hero.image"),
		HeroSVG:          s.store.Setting("hero.svg"),
		FooterNote:       get("site.footer_note", "用 Go 与 SQLite 构建。"),
		HomeFeatured:     get("home.featured_title", tr.T("home.featured")),
		HomeLinks:        get("home.links_title", tr.T("home.links")),
		HomeLatest:       get("home.latest_title", tr.T("home.latest")),
		HomeLabel:        tr.T("nav.home"),
		LinksLabel:       linkAll.Title,
		LinksDescription: linkAll.Description,
		InjectHead:       s.store.Setting("inject.head"),
		InjectBody:       s.store.Setting("inject.body"),
		OGAltLocale:      ogAlt,
	}
}

// themeOverride 取「当前主题」的微调，生成注入 <html> 的内联 CSS 变量。
func (s *Server) themeOverride() template.CSS {
	return s.themeOverrideFor(s.store.Setting("theme"))
}

func (s *Server) themeOverrideFor(theme string) template.CSS {
	if !validTheme(theme) {
		theme = "editorial"
	}
	custom, accent, radius := s.themeTweak(theme)
	var b strings.Builder
	if width := normalizeLayoutWidth(s.store.Setting(layoutWidthKey)); width != "" {
		b.WriteString("--w-wide:" + width + "px;")
	}
	if !custom {
		return template.CSS(b.String())
	}
	if hexColor(accent) {
		b.WriteString("--accent:" + accent + ";")
		b.WriteString("--accent-soft:color-mix(in srgb," + accent + " 80%,#fff);")
		b.WriteString("--accent-wash:color-mix(in srgb," + accent + " 14%,transparent);")
	}
	if n, err := strconv.Atoi(radius); err == nil && n >= 0 && n <= 40 {
		b.WriteString("--radius:" + strconv.Itoa(n) + "px;")
	}
	return template.CSS(b.String())
}

func (s *Server) renderedContent(p *store.Post) (template.HTML, []Heading) {
	if p == nil {
		return "", nil
	}
	key := p.Type + ":" + strconv.FormatInt(p.ID, 10)
	s.cacheMu.RLock()
	if e, ok := s.content[key]; ok && e.updatedAt.Equal(p.UpdatedAt) {
		toc := append([]Heading(nil), e.toc...)
		s.cacheMu.RUnlock()
		return e.html, toc
	}
	s.cacheMu.RUnlock()

	policy := s.externalLinkPolicy()
	html, toc := RenderContentWithLinkPolicy(p.Content, s.imageSizes, &policy)
	s.cacheMu.Lock()
	if len(s.content) > 512 {
		s.content = map[string]contentCacheEntry{}
	}
	s.content[key] = contentCacheEntry{updatedAt: p.UpdatedAt, html: html, toc: append([]Heading(nil), toc...)}
	s.cacheMu.Unlock()
	return html, toc
}

type captureResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newCaptureResponseWriter() *captureResponseWriter {
	return &captureResponseWriter{header: http.Header{}}
}

func (w *captureResponseWriter) Header() http.Header {
	return w.header
}

func (w *captureResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *captureResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

func copyHTTPHeader(dst, src http.Header) {
	for k, vv := range src {
		dst[k] = append([]string(nil), vv...)
	}
}

func pageCacheETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func etagMatches(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	for _, raw := range strings.Split(header, ",") {
		tag := strings.TrimSpace(raw)
		if tag == "*" || tag == etag || strings.TrimPrefix(tag, "W/") == etag {
			return true
		}
	}
	return false
}

func (s *Server) publicPageCacheKey(r *http.Request) string {
	var b strings.Builder
	b.WriteString(langFrom(r))
	b.WriteByte('|')
	b.WriteString(s.publicBaseURL(r))
	b.WriteByte('|')
	b.WriteString(r.URL.Path)
	if r.URL.RawQuery != "" {
		b.WriteByte('?')
		b.WriteString(r.URL.RawQuery)
	}
	b.WriteByte('|')
	b.WriteString(s.assetVer)
	return b.String()
}

func publicPageCacheableRequest(r *http.Request) bool {
	if r.Method != http.MethodGet || r.Header.Get("Range") != "" {
		return false
	}
	if previewNoindexFrom(r.Context()) {
		return false
	}
	return !isReservedPath(r.URL.Path)
}

func (s *Server) cachedPage(key string) (pageCacheEntry, bool) {
	now := time.Now()
	s.cacheMu.RLock()
	e, ok := s.pages[key]
	if ok && now.Before(e.expires) {
		e.body = append([]byte(nil), e.body...)
		s.cacheMu.RUnlock()
		return e, true
	}
	s.cacheMu.RUnlock()
	if ok {
		s.cacheMu.Lock()
		if cur, still := s.pages[key]; still && now.After(cur.expires) {
			delete(s.pages, key)
		}
		s.cacheMu.Unlock()
	}
	return pageCacheEntry{}, false
}

func (s *Server) setCachedPage(key string, e pageCacheEntry) {
	s.cacheMu.Lock()
	if s.pages == nil || len(s.pages) >= publicPageCacheLimit {
		s.pages = map[string]pageCacheEntry{}
	}
	e.body = append([]byte(nil), e.body...)
	s.pages[key] = e
	s.cacheMu.Unlock()
}

func writeCachedPage(w http.ResponseWriter, r *http.Request, e pageCacheEntry) {
	w.Header().Set("Content-Type", e.contentType)
	w.Header().Set("Cache-Control", publicPageCacheControl)
	w.Header().Set("ETag", e.etag)
	if etagMatches(r.Header.Get("If-None-Match"), e.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(e.body)
}

func (s *Server) publicPageCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !publicPageCacheableRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		key := s.publicPageCacheKey(r)
		if e, ok := s.cachedPage(key); ok {
			writeCachedPage(w, r, e)
			return
		}

		cw := newCaptureResponseWriter()
		next.ServeHTTP(cw, r)
		status := cw.status
		if status == 0 {
			status = http.StatusOK
		}
		body := cw.body.Bytes()
		contentType := cw.Header().Get("Content-Type")
		if contentType == "" {
			contentType = "text/html; charset=utf-8"
		}

		copyHTTPHeader(w.Header(), cw.Header())
		if status == http.StatusOK && strings.HasPrefix(strings.ToLower(contentType), "text/html") && cw.Header().Get("Set-Cookie") == "" && !strings.Contains(strings.ToLower(cw.Header().Get("Cache-Control")), "no-store") {
			etag := pageCacheETag(body)
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", publicPageCacheControl)
			w.Header().Set("ETag", etag)
			s.setCachedPage(key, pageCacheEntry{body: body, contentType: contentType, etag: etag, expires: time.Now().Add(publicPageCacheTTL)})
			if etagMatches(r.Header.Get("If-None-Match"), etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

func (s *Server) cachedEndpoint(key string) ([]byte, string, bool) {
	now := time.Now()
	s.cacheMu.RLock()
	e, ok := s.endpoints[key]
	if ok && now.Before(e.expires) {
		body := append([]byte(nil), e.body...)
		s.cacheMu.RUnlock()
		return body, e.contentType, true
	}
	s.cacheMu.RUnlock()
	if ok {
		s.cacheMu.Lock()
		if cur, still := s.endpoints[key]; still && now.After(cur.expires) {
			delete(s.endpoints, key)
		}
		s.cacheMu.Unlock()
	}
	return nil, "", false
}

func (s *Server) setCachedEndpoint(key, contentType string, body []byte, ttl time.Duration) {
	s.cacheMu.Lock()
	s.endpoints[key] = endpointCacheEntry{body: append([]byte(nil), body...), contentType: contentType, expires: time.Now().Add(ttl)}
	s.cacheMu.Unlock()
}

func (s *Server) clearGeneratedCaches() {
	s.cacheMu.Lock()
	s.content = map[string]contentCacheEntry{}
	s.endpoints = map[string]endpointCacheEntry{}
	s.pages = map[string]pageCacheEntry{}
	s.cacheMu.Unlock()
	s.scheduleCloudflareSync("内容或站点配置已更新，Cloudflare 静态站将自动重新发布。")
}

func hexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (s *Server) routes(assetsFS fs.FS) {
	mux := http.NewServeMux()

	// 公开站点（语种前缀由 withLocale 中间件剥离后命中这些原始路由）
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /page/{pageNum}", s.home)
	mux.HandleFunc("GET /page/{pageNum}/{$}", s.home)
	mux.HandleFunc("GET /posts/{slug}", s.article)
	mux.HandleFunc("GET /posts/{slug}/{$}", s.article)
	mux.HandleFunc("GET /category", s.categoryRoot)
	mux.HandleFunc("GET /category/{$}", s.categoryRoot)
	mux.HandleFunc("GET /category/page/{pageNum}", s.categoryRoot)
	mux.HandleFunc("GET /category/page/{pageNum}/{$}", s.categoryRoot)
	mux.HandleFunc("GET /category/{slug}", s.category)
	mux.HandleFunc("GET /category/{slug}/{$}", s.category)
	mux.HandleFunc("GET /category/{slug}/page/{pageNum}", s.category)
	mux.HandleFunc("GET /category/{slug}/page/{pageNum}/{$}", s.category)
	mux.HandleFunc("GET /links", s.links)
	mux.HandleFunc("GET /links/{$}", s.links)
	mux.HandleFunc("GET /links/page/{pageNum}", s.links)
	mux.HandleFunc("GET /links/page/{pageNum}/{$}", s.links)
	mux.HandleFunc("GET /links/cat/{cat}", s.links)
	mux.HandleFunc("GET /links/cat/{cat}/{$}", s.links)
	mux.HandleFunc("GET /links/cat/{cat}/page/{pageNum}", s.links)
	mux.HandleFunc("GET /links/cat/{cat}/page/{pageNum}/{$}", s.links)
	mux.HandleFunc("GET /links/{slug}", s.link)
	mux.HandleFunc("GET /links/{slug}/{$}", s.link)
	mux.HandleFunc("GET /api-docs", s.apiDocs)
	mux.HandleFunc("GET /api-docs/{$}", s.apiDocs)
	mux.HandleFunc("GET /about", s.about)
	mux.HandleFunc("GET /about/{$}", s.about)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /search/{$}", s.search)
	mux.HandleFunc("GET /{slug}", s.page)
	// 「扩展」内容类型的公开路由（全局注册；未对本站启用时列表回退为按 slug 找页面、
	// 详情返回 404，保证未用该类型的站点零回归）。
	s.registerContentTypeRoutes(mux)
	// 数据库自定义类型的公开路由由 contentTypeRouter 包装器在 mux 之前分发
	// （避免通配路由与 /assets/ 等子树冲突）。

	// SEO 端点（动态生成）
	mux.HandleFunc("GET /sitemap.xml", s.sitemap)
	mux.HandleFunc("GET /rss.xml", s.rss)
	mux.HandleFunc("GET /robots.txt", s.robots)

	// 静态资源（embed）
	mux.Handle("GET /assets/", http.FileServer(http.FS(assetsFS)))
	// 浏览器会自动请求站点根的 /favicon.ico
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assetsFS, "assets/favicon.ico")
	})

	// 用户上传（运行时文件，存于磁盘）
	if s.uploadDir != "" {
		mux.HandleFunc("GET /uploads/", s.serveUpload)
	}
	mux.HandleFunc("POST /admin/upload", s.requireAuth(s.adminUpload))
	mux.HandleFunc("POST /admin/render", s.requireAuth(s.adminRender))

	apiCollection := func(collection string, h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("collection", collection)
			h(w, r)
		}
	}

	// 自动化 API（开放语种、站点文案、导航、分类、媒体上传，以及文章 / 页面 / 链接内容操作）。
	mux.HandleFunc("GET /api/admin/v1/openapi.json", s.apiOpenAPI)
	mux.HandleFunc("GET /api/admin/v1/languages", s.apiLanguages)
	mux.HandleFunc("POST /api/admin/v1/languages", s.apiCreateLanguage)
	mux.HandleFunc("PATCH /api/admin/v1/languages/{code}", s.apiUpdateLanguage)
	mux.HandleFunc("GET /api/admin/v1/languages/{code}/catalog", s.apiGetLanguageCatalog)
	mux.HandleFunc("PATCH /api/admin/v1/languages/{code}/catalog", s.apiUpdateLanguageCatalog)
	mux.HandleFunc("GET /api/admin/v1/types", s.apiContentTypes)
	mux.HandleFunc("GET /api/admin/v1/site-profile", s.apiGetSiteProfile)
	mux.HandleFunc("PATCH /api/admin/v1/site-profile", s.apiUpdateSiteProfile)
	mux.HandleFunc("GET /api/admin/v1/navigation", s.apiGetNavigation)
	mux.HandleFunc("PATCH /api/admin/v1/navigation", s.apiUpdateNavigation)
	mux.HandleFunc("POST /api/admin/v1/media", s.apiUploadMedia)
	mux.HandleFunc("GET /api/admin/v1/posts/categories", apiCollection("posts", s.apiListCategories))
	mux.HandleFunc("POST /api/admin/v1/posts/categories", apiCollection("posts", s.apiCreateCategory))
	mux.HandleFunc("GET /api/admin/v1/posts/categories/all-entry", apiCollection("posts", s.apiGetCategoryAllEntry))
	mux.HandleFunc("PATCH /api/admin/v1/posts/categories/all-entry", apiCollection("posts", s.apiUpdateCategoryAllEntry))
	mux.HandleFunc("PATCH /api/admin/v1/posts/categories/{id}", apiCollection("posts", s.apiUpdateCategory))
	mux.HandleFunc("GET /api/admin/v1/links/categories", apiCollection("links", s.apiListCategories))
	mux.HandleFunc("POST /api/admin/v1/links/categories", apiCollection("links", s.apiCreateCategory))
	mux.HandleFunc("GET /api/admin/v1/links/categories/all-entry", apiCollection("links", s.apiGetCategoryAllEntry))
	mux.HandleFunc("PATCH /api/admin/v1/links/categories/all-entry", apiCollection("links", s.apiUpdateCategoryAllEntry))
	mux.HandleFunc("PATCH /api/admin/v1/links/categories/{id}", apiCollection("links", s.apiUpdateCategory))
	mux.HandleFunc("GET /api/admin/v1/{collection}", s.apiListContent)
	mux.HandleFunc("POST /api/admin/v1/{collection}", s.apiCreateContent)
	mux.HandleFunc("GET /api/admin/v1/{collection}/{id}/preview", s.apiPreviewContent)
	mux.HandleFunc("POST /api/admin/v1/{collection}/{id}/preview-url", s.apiCreatePreviewURL)
	mux.HandleFunc("PATCH /api/admin/v1/posts/featured/{id}", s.apiUpdatePostFeatured)
	mux.HandleFunc("PATCH /api/admin/v1/links/featured/{id}", s.apiUpdateLinkFeatured)
	mux.HandleFunc("GET /api/admin/v1/{collection}/{id}", s.apiGetContent)
	mux.HandleFunc("PATCH /api/admin/v1/{collection}/{id}", s.apiUpdateContent)
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/openapi.json", s.apiPlatformOpenAPI)
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/languages", s.apiLanguages)
	mux.HandleFunc("POST /api/platform/v1/sites/{siteID}/languages", s.apiCreateLanguage)
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/languages/{code}", s.apiUpdateLanguage)
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/languages/{code}/catalog", s.apiGetLanguageCatalog)
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/languages/{code}/catalog", s.apiUpdateLanguageCatalog)
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/site-profile", s.apiGetSiteProfile)
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/site-profile", s.apiUpdateSiteProfile)
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/navigation", s.apiGetNavigation)
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/navigation", s.apiUpdateNavigation)
	mux.HandleFunc("POST /api/platform/v1/sites/{siteID}/media", s.apiUploadMedia)
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/posts/categories", apiCollection("posts", s.apiListCategories))
	mux.HandleFunc("POST /api/platform/v1/sites/{siteID}/posts/categories", apiCollection("posts", s.apiCreateCategory))
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/posts/categories/all-entry", apiCollection("posts", s.apiGetCategoryAllEntry))
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/posts/categories/all-entry", apiCollection("posts", s.apiUpdateCategoryAllEntry))
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/posts/categories/{id}", apiCollection("posts", s.apiUpdateCategory))
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/links/categories", apiCollection("links", s.apiListCategories))
	mux.HandleFunc("POST /api/platform/v1/sites/{siteID}/links/categories", apiCollection("links", s.apiCreateCategory))
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/links/categories/all-entry", apiCollection("links", s.apiGetCategoryAllEntry))
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/links/categories/all-entry", apiCollection("links", s.apiUpdateCategoryAllEntry))
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/links/categories/{id}", apiCollection("links", s.apiUpdateCategory))
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/{collection}", s.apiListContent)
	mux.HandleFunc("POST /api/platform/v1/sites/{siteID}/{collection}", s.apiCreateContent)
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/{collection}/{id}/preview", s.apiPreviewContent)
	mux.HandleFunc("POST /api/platform/v1/sites/{siteID}/{collection}/{id}/preview-url", s.apiCreatePreviewURL)
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/posts/featured/{id}", s.apiUpdatePostFeatured)
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/links/featured/{id}", s.apiUpdateLinkFeatured)
	mux.HandleFunc("GET /api/platform/v1/sites/{siteID}/{collection}/{id}", s.apiGetContent)
	mux.HandleFunc("PATCH /api/platform/v1/sites/{siteID}/{collection}/{id}", s.apiUpdateContent)

	// 临时前台预览：由自动化 API 生成短期签名 URL，渲染真实前台模板但不索引、不缓存。
	mux.HandleFunc("GET /preview/{collection}/{id}", s.frontendPreviewContent)

	// 后台
	mux.HandleFunc("GET /admin/login", s.adminLoginForm)
	mux.HandleFunc("POST /admin/login", s.adminLoginPost)
	mux.HandleFunc("GET /admin/language", s.adminLanguage)
	mux.HandleFunc("POST /admin/logout", s.adminLogout)
	mux.HandleFunc("POST /admin/dismiss-pw", s.requireAuth(s.adminDismissPw))
	mux.HandleFunc("GET /admin", s.requireAuth(s.adminDashboard))
	mux.HandleFunc("GET /admin/sites", s.requireAuth(s.adminSites))
	mux.HandleFunc("GET /admin/server-health", s.requireAuth(s.adminServerHealth))
	mux.HandleFunc("GET /admin/google/oauth/start", s.requireAuth(s.adminGoogleOAuthStart))
	mux.HandleFunc("GET /admin/google/oauth/callback", s.requireAuth(s.adminGoogleOAuthCallback))
	mux.HandleFunc("POST /admin/google/oauth/config", s.requireAuth(s.adminGoogleOAuthConfig))
	mux.HandleFunc("POST /admin/google/oauth/clear", s.requireAuth(s.adminGoogleOAuthClear))
	mux.HandleFunc("POST /admin/google/accounts/delete", s.requireAuth(s.adminGoogleAccountDelete))
	mux.HandleFunc("GET /admin/google/analytics/properties", s.requireAuth(s.adminGoogleAnalyticsProperties))
	mux.HandleFunc("GET /admin/google/search-console/sites", s.requireAuth(s.adminGoogleSearchConsoleSites))
	mux.HandleFunc("POST /admin/sites", s.requireAuth(s.adminCreateSite))
	mux.HandleFunc("POST /admin/sites/{id}/enter", s.requireAuth(s.adminEnterSite))
	mux.HandleFunc("GET /admin/sites/{id}/automation/skill.zip", s.requireAuth(s.adminDownloadPlatformAutomationSkill))
	mux.HandleFunc("GET /admin/sites/{id}/automation/starter.zip", s.requireAuth(s.adminDownloadPlatformAutomationStarter))
	mux.HandleFunc("POST /admin/sites/{id}/default", s.requireAuth(s.adminSetDefaultSite))
	mux.HandleFunc("POST /admin/sites/{id}/status", s.requireAuth(s.adminSetSiteStatus))
	mux.HandleFunc("POST /admin/sites/{id}/automation", s.requireAuth(s.adminSetSiteAutomation))
	mux.HandleFunc("POST /admin/sites/{id}/domains", s.requireAuth(s.adminSaveSiteDomains))
	mux.HandleFunc("POST /admin/sites/{id}/google", s.requireAuth(s.adminSaveSiteGoogleIntegration))
	mux.HandleFunc("POST /admin/sites/{id}/google/analytics/summary", s.requireAuth(s.adminGoogleAnalyticsSummary))
	mux.HandleFunc("POST /admin/sites/{id}/google/analytics/stream", s.requireAuth(s.adminCreateGoogleAnalyticsStream))
	mux.HandleFunc("POST /admin/sites/{id}/google/search-console/property", s.requireAuth(s.adminCreateGoogleSearchConsoleProperty))
	mux.HandleFunc("POST /admin/sites/{id}/google/search-console/summary", s.requireAuth(s.adminGoogleSearchConsoleSummary))
	mux.HandleFunc("POST /admin/sites/{id}/cloudflare/deploy", s.requireAuth(s.adminPlatformSiteCloudflareDeploy))
	mux.HandleFunc("GET /admin/sites/{id}/cloudflare/status", s.requireAuth(s.adminPlatformSiteCloudflareStatus))
	mux.HandleFunc("GET /admin/sites/{id}/wizard/proxy", s.requireAuth(s.adminWizardProxyDetect))
	mux.HandleFunc("POST /admin/sites/{id}/wizard/dns", s.requireAuth(s.adminWizardDNSDetect))
	mux.HandleFunc("POST /admin/sites/{id}/wizard/verify", s.requireAuth(s.adminWizardVerify))
	mux.HandleFunc("GET /admin/sites/{id}/wizard/status", s.requireAuth(s.adminWizardStatus))
	mux.HandleFunc("POST /admin/sites/{id}/archive", s.requireAuth(s.adminArchiveSite))
	mux.HandleFunc("GET /admin/sites/{id}/uploads/{name}", s.requireAuth(s.adminSiteUpload))
	mux.HandleFunc("GET /admin/platform/settings", s.requireAuth(s.adminPlatformSettings))
	mux.HandleFunc("GET /admin/backups", s.requireAuth(s.adminBackups))
	mux.HandleFunc("POST /admin/backups", s.requireAuth(s.adminCreateBackup))
	mux.HandleFunc("POST /admin/backups/config", s.requireAuth(s.adminSaveBackupConfig))
	mux.HandleFunc("GET /admin/backups/{name}/download", s.requireAuth(s.adminDownloadBackup))
	mux.HandleFunc("POST /admin/backups/{name}/sync", s.requireAuth(s.adminSyncBackup))
	mux.HandleFunc("POST /admin/backups/{name}/delete", s.requireAuth(s.adminDeleteBackup))
	mux.HandleFunc("GET /admin/archived-sites", s.requireAuth(s.adminArchivedSites))
	mux.HandleFunc("POST /admin/archived-sites/{id}/restore", s.requireAuth(s.adminRestoreArchivedSite))
	mux.HandleFunc("POST /admin/archived-sites/{id}/delete", s.requireAuth(s.adminDeleteArchivedSite))
	mux.HandleFunc("GET /admin/updates", s.requireAuth(s.adminUpdates))
	mux.HandleFunc("GET /admin/updates/status", s.requireAuth(s.adminUpgradeStatus))
	mux.HandleFunc("GET /admin/updates/check", s.requireAuth(s.adminUpdateCheck))
	mux.HandleFunc("POST /admin/updates/upgrade", s.requireAuth(s.adminStartUpgrade))
	mux.HandleFunc("GET /admin/admin-i18n", s.requireAuth(s.adminAdminI18N))
	mux.HandleFunc("POST /admin/admin-i18n", s.requireAuth(s.adminSaveAdminI18N))
	mux.HandleFunc("GET /admin/security", s.requireAuth(s.adminSecurity))
	mux.HandleFunc("POST /admin/security", s.requireAuth(s.adminSavePlatformPassword))
	mux.HandleFunc("GET /admin/posts", s.requireAuth(s.adminPosts))
	mux.HandleFunc("GET /admin/visual", s.requireAuth(s.adminVisual))
	mux.HandleFunc("POST /admin/visual/save", s.requireAuth(s.adminVisualSave))
	mux.HandleFunc("POST /admin/visual/undo", s.requireAuth(s.adminVisualUndo))
	mux.HandleFunc("POST /admin/visual/nav/reorder", s.requireAuth(s.adminVisualNavReorder))
	mux.HandleFunc("POST /admin/visual/categories/reorder", s.requireAuth(s.adminVisualCategoryReorder))
	mux.HandleFunc("GET /admin/settings", s.requireAuth(s.adminSettings))
	mux.HandleFunc("GET /admin/settings/{section}", s.requireAuth(s.adminSettingsSection))
	mux.HandleFunc("GET /admin/theme-preview/{theme}", s.requireAuth(s.adminThemePreview))
	mux.HandleFunc("GET /admin/theme-browse/{theme}", s.requireAuth(s.adminThemeBrowse))
	mux.HandleFunc("GET /admin/theme-browse/{theme}/{rest...}", s.requireAuth(s.adminThemeBrowse))
	mux.HandleFunc("GET /admin/settings/updates/status", s.requireAuth(s.adminUpgradeStatus))
	mux.HandleFunc("GET /admin/settings/updates/check", s.requireAuth(s.adminUpdateCheck))
	mux.HandleFunc("GET /admin/settings/cloudflare/status", s.requireAuth(s.adminCloudflareStatus))
	mux.HandleFunc("POST /admin/settings/site", s.requireAuth(s.adminSaveSite))
	mux.HandleFunc("POST /admin/settings/appearance", s.requireAuth(s.adminSaveAppearance))
	mux.HandleFunc("POST /admin/settings/comments", s.requireAuth(s.adminSaveComments))
	mux.HandleFunc("POST /admin/settings/updates/upgrade", s.requireAuth(s.adminStartUpgrade))
	mux.HandleFunc("POST /admin/settings/cloudflare", s.requireAuth(s.adminSaveCloudflare))
	mux.HandleFunc("POST /admin/settings/cloudflare/sync", s.requireAuth(s.adminSaveCloudflareSync))
	mux.HandleFunc("POST /admin/settings/cloudflare/deploy", s.requireAuth(s.adminStartCloudflareDeploy))
	mux.HandleFunc("POST /admin/settings/cloudflare/unpublish", s.requireAuth(s.adminStartCloudflareUnpublish))
	mux.HandleFunc("POST /admin/settings/cloudflare/purge", s.requireAuth(s.adminCloudflarePurge))
	mux.HandleFunc("POST /admin/settings/cloudflare/reset", s.requireAuth(s.adminCloudflareReset))
	mux.HandleFunc("POST /admin/settings/security", s.requireAuth(s.adminSavePassword))
	mux.HandleFunc("POST /admin/settings/admin-i18n", s.requireAuth(s.adminSaveAdminI18N))
	mux.HandleFunc("POST /admin/settings/demo/reload", s.requireAuth(s.adminReloadProductDemo))
	mux.HandleFunc("POST /admin/settings/demo/clear", s.requireAuth(s.adminClearDemoContent))
	mux.HandleFunc("POST /admin/settings/copy", s.requireAuth(s.adminSaveCopy))
	mux.HandleFunc("POST /admin/settings/menu", s.requireAuth(s.adminSaveMenu))
	mux.HandleFunc("POST /admin/settings/languages", s.requireAuth(s.adminSaveLanguages))
	mux.HandleFunc("POST /admin/settings/languages/catalog", s.requireAuth(s.adminSaveLocaleCatalog))
	mux.HandleFunc("POST /admin/settings/languages/preset", s.requireAuth(s.adminAddLocalePreset))
	mux.HandleFunc("POST /admin/settings/languages/preset/delete", s.requireAuth(s.adminDeleteLocalePreset))
	mux.HandleFunc("POST /admin/settings/categories/all", s.requireAuth(s.adminSaveCategoryAll))
	mux.HandleFunc("POST /admin/settings/categories", s.requireAuth(s.adminSaveCategory))
	mux.HandleFunc("POST /admin/settings/categories/delete", s.requireAuth(s.adminDeleteCategory))
	mux.HandleFunc("POST /admin/settings/categories/reorder", s.requireAuth(s.adminReorderCategories))
	mux.HandleFunc("POST /admin/settings/automation/keys", s.requireAuth(s.adminCreateAutomationKey))
	mux.HandleFunc("POST /admin/settings/automation/keys/update", s.requireAuth(s.adminUpdateAutomationKey))
	mux.HandleFunc("POST /admin/settings/automation/keys/regenerate", s.requireAuth(s.adminRegenerateAutomationKey))
	mux.HandleFunc("POST /admin/settings/automation/keys/revoke", s.requireAuth(s.adminRevokeAutomationKey))
	mux.HandleFunc("POST /admin/settings/automation/keys/delete", s.requireAuth(s.adminDeleteAutomationKey))
	mux.HandleFunc("GET /admin/automation", s.requireAuth(s.adminPlatformAutomation))
	mux.HandleFunc("GET /admin/automation/platform-skill.zip", s.requireAuth(s.adminDownloadPlatformSkill))
	mux.HandleFunc("POST /admin/automation/platform-skill.zip", s.requireAuth(s.adminDownloadPlatformSkill))
	mux.HandleFunc("POST /admin/sites/automation/keys", s.requireAuth(s.adminCreatePlatformKey))
	mux.HandleFunc("POST /admin/sites/automation/keys/update", s.requireAuth(s.adminUpdatePlatformKey))
	mux.HandleFunc("POST /admin/sites/automation/keys/regenerate", s.requireAuth(s.adminRegeneratePlatformKey))
	mux.HandleFunc("POST /admin/sites/automation/keys/revoke", s.requireAuth(s.adminRevokePlatformKey))
	mux.HandleFunc("POST /admin/sites/automation/keys/delete", s.requireAuth(s.adminDeletePlatformKey))
	mux.HandleFunc("GET /admin/settings/automation/skill.zip", s.requireAuth(s.adminDownloadAutomationSkill))
	mux.HandleFunc("POST /admin/settings/automation/skill.zip", s.requireAuth(s.adminDownloadAutomationSkill))
	mux.HandleFunc("GET /admin/settings/automation/starter.zip", s.requireAuth(s.adminDownloadAutomationStarter))
	mux.HandleFunc("POST /admin/settings/automation/starter.zip", s.requireAuth(s.adminDownloadAutomationStarter))

	// 页面（如关于）
	mux.HandleFunc("GET /admin/pages", s.requireAuth(s.adminPages))
	mux.HandleFunc("GET /admin/pages/new", s.requireAuth(s.adminPageNew))
	mux.HandleFunc("GET /admin/pages/{id}/edit", s.requireAuth(s.adminPageEdit))
	mux.HandleFunc("POST /admin/pages", s.requireAuth(s.adminPageCreate))
	mux.HandleFunc("POST /admin/pages/{id}", s.requireAuth(s.adminPageSave))
	mux.HandleFunc("POST /admin/pages/{id}/delete", s.requireAuth(s.adminPageDelete))
	mux.HandleFunc("POST /admin/pages/{id}/translate", s.requireAuth(s.adminTranslate))
	mux.HandleFunc("GET /admin/posts/new", s.requireAuth(s.adminNew))
	mux.HandleFunc("GET /admin/posts/{id}/preview", s.requireAuth(s.adminPostPreview))
	mux.HandleFunc("GET /admin/posts/{id}/edit", s.requireAuth(s.adminEdit))
	mux.HandleFunc("POST /admin/posts", s.requireAuth(s.adminCreate))
	mux.HandleFunc("POST /admin/posts/{id}", s.requireAuth(s.adminUpdate))
	mux.HandleFunc("POST /admin/posts/{id}/delete", s.requireAuth(s.adminDelete))
	mux.HandleFunc("POST /admin/posts/{id}/pin", s.requireAuth(s.adminPin))
	mux.HandleFunc("POST /admin/posts/{id}/status", s.requireAuth(s.adminPostStatus))
	mux.HandleFunc("POST /admin/posts/{id}/translate", s.requireAuth(s.adminTranslate))

	// 链接（type=link）
	mux.HandleFunc("GET /admin/links", s.requireAuth(s.adminLinks))
	mux.HandleFunc("GET /admin/links/new", s.requireAuth(s.adminLinkNew))
	mux.HandleFunc("GET /admin/links/{id}/preview", s.requireAuth(s.adminLinkPreview))
	mux.HandleFunc("GET /admin/links/{id}/edit", s.requireAuth(s.adminLinkEdit))
	mux.HandleFunc("POST /admin/links", s.requireAuth(s.adminLinkCreate))
	mux.HandleFunc("POST /admin/links/{id}", s.requireAuth(s.adminLinkUpdate))
	mux.HandleFunc("POST /admin/links/{id}/delete", s.requireAuth(s.adminLinkDelete))
	mux.HandleFunc("POST /admin/links/{id}/pin", s.requireAuth(s.adminLinkPin))
	mux.HandleFunc("POST /admin/links/{id}/status", s.requireAuth(s.adminLinkStatus))
	mux.HandleFunc("POST /admin/links/{id}/translate", s.requireAuth(s.adminTranslate))

	// 「扩展」内容类型后台
	mux.HandleFunc("GET /admin/extensions", s.requireAuth(s.adminExtHub))
	mux.HandleFunc("POST /admin/extensions/toggle", s.requireAuth(s.adminExtToggle))
	mux.HandleFunc("GET /admin/extensions/types/new", s.requireAuth(s.adminTypeNew))
	mux.HandleFunc("POST /admin/extensions/types", s.requireAuth(s.adminTypeSave))
	mux.HandleFunc("GET /admin/extensions/types/{key}/edit", s.requireAuth(s.adminTypeEdit))
	mux.HandleFunc("POST /admin/extensions/types/{key}", s.requireAuth(s.adminTypeSave))
	mux.HandleFunc("POST /admin/extensions/types/{key}/delete", s.requireAuth(s.adminTypeDelete))
	mux.HandleFunc("GET /admin/ext/{type}", s.requireAuth(s.adminExtList))
	mux.HandleFunc("GET /admin/ext/{type}/new", s.requireAuth(s.adminExtNew))
	mux.HandleFunc("POST /admin/ext/{type}", s.requireAuth(s.adminExtCreate))
	mux.HandleFunc("GET /admin/ext/{type}/archive", s.requireAuth(s.adminExtArchiveForm))
	mux.HandleFunc("POST /admin/ext/{type}/archive", s.requireAuth(s.adminExtArchiveSave))
	mux.HandleFunc("POST /admin/ext/{type}/reorder", s.requireAuth(s.adminExtReorder))
	mux.HandleFunc("GET /admin/ext/{type}/{id}/edit", s.requireAuth(s.adminExtEdit))
	mux.HandleFunc("GET /admin/ext/{type}/{id}/preview", s.requireAuth(s.adminExtPreview))
	mux.HandleFunc("POST /admin/ext/{type}/{id}", s.requireAuth(s.adminExtUpdate))
	mux.HandleFunc("POST /admin/ext/{type}/{id}/delete", s.requireAuth(s.adminExtDelete))
	mux.HandleFunc("POST /admin/ext/{type}/{id}/translate", s.requireAuth(s.adminExtTranslate))

	// 兜底 404
	mux.HandleFunc("GET /", s.notFound)

	s.mux = mux
}

// ---------- 公开处理器 ----------

func (s *Server) view(r *http.Request, nav string) *View {
	return s.viewForLang(r, langFrom(r), nav)
}

func (s *Server) viewForLang(r *http.Request, lang, nav string) *View {
	if !s.langEnabled(lang) {
		lang = s.defaultLang()
	}
	st := s.site(lang)
	st.BaseURL = s.publicBaseURL(r)
	s.applySiteGoogleIntegrations(r, &st)
	tr := s.i18n.Tr(lang, s.defaultLang())
	if prefix := previewRoutePrefixFrom(r.Context()); prefix != "" {
		tr = tr.WithPrefix(localizedPrefix(prefix, lang))
	}
	v := &View{
		Site: st, Nav: nav, Year: time.Now().Year(), Theme: st.Theme, Layout: layoutForTheme(st.Theme), ThemeStyle: s.themeOverride(),
		Tr: tr, Lang: lang, AssetVer: s.assetVer,
		SitemapURL:    previewRootPath(r, "/sitemap.xml"),
		RobotsURL:     previewRootPath(r, "/robots.txt"),
		CategoryAll:   s.archiveConfig(lang, "post"),
		LinksAll:      s.archiveConfig(lang, "link"),
		ExternalLinks: s.externalLinkPolicy(),
		ForceNoindex:  previewNoindexFrom(r.Context()),
	}
	// 主题试穿：预览请求树内所有页面换候选主题渲染（不改站点设置、不进公共页缓存）。
	if th := previewThemeFrom(r.Context()); th != "" {
		s.applyTheme(v, th)
	}
	if r.URL.Query().Get("visual_edit") == "1" {
		if _, ok := s.currentSession(r); ok {
			v.VisualEdit = true
		}
	}
	v.Langs = s.langSwitchForRequest(r, lang, nil, "/")
	v.Social = parseSocialLinks(s.store.Setting("social_links"))
	v.Menu = s.menuItems(r, lang, tr, nav)
	if nav == "home" {
		v.HomeSections, v.HomeHero = s.homeSectionConfig()
	}
	s.applyRootLangRedirect(v)
	return v
}

func (s *Server) applyRootLangRedirect(v *View) {
	locales := s.locales()
	if len(locales) <= 1 {
		return
	}
	codes := make([]string, 0, len(locales))
	for _, loc := range locales {
		code := strings.TrimSpace(loc.Code)
		if code != "" {
			codes = append(codes, code)
		}
	}
	if len(codes) <= 1 {
		return
	}
	localesJSON, err := json.Marshal(codes)
	if err != nil {
		return
	}
	defaultJSON, err := json.Marshal(s.defaultLang())
	if err != nil {
		return
	}
	v.RootLangRedirect = true
	v.RootLangRedirectLocales = template.JS(localesJSON)
	v.RootLangRedirectDefault = template.JS(defaultJSON)
}

func (s *Server) giscusForPost(lang string, p *store.Post) *GiscusView {
	if p == nil || p.Type != "post" || !p.CommentsEnabled || s.store.Setting("comments.provider") != "giscus" {
		return nil
	}
	g := &GiscusView{
		Repo:          strings.TrimSpace(s.store.Setting("comments.giscus.repo")),
		RepoID:        strings.TrimSpace(s.store.Setting("comments.giscus.repo_id")),
		Category:      strings.TrimSpace(s.store.Setting("comments.giscus.category")),
		CategoryID:    strings.TrimSpace(s.store.Setting("comments.giscus.category_id")),
		Mapping:       commentMapping(s.store.Setting("comments.giscus.mapping")),
		Strict:        boolAttr(s.store.Setting("comments.giscus.strict") != "0"),
		Reactions:     boolAttr(s.store.Setting("comments.giscus.reactions") != "0"),
		InputPosition: commentInputPosition(s.store.Setting("comments.giscus.input_position")),
		Theme:         commentTheme(s.store.Setting("comments.giscus.theme")),
		Lang:          giscusLang(s.i18n.Locale(lang)),
	}
	if g.Repo == "" || g.RepoID == "" || g.Category == "" || g.CategoryID == "" {
		return nil
	}
	return g
}

func boolAttr(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

func commentMapping(v string) string {
	switch strings.TrimSpace(v) {
	case "url", "title", "og:title":
		return strings.TrimSpace(v)
	default:
		return "pathname"
	}
}

func commentInputPosition(v string) string {
	if strings.TrimSpace(v) == "top" {
		return "top"
	}
	return "bottom"
}

func commentTheme(v string) string {
	switch strings.TrimSpace(v) {
	case "light", "dark", "dark_high_contrast", "dark_dimmed", "transparent_dark", "noborder_light", "noborder_dark", "cobalt", "purple_dark":
		return strings.TrimSpace(v)
	default:
		return "preferred_color_scheme"
	}
}

func giscusLang(loc i18n.Locale) string {
	supported := map[string]string{
		"en": "en", "zh-cn": "zh-CN", "zh-tw": "zh-TW", "ja": "ja", "ko": "ko",
		"fr": "fr", "de": "de", "es": "es", "it": "it", "pt": "pt", "ru": "ru",
	}
	code := normalizeLangToken(loc.Code)
	if v, ok := supported[code]; ok {
		return v
	}
	tag := normalizeLangToken(loc.Tag)
	if v, ok := supported[tag]; ok {
		return v
	}
	if strings.HasPrefix(tag, "zh-hant") {
		return "zh-TW"
	}
	if strings.HasPrefix(tag, "zh") {
		return "zh-CN"
	}
	if i := strings.IndexByte(tag, '-'); i > 0 {
		if v, ok := supported[tag[:i]]; ok {
			return v
		}
	}
	return "en"
}

func (s *Server) applyTheme(v *View, theme string) {
	if !validTheme(theme) {
		return
	}
	v.Theme = theme
	v.Layout = layoutForTheme(theme)
	v.Site.Theme = theme
	v.ThemeStyle = s.themeOverrideFor(theme)
}

// menuItems 构建前台页眉导航：未配置时回落默认菜单（首页/分类/关于，用 i18n 文案）。
func (s *Server) menuItems(r *http.Request, lang string, tr *i18n.Tr, nav string) []MenuItem {
	rows := parseMenuRows(s.store.Setting("nav_menu"))
	if len(rows) == 0 {
		categoryPath := s.archiveConfig(lang, "post").Path
		linksPath := s.archiveConfig(lang, "link").Path
		return []MenuItem{
			{Href: tr.U("/"), Label: tr.T("nav.home"), Active: nav == "home", Index: 0},
			{Href: tr.U(categoryPath), Label: tr.T("nav.category"), Active: nav == "category", Index: 1},
			{Href: tr.U(linksPath), Label: tr.T("nav.links"), Active: nav == "links", Index: 2},
			{Href: tr.U("/about"), Label: tr.T("nav.about"), Active: nav == "about", Index: 3},
		}
	}
	def := s.defaultLang()
	out := make([]MenuItem, 0, len(rows))
	currentPath := r.URL.Path
	if currentPath == "" {
		currentPath = "/"
	}
	currentFull := currentPath
	if r.URL.RawQuery != "" {
		currentFull += "?" + r.URL.RawQuery
	}
	exactAny := false
	groupCount := 0
	for _, m := range rows {
		if menuURLMatchesCurrent(m.URL, currentPath, currentFull) {
			exactAny = true
		}
		if k := navKeyOf(m.URL); k != "" && k == nav {
			groupCount++
		}
	}
	for i, m := range rows {
		label := strings.TrimSpace(m.Labels[lang])
		if label == "" {
			label = strings.TrimSpace(m.Labels[def])
		}
		if label == "" {
			label = m.URL
		}
		ext := isExternalURL(m.URL)
		href := m.URL
		if !ext {
			href = tr.U(m.URL)
		}
		k := navKeyOf(m.URL)
		active := false
		if exactAny {
			active = menuURLMatchesCurrent(m.URL, currentPath, currentFull)
		} else if groupCount == 1 {
			active = k != "" && k == nav
		}
		out = append(out, MenuItem{Href: href, Label: label, Active: active, External: ext, Index: i})
	}
	return out
}

func (s *Server) menuTargetOptions(admins ...*i18n.AdminTr) []MenuTargetOption {
	def := s.defaultLang()
	locales := s.locales()
	admin := firstAdmin(admins)
	t := func(key, fallback string) string { return adminUI(admin, key, fallback) }
	labelsFromKey := func(key string) map[string]string {
		labels := map[string]string{}
		for _, l := range locales {
			labels[l.Code] = s.i18n.Tr(l.Code, def).T(key)
		}
		return labels
	}
	archiveLabels := func(kind, fallback string) map[string]string {
		labels := map[string]string{}
		for _, l := range locales {
			if title := strings.TrimSpace(s.archiveConfig(l.Code, kind).Title); title != "" {
				labels[l.Code] = title
			} else {
				labels[l.Code] = s.i18n.Tr(l.Code, def).T(fallback)
			}
		}
		return labels
	}
	staticLabels := func(zh, en string) map[string]string {
		labels := map[string]string{}
		for _, l := range locales {
			if l.Code == "en" {
				labels[l.Code] = en
			} else {
				labels[l.Code] = zh
			}
		}
		return labels
	}
	categoryPath := s.archiveConfig(def, "post").Path
	linksPath := s.archiveConfig(def, "link").Path
	joinPath := func(base, slug string) string {
		base = "/" + strings.Trim(strings.TrimSpace(base), "/")
		slug = strings.Trim(strings.TrimSpace(slug), "/")
		if slug == "" {
			return base
		}
		if base == "/" {
			return "/" + slug
		}
		return strings.TrimRight(base, "/") + "/" + slug
	}
	categoryKey := func(c *store.Category) string {
		if c == nil {
			return ""
		}
		if c.TransGroup != "" {
			return c.TransGroup
		}
		return c.Lang + ":" + c.Slug
	}
	pageKey := func(p *store.Post) string {
		if p == nil {
			return ""
		}
		if p.TransGroup != "" {
			return p.TransGroup
		}
		return p.Lang + ":" + p.Slug
	}
	categoryGroups := func(kind string) map[string]map[string]*store.Category {
		groups := map[string]map[string]*store.Category{}
		for _, l := range locales {
			cats, _ := s.store.ListCategories(l.Code, kind)
			for _, c := range cats {
				key := categoryKey(c)
				if key == "" {
					continue
				}
				if groups[key] == nil {
					groups[key] = map[string]*store.Category{}
				}
				groups[key][c.Lang] = c
			}
		}
		return groups
	}
	pageGroups := func() map[string]map[string]*store.Post {
		groups := map[string]map[string]*store.Post{}
		for _, l := range locales {
			pages, _ := s.store.ListPages(l.Code)
			for _, p := range pages {
				key := pageKey(p)
				if key == "" {
					continue
				}
				if groups[key] == nil {
					groups[key] = map[string]*store.Post{}
				}
				groups[key][p.Lang] = p
			}
		}
		return groups
	}
	categoryLabels := func(group string, fallback string, groups map[string]map[string]*store.Category) map[string]string {
		labels := map[string]string{}
		for _, l := range locales {
			if byLang := groups[group]; byLang != nil {
				if c := byLang[l.Code]; c != nil && strings.TrimSpace(c.Name) != "" {
					labels[l.Code] = c.Name
					continue
				}
			}
			labels[l.Code] = fallback
		}
		return labels
	}
	pageLabels := func(group string, fallback string, groups map[string]map[string]*store.Post) map[string]string {
		labels := map[string]string{}
		for _, l := range locales {
			if byLang := groups[group]; byLang != nil {
				if p := byLang[l.Code]; p != nil && strings.TrimSpace(p.Title) != "" {
					labels[l.Code] = p.Title
					continue
				}
			}
			labels[l.Code] = fallback
		}
		return labels
	}
	var opts []MenuTargetOption
	seenURL := map[string]bool{}
	add := func(opt MenuTargetOption) {
		if opt.URL != "" {
			if seenURL[opt.URL] {
				return
			}
			seenURL[opt.URL] = true
		}
		opts = append(opts, opt)
	}
	add(MenuTargetOption{Value: "home", Label: t("admin.settings.menu.target.home", "首页"), URL: "/", Kind: "preset", Labels: labelsFromKey("nav.home")})
	add(MenuTargetOption{Value: "category", Label: t("admin.settings.menu.target.category", "文章分类页"), URL: categoryPath, Kind: "preset", Labels: archiveLabels("post", "nav.category")})
	add(MenuTargetOption{Value: "links", Label: t("admin.settings.menu.target.links", "链接页"), URL: linksPath, Kind: "preset", Labels: archiveLabels("link", "nav.links")})
	add(MenuTargetOption{Value: "about", Label: t("admin.settings.menu.target.about", "关于页"), URL: "/about", Kind: "preset", Labels: labelsFromKey("nav.about")})
	add(MenuTargetOption{Value: "start", Label: t("admin.settings.menu.target.start", "开始使用页"), URL: "/start", Kind: "preset", Labels: staticLabels("开始使用", "Get Started")})
	add(MenuTargetOption{Value: "search", Label: t("admin.settings.menu.target.search", "搜索页"), URL: "/search", Kind: "preset", Labels: labelsFromKey("nav.search")})

	// 「扩展」内容类型的归档页（仅本站已启用的类型）
	for _, ct := range s.activeExtContentTypes() {
		if ct.URLPrefix == "" {
			continue
		}
		labels := map[string]string{}
		for k, v := range ct.Names {
			labels[k] = v
		}
		add(MenuTargetOption{
			Value:  "ext:" + ct.Key,
			Label:  ct.Name(def),
			URL:    "/" + ct.URLPrefix,
			Kind:   "preset",
			Labels: labels,
		})
	}

	postGroups := categoryGroups("post")
	if cats, _ := s.store.ListCategories(def, "post"); cats != nil {
		prefix := t("admin.settings.menu.target.post_category", "文章分类")
		for _, c := range cats {
			slug := strings.TrimSpace(c.Slug)
			if slug == "" {
				continue
			}
			group := categoryKey(c)
			add(MenuTargetOption{
				Value:  "post-category:" + group,
				Label:  prefix + "：" + c.Name,
				URL:    joinPath(categoryPath, slug),
				Kind:   "preset",
				Labels: categoryLabels(group, c.Name, postGroups),
			})
		}
	}
	linkGroups := categoryGroups("link")
	if cats, _ := s.store.ListCategories(def, "link"); cats != nil {
		prefix := t("admin.settings.menu.target.link_category", "链接分类")
		for _, c := range cats {
			slug := strings.TrimSpace(c.Slug)
			if slug == "" {
				continue
			}
			group := categoryKey(c)
			add(MenuTargetOption{
				Value:  "link-category:" + group,
				Label:  prefix + "：" + c.Name,
				URL:    joinPath(linksPath, "cat/"+slug),
				Kind:   "preset",
				Labels: categoryLabels(group, c.Name, linkGroups),
			})
		}
	}
	pageGroupsByKey := pageGroups()
	if pages, _ := s.store.ListPages(def); pages != nil {
		prefix := t("admin.settings.menu.target.page", "页面")
		for _, p := range pages {
			slug := strings.Trim(strings.TrimSpace(p.Slug), "/")
			if slug == "" || slug == "about" || slug == "start" {
				continue
			}
			group := pageKey(p)
			add(MenuTargetOption{
				Value:  "page:" + group,
				Label:  prefix + "：" + p.Title,
				URL:    "/" + slug,
				Kind:   "preset",
				Labels: pageLabels(group, p.Title, pageGroupsByKey),
			})
		}
	}
	add(MenuTargetOption{Value: "__custom__", Label: t("admin.settings.menu.target.custom", "自定义站内路径"), Kind: "custom", Labels: map[string]string{}})
	add(MenuTargetOption{Value: "__external__", Label: t("admin.settings.menu.target.external", "外部链接"), Kind: "external", Labels: map[string]string{}})
	return opts
}

// menuEditRows 为后台导航编辑提供回填行：未配置时给出默认菜单可编辑副本（各语种填 i18n 文案）。
func (s *Server) menuEditRows(admins ...*i18n.AdminTr) []MenuRow {
	targets := s.menuTargetOptions(admins...)
	if rows := parseMenuRows(s.store.Setting("nav_menu")); len(rows) > 0 {
		return decorateMenuRows(rows, targets)
	}
	byValue := map[string]MenuTargetOption{}
	for _, opt := range targets {
		byValue[opt.Value] = opt
	}
	mk := func(value string) MenuRow {
		opt := byValue[value]
		return MenuRow{URL: opt.URL, Labels: opt.Labels}
	}
	return decorateMenuRows([]MenuRow{mk("home"), mk("category"), mk("links"), mk("about")}, targets)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.renderHome(w, r)
}

// adminThemeBrowse 主题试穿：用站点真实数据、按候选主题渲染整个前台，且可正常翻页——
// 站内链接经 previewRoutePrefix 改写回到 /admin/theme-browse/{theme} 前缀下，noindex + 不进公共缓存。
func (s *Server) adminThemeBrowse(w http.ResponseWriter, r *http.Request) {
	theme := r.PathValue("theme")
	if !validTheme(theme) {
		s.notFound(w, r)
		return
	}
	rest := "/" + strings.TrimPrefix(r.PathValue("rest"), "/")
	if rest == "/" {
		rest = "/" + s.defaultLang() + "/"
	}
	// 防递归/越界：试穿只服务前台路径。
	if strings.HasPrefix(rest, "/admin") || strings.HasPrefix(rest, "/api") {
		s.notFound(w, r)
		return
	}
	nextURL := *r.URL
	nextURL.Path = rest
	prefix := "/admin/theme-browse/" + theme
	ctx := withPreviewTheme(withPreviewRoutePrefix(withPreviewNoindex(r.Context()), prefix), theme)
	req := r.Clone(ctx)
	req.URL = &nextURL
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "no-store")
	s.siteHandler().ServeHTTP(w, req)
}

func (s *Server) adminThemePreview(w http.ResponseWriter, r *http.Request) {
	theme := r.PathValue("theme")
	if !validTheme(theme) {
		s.notFound(w, r)
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "private, max-age=60")
	lang := langFrom(r)
	v := s.view(r, "home")
	s.applyTheme(v, theme)
	v.SEO = v.Site.Home()
	v.Site.HeroVisual = ""
	v.Site.HeroImage = ""
	v.Site.HeroSVG = ""
	v.Site.InjectHead = ""
	v.Site.InjectBody = ""

	posts, _ := s.store.ListPublished(lang, 0, 4)
	if len(posts) == 0 {
		posts = []*store.Post{
			{Title: v.Site.Name + " 更新日志", Excerpt: v.Site.Description, PublishedAt: time.Now()},
			{Title: "快速开始", Excerpt: "安装、配置与内容发布流程。", PublishedAt: time.Now()},
			{Title: "设计与主题", Excerpt: "为不同类型的网站选择合适的前台结构。", PublishedAt: time.Now()},
		}
	}
	v.Featured = posts[0]
	if len(posts) > 1 {
		v.Posts = posts[1:]
	}
	v.Categories, _ = s.store.ListCategories(lang, "post")
	if len(v.Categories) == 0 {
		v.Categories = []*store.Category{
			{Slug: "guides", Name: "指南", Count: 3},
			{Slug: "reference", Name: "参考", Count: 2},
			{Slug: "updates", Name: "更新", Count: 1},
		}
	}
	v.KnowledgeGroups = s.knowledgeGroups(lang, v.CategoryAll, v.Categories, posts, len(posts), s.intSetting(homePostsPerPageKey, defaultHomePostsPerPage, minHomePostsPerPage, maxHomePostsPerPage))
	v.FeatLinks = s.knowledgeHeroLinks(lang)
	if len(v.FeatLinks) == 0 {
		v.FeatLinks = []*store.Post{
			{Title: "文档", Excerpt: "查看部署、配置与 API 用法。"},
			{Title: "发布", Excerpt: "版本更新与一键升级流程。"},
			{Title: "生态", Excerpt: "自动化接口与内容助手接入。"},
		}
	}
	s.rnd.ThemePreview(w, http.StatusOK, v)
}

func (s *Server) renderHome(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	page := pageParam(r)
	postsPerPage := s.intSetting(homePostsPerPageKey, defaultHomePostsPerPage, minHomePostsPerPage, maxHomePostsPerPage)
	linksLimit := s.intSetting(homeLinksLimitKey, defaultHomeLinksLimit, minHomeLinksLimit, maxHomeLinksLimit)
	total, _ := s.store.CountPublished(lang)
	totalPages := ceilDiv(total, postsPerPage)
	posts, err := s.store.ListPublished(lang, (page-1)*postsPerPage, postsPerPage)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v := s.view(r, "home")
	v.SEO = v.Site.Home()
	v.Categories, _ = s.store.ListCategories(lang, "post")
	v.KnowledgeGroups = s.knowledgeGroups(lang, v.CategoryAll, v.Categories, posts, total, postsPerPage)
	if page == 1 {
		// 精选优先取置顶文章（可多篇），否则取最新一篇
		if fps, _ := s.store.FeaturedPosts(lang, postsPerPage); len(fps) > 0 {
			v.Featured = fps[0]
			v.FeaturedMore = fps[1:]
			fset := map[int64]bool{}
			for _, f := range fps {
				fset[f.ID] = true
			}
			for _, p := range posts {
				if !fset[p.ID] {
					v.Posts = append(v.Posts, p)
				}
			}
		} else if len(posts) > 0 {
			v.Featured = posts[0]
			v.Posts = posts[1:]
		}
		// 链接模块：仅当存在「置顶」链接时才在首页展示
		if linksLimit > 0 {
			v.FeatLinks, _ = s.store.FeaturedLinks(lang, linksLimit)
		}
	} else {
		v.Posts = posts
	}
	if v.Theme == "knowledge" {
		v.FeatLinks = s.knowledgeHeroLinks(lang)
	}
	// 首页在每个语种都存在 → 全语种 hreflang
	ph := map[string]string{}
	for _, l := range s.locales() {
		ph[l.Code] = "/"
	}
	v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	setPagination(v, page, totalPages, "/")
	s.rnd.Public(w, "home", http.StatusOK, v)
}

func (s *Server) knowledgeHeroLinks(lang string) []*store.Post {
	const limit = 12
	out := make([]*store.Post, 0, limit)
	seen := map[int64]bool{}
	add := func(items []*store.Post) {
		for _, item := range items {
			if item == nil || seen[item.ID] || len(out) >= limit {
				continue
			}
			seen[item.ID] = true
			out = append(out, item)
		}
	}
	if featured, _ := s.store.FeaturedLinks(lang, limit); len(featured) > 0 {
		add(featured)
	}
	if len(out) < limit {
		if latest, _ := s.store.ListLinks(lang, 0, 0, limit*2); len(latest) > 0 {
			add(latest)
		}
	}
	return out
}

func (s *Server) knowledgeGroups(lang string, all ArchiveConfig, cats []*store.Category, allPosts []*store.Post, total, limit int) []*KnowledgeGroup {
	if limit < 1 {
		limit = defaultHomePostsPerPage
	}
	groups := []*KnowledgeGroup{{
		Key:         "all",
		Title:       all.Title,
		Description: all.Description,
		Path:        all.Path,
		Count:       total,
		Posts:       allPosts,
	}}
	if len(groups[0].Posts) > limit {
		groups[0].Posts = groups[0].Posts[:limit]
	}
	for _, c := range cats {
		if c == nil {
			continue
		}
		posts, _ := s.store.ListByCategory(c.ID, 0, limit)
		groups = append(groups, &KnowledgeGroup{
			Key:         fmt.Sprintf("cat-%d", c.ID),
			Title:       c.Name,
			Description: c.Description,
			Path:        "/category/" + c.Slug,
			Count:       c.Count,
			Posts:       posts,
		})
	}
	return groups
}

func (s *Server) article(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	p, err := s.store.GetPostBySlug(lang, r.PathValue("slug"), false)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil {
		s.notFound(w, r)
		return
	}
	s.fillDefaultAuthor(p)
	v := s.view(r, "")
	v.SEO = v.Site.Article(p)
	v.Post = p
	v.ContentHTML, v.TOC = s.renderedContent(p)
	v.Prev, _ = s.store.PrevPost(p)
	v.Next, _ = s.store.NextPost(p)
	v.Related, _ = s.store.Related(p, 3)
	v.Giscus = s.giscusForPost(lang, p)
	ph := map[string]string{p.Lang: publicContentPath(p.Type, p.Slug)}
	if trs, _ := s.store.TranslationsPublished(p.TransGroup); trs != nil {
		for _, t := range trs {
			if t.Type == "post" {
				ph[t.Lang] = publicContentPath(t.Type, t.Slug)
			}
		}
	}
	v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	s.rnd.Public(w, "article", http.StatusOK, v)
}

func (s *Server) category(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	all := s.archiveConfig(lang, "post")
	if all.Path != "/category" && r.PathValue("slug") == all.Slug {
		s.categoryAll(w, r, all)
		return
	}
	c, err := s.store.GetCategoryBySlug(lang, r.PathValue("slug"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if c == nil {
		s.notFound(w, r)
		return
	}
	const size = 8
	page := pageParam(r)
	total, _ := s.store.CountByCategory(c.ID)
	posts, _ := s.store.ListByCategory(c.ID, (page-1)*size, size)
	cats, _ := s.store.ListCategories(lang, "post")
	v := s.view(r, "category")
	v.SEO = v.Site.Category(c)
	v.Category = c
	v.Categories = cats
	v.Posts = posts
	ph := map[string]string{c.Lang: "/category/" + c.Slug}
	if trs, _ := s.store.CategoryTranslations(c.TransGroup); trs != nil {
		for _, t := range trs {
			ph[t.Lang] = "/category/" + t.Slug
		}
	}
	v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	setPagination(v, page, ceilDiv(total, size), "/category/"+c.Slug)
	s.rnd.Public(w, "category", http.StatusOK, v)
}

func (s *Server) categoryRoot(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	s.categoryAll(w, r, s.archiveConfig(lang, "post"))
}

func (s *Server) categoryAll(w http.ResponseWriter, r *http.Request, all ArchiveConfig) {
	const size = 8
	lang := langFrom(r)
	page := pageParam(r)
	total, _ := s.store.CountPublished(lang)
	posts, err := s.store.ListPublished(lang, (page-1)*size, size)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cats, _ := s.store.ListCategories(lang, "post")
	c := &store.Category{Slug: all.Slug, Name: all.Title, Description: all.Description, Lang: lang, Kind: "post"}
	v := s.view(r, "category")
	v.SEO = v.Site.CategoryArchive(c, all.Path)
	v.Category = c
	v.Categories = cats
	v.Posts = posts
	ph := map[string]string{}
	for _, l := range s.locales() {
		ph[l.Code] = s.archiveConfig(l.Code, "post").Path
	}
	v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	setPagination(v, page, ceilDiv(total, size), all.Path)
	s.rnd.Public(w, "category", http.StatusOK, v)
}

func (s *Server) links(w http.ResponseWriter, r *http.Request) {
	const size = 12
	lang := langFrom(r)
	page := pageParam(r)
	// 分类筛选支持静态友好的 /links/cat/slug，也兼容旧的 ?cat=slug。
	var cat *store.Category
	var catID int64
	if cs := trim(r.URL.Query().Get("cat")); cs != "" {
		if c, _ := s.store.GetCategoryBySlug(lang, cs); c != nil && c.Kind == "link" {
			cat, catID = c, c.ID
		}
	} else if cs := trim(r.PathValue("cat")); cs != "" {
		if c, _ := s.store.GetCategoryBySlug(lang, cs); c != nil && c.Kind == "link" {
			cat, catID = c, c.ID
		}
	}
	total, _ := s.store.CountLinks(lang, catID)
	items, err := s.store.ListLinks(lang, catID, (page-1)*size, size)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cats, _ := s.store.ListCategories(lang, "link")
	v := s.view(r, "links")
	v.SEO = v.Site.Links(cat)
	v.Posts = items
	v.Categories = cats
	v.Category = cat
	basePath := v.LinksAll.Path
	if cat != nil {
		basePath = "/links/cat/" + cat.Slug
	}
	ph := map[string]string{}
	if cat != nil {
		ph[cat.Lang] = "/links/cat/" + cat.Slug
		if trs, _ := s.store.CategoryTranslations(cat.TransGroup); trs != nil {
			for _, t := range trs {
				if t.Kind == "link" {
					ph[t.Lang] = "/links/cat/" + t.Slug
				}
			}
		}
	} else {
		for _, l := range s.locales() {
			ph[l.Code] = s.archiveConfig(l.Code, "link").Path
		}
	}
	v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	setPagination(v, page, ceilDiv(total, size), basePath)
	s.rnd.Public(w, "links", http.StatusOK, v)
}

func (s *Server) link(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	all := s.archiveConfig(lang, "link")
	if all.Path != "/links" && r.PathValue("slug") == all.Slug {
		s.links(w, r)
		return
	}
	p, err := s.store.GetLinkBySlug(lang, r.PathValue("slug"), false)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil {
		s.notFound(w, r)
		return
	}
	s.fillDefaultAuthor(p)
	v := s.view(r, "links")
	v.SEO = v.Site.Link(p)
	v.Post = p
	v.ContentHTML, v.TOC = s.renderedContent(p)
	v.Related, _ = s.store.RelatedLinks(p, 6)
	ph := map[string]string{p.Lang: publicContentPath(p.Type, p.Slug)}
	if trs, _ := s.store.TranslationsPublished(p.TransGroup); trs != nil {
		for _, t := range trs {
			if t.Type == "link" {
				ph[t.Lang] = publicContentPath(t.Type, t.Slug)
			}
		}
	}
	v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	s.rnd.Public(w, "link", http.StatusOK, v)
}

func (s *Server) apiDocs(w http.ResponseWriter, r *http.Request) {
	v := s.view(r, "api_docs")
	v.SEO = seo.Meta{
		Title:       "自动化接口开放文档 — " + v.Site.Name,
		Description: "GCMS 自动化接口的开放文档，包含语种、分类、媒体上传、文章、链接、页面的接口地址、权限说明、参数说明与请求示例。",
		Canonical:   v.Site.Abs("/api-docs"),
		Robots:      "noindex, follow",
		OGType:      "article",
		Author:      v.Site.Author,
	}
	s.rnd.Public(w, "api_docs", http.StatusOK, v)
}

func (s *Server) about(w http.ResponseWriter, r *http.Request) {
	s.renderPageBySlug(w, r, "about")
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	slug := trim(r.PathValue("slug"))
	if slug == "" {
		s.notFound(w, r)
		return
	}
	s.renderPageBySlug(w, r, slug)
}

func (s *Server) renderPageBySlug(w http.ResponseWriter, r *http.Request, slug string) {
	lang := langFrom(r)
	p, err := s.store.GetPage(lang, slug)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil {
		s.notFound(w, r)
		return
	}
	v := s.view(r, slug)
	v.SEO = v.Site.Page(p)
	v.Page = p
	v.ContentHTML, _ = s.renderedContent(p)
	ph := map[string]string{p.Lang: publicContentPath(p.Type, p.Slug)}
	if trs, _ := s.store.TranslationsPublished(p.TransGroup); trs != nil {
		for _, t := range trs {
			if t.Type == "page" {
				ph[t.Lang] = publicContentPath(t.Type, t.Slug)
			}
		}
	}
	v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	s.rnd.Public(w, "page", http.StatusOK, v)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	q := trim(r.URL.Query().Get("q"))
	v := s.view(r, "search")
	v.SEO = v.Site.Search(q)
	v.SEO.Title = v.Tr.T("search.title") + " — " + v.Site.Name
	v.Query = q
	if q != "" {
		posts, _ := s.store.SearchInTypes(lang, q, s.searchableTypes(), 50)
		v.Posts = posts
		v.Results = len(posts)
	}
	// 切换器保留查询词
	sp := "/search"
	if q != "" {
		sp += "?q=" + url.QueryEscape(q)
	}
	v.Langs = s.langSwitchForRequest(r, lang, nil, sp)
	s.rnd.Public(w, "search", http.StatusOK, v)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	v := s.view(r, "")
	v.SEO = v.Site.NotFound()
	v.SEO.Title = v.Tr.T("nf.title") + " — " + v.Site.Name
	s.rnd.Public(w, "404", http.StatusNotFound, v)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	http.Error(w, "内部错误", http.StatusInternalServerError)
}

// ---------- 动态 SEO 端点 ----------

func xmlEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// sitemap 生成多语种站点地图：同一逻辑页面的各语种 URL 互相用 xhtml:link 标注 hreflang。
func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	const contentType = "application/xml; charset=utf-8"
	baseURL := s.publicBaseURL(r)
	cacheKey := "sitemap:" + baseURL
	if body, ct, ok := s.cachedEndpoint(cacheKey); ok {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", generatedEndpointCacheControl)
		_, _ = w.Write(body)
		return
	}
	abs := func(path string) string { return absWithBase(baseURL, path) }

	locales := s.locales()
	def := s.defaultLang()
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">` + "\n")

	type sitemapURL struct {
		Path    string
		LastMod time.Time
	}
	writeGroup := func(byLang map[string]sitemapURL, freq, prio string) {
		for _, l := range locales {
			item, ok := byLang[l.Code]
			if !ok || item.Path == "" {
				continue
			}
			b.WriteString("  <url>\n")
			b.WriteString("    <loc>" + xmlEsc(abs("/"+l.Code+item.Path)) + "</loc>\n")
			if lm := sitemapLastMod(item.LastMod); lm != "" {
				b.WriteString("    <lastmod>" + lm + "</lastmod>\n")
			}
			if freq != "" {
				b.WriteString("    <changefreq>" + freq + "</changefreq>\n")
			}
			if prio != "" {
				b.WriteString("    <priority>" + prio + "</priority>\n")
			}
			for _, a := range locales {
				if alt, ok := byLang[a.Code]; ok && alt.Path != "" {
					b.WriteString(`    <xhtml:link rel="alternate" hreflang="` + a.Tag + `" href="` + xmlEsc(abs("/"+a.Code+alt.Path)) + `"/>` + "\n")
				}
			}
			if dp, ok := byLang[def]; ok && dp.Path != "" {
				b.WriteString(`    <xhtml:link rel="alternate" hreflang="x-default" href="` + xmlEsc(abs("/"+def+dp.Path)) + `"/>` + "\n")
			}
			b.WriteString("  </url>\n")
		}
	}

	// 首页（全语种）
	home := map[string]sitemapURL{}
	for _, l := range locales {
		home[l.Code] = sitemapURL{Path: "/"}
	}
	writeGroup(home, "daily", "1.0")

	// 链接列表页（全语种）
	linksList := map[string]sitemapURL{}
	for _, l := range locales {
		linksList[l.Code] = sitemapURL{Path: "/links"}
	}
	writeGroup(linksList, "weekly", "0.6")

	categoryAll := map[string]sitemapURL{}
	for _, l := range locales {
		categoryAll[l.Code] = sitemapURL{Path: s.archiveConfig(l.Code, "post").Path}
	}
	writeGroup(categoryAll, "weekly", "0.7")

	groupBy := func(items func(add func(group, lang, path string, lastMod time.Time))) []map[string]sitemapURL {
		gm := map[string]map[string]sitemapURL{}
		var order []string
		items(func(group, lang, path string, lastMod time.Time) {
			if gm[group] == nil {
				gm[group] = map[string]sitemapURL{}
				order = append(order, group)
			}
			gm[group][lang] = sitemapURL{Path: path, LastMod: lastMod}
		})
		out := make([]map[string]sitemapURL, 0, len(order))
		for _, g := range order {
			out = append(out, gm[g])
		}
		return out
	}

	if cats, err := s.store.AllCategories("post"); err == nil {
		for _, g := range groupBy(func(add func(group, lang, path string, lastMod time.Time)) {
			for _, c := range cats {
				add(c.TransGroup, c.Lang, "/category/"+c.Slug, time.Time{})
			}
		}) {
			writeGroup(g, "weekly", "0.7")
		}
	}
	if posts, err := s.store.AllPublishedAllLangs(); err == nil {
		for _, g := range groupBy(func(add func(group, lang, path string, lastMod time.Time)) {
			for _, p := range posts {
				add(p.TransGroup, p.Lang, publicContentPath(p.Type, p.Slug), contentLastMod(p))
			}
		}) {
			writeGroup(g, "monthly", "0.8")
		}
	}
	if pages, err := s.store.AllPagesAllLangs(); err == nil {
		for _, g := range groupBy(func(add func(group, lang, path string, lastMod time.Time)) {
			for _, p := range pages {
				add(p.TransGroup, p.Lang, publicContentPath(p.Type, p.Slug), contentLastMod(p))
			}
		}) {
			writeGroup(g, "monthly", "0.6")
		}
	}
	if links, err := s.store.AllLinksAllLangs(); err == nil {
		for _, g := range groupBy(func(add func(group, lang, path string, lastMod time.Time)) {
			for _, p := range links {
				add(p.TransGroup, p.Lang, publicContentPath(p.Type, p.Slug), contentLastMod(p))
			}
		}) {
			writeGroup(g, "monthly", "0.7")
		}
	}

	b.WriteString("</urlset>\n")
	body := []byte(b.String())
	s.setCachedEndpoint(cacheKey, contentType, body, generatedEndpointCacheTTL)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", generatedEndpointCacheControl)
	_, _ = w.Write(body)
}

func contentLastMod(p *store.Post) time.Time {
	if p == nil {
		return time.Time{}
	}
	if !p.UpdatedAt.IsZero() {
		return p.UpdatedAt
	}
	if !p.PublishedAt.IsZero() {
		return p.PublishedAt
	}
	return p.CreatedAt
}

func sitemapLastMod(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	Category    string `xml:"category,omitempty"`
	PubDate     string `xml:"pubDate"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Language    string    `xml:"language"`
	Items       []rssItem `xml:"item"`
}

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

func (s *Server) rss(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	cacheKey := "rss:" + lang
	const contentType = "application/rss+xml; charset=utf-8"
	if body, ct, ok := s.cachedEndpoint(cacheKey); ok {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", generatedEndpointCacheControl)
		_, _ = w.Write(body)
		return
	}

	site := s.site(lang)
	feed := rssFeed{Version: "2.0", Channel: rssChannel{
		Title:       site.Name,
		Link:        site.Abs("/"),
		Description: site.Description,
		Language:    site.LangTag,
	}}
	if posts, err := s.store.RecentPublished(lang, 20); err == nil {
		for _, p := range posts {
			cat := ""
			if p.Category != nil {
				cat = p.Category.Name
			}
			feed.Channel.Items = append(feed.Channel.Items, rssItem{
				Title:       p.Title,
				Link:        site.Abs(publicContentPath(p.Type, p.Slug)),
				GUID:        site.Abs(publicContentPath(p.Type, p.Slug)),
				Description: p.Excerpt,
				Category:    cat,
				PubDate:     p.PublishedAt.Format(time.RFC1123Z),
			})
		}
	}
	var b bytes.Buffer
	_, _ = b.WriteString(xml.Header)
	enc := xml.NewEncoder(&b)
	enc.Indent("", "  ")
	_ = enc.Encode(feed)
	body := b.Bytes()
	s.setCachedEndpoint(cacheKey, contentType, body, generatedEndpointCacheTTL)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", generatedEndpointCacheControl)
	_, _ = w.Write(body)
}

func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\nAllow: /\nDisallow: /admin/\nDisallow: /admin\n")
	for _, l := range s.locales() {
		b.WriteString("Disallow: /" + l.Code + "/search\n")
	}
	b.WriteString("\nSitemap: " + s.absForRequest(r, "/sitemap.xml") + "\n")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// ---------- 小工具 ----------

func pageParam(r *http.Request) int {
	raw := r.URL.Query().Get("page")
	if raw == "" {
		raw = r.PathValue("pageNum")
	}
	n, _ := strconv.Atoi(raw)
	if n < 1 {
		n = 1
	}
	return n
}

func boundedInt(raw string, def, min, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		n = def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func (s *Server) intSetting(key string, def, min, max int) int {
	return boundedInt(s.store.Setting(key), def, min, max)
}

func ceilDiv(a, b int) int {
	if b == 0 {
		return 0
	}
	return (a + b - 1) / b
}

func setPagination(v *View, page, totalPages int, base string) {
	v.PageNum = page
	v.TotalPages = totalPages
	v.BasePath = base
	v.HasPrev = page > 1
	v.HasNext = page < totalPages
	v.PrevPage = page - 1
	v.NextPage = page + 1
}

func trim(s string) string { return strings.TrimSpace(s) }
