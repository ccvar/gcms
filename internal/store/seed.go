package store

import (
	"database/sql"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// md 把若干行拼成一段 Markdown（用双引号字符串便于内嵌反引号行内代码）。
func md(lines ...string) string { return strings.Join(lines, "\n") }

// bcryptHash 生成 bcrypt 哈希（供种子写入管理员密码）。
func bcryptHash(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

type seedCat struct {
	Slug, Name, Description, Lang, Group, Kind string
}

type seedPost struct {
	Type, Slug, Title, Excerpt, Content, MetaDesc, Keywords, Author, Cat, Date, Lang, Group, LinkURL, Cover string
	Featured                                                                                                bool
}

// seedIfEmpty 在空库时写入首装样板：分类、文章、页面、站点设置与管理员账号。
// 默认样板是一套 gcms 产品官网内容；如需旧版博客演示内容，可用 CMS_SEED=classic。
func (s *Store) seedIfEmpty() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var seedMode string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key='demo.seed'`).Scan(&seedMode)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if strings.EqualFold(seedMode, "empty") {
		s.Seeded = false
		return nil
	}

	if !strings.EqualFold(os.Getenv("CMS_SEED"), "classic") {
		return s.seedShowcase()
	}

	// 旧版内容博客演示种子（开发/回归用）。
	_ = s.SetSetting("site.name", "CCVAR 简记")
	_ = s.SetSetting("site.tagline", "记录技术、工具与思考")
	_ = s.SetSetting("site.description", "用 Go 与 SQLite 构建的轻量内容站，关注后端工程、极简设计与搜索引擎优化。")
	_ = s.SetSetting("site.hero_eyebrow", "Go · SQLite · SEO")
	_ = s.SetSetting("site.hero_title", "把复杂留给后端，\n把简单留给读者。")
	_ = s.SetSetting("site.footer_note", "用 Go 与 SQLite 构建。")
	// 英文文案（::en 命名空间，site() 取不到时回落默认语种）
	_ = s.SetSetting("site.tagline::en", "Notes on engineering, tools & thinking")
	_ = s.SetSetting("site.description::en", "A lightweight content site built with Go and SQLite — focused on backend engineering, minimal design and SEO.")
	_ = s.SetSetting("site.hero_eyebrow::en", "Go · SQLite · SEO")
	_ = s.SetSetting("site.hero_title::en", "Keep the complexity in the backend,\nkeep it simple for readers.")
	_ = s.SetSetting("site.footer_note::en", "Built with Go and SQLite.")
	// 多语种：启用 zh,en（首个为默认）
	_ = s.SetSetting("locales", "zh,en")
	_ = s.SetSetting("default_lang", "zh")
	// 管理员
	_ = s.SetSetting("admin_user", "admin")
	if hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost); err == nil {
		_ = s.SetSetting("admin_password_hash", string(hash))
	}

	// ---- 分类（中英各一套，按 Group 关联）----
	cats := []seedCat{
		{"engineering", "工程", "把想法变成可运行系统的一切：Go、SQLite、服务端渲染、部署与权衡。", "zh", "cat-engineering", "post"},
		{"seo", "SEO", "让内容被搜索引擎完整、准确地理解与收录。", "zh", "cat-seo", "post"},
		{"design", "设计", "克制的排版与视觉，让内容成为主角。", "zh", "cat-design", "post"},
		{"tools", "工具", "提升效率的小工具与工作流。", "zh", "cat-tools", "post"},
		{"thoughts", "思考", "关于技术与产品的随想。", "zh", "cat-thoughts", "post"},
		{"engineering", "Engineering", "Everything about turning ideas into running systems: Go, SQLite, SSR, deployment and trade-offs.", "en", "cat-engineering", "post"},
		{"seo", "SEO", "Helping search engines fully and accurately understand and index your content.", "en", "cat-seo", "post"},
		{"design", "Design", "Restrained typography and visuals that let the content take the stage.", "en", "cat-design", "post"},
		{"tools", "Tools", "Small tools and workflows that boost productivity.", "en", "cat-tools", "post"},
		{"thoughts", "Thoughts", "Reflections on technology and product.", "en", "cat-thoughts", "post"},
	}
	catID := map[string]map[string]int64{"zh": {}, "en": {}}
	for _, c := range cats {
		res, err := s.db.Exec(`INSERT INTO categories(slug,name,description,position,lang,trans_group,kind) VALUES(?,?,?,?,?,?,?)`,
			c.Slug, c.Name, c.Description, len(catID[c.Lang]), c.Lang, c.Group, nz(c.Kind, "post"))
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		catID[c.Lang][c.Slug] = id
	}

	// ---- 文章（中文全套 + 英文精选译文，按 Group 关联）----
	posts := []seedPost{
		{
			Slug: "go-sqlite-cms", Title: "用 Go 与 SQLite 构建一个轻量内容管理系统",
			Excerpt:  "不需要 Node、不需要数据库服务、不需要复杂构建链。一个静态二进制加一个数据库文件，就能跑起一个完整、可被搜索引擎完全收录的内容站。",
			MetaDesc: "用一个 Go 静态二进制加一个 SQLite 文件，搭建完整、可被搜索引擎完全收录的轻量 CMS，包含数据建模、SSR 模板与 SEO 落地。",
			Keywords: "Go,SQLite,CMS,服务端渲染,SEO", Author: "陈书", Cat: "engineering", Date: "2026-06-02",
			Lang: "zh", Group: "p-go-sqlite-cms",
			Content: md(
				"大多数内容站其实并不需要一套庞大的技术栈。它们要做的事情很朴素：把文章存起来，渲染成 HTML，让人和搜索引擎都能读到。当我们把需求收敛到这一点，**Go + SQLite** 这套组合的优势就变得格外清晰。",
				"",
				"## 为什么是 Go 加 SQLite",
				"",
				"Go 把整个应用编译成**一个静态二进制文件**，配合 `embed.FS` 可以把模板与静态资源一并打包进去。部署时不再有「装运行时、装依赖、配环境」的烦恼——scp 上传，运行，结束。",
				"",
				"SQLite 则把数据库收敛成**一个文件**。没有独立的数据库进程，没有网络往返，读取延迟以微秒计。对于读多写少的内容站，这恰好是最舒服的形态。",
				"",
				"> 把复杂留在编译期，把简单交给运行时。这是这套架构最朴素，也最值钱的一句话。",
				"",
				"## 数据怎么建模",
				"",
				"一个最小但够用的内容模型，通常只需要三张表：文章、分类、以及站点设置。文章表里，除了标题和正文，**每一个 SEO 字段都应当独立成列**，这样模板取值直接，索引也方便。",
				"",
				"~~~sql",
				"CREATE TABLE posts (",
				"  id            INTEGER PRIMARY KEY,",
				"  slug          TEXT NOT NULL,          -- 用于生成干净 URL",
				"  title         TEXT NOT NULL,",
				"  excerpt       TEXT,                   -- 列表与 description",
				"  content       TEXT NOT NULL,          -- 正文（Markdown）",
				"  lang          TEXT NOT NULL,          -- 语种（多语种关键）",
				"  status        TEXT DEFAULT 'draft'",
				");",
				"~~~",
				"",
				"有了 `slug`，路由就能映射出 `/posts/go-sqlite-cms` 这样语义清晰、对 SEO 友好的地址，而不是 `?id=42`。",
				"",
				"## 渲染：服务端，一次到位",
				"",
				"这是整套方案里和 SEO 关系最紧的一环。我们用标准库的 `html/template` 在**服务端**把数据填进 HTML，返回给浏览器的是一份「已经写好的、完整的」页面。爬虫无需执行任何 JavaScript，就能拿到标题、正文与结构化数据。",
				"",
				"## 小结",
				"",
				"当你的目标是「把内容稳定、快速、可被收录地呈现出来」，Go + SQLite 提供了一条极短的路径：一个二进制、一个数据库文件、一份服务端渲染的 HTML。复杂度被压到最低，而该有的能力，一个都不少。",
			),
		},
		{
			Slug: "getting-started-go-sqlite-cms", Title: "Build a Lightweight CMS with Go and SQLite",
			Excerpt:  "No Node, no database server, no complex build chain. One static binary plus one database file is enough to run a complete, fully indexable content site.",
			MetaDesc: "Build a complete, fully indexable lightweight CMS from a single Go binary and one SQLite file — covering data modeling, SSR templates and SEO.",
			Keywords: "Go,SQLite,CMS,SSR,SEO", Author: "Chen Shu", Cat: "engineering", Date: "2026-06-02",
			Lang: "en", Group: "p-go-sqlite-cms",
			Content: md(
				"Most content sites don't actually need a sprawling tech stack. Their job is humble: store articles, render them to HTML, and make sure both people and search engines can read them. Once you narrow the requirements to that, the **Go + SQLite** combo shines.",
				"",
				"## Why Go and SQLite",
				"",
				"Go compiles the whole app into **a single static binary**, and with `embed.FS` you can bundle templates and assets right in. Deployment loses its usual headaches — scp the file, run it, done.",
				"",
				"SQLite collapses the database into **one file**. No separate process, no network round-trips, read latency measured in microseconds. For a read-heavy content site, that is exactly the right shape.",
				"",
				"> Keep the complexity at compile time, keep it simple at runtime. That single sentence is the most valuable thing about this architecture.",
				"",
				"## Modeling the data",
				"",
				"A minimal-but-sufficient model usually needs only three tables: posts, categories and settings. In the posts table, **every SEO field deserves its own column** so templates can read values directly.",
				"",
				"## Rendering: server-side, in one pass",
				"",
				"This is the part most tightly bound to SEO. We use the standard library's `html/template` to fill data into HTML **on the server**, returning a fully-written page. Crawlers get the title, body and structured data without running any JavaScript.",
				"",
				"## Wrap-up",
				"",
				"When your goal is to present content reliably, quickly and indexably, Go + SQLite offers a very short path: one binary, one database file, one server-rendered HTML page.",
			),
		},
		{
			Slug: "ssr-and-seo", Title: "为什么服务端渲染，依然是 SEO 的最优解",
			Excerpt:  "搜索引擎确实能执行 JavaScript，但「第一时间拿到完整 HTML」，永远比「等脚本跑完」更可靠。",
			MetaDesc: "从抓取预算、首屏速度与可访问性三个角度，重新审视服务端渲染（SSR）为何依然是 SEO 的最优解。",
			Keywords: "SSR,服务端渲染,SEO,抓取预算,Core Web Vitals", Author: "陈书", Cat: "seo", Date: "2026-05-28",
			Lang: "zh", Group: "p-ssr-and-seo",
			Content: md(
				"关于「爬虫到底能不能跑 JavaScript」的争论，其实方向就错了。能，不代表它愿意、及时、且对每个页面都这么做。对内容站而言，与其押注爬虫的善意，不如把完整的 HTML 直接端上桌。",
				"",
				"## 一、抓取预算是有限的",
				"",
				"搜索引擎给每个站点分配的抓取资源是有上限的。如果每个页面都要额外启动渲染器去执行脚本、等待请求、拼装 DOM，单页成本上升，能被收录的页面数量就下降。**服务端渲染让单页抓取成本接近于零**。",
				"",
				"## 二、首屏速度直接进入排名信号",
				"",
				"Core Web Vitals 已是明确的排名因素。SSR 把「内容出现的时刻」提前到了第一个响应包里。",
				"",
				"## 结论",
				"",
				"在「内容呈现」这件事上，服务端渲染不是一种妥协，而是一种回归。它让最重要的东西——内容本身——以最短路径抵达每一个读者，无论那个读者是人，还是爬虫。",
			),
		},
		{
			Slug: "why-ssr-still-wins-seo", Title: "Why Server-Side Rendering Still Wins at SEO",
			Excerpt:  "Search engines can execute JavaScript, but getting complete HTML up front is always more reliable than waiting for scripts to finish.",
			MetaDesc: "Reconsidering why server-side rendering (SSR) is still the best choice for SEO, from crawl budget, first-paint speed and accessibility.",
			Keywords: "SSR,server-side rendering,SEO,crawl budget,Core Web Vitals", Author: "Chen Shu", Cat: "seo", Date: "2026-05-28",
			Lang: "en", Group: "p-ssr-and-seo",
			Content: md(
				"The debate over whether crawlers can run JavaScript is aimed in the wrong direction. They can — but that doesn't mean they will, promptly, for every page. For a content site, rather than betting on a crawler's goodwill, serve the complete HTML directly.",
				"",
				"## 1. Crawl budget is finite",
				"",
				"Search engines allocate a limited amount of crawling resource per site. If every page needs a renderer to execute scripts and assemble the DOM, per-page cost rises and the number of indexed pages drops. **SSR brings per-page crawl cost close to zero.**",
				"",
				"## 2. First-paint speed is a ranking signal",
				"",
				"Core Web Vitals are an explicit ranking factor. SSR moves the moment content appears into the very first response packet.",
				"",
				"## Conclusion",
				"",
				"For presenting content, SSR isn't a compromise — it's a return to fundamentals. It delivers the most important thing, the content itself, by the shortest path to every reader, whether that reader is a person or a crawler.",
			),
		},
		{
			Slug: "minimal-typography", Title: "克制的美学：极简排版的七条准则",
			Excerpt:  "留白不是空，而是呼吸。字号、行高、对比与节奏，如何共同构成一页让人愿意读下去的文字。",
			MetaDesc: "极简排版的七条实用准则：留白、行高、对比、字号层级与阅读节奏。",
			Keywords: "排版,极简,设计,留白,可读性", Author: "林见", Cat: "design", Date: "2026-05-19",
			Lang: "zh", Group: "p-minimal-typography",
			Content: md(
				"好的排版是隐形的。读者不会注意到它，只会觉得「这页字读起来很舒服」。下面是我们在本站实践的七条准则。",
				"",
				"## 1. 给文字留出呼吸的空间",
				"正文行高建议 1.7 以上，段落之间留足间距。留白不是浪费，而是让眼睛有地方休息。",
				"",
				"## 2. 控制每行字数",
				"中文每行 30–40 字最舒适。过宽的阅读栏会让视线在折返时迷路，因此本站正文限制在 720px。",
				"",
				"## 3. 建立清晰的层级",
				"标题用衬线、正文用无衬线，靠字体与字号拉开层次，而不是靠颜色堆叠。",
				"",
				"> 设计的尽头是减法。当你不知道该加什么时，先看看能减掉什么。",
				"",
				"其余四条，留给你在阅读本站时自己体会。",
			),
		},
		{
			Slug: "sqlite-boundaries", Title: "SQLite 没你想的那么「小」：单文件数据库的真实边界",
			Excerpt:  "它能扛住每天数十万次读取，也能在一台 5 美元的机器上服务一个中型站点。我们聊聊它适合什么，又在哪里会先碰到天花板。",
			MetaDesc: "SQLite 的真实性能边界：并发模型、WAL、适用场景与何时该迁移到客户端-服务器数据库。",
			Keywords: "SQLite,数据库,WAL,并发,性能", Author: "陈书", Cat: "engineering", Date: "2026-05-11",
			Lang: "zh", Group: "p-sqlite-boundaries",
			Content: md(
				"「SQLite 只适合玩具项目」是流传最广的误解之一。事实上，它每天在数以十亿计的设备上运行，也完全能胜任中小型网站的后端。",
				"",
				"## 并发模型：单写者，多读者",
				"开启 WAL 模式后，读操作不会阻塞写，写操作也不会阻塞读。瓶颈只在于「同一时刻只能有一个写者」——对读多写少的内容站，这几乎不构成限制。",
				"",
				"## 什么时候该考虑别的",
				"- 需要多台机器共享同一份数据；",
				"- 写入高度并发且持续；",
				"- 单库体积逼近数百 GB。",
				"",
				"在触及这些边界之前，SQLite 的简单与可靠，是一笔划算到不可思议的买卖。",
			),
		},
		{
			Slug: "single-binary-deploy", Title: "用一个 Go 二进制文件，部署整个网站",
			Excerpt:  "embed.FS 把模板、样式、静态资源全部打进可执行文件。一次编译，scp 上传，立即上线——没有依赖地狱。",
			MetaDesc: "用 Go 的 embed.FS 把模板与静态资源打包进单一二进制，实现零依赖部署。",
			Keywords: "Go,embed,部署,单一二进制,运维", Author: "陈书", Cat: "tools", Date: "2026-05-03",
			Lang: "zh", Group: "p-single-binary-deploy",
			Content: md(
				"传统部署的痛点，大多来自「环境」。Go 用一招把它消解掉：把一切都编译进一个文件。",
				"",
				"## embed 把资源焊进二进制",
				"用 `//go:embed templates assets` 指令，模板与 CSS 在编译期就被嵌入。运行时不再依赖磁盘上的任何外部文件。",
				"",
				"## 部署就是复制",
				"`GOOS=linux go build` 产出一个 Linux 可执行文件，scp 到服务器，配一个 systemd 单元，结束。没有 node_modules，没有虚拟环境，没有版本冲突。",
			),
		},
		{
			Slug: "structured-data", Title: "结构化数据实战：让搜索结果长出富摘要",
			Excerpt:  "JSON-LD 不只是给爬虫看的元数据。正确标注 Article、Breadcrumb 与 FAQ，能让你的链接在搜索结果里更醒目。",
			MetaDesc: "用 JSON-LD 结构化数据标注 Article、BreadcrumbList 与 FAQ，争取搜索结果中的富摘要展示。",
			Keywords: "结构化数据,JSON-LD,富摘要,Schema.org,SEO", Author: "林见", Cat: "seo", Date: "2026-04-25",
			Lang: "zh", Group: "p-structured-data",
			Content: md(
				"两个排名相近的链接，带富摘要的那个，点击率往往高出一截。结构化数据，就是争取富摘要的入场券。",
				"",
				"## 从 BlogPosting 开始",
				"为每篇文章注入 `BlogPosting`：标题、作者、发布时间、配图。这是搜索引擎理解「这是一篇文章」的最直接信号。",
				"",
				"## 面包屑同样重要",
				"`BreadcrumbList` 让搜索结果显示出层级路径，既美观，也帮助爬虫理解站点结构。",
				"",
				"## 校验，再上线",
				"用官方的富媒体测试工具验证每一段 JSON-LD，确保没有语法或字段错误——本站所有页面都通过了这一步。",
			),
		},
		{
			Slug: "structured-data-rich-results", Title: "Structured Data in Practice: Growing Rich Results",
			Excerpt:  "JSON-LD isn't just metadata for crawlers. Marking up Article, Breadcrumb and FAQ correctly makes your links stand out in search results.",
			MetaDesc: "Use JSON-LD structured data to mark up Article, BreadcrumbList and FAQ, and earn rich results in search.",
			Keywords: "structured data,JSON-LD,rich results,Schema.org,SEO", Author: "Lin Jian", Cat: "seo", Date: "2026-04-25",
			Lang: "en", Group: "p-structured-data",
			Content: md(
				"Between two links of similar rank, the one with a rich result usually wins a meaningfully higher click-through rate. Structured data is your ticket to those rich results.",
				"",
				"## Start with BlogPosting",
				"Inject `BlogPosting` into every article: title, author, publish time, image. It's the most direct signal that tells a search engine \"this is an article.\"",
				"",
				"## Breadcrumbs matter too",
				"`BreadcrumbList` shows a hierarchical path in search results — both attractive and helpful for crawlers to understand your site structure.",
				"",
				"## Validate, then ship",
				"Validate every JSON-LD block with the official rich-results test before publishing. Every page on this site passes that step.",
			),
		},
	}

	for _, sp := range posts {
		if err := s.insertSeed(sp, catID[sp.Lang]); err != nil {
			return err
		}
	}

	// ---- 关于页（type=page，中英各一，按 Group 关联）----
	abouts := []seedPost{
		{
			Type: "page", Slug: "about", Title: "关于", Lang: "zh", Group: "page-about",
			Excerpt:  "一个用 Go 与 SQLite 构建的轻量内容站，专注后端工程、极简设计与 SEO。",
			MetaDesc: "CCVAR 简记是一个用 Go 与 SQLite 构建的轻量内容站，专注后端工程、极简设计与搜索引擎优化。",
			Keywords: "关于,CCVAR,Go,SQLite,极简", Author: "CCVAR", Date: "2026-05-01",
			Content: md(
				"我们相信，大多数网站被过度工程化了。一个内容站真正需要的，不过是把文字稳定、快速、可被检索地呈现给读者。围绕这个朴素目标，我们做了几个刻意的选择。",
				"",
				"## 技术，是一种克制",
				"整站由 **Go** 编译成单个静态二进制，数据存放在一个 **SQLite** 文件里。没有独立数据库进程，没有前端构建链，没有运行时依赖。",
				"",
				"## 渲染，为人也为机器",
				"每一页都在服务端用 `html/template` 完整渲染。读者第一时间看到内容，搜索引擎第一时间收录内容。",
				"",
				"> 工具应当隐形。当你不再注意到它，它才算做对了。",
			),
		},
		{
			Type: "page", Slug: "about", Title: "About", Lang: "en", Group: "page-about",
			Excerpt:  "A lightweight content site built with Go and SQLite, focused on backend engineering, minimal design and SEO.",
			MetaDesc: "CCVAR Notes is a lightweight content site built with Go and SQLite, focused on backend engineering, minimal design and SEO.",
			Keywords: "about,CCVAR,Go,SQLite,minimal", Author: "CCVAR", Date: "2026-05-01",
			Content: md(
				"We believe most websites are over-engineered. What a content site truly needs is to present text to readers reliably, quickly and searchably. Around that humble goal, we made a few deliberate choices.",
				"",
				"## Technology as restraint",
				"The whole site compiles into a single static **Go** binary, with data in one **SQLite** file. No separate database process, no frontend build chain, no runtime dependencies.",
				"",
				"## Rendering for humans and machines",
				"Every page is fully rendered on the server with `html/template`. Readers see content immediately, and so do search engines.",
				"",
				"> Tools should be invisible. When you stop noticing them, they're done right.",
			),
		},
	}
	for _, sp := range abouts {
		if err := s.insertSeed(sp, catID[sp.Lang]); err != nil {
			return err
		}
	}

	// ---- 链接分类（kind=link，与文章分类相互独立；中英各一套）----
	linkCats := []seedCat{
		{"dev-tools", "开发工具", "提升编码效率的工具与服务。", "zh", "lcat-dev", "link"},
		{"design-res", "设计资源", "配色、字体、图标与灵感。", "zh", "lcat-design", "link"},
		{"learning", "学习资料", "文档、教程与课程。", "zh", "lcat-learn", "link"},
		{"dev-tools", "Dev Tools", "Tools and services that boost coding efficiency.", "en", "lcat-dev", "link"},
		{"design-res", "Design Resources", "Colors, fonts, icons and inspiration.", "en", "lcat-design", "link"},
		{"learning", "Learning", "Docs, tutorials and courses.", "en", "lcat-learn", "link"},
	}
	linkCatID := map[string]map[string]int64{"zh": {}, "en": {}}
	for _, c := range linkCats {
		res, err := s.db.Exec(`INSERT INTO categories(slug,name,description,position,lang,trans_group,kind) VALUES(?,?,?,?,?,?,?)`,
			c.Slug, c.Name, c.Description, len(linkCatID[c.Lang]), c.Lang, c.Group, "link")
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		linkCatID[c.Lang][c.Slug] = id
	}

	// ---- 链接（type=link，演示资源/产品展示；中英各一套，按 Group 关联）----
	links := []seedPost{
		{
			Type: "link", Slug: "go-dev", Title: "Go 官方网站", Cat: "dev-tools", Lang: "zh", Group: "link-godev",
			LinkURL: "https://go.dev", Date: "2026-06-06", Cover: "/assets/covers/go-brand.webp", Featured: true,
			Excerpt:  "Go 语言官网：语言规范、标准库文档、下载、博客与在线 Playground。",
			MetaDesc: "Go 语言官方网站，提供文档、下载、博客与在线 Playground。",
			Keywords: "Go,golang,官网,文档",
			Content:  md("Go 官方站点，集中了语言规范、标准库文档、版本下载、官方博客与在线 Playground。", "", "## 为什么收录", "学 Go、查标准库、跑一段小代码，从这里开始最快——而且全部免费、无广告。"),
		},
		{
			Type: "link", Slug: "sqlite", Title: "SQLite", Cat: "dev-tools", Lang: "zh", Group: "link-sqlite",
			LinkURL: "https://sqlite.org", Date: "2026-06-04", Cover: "/assets/covers/sqlite-brand.webp", Featured: true,
			Excerpt:  "世界上部署最广的数据库引擎：单文件、零配置、极其可靠。",
			MetaDesc: "SQLite 官网：单文件、零配置、部署最广的嵌入式数据库引擎。",
			Keywords: "SQLite,数据库,嵌入式",
			Content:  md("SQLite 是一个进程内的单文件数据库引擎，无需独立服务、零配置，却足够可靠，运行在数以十亿计的设备上。", "", "## 适合什么", "中小型站点后端、桌面/移动应用本地存储、原型验证——读多写少时尤其顺手。"),
		},
		{
			Type: "link", Slug: "mdn", Title: "MDN Web Docs", Cat: "learning", Lang: "zh", Group: "link-mdn",
			LinkURL: "https://developer.mozilla.org", Date: "2026-06-02", Cover: "/assets/covers/mdn.svg", Featured: true,
			Excerpt:  "前端开发者的权威参考：HTML、CSS、JavaScript 与 Web API。",
			MetaDesc: "MDN Web Docs：HTML/CSS/JavaScript 与 Web API 的权威开发者文档。",
			Keywords: "MDN,前端,文档,Web",
			Content:  md("MDN 由 Mozilla 维护，是前端最权威、最全面的参考文档，覆盖 HTML、CSS、JavaScript 与各类 Web API，附带大量可运行示例与兼容性表格。"),
		},
		{
			Type: "link", Slug: "coolors", Title: "Coolors 配色生成", Cat: "design-res", Lang: "zh", Group: "link-coolors",
			LinkURL: "https://coolors.co", Date: "2026-05-30", Cover: "/assets/covers/coolors.svg",
			Excerpt:  "几秒生成和谐配色方案，设计师的取色利器。",
			MetaDesc: "Coolors：快速生成与导出和谐配色方案的在线工具。",
			Keywords: "配色,设计,工具,colors",
			Content:  md("空格一按即可生成一组和谐配色，支持锁定、微调、导出多种格式，是做品牌色、界面色板时的高效起点。"),
		},
		{
			Type: "link", Slug: "go-dev", Title: "Go Official Site", Cat: "dev-tools", Lang: "en", Group: "link-godev",
			LinkURL: "https://go.dev", Date: "2026-06-06", Cover: "/assets/covers/go-brand.webp", Featured: true,
			Excerpt:  "The Go language home: spec, standard library docs, downloads, blog and Playground.",
			MetaDesc: "The official Go website — docs, downloads, blog and an online Playground.",
			Keywords: "Go,golang,docs",
			Content:  md("The official Go site gathers the language spec, standard-library docs, downloads, the blog and an online Playground.", "", "## Why it's here", "To learn Go, look up the stdlib, or run a quick snippet — this is the fastest start, free and ad-free."),
		},
		{
			Type: "link", Slug: "sqlite", Title: "SQLite", Cat: "dev-tools", Lang: "en", Group: "link-sqlite",
			LinkURL: "https://sqlite.org", Date: "2026-06-04", Cover: "/assets/covers/sqlite-brand.webp", Featured: true,
			Excerpt:  "The most widely deployed database engine: single file, zero-config, rock solid.",
			MetaDesc: "SQLite — a single-file, zero-config, widely deployed embedded database engine.",
			Keywords: "SQLite,database,embedded",
			Content:  md("SQLite is an in-process single-file database engine — no separate server, zero configuration, yet reliable enough to run on billions of devices.", "", "## Good for", "Backends for small-to-mid sites, local storage in desktop/mobile apps, and prototyping — especially read-heavy workloads."),
		},
		{
			Type: "link", Slug: "mdn", Title: "MDN Web Docs", Cat: "learning", Lang: "en", Group: "link-mdn",
			LinkURL: "https://developer.mozilla.org", Date: "2026-06-02", Cover: "/assets/covers/mdn.svg", Featured: true,
			Excerpt:  "The authoritative reference for web developers: HTML, CSS, JavaScript and Web APIs.",
			MetaDesc: "MDN Web Docs: authoritative developer docs for HTML, CSS, JavaScript and Web APIs.",
			Keywords: "MDN,frontend,docs,web",
			Content:  md("Maintained by Mozilla, MDN is the most authoritative and comprehensive frontend reference, covering HTML, CSS, JavaScript and Web APIs with runnable examples and compatibility tables."),
		},
		{
			Type: "link", Slug: "coolors", Title: "Coolors", Cat: "design-res", Lang: "en", Group: "link-coolors",
			LinkURL: "https://coolors.co", Date: "2026-05-30", Cover: "/assets/covers/coolors.svg",
			Excerpt:  "Generate harmonious color palettes in seconds — a designer's go-to.",
			MetaDesc: "Coolors: an online tool to quickly generate and export harmonious color palettes.",
			Keywords: "colors,design,tool,palette",
			Content:  md("Hit the spacebar to generate a harmonious palette, lock and tweak swatches, and export to many formats — a fast starting point for brand and UI colors."),
		},
	}
	for _, sp := range links {
		if err := s.insertSeed(sp, linkCatID[sp.Lang]); err != nil {
			return err
		}
	}
	s.Seeded = true // 触发了空库播种 → 首次启动
	return nil
}

// DefaultAdminPassword 是演示用默认管理员密码（仅用于首启提示与「是否仍为默认」校验）。
const DefaultAdminPassword = "admin123"

// IsDefaultPassword 报告当前管理员密码是否仍为内置默认密码（用于后台提示尽快修改）。
// bcrypt 校验较慢，按 hash 缓存结果，仅当密码变更（hash 改变）时才重算。
func (s *Store) IsDefaultPassword() bool {
	hash, _ := s.GetSetting("admin_password_hash")
	if hash == "" {
		return false
	}
	s.pwMu.Lock()
	defer s.pwMu.Unlock()
	if hash == s.pwHash {
		return s.pwIsDefault
	}
	s.pwHash = hash
	s.pwIsDefault = bcrypt.CompareHashAndPassword([]byte(hash), []byte(DefaultAdminPassword)) == nil
	return s.pwIsDefault
}

func (s *Store) insertSeed(sp seedPost, catID map[string]int64) error {
	typ := sp.Type
	if typ == "" {
		typ = "post"
	}
	lang := nz(sp.Lang, "zh")
	group := sp.Group
	if group == "" {
		group = lang + ":" + sp.Slug
	}
	ts, _ := time.Parse("2006-01-02", sp.Date)
	when := ts.UTC().Format(time.RFC3339)
	var cat any
	if id, ok := catID[sp.Cat]; ok {
		cat = id
	}
	featured := 0
	if sp.Featured {
		featured = 1
	}
	_, err := s.db.Exec(`INSERT INTO posts
		(type,slug,title,excerpt,content,meta_desc,keywords,cover_image,author,status,featured,link_url,lang,trans_group,category_id,published_at,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		typ, sp.Slug, sp.Title, sp.Excerpt, sp.Content, sp.MetaDesc, sp.Keywords, sp.Cover, sp.Author,
		"published", featured, sp.LinkURL, lang, group, cat, when, when, when)
	return err
}
