# CCVAR 简记 · 轻量 CMS

用 **Go + SQLite** 构建的极简内容管理系统：简约大气、100% 服务端渲染、完全适配 SEO，最终交付为**单一静态二进制 + 一个数据库文件**。

---

## 快速开始

```bash
# 需要 Go 1.23+（本机已装 go1.26）
go run .
# 打开 http://localhost:8080
# 后台 http://localhost:8080/admin  默认账号 admin / admin123
```

首次启动会自动在 `data/cms.db` 建库并写入演示内容（分类、文章、关于页、管理员账号）。

### 一键启停脚本（自动装环境）

跨平台启停脚本，`start` 会**自动检测 Go**（缺失或低于 1.23 时自动下载官方工具链到项目内 `.go/`，不污染系统），随后构建并后台运行，PID 与日志写入 `run/`。

```bash
# macOS / Linux
./scripts/cms.sh start      # 启动（自动装 Go → 构建 → 后台运行）
./scripts/cms.sh status     # 查看状态
./scripts/cms.sh restart    # 重启
./scripts/cms.sh stop       # 停止
./scripts/cms.sh logs       # 跟踪日志
# 可覆盖：ADDR=:9090 BASE_URL=https://cms.ccvar.com ./scripts/cms.sh start
```

```powershell
# Windows（PowerShell）
.\scripts\cms.ps1 start      # 自动尝试 winget / choco，再回退官方 zip
.\scripts\cms.ps1 restart
.\scripts\cms.ps1 stop
```

### 编译为单一二进制

```bash
go build -o cms .
./cms                       # 直接运行，模板与静态资源已 embed 进二进制
# 交叉编译到 Linux 服务器（纯 Go，无需 CGO）：
GOOS=linux GOARCH=amd64 go build -o cms .
```

### 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `ADDR` | `:8080` | 监听地址 |
| `CMS_DB` | `data/cms.db` | SQLite 文件路径 |
| `BASE_URL` | `http://localhost:8080` | 站点绝对地址（用于 canonical / OG / sitemap）。**生产环境务必设为 `https://cms.ccvar.com`** |
| `GCMS_RELEASE_REPO` | `ccvar/gcms-releases` | 后台检查更新使用的公开发布仓库 |
| `GCMS_UPDATE_URL` | `https://github.com/ccvar/gcms-releases/releases/latest/download/manifest.json` | 自定义更新清单地址，留空则按发布仓库自动拼接 |

### 使用 Caddy 作为入口

生产环境建议让 CMS 只监听本机回环地址，由 Caddy 负责 HTTPS、HTTP/2/3、压缩与静态资源缓存：

```bash
ADDR=127.0.0.1:8080 BASE_URL=https://cms.ccvar.com ./scripts/cms.sh start
```

示例 `Caddyfile`：

```caddyfile
cms.ccvar.com {
    encode zstd gzip

    header /assets/* Cache-Control "public, max-age=31536000, immutable"
    header /uploads/* Cache-Control "public, max-age=2592000"

    reverse_proxy 127.0.0.1:8080
}
```

`/assets/` 的 URL 自带内容指纹参数，适合长缓存；`/uploads/` 是用户上传文件，建议缓存时间短一些。动态 HTML、RSS 与 sitemap 由应用生成，保持经 Caddy 反代即可。

### 升级目录规划

为了支持后续后台一键升级，生产部署建议把“程序版本”和“用户数据”分开：

```
/opt/gcms/
├── current -> releases/v1.0.3
├── releases/
│   ├── v1.0.2/
│   └── v1.0.3/
├── shared/
│   ├── data/
│   │   ├── cms.db
│   │   └── uploads/
│   └── cms.conf
├── backups/
└── tmp/
```

`current` 指向当前运行版本；`releases/` 保存历史版本；`shared/` 保存数据库、上传文件和配置，升级时不覆盖；`backups/` 保存升级前数据库备份。服务启动时可让 `CMS_DB` 指向共享数据库，例如：

```bash
CMS_DB=/opt/gcms/shared/data/cms.db ADDR=127.0.0.1:8080 BASE_URL=https://cms.ccvar.com /opt/gcms/current/bin/cms
```

后台「设置 → 系统更新」会显示当前版本、最新 GitHub Release、当前平台对应的发布包、SHA256 与校验文件。完整一键升级会在此目录结构基础上实现下载、校验、备份、切换、重启与失败回滚。

### 公开发布仓库

源码仓库保持私有，二进制发布到公开仓库 `ccvar/gcms-releases`。后台更新检测默认读取：

```text
https://github.com/ccvar/gcms-releases/releases/latest/download/manifest.json
```

私有源码仓库推送 `v*` tag 后，GitHub Actions 会交叉编译各平台部署包，生成 `checksums.txt` 与 `manifest.json`，再把这些文件发布到公开仓库的 GitHub Release。

需要在私有源码仓库配置 Actions Secret：

| Secret | 用途 |
|--------|------|
| `GCMS_RELEASE_TOKEN` | GitHub fine-grained token，仅授予公开仓库 `ccvar/gcms-releases` 的 `Contents: Read and write` 权限，用于创建 Release 与上传二进制产物 |

`manifest.json` 是后台升级链路的稳定协议，包含版本号、Release 地址、各平台包的 `os` / `arch` / 下载 URL / SHA256 / 文件大小。这样用户部署环境不需要访问私有源码仓库，也不需要配置 GitHub token。

公开仓库需要至少有一个 `main` 分支初始提交（例如 README），Release workflow 会把公开仓库里的版本 tag 挂到 `main` 上。

---

## 项目结构

```
cms.ccvar.com/
├── main.go                  # 入口：embed 资源、装配、启动 HTTP
├── go.mod / go.sum
├── internal/
│   ├── store/               # SQLite：模型、迁移、查询、种子数据
│   │   ├── store.go
│   │   └── seed.go
│   ├── seo/                 # 每页 meta / OG / Twitter / JSON-LD 构建器
│   │   └── seo.go
│   ├── i18n/                # 多语种：语种注册表 + 界面文案目录 + 翻译助手 Tr
│   │   ├── i18n.go
│   │   └── locales/         #   各语种界面文案（zh.json / en.json …）
│   └── web/                 # HTTP：渲染器、公开处理器、后台、会话
│       ├── render.go        #   html/template 解析 + Markdown(goldmark) + 模板函数
│       ├── web.go           #   locale 中间件 + 路由 + 首页/文章/分类/关于/搜索/404 + sitemap/rss/robots
│       └── admin.go         #   会话/CSRF、bcrypt 登录、文章 CRUD、互译关联
├── templates/               # 服务端模板（由第一阶段静态页平移而来）
│   ├── layout.html
│   ├── partials/            #   head（SEO）/ header / footer
│   ├── home / article / category / page / search / 404 .html
│   └── admin/               #   layout / login / dashboard / edit / settings
├── assets/                  # embed 进二进制
│   ├── css/style.css        # 全站唯一样式表（10 套主题）
│   ├── favicon.svg          # 默认站点图标
│   └── js/
│       ├── toc.js           # 公开页：页眉测量 / 阅读进度 / 回顶 / 大纲高亮
│       └── admin.js         # 后台：上传 / 自定义下拉 / 主题微调 / 富文本编辑器
├── data/
│   ├── cms.db               # SQLite（运行时生成）
│   └── uploads/             # 用户上传图片（运行时，经 /uploads/ 提供）
└── data/cms.db              # 运行时生成（已 gitignore）
```

> 仓库根目录的 `index.html`、`article.html` 等是**第一阶段的静态设计稿**，仅作视觉参考；正式实现已全部迁移到 `templates/`。确认无需后可删除它们。

---

## 功能

**公开站点**（全部服务端渲染）
- 首页（精选 + 最新列表 + 分页）、文章详情（上一篇/下一篇、相关阅读）、分类归档、关于页、站内搜索、404
- **链接（资源/产品展示）** `/{lang}/links`：卡片网格列表（封面或首字母字形）+ 分类筛选，详情页含大图/介绍/详情正文/**「访问 ↗」外链按钮**/相关推荐，每条独立 SEO 与结构化数据。**首页「精选链接」模块仅在有置顶链接时出现**（无置顶则整块隐藏）；演示数据内置 Go·SQLite·MDN·Coolors 四张品牌化 SVG 封面（`assets/covers/`）
- **多语种**：每个语种独立 URL 前缀 `/{lang}/…`（如 `/en/posts/…`），页眉语言切换器，界面文案与内容均按语种本地化（详见下方「多语种」）
- 基于 slug 的干净 URL：`/{lang}/posts/{slug}`、`/{lang}/category/{slug}`（slug 各语种独立，`(lang, slug)` 复合唯一）
- 文章正文用 Markdown 撰写，goldmark 渲染（支持表格、代码块等 GFM 扩展）
- **文章大纲（TOC）**：自动从 h2/h3 提取，桌面端粘性右栏、移动端可折叠；标题锚点保留中文（如 `#数据怎么建模`），便于分享。粘性偏移由 JS 实测页眉高度（`--header-h`）自适应，**任意主题都不被页眉遮挡**
- **阅读进度条 + 回到顶部**：文章页顶部进度条随滚动填充，下滑后右下角出现回到顶部按钮（均为渐进增强 JS）
- **文章封面图**：编辑器可上传/粘贴封面，首页精选与文章详情自动以 `<img>` 呈现

**SEO（每页动态生成，已实测通过）**
- 每页独立 `title` / `description` / `canonical` / `robots`
- Open Graph + Twitter Card（文章含 `article:published_time` 等）
- JSON-LD：首页 `WebSite`+`Organization`、文章 `BlogPosting`+`BreadcrumbList`、分类 `CollectionPage`、关于 `AboutPage` —— 均为合法 JSON，`inLanguage` 随语种
- **多语种 SEO**：每页 `<html lang>`、自指 `canonical`、`hreflang` 备份链接（含 `x-default`，仅在真有译文时输出）、`og:locale` + `og:locale:alternate`
- 动态 `/sitemap.xml`（多语种，互译版本用 `xhtml:link` 标注 hreflang）、按语种的 `/{lang}/rss.xml`、`/robots.txt`
- 搜索页 / 404 / 后台自动 `noindex`

**后台 `/admin`**
- 基于 Cookie 会话登录（密码 bcrypt 存储），SameSite=Lax + CSRF 令牌防护
- 文章列表、新建 / 编辑 / 删除；保存后留在编辑页并提示成功（PRG）；所有保存按钮提交后禁用并显示「处理中…」防重复
- **编辑页吸顶操作栏**：标题与「查看 / 取消 / 保存」同处顶栏，向下滚动时**固定在页面上方**始终可点
- **脏检查**：表单内容与初始一致（未发生改动）时「保存」按钮**自动置灰禁用**，任一字段 / 下拉 / 上传 / 正文变化即恢复可点（改回原值会再次禁用）
- **所有删除动作统一为自定义确认弹层**（替代系统 `confirm`，捕获阶段拦截提交、红色危险按钮、点遮罩/取消/Esc 关闭），后台删除图标也统一为同一个垃圾桶图标
- **后台移动端体验**：顶栏在窄屏折叠为汉堡下拉菜单，表格横向滚动不撑破布局，操作栏纵向堆叠
- 文章列表行**悬停出现灰色置顶星，点击置顶并点亮**；可置顶多篇——首页「精选」展示为「大卡 + 卡片网格」（无置顶则取最新）
- 分类支持**拖动排序**（`position` 列），顺序影响前台分类导航；新增/编辑走模态框
- 图片上传交互：**点击占位即选图，删除按钮浮于图片右上角且仅在有图时显示**
- 编辑器**记住上次用 Markdown 还是富文本**，下次进入自动切到对应方式；富文本**粘贴/插入图片采用「延迟上传」**——先以本地 blob 占位预览，**点击保存时才统一转 WebP 上传**并回填正式地址（避免编辑中途产生孤儿文件）；表格行列把手在悬停单元格时就近出现
- **发布状态**：草稿 / 立即发布 / **定时发布**（到点由每分钟定时器自动转为已发布；未到点前台不可见）
- **链接管理 `/admin/links`**：与「文章」平级的内容类型（`type=link` + `link_url` 列），编辑页复用富文本/封面/SEO/多语种/置顶/翻译，仅多出「链接地址」字段；链接分类与文章分类**相互独立**（`categories.kind`，在「设置 → 分类」用 文章/链接 切换维护）
- **页面管理 `/admin/pages`**：维护「关于」等独立页面，复用同一套编辑器
- **多语种维护**：文章列表 / 页面 / 分类 / 文案均带「内容语种」切换标签，按语种独立维护；编辑页可一键「翻译为 X」生成互译版本（自动共享 `trans_group`、映射对应分类），并列出已有的各语种版本
- 分类管理在「设置 → 分类」增删改（**按语种**）；favicon/logo + 页眉品牌（Logo / Logo+名称 / 仅名称）
- **双模式编辑器**：Markdown ⇄ Medium 式富文本一键切换。富文本支持选中浮动气泡工具栏（粗体/斜体/H2/H3/引用/链接，**链接为自写弹层**非系统 prompt）、空行加号菜单（插入图片/**表格**/分割线，全 SVG 图标）。**存储始终为 Markdown**（进入富文本时服务端 goldmark 渲染 MD→HTML，保存前前端 HTML→MD 写回，含表格↔GFM 与图片），SEO 与大纲不受影响
- 富文本编辑器**所见即所得**：正文字号 / 行高 / 段距 / 标题与引用样式与前台文章 `.prose` 完全一致；气泡工具栏在**选区完成时**（松开鼠标 / Shift+方向键）出现，避免拖选过程被打断
- **块拖动排序**：富文本中悬停段落 / 标题 / 引用 / 图片 / 表格等「块」，左侧浮出拖拽把手，按住即可上下调整顺序（把手与指示线浮于编辑器之外，不写入正文）
- 首页各栏目 H2 标题（精选文章 / 精选链接 / 最新文章）可在「设置 → 文案」**按语种自定义**（留空回落语种默认）
- 文章封面上传 + 独立 SEO 字段（slug、摘要、meta description、关键词、分类、作者）
- **自定义下拉组件**替代原生 `<select>`（状态、分类），跨平台样式一致；后台图标统一 SVG
- 图片上传 `/admin/upload` → `data/uploads/`（限 8MB，类型白名单含 svg/ico）。**前端在浏览器支持时先把 png/jpg 转 WebP** 再上传
- **设置页 `/admin/settings`**：左侧菜单分区，**各区独立保存**——
  - `站点信息`（`/site`）：站名 / 标语 / 描述 / **favicon / logo**（上传或 URL）/ **社交链接**（页脚「关注」栏，可增删，图标按域名自动识别 GitHub·X·YouTube·Telegram·LinkedIn·邮箱等，存 `social_links`）
  - `外观与主题`（`/appearance`）：10 套主题单选 + 预览色板 + **按主题各自保存的可视化微调**（主色取色器、圆角滑杆，存 `theme.<id>.*`，切卡时控件随主题同步，以内联 CSS 变量覆盖当前主题默认）；**首页 Hero 右侧视觉可替换**：默认动画 / 上传图片或 SVG 文件 / 直接粘贴 SVG 代码（存 `hero.visual`·`hero.image`·`hero.svg`）
  - `文案`（`/copy`）：首页 hero 眉标/大标题、标语、描述、页脚说明等前台文案可编辑，**按语种切换标签分别维护**（非默认语种存 `site.x::<lang>`，留空回落默认语种）；字段按「首页 Hero / 站点描述 / 页脚」分组展示
  - `导航`（`/menu`）：**页眉菜单构建器**——自定义每项名称、**拖动排序**、**每语种单独命名**（存 `nav_menu` JSON）；内部路径自动加语种前缀，外部 `https://…` 新窗口打开；未配置时回落默认菜单（首页/分类/关于）
  - 左侧分区菜单每项带 SVG 图标
  - `语言`（`/languages`）：勾选启用的语种、指定默认语种（`/` 的跳转目标与回落语种）；内置 7 种之外可**新增自定义语种预设**（代码/名称/BCP47 标记/OG locale，存 `custom_locales`），自定义语种的界面文案回落默认语种
  - `分类`（`/categories`）：列表 + 「新增分类」**模态框**增删改（**按语种**）
  - `安全`（`/security`）：在线改密（校验当前密码、新密码 ≥6 位、两次一致）

### 前台主题（10 套，布局风格各异，非简单换色）

在设置页切换，存于 `settings.theme`，服务端渲染即时生效（`<html data-theme="…">`，无闪烁）：

| 主题 | 风格 | 布局差异要点 |
|------|------|---------|
| `editorial` | 编辑 · 暖色衬线（默认） | 左侧刊名、左右双栏 hero、单列大字号编号列表 |
| `magazine` | 杂志 · 无衬线 | 居中刊头、居中 hero、三列卡片网格 |
| `terminal` | 极客 · 深色等宽 | `~/` 刊名 + `[方括号]` 导航、`//`·`>` 命令行装饰、终端清单 |
| `brutalist` | 粗野 · 黑白 | 粗黑边框、硬阴影（offset）、无圆角、电光蓝、双列卡 |
| `notebook` | 手账 · 米黄 | 横格纸背景、衬线斜体、虚线分隔、✦ 标记、暖橙 |
| `swiss` | 瑞士 · 国际主义 | 红黑、细黑分隔线、巨号红色编号、严格无衬线 |
| `pastel` | 柔彩 · 浅彩 | 渐变背景、渐变标题、大圆角卡片、圆形分页、紫粉 |
| `newspaper` | 报纸 · 衬线 | 居中刊头双线、小型大写导航、灰度封面、**多栏流式列表** |
| `darkpro` | 暗夜 · 现代暗色 | 靛蓝/品红渐变、卡片网格、霓虹悬停、毛玻璃页眉 |
| `landing` | 官网 · 产品落地页 | 大居中 hero + CTA 按钮 + 特性卡片网格，首页像产品官网 |

首页 hero 右侧的科技感图形（SSR 窗口 + 轨道数据流）为纯 SVG/CSS，随主题 token 自动变色，并尊重 `prefers-reduced-motion`；如需也可在「外观与主题」里替换为自定义图片、SVG 文件或内联 SVG 代码。

> 默认 `admin / admin123`。除设置页外，也可直接用新 bcrypt 哈希更新 `settings` 表的 `admin_password_hash`。

### 多语种（i18n）

开箱演示中英双语；URL 形如 `/zh/…`、`/en/…`，访问 `/` 自动跳默认语种。

- **路由**：一层 `locale` 中间件识别并剥掉路径里的语种前缀写入 `context`，再交给原始 `ServeMux`——**现有 30+ 条路由零改动**。`/admin`、`/assets`、`/sitemap.xml`、`/robots.txt` 不参与前缀。
- **内容模型**：`posts` / `categories` 各加 `lang` 与 `trans_group` 两列；同一逻辑内容的各语种版本是**独立的行**、共享 `trans_group` 关联（用于语言切换与 hreflang）。slug 改为 `(lang, slug)` 复合唯一，故 `/zh/about` 与 `/en/about` 可并存、各语种 slug 互不影响。
- **界面文案**：`internal/i18n/locales/<code>.json` 目录，模板用 `{{.Tr.T "key"}}` 取文案、`{{.Tr.U "/path"}}` 加语种前缀、`{{.Tr.Date}}` 本地化日期——模板集合仍只解析一次、全站共享，语种随请求数据 `Tr` 流动。新增语言只需加一个 JSON。
- **站点文案**：站名/标语/描述/hero/页脚按语种存储（非默认语种存 `site.x::<lang>`，留空回落默认语种）。
- **语言切换器**：页眉零 JS 的 `<details>` 下拉；文章/分类/关于切换时跳到**对应译文**（无译文则回该语种首页，且不对其输出 hreflang）。
- **启用与默认**：在「设置 → 语言」勾选启用语种、指定默认语种；内置 `zh / en / ja / ko / fr / de / es`。
- **平滑迁移**：旧库（slug 全局唯一、无 `lang`）首启时自动整表重建为多语种结构，存量内容归默认语种 `zh`、原内容经 `/zh/…` 继续可达——**向后兼容、零数据丢失**（已用真实旧库实测）。

---

## 技术选型

- **路由**：标准库 `net/http`（Go 1.22+ `ServeMux`，方法 + 路径参数，零第三方路由）+ 自写 locale 前缀中间件
- **数据库**：`modernc.org/sqlite`（纯 Go，免 CGO，开启 WAL）
- **Markdown**：`github.com/yuin/goldmark`
- **多语种**：自建轻量 `internal/i18n`（embed JSON 文案目录 + 每请求 `Tr` 助手 + 语种注册表），零第三方依赖
- **密码**：`golang.org/x/crypto/bcrypt`
- **资源打包**：`embed.FS`

## 可选的后续增强

- 标签系统（目前用关键词字段兼作标签）
- 评论 / 浏览量统计
- 更多主题，或把主题做成可视化定制
- 自动按 `Accept-Language` 协商首选语种（目前 `/` 固定跳默认语种，由 hreflang/x-default 引导爬虫）

数据模型已为这些预留空间；前端样式如需调整，集中在 `assets/css/style.css` 与 `templates/`。
