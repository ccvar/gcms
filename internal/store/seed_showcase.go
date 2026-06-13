package store

// seed_showcase.go —— 「产品官网」样板内容（用 CMS_SEED=showcase 触发，仅用于演示/评审）。
// 目的：让 Demo 本身成为 gcms 的卖点——你正在浏览的这个站，就是一个单文件二进制跑出来的。
// 默认种子（seedIfEmpty 主体）不受影响；评审满意后再并入主种子并铺开。

// seedShowcase 写入产品营销向的样板：站点身份、首页、关于页、8 篇双语特性文 + 生态链接。
func (s *Store) seedShowcase() error {
	// ---- 站点身份（产品定位，中英双语）----
	_ = s.SetSetting("site.name", "gcms")
	_ = s.SetSetting("site.tagline", "一个二进制，一个文件，一个完整的内容站")
	_ = s.SetSetting("site.description", "gcms 是用 Go + SQLite 构建的开源轻量 CMS：100% 服务端渲染、原生 SEO、多语种、18 套主题，最终交付为单个静态二进制 + 一个数据库文件。下载即用，5 分钟自托管上线。")
	_ = s.SetSetting("site.hero_eyebrow", "开源 · 单文件 · 自托管")
	_ = s.SetSetting("site.hero_title", "把复杂交给一个二进制，\n把内容留给你。")
	_ = s.SetSetting("site.footer_note", "gcms · 用 Go 与 SQLite 构建的开源 CMS")
	_ = s.SetSetting("site.tagline::en", "One binary, one file, a complete content site")
	_ = s.SetSetting("site.description::en", "gcms is an open-source lightweight CMS built with Go + SQLite: 100% server-rendered, SEO-native, multilingual, 18 themes — shipped as a single static binary plus one database file. Download and run, self-host in 5 minutes.")
	_ = s.SetSetting("site.hero_eyebrow::en", "Open source · Single binary · Self-hosted")
	_ = s.SetSetting("site.hero_title::en", "Hand the complexity to one binary,\nkeep the content yours.")
	_ = s.SetSetting("site.footer_note::en", "gcms · an open-source CMS built with Go and SQLite")

	// 产品官网气质：用 product 主题；页眉直接显示产品名
	_ = s.SetSetting("theme", "product")
	_ = s.SetSetting("site.brand", "text")

	// 多语种 + 管理员
	_ = s.SetSetting("locales", "zh,en")
	_ = s.SetSetting("default_lang", "zh")
	_ = s.SetSetting("admin_user", "admin")
	if hash, err := bcryptHash(DefaultAdminPassword); err == nil {
		_ = s.SetSetting("admin_password_hash", hash)
	}

	// ---- 文章分类：特性支柱（中英各一套）----
	cats := []seedCat{
		{"start", "快速上手", "从下载到上线：拿到二进制、运行、反向代理。", "zh", "cat-start", "post"},
		{"features", "特性", "SSR、SEO、多语种、主题与编辑器——核心能力一览。", "zh", "cat-features", "post"},
		{"ops", "运维与自动化", "在线更新、自动化 API 与日常运维。", "zh", "cat-ops", "post"},
		{"start", "Getting Started", "From download to live: get the binary, run, reverse-proxy.", "en", "cat-start", "post"},
		{"features", "Features", "SSR, SEO, i18n, themes and the editor — core capabilities.", "en", "cat-features", "post"},
		{"ops", "Ops & Automation", "In-app updates, the automation API and day-to-day ops.", "en", "cat-ops", "post"},
	}
	catID := map[string]map[string]int64{"zh": {}, "en": {}}
	for _, c := range cats {
		res, err := s.db.Exec(`INSERT INTO categories(slug,name,description,position,lang,trans_group,kind) VALUES(?,?,?,?,?,?,?)`,
			c.Slug, c.Name, c.Description, len(catID[c.Lang]), c.Lang, c.Group, "post")
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		catID[c.Lang][c.Slug] = id
	}

	posts := showcasePosts()
	for _, sp := range posts {
		if err := s.insertSeed(sp, catID[sp.Lang]); err != nil {
			return err
		}
	}

	// ---- 关于页（产品电梯演讲，中英双语）----
	for _, sp := range showcaseAbouts() {
		if err := s.insertSeed(sp, catID[sp.Lang]); err != nil {
			return err
		}
	}

	// ---- 链接分类（kind=link）----
	linkCats := []seedCat{
		{"project", "项目", "gcms 的源码、Demo 与下载。", "zh", "s-lcat-project", "link"},
		{"stack", "技术栈", "gcms 依赖的基础设施。", "zh", "s-lcat-stack", "link"},
		{"project", "Project", "gcms source, demo and downloads.", "en", "s-lcat-project", "link"},
		{"stack", "Stack", "The infrastructure gcms builds on.", "en", "s-lcat-stack", "link"},
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

	for _, sp := range showcaseLinks() {
		if err := s.insertSeed(sp, linkCatID[sp.Lang]); err != nil {
			return err
		}
	}

	s.Seeded = true
	return nil
}

// showcasePosts 返回 8 篇双语特性文（按 trans_group 关联中英版本）。
func showcasePosts() []seedPost {
	return []seedPost{
		// ===== 1 · 旗舰：5 分钟部署（start，置顶）=====
		{
			Slug: "deploy-in-5-minutes", Title: "5 分钟，拥有一个自托管的内容站",
			Excerpt:  "下载一个 14MB 的文件，运行它，浏览器打开——一个带后台、SEO、多语种和 18 套主题的内容站就上线了。不需要数据库服务、不需要 Node、不需要配环境。",
			MetaDesc: "用 gcms 自托管一个内容站只需 5 分钟：下载单个二进制、运行、用 Caddy 反代 HTTPS。无需数据库服务、无需 Node、无需配置环境。",
			Keywords: "gcms,自托管,部署,单文件,Go,SQLite,开源CMS", Author: "gcms", Cat: "start", Date: "2026-06-12",
			Lang: "zh", Group: "s-deploy-5min", Featured: true, Cover: "/assets/covers/go.svg",
			Content: md(
				"你现在浏览的这个站，就是一个 **gcms** 实例——单个静态二进制 + 一个 `.db` 文件跑出来的。整套上线过程，大概就是接下来这几步，五分钟足够。",
				"",
				"## 第一步 · 拿到二进制",
				"",
				"到 [发布页](https://github.com/ccvar/gcms-releases/releases/latest) 下载对应平台的包（linux / macOS / windows，amd64 / arm64 都有），解压即可。里面是一个预编译好的 `bin/cms`、启停脚本和默认配置——**部署机上不需要安装 Go**。",
				"",
				"如果你想从源码跑，也只要一行：",
				"",
				"~~~bash",
				"go run .            # 或 ./scripts/cms.sh start（缺 Go 会自动装）",
				"~~~",
				"",
				"## 第二步 · 运行",
				"",
				"~~~bash",
				"./bin/cms           # 直接运行；或 ./scripts/cms.sh start 后台运行",
				"# → http://localhost:8080   后台 /admin",
				"~~~",
				"",
				"首次启动会自动建好数据库、写入演示内容，并在控制台**打印默认后台账号**（`admin / admin123`）。登录后第一件事，就是去「设置 → 安全」改掉它——首页那条提醒也会一直挂着，直到你改完。",
				"",
				"## 第三步 · 上线",
				"",
				"生产环境让 gcms 只监听回环地址，把 HTTPS、压缩、HTTP/3 交给 [Caddy](https://caddyserver.com)：",
				"",
				"~~~bash",
				"ADDR=127.0.0.1:8080 BASE_URL=https://your-domain.com ./scripts/cms.sh start",
				"~~~",
				"",
				"~~~caddyfile",
				"your-domain.com {",
				"    encode zstd gzip",
				"    reverse_proxy 127.0.0.1:8080",
				"}",
				"~~~",
				"",
				"`caddy run`，证书自动签发。完事。",
				"",
				"## 它已经替你做好了这些",
				"",
				"上线的同时，下面这些**默认就位**，不用额外配置：",
				"",
				"- **原生 SEO**：每页服务端渲染，自带 canonical、`hreflang`、Open Graph、`sitemap.xml`、RSS 与 JSON-LD 结构化数据；",
				"- **多语种**：URL 前缀路由（`/zh/…`、`/en/…`），逐语种维护内容，自动输出 hreflang 备份链接；",
				"- **18 套主题**：后台一键切换，还能按主题微调主色与圆角；",
				"- **双模式编辑器**：Markdown ⇄ 所见即所得，粘贴图片、插表格、拖动段落；",
				"- **链接展示、代码注入、社媒页脚、自定义导航、后台在线更新**……",
				"",
				"> 把复杂留在编译期，把简单交给运行时——这套架构最值钱的一句话。",
				"",
				"## 为什么能这么简单",
				"",
				"因为只有两个「单数」：Go 用 `embed.FS` 把模板和静态资源**焊进一个二进制**，SQLite 把数据库收敛成**一个文件**。没有独立数据库进程、没有前端构建链、没有运行时依赖。备份就是复制那个 `.db`，迁移就是 `scp` 一个文件。",
				"",
				"五分钟，你就拥有了一个完全属于自己、可被搜索引擎完整收录、还挺好看的内容站。",
			),
		},
		{
			Slug: "deploy-in-5-minutes", Title: "Self-Host a Content Site in 5 Minutes",
			Excerpt:  "Download one 14MB file, run it, open the browser — a content site with an admin, SEO, i18n and 18 themes is live. No database server, no Node, no environment setup.",
			MetaDesc: "Self-host a content site with gcms in 5 minutes: download a single binary, run it, reverse-proxy HTTPS with Caddy. No database server, no Node, no environment setup.",
			Keywords: "gcms,self-hosted,deploy,single binary,Go,SQLite,open source CMS", Author: "gcms", Cat: "start", Date: "2026-06-12",
			Lang: "en", Group: "s-deploy-5min", Featured: true, Cover: "/assets/covers/go.svg",
			Content: md(
				"The site you're reading is a **gcms** instance — a single static binary plus one `.db` file. Getting it online is roughly the steps below. Five minutes is plenty.",
				"",
				"## Step 1 · Get the binary",
				"",
				"Grab the package for your platform from the [releases page](https://github.com/ccvar/gcms-releases/releases/latest) (linux / macOS / windows, amd64 / arm64) and unpack it. Inside is a prebuilt `bin/cms`, start/stop scripts and a default config — **no Go required on the server**.",
				"",
				"From source it's a single line:",
				"",
				"~~~bash",
				"go run .            # or ./scripts/cms.sh start (auto-installs Go if missing)",
				"~~~",
				"",
				"## Step 2 · Run",
				"",
				"~~~bash",
				"./bin/cms           # or ./scripts/cms.sh start to run in the background",
				"# → http://localhost:8080   admin at /admin",
				"~~~",
				"",
				"On first launch it creates the database, writes demo content, and **prints the default admin credentials** (`admin / admin123`). Change them first thing under Settings → Security — a banner keeps nagging until you do.",
				"",
				"## Step 3 · Go live",
				"",
				"In production, bind gcms to loopback and let [Caddy](https://caddyserver.com) handle HTTPS, compression and HTTP/3:",
				"",
				"~~~caddyfile",
				"your-domain.com {",
				"    encode zstd gzip",
				"    reverse_proxy 127.0.0.1:8080",
				"}",
				"~~~",
				"",
				"`caddy run`, certificate issued automatically. Done.",
				"",
				"## What's already done for you",
				"",
				"- **SEO-native**: server-rendered pages with canonical, `hreflang`, Open Graph, `sitemap.xml`, RSS and JSON-LD;",
				"- **Multilingual**: URL-prefix routing (`/zh/…`, `/en/…`), per-language content, automatic hreflang alternates;",
				"- **18 themes**, switchable from the admin, with per-theme accent and radius tweaks;",
				"- **Dual-mode editor**: Markdown ⇄ WYSIWYG, paste images, insert tables, drag blocks;",
				"- Link showcase, code injection, social footer, custom nav, in-app updates…",
				"",
				"> Keep the complexity at compile time, keep it simple at runtime.",
				"",
				"## Why it's this simple",
				"",
				"Two singulars: Go's `embed.FS` welds templates and assets **into one binary**, and SQLite collapses the database into **one file**. No separate database process, no frontend build chain, no runtime dependencies. Backup is copying that `.db`; migration is `scp`-ing one file.",
				"",
				"Five minutes, and you own a content site that's entirely yours, fully indexable, and rather good-looking.",
			),
		},

		// ===== 2 · 为什么 Go + SQLite（features，置顶）=====
		{
			Slug: "why-go-and-sqlite", Title: "为什么是 Go + SQLite",
			Excerpt:  "不是因为时髦，而是因为它们把「运维复杂度」直接压到了地板上：一个文件编译，一个文件存储。",
			MetaDesc: "gcms 选择 Go + SQLite 的理由：单一静态二进制、零运行时依赖、单文件数据库、读多写少场景下的极致简单。",
			Keywords: "Go,SQLite,架构,单文件,零依赖,WAL", Author: "gcms", Cat: "features", Date: "2026-06-11",
			Lang: "zh", Group: "s-why-go-sqlite", Featured: true, Cover: "/assets/covers/sqlite.svg",
			Content: md(
				"内容站的真实需求很朴素：把文章存起来、渲染成 HTML、让人和爬虫都能读到。把需求收敛到这一点，**Go + SQLite** 的优势就格外清晰。",
				"",
				"## 一个二进制",
				"Go 把整个应用编译成一个静态可执行文件，`embed.FS` 把模板与静态资源一并打进去。部署没有「装运行时、装依赖、配环境」——上传，运行，结束。交叉编译到任意平台也只是换个 `GOOS`。",
				"",
				"## 一个文件",
				"SQLite 没有独立进程、没有网络往返，读取延迟以微秒计。开启 **WAL** 后读不阻塞写、写不阻塞读，瓶颈只剩「同一时刻一个写者」——对读多写少的内容站，这几乎不构成限制。",
				"",
				"## 划算到不可思议",
				"它能在一台很便宜的机器上稳稳服务一个中型站点。备份是复制一个文件，回滚是换回一个文件。在你真正撞上「需要多机共享数据」之前，这套简单是一笔极划算的买卖。",
				"",
				"> 复杂度被压到最低，而该有的能力，一个都不少。",
			),
		},
		{
			Slug: "why-go-and-sqlite", Title: "Why Go + SQLite",
			Excerpt:  "Not because it's trendy — because it flattens operational complexity: one file to compile, one file to store.",
			MetaDesc: "Why gcms is built on Go + SQLite: a single static binary, zero runtime dependencies, a single-file database, and radical simplicity for read-heavy sites.",
			Keywords: "Go,SQLite,architecture,single file,zero dependency,WAL", Author: "gcms", Cat: "features", Date: "2026-06-11",
			Lang: "en", Group: "s-why-go-sqlite", Featured: true, Cover: "/assets/covers/sqlite.svg",
			Content: md(
				"A content site's real needs are humble: store articles, render HTML, let people and crawlers read them. Narrow it to that, and **Go + SQLite** shines.",
				"",
				"## One binary",
				"Go compiles the whole app into one static executable; `embed.FS` bundles templates and assets in. No runtime, no dependency install, no environment config — upload, run, done. Cross-compiling to any platform is just a different `GOOS`.",
				"",
				"## One file",
				"SQLite has no separate process and no network round-trips; read latency is measured in microseconds. With **WAL**, reads don't block writes and writes don't block reads — the only limit is one writer at a time, which a read-heavy content site barely notices.",
				"",
				"## An absurdly good deal",
				"It serves a mid-sized site comfortably on a very cheap box. Backup is copying one file; rollback is swapping it back. Until you genuinely need to share data across machines, this simplicity is a bargain.",
				"",
				"> Complexity squeezed to a minimum, with none of the capability lost.",
			),
		},

		// ===== 3 · 多语种内置（features）=====
		{
			Slug: "multilingual-built-in", Title: "多语种，内置——不是事后翻译",
			Excerpt:  "URL 前缀路由、逐语种维护、自动 hreflang——做一个真正能被多语种搜索收录的站，gcms 把地基替你打好了。",
			MetaDesc: "gcms 的多语种能力：URL 前缀路由、按语种独立维护内容、自动 hreflang 备份链接、可自定义语种，让每种语言都被对应搜索引擎正确收录。",
			Keywords: "多语种,i18n,hreflang,国际化,SEO,语种", Author: "gcms", Cat: "features", Date: "2026-06-10",
			Lang: "zh", Group: "s-i18n",
			Content: md(
				"多语种不是把界面文案翻一遍那么简单。真正难的是：**让每种语言的内容都能被对应区域的搜索引擎正确发现与收录**。gcms 从路由到 SEO 都为此设计。",
				"",
				"## 一种语言，一段路径",
				"`/zh/…`、`/en/…`——每种语言有独立的 URL 前缀，甚至可以用各自的 slug。地址语义清晰，分享与收录都干净。",
				"",
				"## 内容逐语种维护",
				"同一篇文章的各语种版本通过「互译分组」关联，可以分别编辑、分别发布、互不打架。某种语言还没译？那一页就只出现在那种语言里，不会有半成品。",
				"",
				"## hreflang 自动输出",
				"每个页面都会自动生成 `hreflang` 备份链接（含 `x-default`），明确告诉搜索引擎「这页还有这些语言版本」，避免重复内容、把流量导向正确的语言。",
				"",
				"## 还能自定义语种",
				"内置一批预设语种，也能在后台新增任意语种——加一门语言，不用改一行代码。",
				"",
				"> 你正在读的这页，就有一个英文版本。点右上角的语言切换试试。",
			),
		},
		{
			Slug: "multilingual-built-in", Title: "Multilingual, Built In — Not Bolted On",
			Excerpt:  "URL-prefix routing, per-language content, automatic hreflang — gcms lays the groundwork for a site that's genuinely indexable in every language.",
			MetaDesc: "gcms multilingual support: URL-prefix routing, independently maintained per-language content, automatic hreflang alternates, and custom locales — so every language is correctly indexed.",
			Keywords: "multilingual,i18n,hreflang,internationalization,SEO,locale", Author: "gcms", Cat: "features", Date: "2026-06-10",
			Lang: "en", Group: "s-i18n",
			Content: md(
				"Going multilingual isn't just translating the UI. The hard part is making **each language's content discoverable and indexable by the right regional search engine**. gcms is designed for that, from routing to SEO.",
				"",
				"## One language, one path",
				"`/zh/…`, `/en/…` — each language gets its own URL prefix, and can even use its own slugs. Clean to share, clean to index.",
				"",
				"## Content maintained per language",
				"Versions of the same article are linked by a translation group, so you edit and publish each independently. Not translated yet? That page simply doesn't appear in that language — no half-finished stubs.",
				"",
				"## hreflang, automatically",
				"Every page emits `hreflang` alternates (including `x-default`), telling search engines which language versions exist — avoiding duplicate content and routing traffic to the right language.",
				"",
				"## Custom locales too",
				"A set of locales ships built in, and you can add any locale from the admin — a new language without a line of code.",
				"",
				"> The page you're reading has a Chinese version. Try the language switcher, top right.",
			),
		},

		// ===== 4 · 双模式编辑器（features）=====
		{
			Slug: "dual-mode-editor", Title: "Markdown ⇄ 富文本：两种手感，随时切换",
			Excerpt:  "想敲 Markdown 就敲 Markdown，想所见即所得就切过去——粘贴图片、插表格、拖段落，内容无损。",
			MetaDesc: "gcms 的双模式编辑器：Markdown 与所见即所得随时一键切换，支持粘贴图片自动转 WebP、插入与编辑表格、拖动段落重排，写作零阻力。",
			Keywords: "编辑器,Markdown,富文本,所见即所得,WebP,写作", Author: "gcms", Cat: "features", Date: "2026-06-09",
			Lang: "zh", Group: "s-editor",
			Content: md(
				"写作的阻力越小越好。gcms 的编辑器给你两种手感，随时一键切换，内容无损迁移。",
				"",
				"## Markdown 模式",
				"纯文本、可预期、对版本管理友好。习惯键盘流的人，从头到尾不用碰鼠标。",
				"",
				"## 富文本模式",
				"所见即所得，而且**间距、字号、标题层级都和前台文章页完全一致**——你在编辑器里看到的样子，就是读者最终看到的样子。",
				"",
				"## 顺手的细节",
				"- 选中文字，工具栏在选区**上方**浮现，不遮挡你正在改的字；",
				"- 粘贴或插入图片自动转 **WebP**，并在**点保存时**才上传，草稿期不留垃圾文件；",
				"- 插入、增删行列地编辑**表格**；",
				"- 抓住段落左侧把手，像整理卡片一样**拖动重排**顺序。",
				"",
				"> 工具不该挡在你和文字之间。它该隐形。",
			),
		},
		{
			Slug: "dual-mode-editor", Title: "Markdown ⇄ Rich Text: Two Feels, Switch Anytime",
			Excerpt:  "Type Markdown when you want to; flip to WYSIWYG when you don't — paste images, insert tables, drag blocks, losslessly.",
			MetaDesc: "gcms's dual-mode editor: switch between Markdown and WYSIWYG anytime, paste images auto-converted to WebP, insert and edit tables, and drag blocks to reorder — writing with zero friction.",
			Keywords: "editor,Markdown,rich text,WYSIWYG,WebP,writing", Author: "gcms", Cat: "features", Date: "2026-06-09",
			Lang: "en", Group: "s-editor",
			Content: md(
				"The less friction in writing, the better. gcms's editor gives you two feels, switchable anytime, with no content loss.",
				"",
				"## Markdown mode",
				"Plain text, predictable, version-control friendly. Keyboard-flow writers never touch the mouse.",
				"",
				"## Rich-text mode",
				"WYSIWYG — and the **spacing, sizes and heading hierarchy match the public article page exactly**. What you see in the editor is what the reader gets.",
				"",
				"## Details that help",
				"- Select text and the toolbar appears **above** the selection, never covering what you're editing;",
				"- Pasted or inserted images are auto-converted to **WebP** and uploaded only **on save**, so drafts leave no orphan files;",
				"- Insert and edit **tables**, adding or removing rows and columns;",
				"- Grab a block's left handle and **drag to reorder**, like rearranging cards.",
				"",
				"> A tool shouldn't stand between you and your words. It should disappear.",
			),
		},

		// ===== 5 · 原生 SEO（features）=====
		{
			Slug: "seo-by-default", Title: "原生 SEO：你几乎不用做什么",
			Excerpt:  "服务端渲染、canonical、hreflang、sitemap、JSON-LD——这些 SEO 的「脏活」，gcms 默认就替你做好了。",
			MetaDesc: "gcms 默认内置的 SEO 能力：服务端渲染、canonical、多语种 hreflang、sitemap、RSS 与 JSON-LD 结构化数据，开箱即用、自动校验通过。",
			Keywords: "SEO,SSR,hreflang,sitemap,结构化数据,JSON-LD,canonical", Author: "gcms", Cat: "features", Date: "2026-06-08",
			Lang: "zh", Group: "s-seo-default",
			Content: md(
				"很多站点把 SEO 当成上线后再补的功课。gcms 把它做成了**默认行为**——你写好内容，剩下的它替你处理。",
				"",
				"## 每一页都是「写好的」HTML",
				"全站服务端渲染，返回给浏览器的是一份完整页面。爬虫无需执行任何 JavaScript，就能拿到标题、正文与结构化数据；首屏内容进入第一个响应包，对 Core Web Vitals 也友好。",
				"",
				"## 该有的标记，一个不缺",
				"- 自指 **canonical**，杜绝重复内容；",
				"- 多语种 **hreflang**（含 `x-default`）；",
				"- **Open Graph** 社交卡片；",
				"- **sitemap.xml** 与分语种 **RSS**；",
				"- `BlogPosting` / `BreadcrumbList` 等 **JSON-LD** 结构化数据，争取搜索结果里的富摘要。",
				"",
				"## 全部自动、且校验通过",
				"以上都按页动态生成，本站每一页都能通过官方富媒体测试。你要做的，只是把文字写好。",
			),
		},
		{
			Slug: "seo-by-default", Title: "SEO by Default, Not as an Afterthought",
			Excerpt:  "Server rendering, canonical, hreflang, sitemap, JSON-LD — gcms does the SEO grunt work for you, by default.",
			MetaDesc: "gcms's built-in SEO: server-side rendering, canonical, multilingual hreflang, sitemap, RSS and JSON-LD structured data — out of the box and validation-clean.",
			Keywords: "SEO,SSR,hreflang,sitemap,structured data,JSON-LD,canonical", Author: "gcms", Cat: "features", Date: "2026-06-08",
			Lang: "en", Group: "s-seo-default",
			Content: md(
				"Many sites treat SEO as homework for after launch. gcms makes it the **default** — you write the content, it handles the rest.",
				"",
				"## Every page is already-written HTML",
				"The whole site is server-rendered, returning a complete page. Crawlers get the title, body and structured data without running any JavaScript, and above-the-fold content lands in the first response packet — friendly to Core Web Vitals.",
				"",
				"## Every tag you need, none missing",
				"- Self-referencing **canonical** to kill duplicate content;",
				"- Multilingual **hreflang** (with `x-default`);",
				"- **Open Graph** social cards;",
				"- **sitemap.xml** and per-language **RSS**;",
				"- `BlogPosting` / `BreadcrumbList` **JSON-LD** to earn rich results.",
				"",
				"## All automatic, all validated",
				"Everything is generated per page, and every page here passes the official rich-results test. Your only job is to write well.",
			),
		},

		// ===== 6 · 18 套主题（features）=====
		{
			Slug: "eighteen-themes", Title: "18 套主题，一键切换 + 可视化微调",
			Excerpt:  "从编辑部、杂志、极客终端，到产品官网、交易所、作品集——换个气质，只要点一下。",
			MetaDesc: "gcms 内置 18 套各具风格的前台主题，后台一键切换，并可按主题微调主色与圆角，无需改一行 CSS。",
			Keywords: "主题,模板,设计,可定制,前端,换肤", Author: "gcms", Cat: "features", Date: "2026-06-07",
			Lang: "zh", Group: "s-themes",
			Content: md(
				"同一份内容，能穿上完全不同的「衣服」。gcms 内置 **18 套**布局与气质各异的主题：编辑部、杂志、极客终端、粗野、手账、瑞士、报纸、暗夜、产品官网、交易所、智课、作品集……",
				"",
				"## 一键切换",
				"后台「外观与主题」里点一下即生效，前台立刻换装，内容一字不动。挑主题就像试衣服，不满意随时换回来。",
				"",
				"## 还能微调",
				"每套主题都能单独调**主色**与**圆角**，并把这份偏好存成你自己的风格——不必碰一行 CSS。",
				"",
				"## 主题安全",
				"主题只决定外观，不触碰你的数据；切换、微调都是可逆的设置项。你看到的这身「产品官网」皮肤，就是其中一套。",
			),
		},
		{
			Slug: "eighteen-themes", Title: "18 Themes, One Click, Plus Fine-Tuning",
			Excerpt:  "From editorial and magazine to terminal, product and exchange — change the whole vibe with one click.",
			MetaDesc: "gcms ships 18 distinct front-end themes, switchable from the admin in one click, with per-theme accent and radius tuning — no CSS required.",
			Keywords: "themes,templates,design,customizable,frontend,skins", Author: "gcms", Cat: "features", Date: "2026-06-07",
			Lang: "en", Group: "s-themes",
			Content: md(
				"The same content can wear completely different clothes. gcms ships **18** themes with distinct layouts and moods: editorial, magazine, terminal, brutalist, notebook, swiss, newspaper, dark pro, product, exchange, academy, studio…",
				"",
				"## One click",
				"Pick one under Appearance & Themes and it takes effect instantly — the front end re-skins, the content untouched. Trying themes is like trying on clothes; switch back anytime.",
				"",
				"## And fine-tune",
				"Each theme has its own **accent** and **corner radius** you can adjust and save as your own look — without touching a line of CSS.",
				"",
				"## Themes are safe",
				"A theme only decides appearance; it never touches your data. The 'product' skin you're looking at is just one of them.",
			),
		},

		// ===== 7 · 自动化与 API（ops）=====
		{
			Slug: "automation-api", Title: "自动化与 API：让脚本和 AI 替你发布",
			Excerpt:  "API 密钥、细粒度权限、OpenAPI 描述与技能包——把重复劳动交给自动化，把判断留给你自己。",
			MetaDesc: "gcms 的自动化接口：带权限范围的 API 密钥、OpenAPI 描述、调用日志与可导出的 AI 技能包，让脚本、定时任务与 AI 助手安全地发布与管理内容。",
			Keywords: "API,自动化,OpenAPI,AI,密钥,集成", Author: "gcms", Cat: "ops", Date: "2026-06-06",
			Lang: "zh", Group: "s-automation",
			Content: md(
				"内容不一定都要手敲。gcms 内置一套自动化接口，让脚本、定时任务、甚至 AI 助手都能安全地发布与管理内容。",
				"",
				"## 带权限的 API 密钥",
				"在后台签发密钥，按「读 / 写」等范围授权，可随时吊销、可查最近使用时间。密钥只在创建时完整显示一次，存的是哈希——丢了就吊销重发，不怕泄露。",
				"",
				"## 标准化描述",
				"自带 **OpenAPI** 描述与调用日志，接入任何 HTTP 客户端、Webhook 或自动化平台都很直接。每一次写入都有迹可循。",
				"",
				"## 给 AI 的技能包",
				"还能导出一份「技能包」，把站点的发布能力描述给 AI 助手——让它按你的规则产出草稿、补全 SEO、安排发布。",
				"",
				"> 把重复劳动交给自动化，把判断留给你自己。",
			),
		},
		{
			Slug: "automation-api", Title: "Automation & API: Let Scripts and AI Publish for You",
			Excerpt:  "API keys, fine-grained scopes, an OpenAPI description and a skill package — hand the repetitive work to automation, keep the judgment yourself.",
			MetaDesc: "gcms's automation layer: scoped API keys, an OpenAPI description, call logs and an exportable AI skill package, so scripts, cron jobs and AI assistants can publish and manage content safely.",
			Keywords: "API,automation,OpenAPI,AI,keys,integration", Author: "gcms", Cat: "ops", Date: "2026-06-06",
			Lang: "en", Group: "s-automation",
			Content: md(
				"Content doesn't all have to be typed by hand. gcms ships an automation layer so scripts, cron jobs and even AI assistants can publish and manage content safely.",
				"",
				"## Scoped API keys",
				"Issue keys from the admin, grant them read / write scopes, revoke anytime, and see when each was last used. A key is shown in full once at creation; only its hash is stored — lost one? Revoke and reissue.",
				"",
				"## A standard description",
				"It ships an **OpenAPI** description and call logs, so wiring up any HTTP client, webhook or automation platform is straightforward — and every write is traceable.",
				"",
				"## A skill package for AI",
				"Export a 'skill package' that describes the site's publishing capabilities to an AI assistant — so it can draft, fill in SEO and schedule posts by your rules.",
				"",
				"> Hand the repetitive work to automation; keep the judgment yourself.",
			),
		},

		// ===== 8 · 后台在线更新（ops）=====
		{
			Slug: "in-app-updates", Title: "后台一键在线更新",
			Excerpt:  "新版本出来了，后台点一下就能检查与升级——不用 SSH、不用重新编译、不用记命令。",
			MetaDesc: "gcms 的在线更新：后台比对公开发布仓库的 manifest 检查新版本，一键下载对应平台包、校验 SHA256、替换并重启，自托管也能像用 App 一样省心。",
			Keywords: "更新,升级,自托管,manifest,校验和,运维", Author: "gcms", Cat: "ops", Date: "2026-06-05",
			Lang: "zh", Group: "s-updates",
			Content: md(
				"自托管最怕「装好就没人管」。gcms 把升级也做进了后台，让维护这件事不再需要你登服务器敲命令。",
				"",
				"## 自动检查",
				"后台「系统更新」会比对公开发布仓库的清单（`manifest.json`），告诉你**有没有新版本、更新了什么**。源码仓库可以私有，发布仓库公开，互不影响。",
				"",
				"## 一键升级",
				"确认后，自动下载对应平台的包、**校验 SHA256**、替换二进制并重启——全程在浏览器里完成。校验和保证下载完整，绝不会装上半个文件。",
				"",
				"## 始终可控",
				"发布仓库公开可查，你随时知道升级到的是哪个版本、来自哪次发布。要稳妥，也可以继续用发布包手动替换——在线更新只是更省心的那条路。",
				"",
				"> 自托管，也可以像用 App 一样省心。",
			),
		},
		{
			Slug: "in-app-updates", Title: "One-Click In-App Updates",
			Excerpt:  "New version out? Check and upgrade from the admin with one click — no SSH, no recompiling, no commands to remember.",
			MetaDesc: "gcms in-app updates: the admin checks a public release manifest for new versions, then downloads the right platform package, verifies SHA256, replaces the binary and restarts — self-hosting as easy as using an app.",
			Keywords: "updates,upgrade,self-hosted,manifest,checksum,ops", Author: "gcms", Cat: "ops", Date: "2026-06-05",
			Lang: "en", Group: "s-updates",
			Content: md(
				"The danger of self-hosting is the 'install it and forget it' trap. gcms builds upgrading into the admin, so maintenance no longer means SSH-ing in to run commands.",
				"",
				"## Automatic checks",
				"Settings → System Updates compares a public release manifest (`manifest.json`) and tells you **whether there's a new version and what changed**. Your source repo can stay private while the release repo is public — independent of each other.",
				"",
				"## One-click upgrade",
				"Confirm, and it downloads the right platform package, **verifies the SHA256**, replaces the binary and restarts — all in the browser. The checksum guarantees a complete download; you'll never install half a file.",
				"",
				"## Always in control",
				"The release repo is public and auditable, so you always know which version you upgraded to and from which release. Prefer caution? Keep replacing packages by hand — in-app update is just the easier path.",
				"",
				"> Self-hosting can be as low-maintenance as using an app.",
			),
		},
	}
}

// showcaseAbouts 返回关于页（中英双语）。
func showcaseAbouts() []seedPost {
	return []seedPost{
		{
			Type: "page", Slug: "about", Title: "关于 gcms", Lang: "zh", Group: "s-page-about",
			Excerpt:  "gcms 是一个用 Go + SQLite 构建的开源轻量 CMS：单文件、原生 SEO、多语种、可自托管。",
			MetaDesc: "gcms 是用 Go 与 SQLite 构建的开源轻量内容管理系统：单一静态二进制 + 单文件数据库，原生 SEO、多语种、18 套主题，5 分钟自托管上线。",
			Keywords: "gcms,关于,开源,CMS,Go,SQLite,自托管", Author: "gcms", Date: "2026-06-12",
			Content: md(
				"**gcms** 是一个把「简单」当成第一性原则的开源内容管理系统。我们相信，大多数网站被过度工程化了——一个内容站真正需要的，不过是把文字稳定、快速、可被检索地呈现给读者。",
				"",
				"## 它是什么",
				"- 用 **Go + SQLite** 构建，最终是**一个静态二进制 + 一个数据库文件**；",
				"- 100% 服务端渲染，**原生 SEO**（canonical / hreflang / sitemap / JSON-LD）；",
				"- **多语种**、**18 套主题**、**Markdown ⇄ 富文本编辑器**、链接展示、代码注入；",
				"- 自带跨平台启停脚本、单文件部署、**后台在线更新**与自动化 API。",
				"",
				"## 它的取舍",
				"没有独立数据库进程，没有前端构建链，没有运行时依赖。备份是复制一个文件，迁移是 `scp` 一个文件。把复杂留在编译期，把简单交给你。",
				"",
				"## 开始使用",
				"- 在线 Demo：你正在看的这个站；",
				"- 源码与文档：[GitHub](https://github.com/ccvar/gcms)；",
				"- 下载发布包：[Releases](https://github.com/ccvar/gcms-releases/releases/latest)。",
				"",
				"> 工具应当隐形。当你不再注意到它，它才算做对了。",
			),
		},
		{
			Type: "page", Slug: "about", Title: "About gcms", Lang: "en", Group: "s-page-about",
			Excerpt:  "gcms is an open-source lightweight CMS built with Go + SQLite: single-file, SEO-native, multilingual, self-hostable.",
			MetaDesc: "gcms is an open-source lightweight CMS built with Go and SQLite: a single static binary plus a single-file database, SEO-native, multilingual, 18 themes, self-hosted in 5 minutes.",
			Keywords: "gcms,about,open source,CMS,Go,SQLite,self-hosted", Author: "gcms", Date: "2026-06-12",
			Content: md(
				"**gcms** is an open-source content management system that treats simplicity as a first principle. Most websites are over-engineered; a content site really only needs to present text to readers reliably, quickly and searchably.",
				"",
				"## What it is",
				"- Built with **Go + SQLite**, shipped as **one static binary plus one database file**;",
				"- 100% server-rendered, **SEO-native** (canonical / hreflang / sitemap / JSON-LD);",
				"- **Multilingual**, **18 themes**, **Markdown ⇄ WYSIWYG editor**, link showcase, code injection;",
				"- Cross-platform start/stop scripts, single-file deploy, **in-app updates** and an automation API.",
				"",
				"## The trade-off",
				"No separate database process, no frontend build chain, no runtime dependencies. Backup is copying one file; migration is `scp`-ing one file.",
				"",
				"## Get started",
				"- Live demo: the very site you're reading;",
				"- Source & docs: [GitHub](https://github.com/ccvar/gcms);",
				"- Downloads: [Releases](https://github.com/ccvar/gcms-releases/releases/latest).",
				"",
				"> Tools should be invisible. When you stop noticing them, they're done right.",
			),
		},
	}
}

// showcaseLinks 返回项目生态链接（中英双语，置顶项用于首页「精选链接」与安装入口）。
func showcaseLinks() []seedPost {
	return []seedPost{
		// 中文
		{Type: "link", Slug: "github", Title: "GitHub 仓库", Cat: "project", Lang: "zh", Group: "s-link-gh", Featured: true,
			LinkURL: "https://github.com/ccvar/gcms", Date: "2026-06-12",
			Excerpt: "gcms 的开源源码、文档与 Issue。", MetaDesc: "gcms 开源仓库", Keywords: "gcms,GitHub,开源",
			Content: md("gcms 的开源代码、文档与问题追踪都在这里。欢迎 Star、提 Issue 或贡献代码。")},
		{Type: "link", Slug: "demo", Title: "在线 Demo", Cat: "project", Lang: "zh", Group: "s-link-demo", Featured: true,
			LinkURL: "https://cms.ccvar.com", Date: "2026-06-12",
			Excerpt: "你正在看的这个站，就是 gcms 本身。", MetaDesc: "gcms 在线演示", Keywords: "gcms,Demo,演示",
			Content: md("最好的介绍就是亲自用。这个 Demo 站本身就跑在 gcms 上——你看到的每一页，都是它渲染的。")},
		{Type: "link", Slug: "releases", Title: "下载发布包", Cat: "project", Lang: "zh", Group: "s-link-rel", Featured: true,
			LinkURL: "https://github.com/ccvar/gcms-releases/releases/latest", Date: "2026-06-11",
			Excerpt: "各平台预编译部署包：下载、解压、运行。", MetaDesc: "gcms 各平台发布包下载", Keywords: "gcms,下载,发布包",
			Content: md("各平台（linux / macOS / windows）的预编译部署包，内含二进制、启停脚本与默认配置。下载、解压、运行即可。")},
		{Type: "link", Slug: "go", Title: "Go", Cat: "stack", Lang: "zh", Group: "s-link-go", Featured: true,
			LinkURL: "https://go.dev", Date: "2026-06-10", Cover: "/assets/covers/go.svg",
			Excerpt: "把 gcms 编译成单文件的语言。", MetaDesc: "Go 官网", Keywords: "Go,golang",
			Content: md("gcms 用 Go 构建：一次编译产出零依赖的静态二进制，交叉编译到各平台轻而易举。")},
		{Type: "link", Slug: "sqlite", Title: "SQLite", Cat: "stack", Lang: "zh", Group: "s-link-sqlite", Featured: true,
			LinkURL: "https://sqlite.org", Date: "2026-06-10", Cover: "/assets/covers/sqlite.svg",
			Excerpt: "gcms 的单文件数据库。", MetaDesc: "SQLite 官网", Keywords: "SQLite,数据库",
			Content: md("gcms 的数据存在一个 SQLite 文件里：零配置、极可靠，备份就是复制一个文件。")},
		{Type: "link", Slug: "caddy", Title: "Caddy", Cat: "stack", Lang: "zh", Group: "s-link-caddy",
			LinkURL: "https://caddyserver.com", Date: "2026-06-09",
			Excerpt: "推荐的反向代理，自动 HTTPS。", MetaDesc: "Caddy 官网", Keywords: "Caddy,反向代理,HTTPS",
			Content: md("生产环境推荐用 Caddy 给 gcms 做反向代理：自动签发证书、HTTP/3 与压缩开箱即用。")},
		// 英文
		{Type: "link", Slug: "github", Title: "GitHub", Cat: "project", Lang: "en", Group: "s-link-gh", Featured: true,
			LinkURL: "https://github.com/ccvar/gcms", Date: "2026-06-12",
			Excerpt: "gcms source, docs and issues.", MetaDesc: "gcms open-source repository", Keywords: "gcms,GitHub,open source",
			Content: md("Source code, docs and issue tracking for gcms. Stars, issues and PRs welcome.")},
		{Type: "link", Slug: "demo", Title: "Live Demo", Cat: "project", Lang: "en", Group: "s-link-demo", Featured: true,
			LinkURL: "https://cms.ccvar.com", Date: "2026-06-12",
			Excerpt: "The site you're reading is gcms itself.", MetaDesc: "gcms live demo", Keywords: "gcms,demo",
			Content: md("The best introduction is to use it. This demo site runs on gcms itself — every page you see is rendered by it.")},
		{Type: "link", Slug: "releases", Title: "Downloads", Cat: "project", Lang: "en", Group: "s-link-rel", Featured: true,
			LinkURL: "https://github.com/ccvar/gcms-releases/releases/latest", Date: "2026-06-11",
			Excerpt: "Prebuilt packages for every platform.", MetaDesc: "gcms downloads for every platform", Keywords: "gcms,download,release",
			Content: md("Prebuilt deploy packages for linux / macOS / windows — binary, scripts and a default config. Download, unpack, run.")},
		{Type: "link", Slug: "go", Title: "Go", Cat: "stack", Lang: "en", Group: "s-link-go", Featured: true,
			LinkURL: "https://go.dev", Date: "2026-06-10", Cover: "/assets/covers/go.svg",
			Excerpt: "The language that compiles gcms into one file.", MetaDesc: "The Go programming language", Keywords: "Go,golang",
			Content: md("gcms is built with Go: one compile yields a zero-dependency static binary, and cross-compiling to any platform is trivial.")},
		{Type: "link", Slug: "sqlite", Title: "SQLite", Cat: "stack", Lang: "en", Group: "s-link-sqlite", Featured: true,
			LinkURL: "https://sqlite.org", Date: "2026-06-10", Cover: "/assets/covers/sqlite.svg",
			Excerpt: "gcms's single-file database.", MetaDesc: "SQLite official site", Keywords: "SQLite,database",
			Content: md("gcms keeps its data in a single SQLite file: zero-config, rock solid, and backup is copying one file.")},
		{Type: "link", Slug: "caddy", Title: "Caddy", Cat: "stack", Lang: "en", Group: "s-link-caddy",
			LinkURL: "https://caddyserver.com", Date: "2026-06-09",
			Excerpt: "The recommended reverse proxy, automatic HTTPS.", MetaDesc: "Caddy web server", Keywords: "Caddy,reverse proxy,HTTPS",
			Content: md("In production, put Caddy in front of gcms as a reverse proxy: automatic certificates, HTTP/3 and compression out of the box.")},
	}
}
