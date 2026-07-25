# GCMS 自由页面平台设计规格

> 状态：Proposed（后续实现基线）  
> 文档版本：0.1  
> 更新日期：2026-07-25  
> 适用范围：gcms 服务端、gcms 后台、自动化 API、AI 技能包、Pilot、前台渲染与 Cloudflare 静态发布

## 0. 文档用途

这份文档用于冻结“标准页面 + 自由编排页面 + 互动应用”的产品和技术方向，防止后续开发因上下文缺失而重新解释目标。

实施时遵循以下规则：

1. 本文的“必须”“不得”“默认”属于实现约束。
2. 如果实现过程中需要改变核心决策，先更新本文的“决策记录”，再修改代码。
3. 不允许为了快速演示，把自由页面源码塞进现有 `posts.content`，也不允许让 Pilot 通过模拟点击后台完成操作。
4. 每个阶段都必须先通过老数据库升级测试和标准页面视觉回归，才能进入下一阶段。
5. 未在本文授权的能力默认关闭；尤其是外部网络、数据写入、发布和破坏性操作。

---

## 1. 一句话目标

gcms 是同一套页面能力、数据、权限、版本与发布流程的所有者：

- 人在 gcms 后台通过表单、可视化编排和高级编辑器手动操作；
- 人在 Pilot 端只通过自然语言对话，让 AI 调用技能包和 API 完成同样的操作；
- 两端始终编辑同一份页面，不产生“后台版”和“AI 版”两套数据；
- 现有客户升级后，旧页面、旧主题、旧 API 和旧技能包行为保持不变。

```text
                     ┌─────────────────────────────┐
                     │  GCMS 页面领域能力与数据层  │
                     │  数据 / 版本 / 权限 / 发布   │
                     └──────────────┬──────────────┘
                                    │
                   ┌────────────────┴────────────────┐
                   │                                 │
        ┌──────────▼──────────┐           ┌──────────▼──────────┐
        │ GCMS 后台（人工）    │           │ Pilot（纯对话）      │
        │ 表单 / 编排 / 预览   │           │ AI 技能包 / API      │
        └─────────────────────┘           └─────────────────────┘
```

---

## 2. 已冻结的核心决策

### 2.1 三种页面模式

| 模式 | 内部值 | 主要用途 | 渲染方式 |
|---|---|---|---|
| 标准页面 | `standard` | 关于、帮助、协议、普通内容页 | 保留现有 Markdown/富文本与 `page.html` |
| 自由编排页面 | `composition` | 宣传页、活动页、专题页、产品落地页、轻量计算器 | 结构化 Manifest + gcms 组件渲染器 |
| 互动应用 | `app` | 小游戏、测评、交互演示、复杂计算器 | 隔离的静态 HTML/CSS/JS 应用包 |

### 2.2 双入口、单能力

- gcms 后台拥有完整人工操作能力。
- Pilot 只提供对话，不复制 gcms 的复杂表单。
- 后台和 Pilot 调用同一服务层、校验器、版本机制和发布机制。
- Pilot 不通过浏览器自动化模拟后台点击。

### 2.3 老页面零转换

- 现有 `type=page` 的记录全部继续视为 `standard`。
- 升级时不批量转换、不重写 `content`、不重新保存 Markdown、不改变 URL。
- 只有用户主动新建自由页面或明确发起转换，才创建新的页面工程记录。

### 2.4 自由编排不等于任意 HTML

- `composition` 保存结构化、可校验、可迁移的页面 Manifest。
- AI 和人工操作的是组件、属性、响应式规则和数据绑定。
- 不把任意 HTML、CSS、JavaScript 混入普通正文。

### 2.5 互动应用必须隔离

- `app` 只运行前端静态资源，不允许在 gcms 服务器执行 AI 生成的程序。
- 默认无网络、无 Cookie、无站点后台访问、无任意存储。
- 需要表单、内容读取、外部请求等能力时，必须显式授权。
- 发布版本使用不可变构建产物和内容哈希。

### 2.6 保存不等于发布

- 修改工作副本只产生新修订。
- 预览只读取指定修订。
- 发布是单独动作，必须记录发布者、来源和目标修订。
- Pilot 默认只能创建/编辑草稿和生成预览；发布需要明确授权和用户确认。

### 2.7 保持 gcms 低维护属性

- `composition` 不引入 Node、npm 或服务器端前端构建链。
- `app` 第一版采用可直接运行的静态 HTML/CSS/JS 包。
- Pilot 可以在本地工作区生成资源包，gcms 负责校验、存储、预览和发布。
- 未来支持框架构建时，构建发生在明确隔离的构建环境，不成为 gcms 服务端运行依赖。

---

## 3. 当前系统基础与缺口

### 3.1 可以复用的基础

当前仓库已经具备：

- `posts` 中统一的标题、Slug、语言、SEO、状态和正文数据；
- 标准页面的 Markdown/富文本编辑和固定模板渲染；
- 多语言 `trans_group`；
- 内容修订与回滚；
- 扩展内容类型及结构化字段 Schema；
- 带 scope 的自动化 API 和 AI 技能包；
- API 操作日志；
- 站点私有预览；
- Cloudflare 静态站导出与发布；
- Pilot 中已有“AI 建页面 → 本地预览 → 部署 → 存模板”的独立建站工作流。

相关实现入口：

- `internal/store/store.go`：`Post`、修订和 SQLite 迁移；
- `templates/admin/edit.html`：现有标准内容编辑器；
- `templates/page.html`：现有标准页面模板；
- `internal/web/content_types.go`：扩展类型 Schema；
- `internal/web/api.go`：内容自动化 API；
- `internal/web/control_api.go`：能力发现、风险级别、确认和解锁模式；
- `internal/web/cloudflare.go`：静态发布链路；
- `desktop/src/routes/+page.svelte`：Pilot 的对话、预览和部署入口。

### 3.2 当前缺口

当前“页面”仍然是固定结构：

- 编辑器只有 Markdown/富文本；
- `posts.content` 表达正文，不表达完整页面结构；
- 前台统一进入固定 `page.html`；
- 扩展内容类型解决“数据字段可扩展”，没有解决“页面布局和交互可扩展”；
- Pilot 的自由建站工作区与 gcms 页面、SEO、多语言、版本和发布记录尚未统一；
- 公开页 CSP 当前主要是报告模式，不能作为互动应用的最终安全边界；
- 后台标准页面保存目前直接保持已发布，缺少清晰的“工作副本 → 预览 → 发布修订”语义。

---

## 4. 产品目标与非目标

### 4.1 产品目标

1. 不使用 Pilot 时，用户能在 gcms 后台独立完成页面创建、编辑、预览、发布和回滚。
2. 使用 Pilot 时，用户全程只需对话，不需要理解 API、JSON 或文件结构。
3. 人工和 AI 可以交替编辑，双方总能看到对方最新保存的修订。
4. 宣传页可以绑定商品、文章、案例等后台真实数据，而不是硬编码展示内容。
5. 页面在桌面、平板、手机和后台宽度预览中正常适配。
6. 自由页面能够随站点主题继承颜色、字体、圆角、导航和页脚。
7. 互动应用在可控权限下运行，不能获得服务器或后台的隐式访问权。
8. Cloudflare 静态导出包含自由页面及其不可变资源。
9. 老客户升级无须手动迁移，旧页面视觉和行为不变。

### 4.2 非目标

第一版不做：

- 通用云端 IDE；
- 在 gcms 服务器运行任意 Node/Python/Go 代码；
- 允许 AI 修改 gcms 自身模板、二进制或生产服务器文件；
- 完整替代 Figma、Webflow 或专业游戏引擎；
- 多人实时光标协作；
- 任意 npm 依赖在线安装；
- 自由页面跨站共享数据库写权限；
- 未经确认由 AI 直接覆盖线上版本；
- 把所有主题都改造成自由页面专用主题。

---

## 5. 术语

| 术语 | 含义 |
|---|---|
| 页面记录 | 现有 `posts` 中 `type=page` 的记录，保存公共元数据 |
| 页面工程 | 仅自由页面拥有的结构、源码、能力和构建信息 |
| 工作修订 | 当前正在编辑、尚未正式发布的修订 |
| 发布修订 | 当前公开 URL 正在使用的不可变修订 |
| Manifest | 描述页面结构、组件、绑定、样式和能力的版本化 JSON |
| 组件注册表 | gcms 支持的组件类型、属性 Schema 和渲染器集合 |
| 数据绑定 | 页面组件查询 gcms 内容的结构化描述，不是内容快照 |
| 应用包 | `app` 页面的静态 HTML/CSS/JS/资源集合 |
| 能力 | 应用被允许使用的受控接口，例如表单提交或内容只读 |
| Site Shell | 站点导航、页脚、全局字体和主题 Token 外壳 |
| Pilot 会话来源 | 标记某次修订来自哪个连接、对话和请求 |

---

## 6. 产品信息架构

### 6.1 页面列表

现有“页面”列表继续作为统一入口，增加：

- 类型：标准页面 / 自由页面 / 互动应用；
- 状态：草稿 / 已发布 / 有未发布修改 / 构建失败；
- 数据：静态内容 / 绑定商品 N 项 / 绑定文章查询等摘要；
- 来源：人工 / Pilot；
- 最后修改人和时间；
- 预览、编辑、复制、历史、发布操作。

示例：

| 页面 | 类型 | 数据 | 状态 | 最近来源 |
|---|---|---|---|---|
| 关于 gcms | 标准页面 | 静态正文 | 已发布 | 人工 |
| 夏季新品 | 自由页面 | 商品查询 | 有未发布修改 | Pilot |
| 成本计算器 | 互动应用 | 本地计算 | 草稿 | 人工 |
| 品牌小游戏 | 互动应用 | 本地状态 | 待发布 | Pilot |

列表筛选至少包含：

- 页面模式；
- 发布状态；
- 语言；
- 创建/修改来源；
- 构建状态；
- 搜索标题与 Slug。

### 6.2 新建页面

点击“新建页面”后选择：

1. 标准页面；
2. 自由编排页面；
3. 互动应用（标记“高级”）。

共同填写：

- 标题；
- Slug；
- 语言；
- 是否加入导航（默认否）；
- Site Shell 模式；
- 初始状态（默认草稿；兼容旧标准页面行为见第 17 节）。

Slug 校验必须覆盖当前语言下的页面/文章/扩展内容、系统保留路径、管理/API 路径和多语言前缀，不能只检查页面表内部重复。

创建成功后进入对应编辑器。

### 6.3 标准页面编辑器

保持现有布局和字段，不强迫用户学习新概念：

- 标题；
- Markdown/富文本正文；
- 摘要、作者、SEO、语言、Slug；
- 查看、保存、历史。

可以增加但不得干扰旧流程：

- “标准页面”类型标识；
- 当前发布状态；
- “基于副本转换为自由页面”操作；
- 转换前影响说明。

转换规则：

- 标准页面转换先创建自由编排工作修订，公开页面继续显示原标准正文；
- 转换事务先保存一条不受普通修订裁剪影响的不可变 `standard_baseline`，包含原正文和公共元数据；
- 转换预览和人工确认通过后，发布第一条自由修订才切换公开渲染方式；
- 原正文和标准页面历史必须保留，可回滚到标准渲染；
- 自由编排页面与互动应用之间不原地改 `mode`，使用“复制为新页面/新工程”，避免两种安全模型混杂。

### 6.4 自由编排页面编辑器

采用三栏工作台：

```text
┌──────────────────────────────────────────────────────────────────┐
│ 面包屑 / 页面标题                  设备预览  保存  预览  发布     │
├───────────────┬────────────────────────────────┬─────────────────┤
│ 页面结构       │                                │ 当前项属性       │
│ Hero          │         实时页面画布            │ 内容             │
│ 产品优势       │                                │ 样式             │
│ 商品列表       │                                │ 数据绑定         │
│ 客户评价       │                                │ 响应式           │
│ CTA           │                                │ 可见条件         │
│               │                                │                 │
│ + 添加区块     │                                │ 页面设置         │
└───────────────┴────────────────────────────────┴─────────────────┘
```

人工操作必须支持：

- 新增、复制、删除、排序区块；
- 修改组件内容和视觉属性；
- 选择数据来源和查询条件；
- 设置桌面/平板/手机布局；
- 切换 Site Shell；
- 编辑页面级 SEO、语言和导航；
- 查看验证问题；
- 生成预览；
- 查看修订差异；
- 发布指定修订；
- 回滚到历史发布版本。

第一版不要求完全自由拖拽坐标。采用“结构树 + 受控布局属性”，优先保证响应式和可维护性。

### 6.5 互动应用编辑器

默认界面仍以预览和配置为主：

- 左侧：文件列表或应用结构；
- 中间：沙箱实时预览；
- 右侧：页面设置、能力、资源、错误；
- 底部高级区：HTML/CSS/JS 编辑、验证日志。

人工操作支持：

- 新建基础应用；
- 上传或替换应用包；
- 编辑 `index.html`、CSS 和 JS；
- 管理图片、音频等资源；
- 配置输入法、触控、音效等纯客户端能力；
- 申请或撤销表单、内容读取、外部网络等敏感能力；
- 预览、验证、发布和回滚。

不得在后台默认展示服务器目录或允许访问站点私有文件。

### 6.6 历史与审核

每个自由页面提供统一历史：

- 修订号；
- 来源：后台 / Pilot / API；
- 操作者或自动化 Key；
- Pilot 对话 ID（如有）；
- 修改摘要；
- 校验结果；
- 是否曾发布；
- 发布时间和发布人；
- 与当前修订的结构化差异；
- 回滚操作。

---

## 7. Pilot 的纯对话体验

### 7.1 Pilot 不提供第二套表单

Pilot 可以显示：

- 对话；
- 任务进度；
- 预览；
- 变更摘要；
- 确认卡片；
- 构建/校验错误；
- 页面链接。

Pilot 不复制：

- 页面属性面板；
- 拖拽画布；
- SEO 完整表单；
- 文件管理后台；
- 导航设置后台。

用户需要精细手工操作时，Pilot 提供“在 gcms 后台打开”链接。

### 7.2 标准对话流程

```text
用户描述目标
→ AI 读取站点能力、主题、内容类型和相关数据
→ AI 输出简短方案
→ AI 创建/修改工作修订
→ GCMS 校验
→ AI 生成私有预览
→ AI 汇报修改内容、数据绑定和风险
→ 用户继续修改或明确确认发布
→ AI 发布指定修订
→ AI 返回正式 URL 和发布摘要
```

示例：

```text
用户：
给“夏季新品”做一个宣传页，使用后台智能灯具分类的商品。
沿用当前主题，包含 Hero、卖点、商品列表和询盘表单。
先出草稿，不要发布。

AI：
已创建自由页面草稿：
- Hero 使用站点主色和当前品牌字体
- 商品列表绑定“智能灯具”分类，按发布时间倒序显示 4 项
- 询盘表单申请“站内表单提交”能力
- 已检查桌面、平板和手机布局
[打开预览] [在 gcms 后台编辑]
```

后续对话：

```text
用户：首屏图片缩小，手机端商品改成单列，再做英文版。
AI：读取最新修订后修改，不覆盖期间发生的后台人工调整。
```

### 7.3 人工与 AI 交替

必须支持以下流程：

1. Pilot 创建修订 A。
2. 人在 gcms 后台基于 A 保存修订 B。
3. Pilot 再次操作时先读取 B。
4. 如果 Pilot 仍基于 A 提交，API 返回 `409 revision_conflict`。
5. AI 读取最新修订并重新应用意图，不能强制覆盖。

### 7.4 Pilot 发布规则

- “看起来不错”“可以了”不能自动等同于发布，除非当前对话明确处于发布确认上下文。
- 推荐要求用户出现明确动作词：“发布”“上线”“定时发布到……”。
- 发布请求必须携带用户已预览或明确跳过预览的记录。
- 发布指定 `revision_id`，不能只写“发布当前页面”。
- 页面在确认后又发生变化时，旧确认失效，需要重新确认。

### 7.5 Pilot 与现有 Cloudflare 建站工作区

可以复用 Pilot 现有的本地项目、预览、视觉检查和静态构建能力，但 gcms 页面会话的交付路径固定为：

```text
Pilot 本地生成/检查
→ 上传 gcms 页面工程、Manifest 或不可变应用包
→ gcms 校验并创建修订
→ gcms 签发私有预览
→ 用户批准
→ gcms 发布页面修订
→ gcms 按站点策略调度 Cloudflare 交付
```

Pilot 不得在 gcms 页面任务中绕过 gcms 直接调用 Wrangler 发布，否则后台无法审计、接管或回滚该页面。

---

## 8. 领域模型

### 8.1 保留 `posts` 作为公共页面记录

`posts` 继续保存：

- `id`；
- `type=page`；
- `slug`；
- `title`；
- `excerpt`；
- `content`（仅标准页面正文）；
- SEO；
- 作者；
- 状态；
- 语言和翻译组；
- 发布时间；
- 创建/更新时间。

规则：

- 标准页面只使用 `posts`，不要求存在页面工程。
- 自由页面也拥有一条 `posts` 记录，用于 URL、SEO、多语言、列表、搜索和权限。
- 对已发布自由页面，`posts` 保存当前线上元数据；工作标题、摘要、SEO 和候选 Slug 保存在工作修订中，保存草稿不得提前改线上记录。
- 后台列表需要同时读取工作修订和发布修订，明确显示“线上标题/Slug”和“待发布修改”，不能把二者混为一谈。
- 自由页面的 Manifest、源码和构建产物不得放入 `posts.content`。
- 自由页面的 `content` 不是数据源，不得保存 Manifest、HTML、CSS 或 JavaScript。
- 服务端可以把 Manifest 提取成只读 Markdown/纯文本 fallback 写入 `content`，用于搜索、无障碍降级和旧程序读取；该内容必须标记为派生结果，任何时候都能由修订重新生成。

### 8.2 `page_projects`

每个自由页面最多一条工程记录：

```text
page_projects
- id                 INTEGER PRIMARY KEY
- post_id            INTEGER NOT NULL UNIQUE REFERENCES posts(id)
- mode               TEXT NOT NULL               # composition | app
- schema_version     INTEGER NOT NULL
- working_revision_id INTEGER
- published_revision_id INTEGER
- shell_mode         TEXT NOT NULL               # site | minimal | none
- build_status       TEXT NOT NULL               # idle | validating | ready | failed
- created_by         TEXT NOT NULL               # admin | api | pilot
- created_at         TEXT NOT NULL
- updated_at         TEXT NOT NULL
```

约束：

- `post_id` 必须指向 `type=page`。
- 不允许 `mode=standard` 的工程记录。
- `published_revision_id` 只能指向同一工程且校验成功的修订。
- 删除页面时，工程数据进入同一事务处理。
- `build_status` 只是后台列表缓存；真实构建状态以 `page_builds` 最新记录为准，防止两个状态漂移。

### 8.3 `page_project_revisions`

自由页面使用不可变修订：

```text
page_project_revisions
- id                 INTEGER PRIMARY KEY
- project_id         INTEGER NOT NULL
- revision_no        INTEGER NOT NULL
- parent_revision_id INTEGER
- revision_kind      TEXT NOT NULL               # standard_baseline | composition | app
- page_meta_json     TEXT NOT NULL               # 标题/摘要/SEO/Slug 等本修订快照
- page_meta_hash     TEXT NOT NULL
- manifest_json      TEXT NOT NULL
- manifest_hash      TEXT NOT NULL
- standard_content   TEXT NOT NULL DEFAULT ''     # 仅 standard_baseline
- source_bundle_ref  TEXT NOT NULL DEFAULT ''
- source_hash        TEXT NOT NULL DEFAULT ''
- origin             TEXT NOT NULL               # admin | pilot | api | restore
- actor_id           TEXT NOT NULL DEFAULT ''
- conversation_id    TEXT NOT NULL DEFAULT ''
- request_id         TEXT                         # NULL 表示非幂等导入
- summary            TEXT NOT NULL DEFAULT ''
- validation_json    TEXT NOT NULL DEFAULT '{}'
- created_at         TEXT NOT NULL
- UNIQUE(project_id, revision_no)
```

规则：

- 保存产生新修订，不原地改旧修订。
- `standard_baseline` 只在转换时生成，永久保留，使自由页面可以完整回到转换前的标准渲染。
- 自由页面的标题、摘要、SEO 和路由元数据与 Manifest 一起形成修订快照；否则编辑草稿标题会提前改变线上页面。
- 公开渲染和路由使用 `posts` 与发布修订；后台编辑使用工作修订；发布时把目标 `page_meta_json` 原子同步到 `posts`。
- `manifest_hash` 和 `source_hash` 使用稳定序列化后计算。
- `request_id` 用于幂等；同一项目、同一请求不得产生两个不同修订。
- `request_id` 使用 NULL 和部分唯一索引实现项目级幂等，不能让空字符串参与普通 UNIQUE 约束。
- Pilot 对话 ID 只作审计，不包含密钥和完整敏感提示词。
- 修订默认保留数量应可配置；已发布修订不得被自动裁剪。

### 8.4 `page_builds`

```text
page_builds
- id                 INTEGER PRIMARY KEY
- project_id         INTEGER NOT NULL
- revision_id        INTEGER NOT NULL
- status             TEXT NOT NULL               # queued | validating | ready | failed
- artifact_ref       TEXT NOT NULL DEFAULT ''
- artifact_hash      TEXT NOT NULL DEFAULT ''
- diagnostics_json   TEXT NOT NULL DEFAULT '[]'
- runtime_version    TEXT NOT NULL
- started_at         TEXT
- finished_at        TEXT
- created_at         TEXT NOT NULL
```

`composition` 的“构建”主要是 Schema、数据绑定、资源、响应式和安全校验；`app` 还包括静态应用包校验与封装。

### 8.5 `page_assets`

```text
page_assets
- id                 INTEGER PRIMARY KEY
- project_id         INTEGER NOT NULL
- logical_key        TEXT NOT NULL
- version_no         INTEGER NOT NULL
- storage_ref        TEXT NOT NULL
- media_type         TEXT NOT NULL
- byte_size          INTEGER NOT NULL
- sha256             TEXT NOT NULL
- origin             TEXT NOT NULL               # upload | pilot | generated | library
- provenance_json    TEXT NOT NULL DEFAULT '{}'  # 来源、生成记录、许可证
- width              INTEGER
- height             INTEGER
- created_at         TEXT NOT NULL
- UNIQUE(project_id, logical_key, version_no)
- UNIQUE(project_id, sha256)
```

规则：

- `storage_ref` 由服务端生成，客户端不能提交任意文件路径。
- 资源只能位于当前站点允许的存储根目录。
- SVG、HTML、脚本和字体执行各自的安全策略。
- 资源记录和底层 Blob 不可变；替换同名素材必须创建新的 `version_no`。
- Manifest 必须引用不可变 `asset_id + sha256`，`logical_key` 只用于后台展示和选择，不能在发布后动态解析到新文件。
- 删除被任何保留修订引用的资源时必须拒绝；垃圾回收只处理无引用 Blob。

### 8.6 `page_capability_grants`

```text
page_capability_grants
- id                 INTEGER PRIMARY KEY
- project_id         INTEGER NOT NULL
- capability         TEXT NOT NULL
- config_json        TEXT NOT NULL DEFAULT '{}'
- status             TEXT NOT NULL               # requested | approved | denied | revoked
- requested_by       TEXT NOT NULL
- approved_by        TEXT NOT NULL DEFAULT ''
- created_at         TEXT NOT NULL
- updated_at         TEXT NOT NULL
- UNIQUE(project_id, capability)
```

第一版能力：

| 能力 | 默认 | 说明 |
|---|---|---|
| `client.storage` | 允许 | 受限的页面本地状态 |
| `client.audio` | 允许 | 用户手势后播放音效 |
| `content.read` | 需配置 | 只读查询允许的内容类型和字段 |
| `form.submit` | 需批准 | 发送到 gcms 受控表单端点 |
| `analytics.event` | 需批准 | 发送白名单事件 |
| `network.fetch` | 默认拒绝 | 仅允许配置的 HTTPS 域名和方法 |

不提供：

- 任意 SQL；
- 后台会话；
- 自动化 Key；
- 服务器文件；
- 任意命令执行；
- 跨站私有数据；
- 隐式同源 Cookie。

### 8.7 `page_publications`

发布、定时发布、回滚和交付状态使用独立记录：

```text
page_publications
- id                 INTEGER PRIMARY KEY
- project_id         INTEGER NOT NULL
- revision_id        INTEGER NOT NULL
- action             TEXT NOT NULL               # publish | schedule | rollback | unpublish
- status             TEXT NOT NULL               # pending | approved | published | cancelled | failed
- approval_id        TEXT
- scheduled_at       TEXT
- published_at       TEXT
- actor_id           TEXT NOT NULL
- origin             TEXT NOT NULL
- request_id         TEXT
- deployment_job_id  TEXT
- delivery_status    TEXT NOT NULL DEFAULT ''    # queued | live | failed
- page_meta_hash     TEXT NOT NULL
- manifest_hash      TEXT NOT NULL
- data_snapshot_hash TEXT NOT NULL DEFAULT ''
- artifact_hash      TEXT NOT NULL DEFAULT ''
- runtime_version    TEXT NOT NULL
- created_at         TEXT NOT NULL
```

规则：

- 定时任务绑定明确的 `revision_id`，不能只把 `posts.status` 从 scheduled 改成 published。
- 到期执行时重新检查修订、能力、资源和审批是否仍有效。
- 页面发布与 Cloudflare 交付是两个状态；源站切换成功但静态部署失败时必须如实记录。
- 页面工作区频繁保存不得创建发布记录，也不得触发 Cloudflare 正式同步。
- `request_id` 同样使用 NULL + 部分唯一索引。

### 8.8 私有工程存储

应用源码、Blob 和构建产物放在站点私有工程目录，不复用公开 `/uploads` 路由：

```text
data/sites/{site}/
├─ cms.db
├─ uploads/
└─ page-projects/
   ├─ blobs/
   ├─ sources/
   └─ artifacts/
```

规则：

- 文件名和目录由服务端根据 ID/哈希生成；
- `sources` 和未发布 `artifacts` 不提供直接公开访问；
- 正式访问由修订与产物路由解析，不能把磁盘路径暴露给客户端；
- 平台备份、升级前备份、站点归档、恢复和迁移必须把整个 `page-projects` 纳入；
- 数据库恢复和私有工程目录恢复视为一个恢复单元；
- 垃圾回收必须从发布、定时发布和保留修订的可达关系出发。

### 8.9 候选路由保留

已发布自由页面修改 Slug 时，旧 Slug 必须继续在线，候选 Slug 同时不能被其他内容抢占。

建议增加受领域服务管理的路由保留：

```text
page_route_reservations
- id                 INTEGER PRIMARY KEY
- project_id         INTEGER NOT NULL
- revision_id        INTEGER NOT NULL
- lang               TEXT NOT NULL
- slug               TEXT NOT NULL
- created_at         TEXT NOT NULL
- UNIQUE(lang, slug)
```

保存工作修订时：

1. 检查 `posts`、系统保留路径和其他候选保留；
2. 为当前工作修订保留候选 Slug；
3. 新修订替换旧候选保留；
4. 发布时在同一事务内更新 `posts.slug` 并释放保留；
5. 冲突时拒绝保存或发布，不能自动给 Slug 加随机后缀；
6. 是否为旧 Slug 创建重定向由发布预检明确显示，不能静默断开旧链接。

---

## 9. 自由编排 Manifest

### 9.1 顶层结构

示例仅表达协议，内容不得作为代码中的固定示例数据：

```json
{
  "schema_version": 1,
  "mode": "composition",
  "shell": {
    "mode": "site",
    "sticky_header": true
  },
  "theme": {
    "inherit": true,
    "tokens": {}
  },
  "layout": {
    "content_max_width": "wide",
    "section_gap": "comfortable"
  },
  "sections": [
    {
      "id": "hero-main",
      "type": "hero.split",
      "props": {
        "eyebrow": "新品发布",
        "title": "让光线更懂空间",
        "description": "示例文案",
        "primary_action": {
          "label": "查看产品",
          "href": "#products"
        }
      },
      "media": {
        "asset_id": 18,
        "sha256": "content-hash"
      },
      "responsive": {
        "mobile": {
          "layout": "stack",
          "media_position": "after-content"
        }
      }
    },
    {
      "id": "products",
      "type": "content.cards",
      "props": {
        "columns": {
          "desktop": 4,
          "tablet": 2,
          "mobile": 1
        }
      },
      "binding": {
        "source": "product",
        "filter": {
          "category_slug": "smart-lighting"
        },
        "sort": "-published_at",
        "limit": 4
      }
    }
  ]
}
```

### 9.2 Manifest 规则

- 所有结构必须有 `schema_version`。
- 组件实例拥有稳定、页面内唯一的 `id`。
- 未知字段按版本策略处理，不能静默变成可执行代码。
- 所有枚举、尺寸和数量有 Schema 范围。
- 样式使用 Token 或受控属性，不接受任意全局 CSS。
- 组件不能读取未声明的数据。
- 数据查询有数量、排序和字段白名单。
- 响应式规则至少覆盖 desktop/tablet/mobile，缺省时使用组件默认值。
- Manifest 更新必须先标准化再计算哈希。

### 9.3 第一版组件注册表

内容组件：

- `text.heading`
- `text.rich`
- `media.image`
- `media.gallery`
- `media.video_embed`

营销组件：

- `hero.centered`
- `hero.split`
- `features.grid`
- `stats.row`
- `logos.cloud`
- `testimonials.cards`
- `pricing.cards`
- `faq.accordion`
- `cta.banner`

数据组件：

- `content.cards`
- `content.list`
- `products.grid`
- `posts.grid`
- `custom_content.grid`

布局组件：

- `layout.columns`
- `layout.section`
- `layout.divider`
- `layout.spacer`

交互组件：

- `form.contact`
- `form.subscribe`
- `tool.calculator`（受控表达式，不执行任意脚本）

每个组件注册项必须包含：

- 属性 Schema；
- 后台属性面板描述；
- 服务端渲染器；
- 响应式默认值；
- 可访问性要求；
- 可绑定的数据类型；
- Manifest 升级函数；
- 快照/渲染测试。

### 9.4 主题与布局

自由页面默认继承当前主题的语义 Token：

```text
color.background
color.surface
color.text
color.muted
color.accent
color.border
font.body
font.display
radius.control
radius.card
shadow.card
space.section
width.content
```

禁止依赖某个主题的固定颜色值或 DOM 结构。

`shell.mode`：

- `site`：完整站点导航和页脚；
- `minimal`：品牌标识 + 精简导航/返回入口；
- `none`：无站点外壳，仍保留 gcms 安全容器和 SEO Head。

“不是全屏页面”和“导航固定”等需求由 Manifest 和主题 Token 表达，不能在单个页面写死全局 CSS。

主题更新语义：

- `composition` 默认 `inherit`，主题切换后使用新语义 Token，不改 Manifest；
- 页面可以选择 `snapshot` 固定本次发布的 Token，但后台必须显示“未跟随主题”；
- `app` 声明 `inherit` 或 `isolated`；继承时由父 Shell/Bridge 传递白名单 Token，不授予同源访问；
- 主题切换预检要抽样渲染自由页面，避免新 Token 使已发布页面不可读。

### 9.5 多语言

- 每个语言版本继续拥有独立 `posts`、页面工程和修订，通过 `trans_group` 关联；
- “创建译文”默认复制结构、组件 ID 映射、非本地化素材和可共享绑定，再翻译可本地化文案；
- 各语言创建后独立维护，不持续静默同步结构；
- 用户可以显式执行“把结构变更同步到其他语言”，执行前显示影响并创建各语言新修订；
- AI 翻译不得改写组件类型、资源哈希、数据源键和能力授权；
- 数据绑定必须选择对应语言的内容；允许回退时在组件和 SEO 检查中明确提示；
- 缺失译文不创建半成品公开页。

---

## 10. 数据绑定

### 10.1 绑定而不是复制

宣传页上的商品、文章和案例应保存查询条件，不保存一份硬编码内容副本。

示例：

```json
{
  "source": "product",
  "filter": {
    "category_slug": "smart-lighting",
    "status": "published"
  },
  "sort": "-published_at",
  "limit": 4,
  "fields": ["title", "slug", "cover_image", "price"]
}
```

后台内容变化后，动态前台或下一次静态发布自动反映最新结果。

每个绑定必须声明更新语义：

- `live`：源站动态渲染时读取最新已发布内容；Cloudflare 静态站在下一次部署时更新。
- `release_snapshot`：发布页面修订时固定内容 ID 和字段快照，后续内容变化不影响该页面，直到重新发布。

后台和 Pilot 必须显示当前模式，不能让用户误以为静态部署会实时变化。

### 10.2 查询约束

- 只能查询已启用的内容类型；
- 公开页面只能读取已发布内容；
- 字段必须来自内容类型 Schema；
- 单个组件和单页都有数量上限；
- 不提供任意 SQL、正则扫描或文件查询；
- 排序字段白名单；
- 查询结果必须稳定排序；
- 数据缺失时组件有明确空状态；
- 被绑定内容删除、下架或缺少译文时必须产生后台告警，并根据组件配置隐藏、显示替代内容或阻止发布；
- 预览可以选择“真实已发布数据”或“当前用户有权看的草稿数据”，两者必须标识清楚。

### 10.3 静态发布语义

- Cloudflare 静态导出时解析绑定并冻结为本次部署的 HTML。
- 后台内容更新不会自动改变已经部署的静态文件，直到下一次同步。
- 部署记录保存页面修订哈希和数据快照哈希，便于追溯。

---

## 11. 互动应用协议

### 11.1 第一版应用包

第一版仅支持无服务器构建依赖的静态包：

```text
app.zip
├─ app-manifest.json
├─ index.html
├─ styles.css
├─ app.js
└─ assets/
```

`app-manifest.json` 示例：

```json
{
  "schema_version": 1,
  "entry": "index.html",
  "viewport": "responsive",
  "shell_mode": "site",
  "capabilities": [
    {
      "name": "client.storage"
    }
  ]
}
```

### 11.2 包约束

- 文件路径必须是相对路径，拒绝 `..`、绝对路径、符号链接和设备文件；
- 文件数量、单文件大小、总大小、压缩比和嵌套深度有可配置上限；
- 入口必须存在且唯一；
- 不允许远程 `<script src>`；
- 外部图片、字体、音视频根据能力和域名白名单处理；
- 不执行安装脚本、构建脚本或 Service Worker；
- 依赖必须打包进产物并锁定版本，不默认从 CDN 动态加载；
- 应用包保留依赖清单、许可证和 AI 生成资产来源记录；
- 不允许覆盖站点根路径；
- 所有文件计算哈希，发布后不可变；
- 上传包先进入草稿区，验证成功后才能预览或发布。

### 11.3 运行隔离

互动应用放入带 `sandbox` 的 iframe：

- 允许 `allow-scripts`；
- 默认不允许 `allow-same-origin`；
- 默认不允许弹窗、顶层导航、下载、表单直出；
- 父页面通过版本化 `postMessage` Bridge 提供获批能力；
- 父页面同时发送最小化 `Permissions-Policy`，浏览器能力默认关闭；
- Bridge 校验来源窗口、消息类型、项目 ID、修订 ID、请求 ID和参数 Schema；
- 每个能力有速率、大小和超时限制；
- 页面撤销能力后，Bridge 立即拒绝后续调用。

互动应用响应必须使用强制 CSP，而不是仅报告模式。

部署条件允许时优先使用独立应用 Origin；同域部署也必须依靠不含 `allow-same-origin` 的 sandbox 形成 opaque origin，不能让应用继承路径为 `/` 的后台会话 Cookie。

### 11.4 能力 Bridge

示例消息：

```json
{
  "protocol": "gcms-page-bridge/1",
  "request_id": "req-123",
  "capability": "form.submit",
  "action": "submit",
  "payload": {
    "form_id": "lead",
    "fields": {
      "email": "user@example.com"
    }
  }
}
```

Bridge 返回结构化成功或错误，不向子应用暴露内部 API Token。

---

## 12. 渲染、预览和发布

### 12.1 路由分发

公开页面仍使用现有页面 URL：

```text
/{slug}
/{lang}/{slug}
```

服务端读取 `posts` 后：

1. 没有 `page_projects` → 走现有 `page.html`；
2. 有工程但没有 `published_revision_id` → 已发布旧标准页继续走 `page.html`，新草稿页不公开；
3. 发布修订是 `standard_baseline` → 使用基线正文走现有 `page.html`；
4. `mode=composition` 且有发布修订 → 渲染已发布 Manifest；
5. `mode=app` 且有发布修订 → 渲染 Site Shell + 沙箱应用容器。

不得改变标准页面现有路由优先级。

### 12.2 私有预览

预览 URL 必须：

- 绑定站点、页面、修订和过期时间；
- 对互动应用同时绑定 `build_id`、运行时版本和主题；
- 使用签名 Token；
- 默认 `noindex, nofollow`；
- 不改变公开发布状态；
- 对 `app` 使用与正式运行相同的 CSP 和沙箱；
- 显示“草稿预览”和修订号；
- Token 到期后不可继续访问。
- 修订、构建或安全策略变化后，旧预览返回 410 并要求重新生成。

推荐接口返回：

```json
{
  "preview_url": "...",
  "revision_id": 42,
  "expires_at": "...",
  "warnings": []
}
```

### 12.3 发布原子性

发布过程：

1. 校验页面记录和目标修订；
2. 校验能力授权；
3. 校验资源完整性；
4. 获取或生成 `ready` 构建；
5. 校验绑定页面和修订的有效批准凭证；
6. 在事务中设置 `published_revision_id`、公共元数据和 `posts.status`；
7. 同步 `posts.updated_at` 等现有公开更新时间索引，确保搜索、缓存和静态同步能发现变化；
8. 清除公开内容相关缓存；
9. 记录 publication 和审计日志；
10. 仅在公开修订指针变化后，根据站点同步策略触发或排队 Cloudflare 发布。

任何一步失败不得让公开页面指向半成品。

页面发布与公网交付分开报告：

```json
{
  "publication_status": "published",
  "publication_id": "pub-...",
  "delivery_status": "queued",
  "deployment_job_id": "deploy-..."
}
```

动态源站可能已经切换成功，而 Cloudflare 仍在排队或失败。后台和 Pilot 必须准确描述两种状态，不能把“页面修订已发布”误报成“公网部署已完成”。

缓存分成两条失效路径：

- `draft invalidation`：刷新后台画布和私有预览，不触发公开缓存或 Cloudflare；
- `public publication invalidation`：只有公开修订指针变化时清公开缓存并调度交付。

公开页面缓存键或缓存元数据必须包含 `published_revision`/artifact hash，不能只依赖 URL 和全局资源版本。

### 12.4 Cloudflare 静态导出

静态导出必须包含：

- `composition` 渲染后的 HTML；
- `app` 的不可变构建产物；
- 页面依赖资源；
- 当前 Site Shell；
- 页面 SEO 和多语言 alternate；
- 数据绑定在本次部署的结果；
- 页面和数据快照哈希。

不兼容静态环境的能力必须：

- 在发布前阻止并说明；或
- 明确回退到 gcms 受控公网端点。

不得静默发布一个功能失效的页面。

---

## 13. API 设计

### 13.1 总体原则

- gcms 后台和 API 共用领域服务，不复制校验逻辑；
- API 为 Pilot 提供语义操作，不要求 AI 自己拼接数据库结构；
- 所有写请求支持幂等；
- 所有修订写入使用乐观并发；
- 高风险操作支持 `dry_run`、影响预检、明确确认和必要时的短时解锁；
- 平台入口继续使用现有 `site_id` 前缀转发到同一站点能力。
- 能力描述复用现有控制层的操作 ID、scope、风险、确认、dry-run、解锁和可用状态结构，不另造一套不兼容协议。
- OpenAPI 描述请求/响应结构，capabilities 描述当前站点、当前密钥和当前版本实际上能做什么，两者职责分开。

### 13.2 能力发现

新增页面能力描述：

```json
{
  "page_platform": {
    "version": "1",
    "modes": ["standard", "composition", "app"],
    "manifest_versions": {
      "composition": [1],
      "app": [1]
    },
    "features": {
      "revision_conflict": true,
      "private_preview": true,
      "static_export": true,
      "capability_bridge": true,
      "publish_approval_token": true
    },
    "limits": {
      "max_sections": 100,
      "max_assets": 200
    }
  }
}
```

限制值来自服务端配置，Pilot 不得自行假设。

### 13.3 建议端点

页面工程：

```text
POST   /api/admin/v1/page-projects
GET    /api/admin/v1/page-projects/{project_id}
PATCH  /api/admin/v1/page-projects/{project_id}
POST   /api/admin/v1/pages/{page_id}/convert-plan
POST   /api/admin/v1/pages/{page_id}/convert
```

所有页面读取（包括后台编辑器）返回统一 ETag。页面公共元数据和工程内容在一次保存中形成同一个逻辑修订，不能由后台和 Pilot 分别走无并发保护的保存路径。

兼容例外：

- 旧 `/pages` 客户端操作标准页面时可以暂不强制 `If-Match`；若提供则必须校验；
- 新后台编辑器、新技能包和所有页面工程端点必须使用 ETag；
- 旧 `/pages` 端点遇到已有自由工程的页面时，不得覆盖 Manifest、派生 fallback 或发布元数据，应返回 `project_api_required` 并引导使用页面工程协议；
- 以后若要让标准页面也强制并发和批准协议，使用明确的 API 合约版本升级，不能静默破坏旧客户端。

修订：

```text
GET    /api/admin/v1/page-projects/{project_id}/revisions
GET    /api/admin/v1/page-projects/{project_id}/revisions/{revision_id}
POST   /api/admin/v1/page-projects/{project_id}/revisions
POST   /api/admin/v1/page-projects/{project_id}/restore
```

组件和数据：

```text
GET    /api/admin/v1/page-components
GET    /api/admin/v1/page-data-sources
POST   /api/admin/v1/page-bindings/preview
```

资源：

```text
GET    /api/admin/v1/page-projects/{project_id}/assets
POST   /api/admin/v1/page-projects/{project_id}/assets
DELETE /api/admin/v1/page-projects/{project_id}/assets/{asset_id}
```

验证、预览和发布：

```text
POST   /api/admin/v1/page-projects/{project_id}/validate
POST   /api/admin/v1/page-projects/{project_id}/builds
GET    /api/admin/v1/page-projects/{project_id}/builds/{build_id}
POST   /api/admin/v1/page-projects/{project_id}/preview-url
POST   /api/admin/v1/page-projects/{project_id}/publish-plan
POST   /api/admin/v1/page-projects/{project_id}/publish
GET    /api/admin/v1/page-projects/{project_id}/publications
POST   /api/admin/v1/page-projects/{project_id}/rollback-plan
POST   /api/admin/v1/page-projects/{project_id}/rollback
```

能力：

```text
GET    /api/admin/v1/page-projects/{project_id}/capabilities
POST   /api/admin/v1/page-projects/{project_id}/capabilities/request
POST   /api/admin/v1/page-projects/{project_id}/capabilities/apply
```

### 13.4 修订写入

请求：

```json
{
  "base_revision_id": 41,
  "manifest": {},
  "summary": "缩小 Hero 图片并把手机端商品改成单列",
  "origin": {
    "kind": "pilot",
    "conversation_id": "conv-...",
    "request_id": "..."
  }
}
```

请求头：

```text
Idempotency-Key: stable-request-id
If-Match: "revision-41"
```

成功返回新修订。当前修订不是 41 时返回：

```json
{
  "error": "revision_conflict",
  "message": "页面已被其他操作更新。",
  "expected_revision_id": 41,
  "current_revision_id": 43,
  "current_updated_by": "admin"
}
```

服务端不得提供“忽略冲突强制覆盖”的普通接口。

### 13.5 发布批准凭证

发布不能只依赖 AI 可以自行填写的确认 Header 或提示词。Pilot 原生界面或 gcms 登录后台签发短时、一次性的批准凭证：

```text
approval_id
site_id
page_id
revision_id
operation=pages.publish
actor_id
expires_at
nonce
```

规则：

- Pilot 技能包只能发起 `publish-plan`，不能自行签发批准凭证；
- Pilot 原生确认 UI 展示目标页面、修订、预览状态、能力和发布影响；
- 凭证只允许发布绑定的页面和修订，修订变化立即失效；
- 凭证单次使用并写入审计；
- 后台人工发布由登录会话产生同等级批准记录；
- 正式发布同时要求有效 scope、`If-Match`、`Idempotency-Key` 和批准凭证；
- 高风险能力或平台策略可以额外要求短时解锁。

该硬边界首先适用于所有自由页面发布端点。旧标准页面 API 的历史行为按兼容策略保留；新 Pilot 即使操作标准页面，也应优先走带批准凭证的新版本协议。

### 13.6 错误码

至少定义：

| 错误码 | HTTP | 含义 |
|---|---:|---|
| `page_mode_unsupported` | 422 | 当前服务器不支持页面模式 |
| `manifest_invalid` | 422 | Manifest 不符合 Schema |
| `component_unknown` | 422 | 组件不存在或版本不支持 |
| `binding_invalid` | 422 | 数据绑定无效 |
| `asset_invalid` | 422 | 资源格式、路径或大小不符合要求 |
| `capability_required` | 403 | 页面请求了未授权能力 |
| `revision_conflict` | 409 | 基准修订已过期 |
| `build_not_ready` | 409 | 目标修订尚未验证成功 |
| `publish_confirmation_required` | 409 | 缺少有效发布确认 |
| `static_export_unsupported` | 422 | 当前能力不能静态发布 |
| `idempotency_conflict` | 409 | 同一幂等键对应不同请求 |
| `project_api_required` | 409 | 目标是自由页面，旧标准页面接口不能修改 |

---

## 14. 权限与风险等级

建议 scopes：

| Scope | 用途 |
|---|---|
| `page_projects:read` | 读取自由页面、组件和修订 |
| `page_projects:write` | 创建和修改自由页面草稿 |
| `page_projects:build` | 校验和生成页面构建 |
| `page_assets:write` | 上传和维护页面资源 |
| `page_apps:write` | 上传或修改互动应用源码 |
| `page_preview:read` | 创建私有预览 |
| `pages:publish` | 发布页面，尽量复用现有发布权限 |
| `page_capabilities:request` | 请求应用能力 |
| `page_capabilities:grant` | 批准敏感能力 |

兼容与最小权限规则：

- 老 Key 升级后不自动获得 `page_apps:*`、`page_assets:write` 或能力批准权限；
- 既有 `content:write` 不能隐式获得上传和运行不可信应用代码的能力；
- 新 scopes 必须从现有 `content:*`/`{collection}:*` 通配判定中显式排除，不能只依赖后台不勾选；
- AI 的有效权限是“当前用户权限 ∩ Key scopes ∩ 站点策略 ∩ 本次任务授权”；
- 任务级授权绑定站点、页面、操作和有效期，AI 不能扩大授权范围。

风险分类：

| 操作 | 风险 | 默认确认 |
|---|---|---|
| 读取页面、组件、数据源 | read | 不需要 |
| 创建或编辑草稿 | write | 对话中已明确目标即可 |
| 上传资源 | write | 普通资源不需要，异常格式需要 |
| 生成预览 | read/write-safe | 不需要 |
| 请求能力 | sensitive | 显示影响 |
| 批准外部网络/表单能力 | sensitive | 必须明确确认 |
| 发布、回滚线上版本 | sensitive | 必须明确确认 |
| 删除页面工程或资源 | destructive | 影响预检 + 明确确认 |

Pilot 的平台控制能力继续遵循现有“capabilities → plan → confirmation → unlock → apply”模式。

---

## 15. 安全模型

### 15.1 信任边界

- 标准页面内容：按现有可信后台作者模型处理；
- `composition`：只接受 Schema 允许的结构和属性；
- `app`：始终视为不可信代码；
- Pilot：是有 scope 的客户端，不是服务器管理员；
- 预览：仍按生产安全级别运行，不因“仅预览”而放宽；
- Cloudflare 导出：不能扩大页面在源站拥有的能力。

### 15.2 必须执行的安全检查

`composition`：

- Schema 校验；
- URL 协议白名单；
- 富文本清理；
- 数据绑定字段白名单；
- 组件层级和数量上限；
- 禁止全局样式和脚本；
- 表单目标必须为受控端点。
- 表单端点执行字段白名单、限流、垃圾信息防护、隐私同意和可配置的数据保留策略。

`app`：

- Zip Slip、防压缩炸弹、文件类型和文件数校验；
- HTML 入口解析；
- 禁止远程脚本和 Service Worker；
- 强制 CSP；
- iframe sandbox；
- 能力 Bridge 参数校验；
- 资源哈希；
- 外部网络白名单；
- 不把 API Key、后台 Cookie 或私有 URL注入应用。

### 15.3 可配置默认限制

限制必须集中配置并通过能力 API 返回，禁止散落在前后端：

- Manifest 字节数；
- 页面区块数和嵌套深度；
- 单页数据绑定查询数；
- 单次查询结果数；
- 单个资源和项目总大小；
- 应用包文件数、压缩比和解压后大小；
- Bridge 调用频率、请求体和超时；
- 私有预览有效期；
- 单站自由页面数量。

---

## 16. 版本、审计与可观测性

### 16.1 审计字段

每次写入至少记录：

- 站点 ID；
- 页面和项目 ID；
- 基准修订和新修订；
- 来源：后台、Pilot、API；
- 管理员或自动化 Key ID；
- 请求 ID；
- Pilot 对话 ID（如有）；
- 操作摘要；
- 校验结果；
- 是否触发预览、发布或回滚；
- 时间；
- 结果和错误码。

### 16.2 不记录的内容

- 自动化 Key 原文；
- 管理员密码；
- Cloudflare Token；
- 用户未授权保存的完整对话；
- 应用本地存储内容；
- 表单中的敏感字段明文日志。

### 16.3 运行指标

- 页面工程数量，按模式和状态；
- 校验成功/失败率；
- 发布成功/失败率；
- 修订冲突次数；
- Pilot 创建、后台接管、Pilot 再接管的次数；
- 应用能力拒绝次数；
- 静态导出不兼容次数；
- 页面预览和构建耗时。

---

## 17. 老客户兼容与数据库迁移

### 17.1 兼容承诺

升级到支持自由页面的版本后：

- 旧 `posts` 行不被重写；
- 旧 `page` 不自动创建 `page_projects`；
- 旧页面继续进入原 `page.html`；
- 旧 Markdown/富文本不重新解析保存；
- 原有 URL、Slug、语言、SEO、发布时间和状态不变；
- 原有主题不要求增加自由页面组件；
- 原 API 请求和响应字段保持兼容；
- 老技能包继续管理标准页面；
- 未启用新功能的客户看不到前台变化。

### 17.2 迁移策略

优先新增表，不改 `posts` 的既有语义。

推荐迁移流程：

1. 打开站点库前确认数据库可读写；
2. 创建升级前备份或要求平台升级器已完成备份；
3. 在事务中创建新表和索引；
4. 写入独立的 Schema 迁移版本；
5. 校验表、索引和外键；
6. 提交事务；
7. 重新读取旧页面做冒烟检查；
8. 失败则回滚事务并保持旧库可用。

这批关键迁移不能只依赖忽略错误的“尝试补列”方式；迁移错误必须向上返回并阻止带不完整 Schema 启动。

完整备份单元不再只是 `cms.db`：还必须包含页面工程源码、素材、不可变构建产物和能力配置。恢复测试要证明自由页面可以在另一台机器重新预览、发布和回滚。

### 17.3 标准页面发布状态

当前后台标准页面保存会保持 `published`。兼容期按以下方式处理：

- 已存在标准页面升级后仍为 `published`；
- 编辑旧标准页面时默认保留其现有状态，不因新 UI 自动变成草稿；
- 新建自由页面默认 `draft`；
- 新建标准页面第一阶段可保留现有默认，也可展示明确选项，但不得改变老页面；
- 当统一页面发布工作流正式上线时，只改变新操作的 UI 语义，不批量修改存量状态。

### 17.4 API 与技能包兼容

- 原 `/pages` CRUD 继续工作；
- 原 `editor_mode=markdown|rich` 校验不变；
- 新页面工程使用新增端点；
- 能力发现明确返回服务器是否支持新端点；
- 新技能包连接旧服务器时自动降级为标准页面能力；
- 老技能包连接新服务器时看不到或忽略新能力；
- 不允许 Pilot 根据版本号猜功能，必须读取能力描述。
- 旧 Key 不因升级自动获得应用源码、应用资源、运行时能力或能力批准 scopes。

### 17.5 主题兼容

- 标准页面仍使用旧主题的模板与 CSS；
- 自由页面组件使用新的语义 Token 层；
- 旧主题没有新增 Token 时由核心默认 Token 回退；
- 不批量修改旧主题 CSS；
- 新增组件 CSS 必须在自由页面作用域内；
- 标准页面视觉回归截图必须逐主题通过。

### 17.6 降级边界

- 从旧版升级到新版：必须兼容。
- 新版创建自由页面后再降级：旧内容数据不应损坏，但旧程序无法渲染新页面工程。
- 升级器应明确提示这一点并保留升级前备份。
- 不承诺旧二进制理解新应用包。

### 17.7 多站点

- 每个站点的 `cms.db` 独立迁移；
- 单个站点失败不能把其他站点标记为已迁移；
- 采用“按站点 fail closed”：迁移失败的站点不启动前台和自动化能力，健康站点继续服务，平台后台显示失败站点、错误和恢复入口；
- 默认站点失败时平台管理入口仍应可用，不能因单个站点库损坏拖垮整个 runtime pool；
- 平台展示每个站点的迁移结果；
- 站点运行时缓存必须包含页面工程和发布修订维度；
- 资源目录必须按站点隔离；
- 平台 API 仍通过 `site_id` 进入目标站点的同一领域服务。

---

## 18. 冲突与失败处理

| 场景 | 必须行为 |
|---|---|
| 人工和 Pilot 同时编辑 | 后提交者收到 `409`，读取新修订后重做 |
| Manifest 无效 | 保存草稿可以保留明确错误状态，但不能发布 |
| 应用包校验失败 | 不生成可公开预览，不覆盖上一个可用构建 |
| 已发布资源被删除 | 拒绝删除或保留内容寻址副本 |
| 数据绑定内容为空 | 使用组件空状态并在发布检查中提示 |
| 能力被撤销 | 新调用立即失败；已发布页显示可控降级状态 |
| Cloudflare 发布失败 | 源站发布状态和上一次 Cloudflare 成功版本都可追溯 |
| Pilot 中途断开 | 已成功保存的修订保留；未完成发布不执行 |
| 幂等请求重试 | 返回原结果，不重复创建修订或资源 |
| 新技能包连接旧服务器 | 降级并明确说明不支持自由页面 |
| 组件版本升级 | 先迁移 Manifest 副本，验证后创建新修订 |

---

## 19. 分阶段实施

### Phase 0：领域基础与兼容护栏

交付：

- 新表迁移；
- 页面工程领域模型；
- 修订、哈希、幂等和乐观并发；
- 能力发现；
- 页面级签名草稿预览（覆盖标准页面和自由页面）；
- 后台与 API 使用同一 ETag/修订冲突保护；
- 标准页面零变化测试；
- 多站点迁移测试；
- API 基础骨架。

退出条件：

- 使用真实旧数据库升级后，内容计数和关键字段一致；
- 所有现有主题的标准页面视觉回归通过；
- 连续启动迁移幂等；
- 迁移失败可回滚。

### Phase 1：自由编排页面 + gcms 后台

交付：

- 页面列表类型与状态；
- 新建类型选择；
- 组件注册表；
- 三栏编排编辑器；
- Manifest 校验；
- 数据绑定；
- 响应式预览；
- 修订、预览、发布和回滚；
- Site Shell 三种模式；
- Cloudflare 静态导出。

第一版优先组件：

- Hero；
- 富文本；
- 图片；
- 特性；
- 商品/文章/自定义内容卡片；
- FAQ；
- CTA；
- 联系表单。

退出条件：

- 人工能独立创建并上线一张真实宣传页；
- 页面不硬编码商品数据；
- 五种后台预览宽度和手机真实视口通过；
- 标准页面无回归。

### Phase 2：Pilot 纯对话接入

交付：

- 新 API 写入技能包；
- Pilot 能力发现；
- 创建、修改、校验、预览、发布对话流程；
- 发布确认；
- 修订冲突处理；
- “在 gcms 后台打开”；
- 对话变更摘要；
- 老服务器降级。

退出条件：

- Pilot 创建的页面能在后台继续手动编辑；
- 后台修改后 Pilot 能继续编辑且不覆盖；
- 未明确发布时只能产生草稿；
- 老技能包/旧服务器兼容测试通过。

### Phase 3：互动应用

交付：

- 静态应用包协议；
- 后台文件/资源管理；
- 沙箱预览；
- 强制 CSP；
- 能力 Bridge；
- 能力申请与批准；
- Pilot 生成和上传应用包；
- Cloudflare 应用资源导出。

退出条件：

- 能发布一个触屏/键盘可用的客户端小游戏；
- 默认不能访问后台 Cookie、Key 或外部网络；
- 撤销能力后立即生效；
- 安全测试和压缩包攻击测试通过。

### Phase 4：增强能力

候选：

- 页面模板市场；
- 跨页面可复用 Section；
- 表单结果后台；
- 实验与 A/B 版本；
- 更丰富的数据查询；
- 独立安全构建服务；
- 高级框架应用包；
- 团队审批流。

这些能力不得阻塞 Phase 1–3 的核心闭环。

---

## 20. 开发工作流拆分

### 20.1 Store

- 新表和事务迁移；
- CRUD；
- 不可变修订；
- 内容哈希；
- 资源引用；
- 发布指针；
- 多站点测试；
- 备份/恢复测试。

### 20.2 Web 领域服务

- 页面模式分发；
- Manifest 标准化与校验；
- 组件注册表；
- 数据绑定解析；
- 发布事务；
- 缓存失效；
- 静态导出接口。

### 20.3 gcms 后台

- 页面列表；
- 新建向导；
- 编排工作台；
- 设备预览；
- 历史、校验和发布；
- 应用包与能力管理。

### 20.4 自动化 API 与技能包

- OpenAPI；
- scopes；
- capabilities；
- CRUD；
- 预览；
- plan/apply；
- 错误码；
- CLI 命令；
- 技能说明和示例。

### 20.5 Pilot

- 能力发现；
- 对话任务规范；
- 进度与预览；
- 原生确认；
- 冲突重试；
- 后台深链接；
- 新旧服务端兼容。

### 20.6 安全

- Manifest 安全；
- Zip 安全；
- iframe sandbox；
- CSP；
- Bridge；
- 外部网络；
- 资源隔离；
- 速率和大小限制。

### 20.7 发布

- 源站渲染；
- 静态导出；
- Cloudflare Worker Assets；
- Cloudflare Pages；
- 数据快照；
- 缓存与回滚。

---

## 21. 验收标准

### 21.1 老数据

- [ ] 使用至少三个历史版本生成的数据库升级成功。
- [ ] 升级前后 `posts`、页面、语言和状态计数一致。
- [ ] 旧页面 URL、SEO 和正文一致。
- [ ] 旧页面未产生 `page_projects`。
- [ ] 所有旧主题关键页面截图无非预期差异。
- [ ] 升级后老 API 和老技能包测试通过。

### 21.2 人工后台

- [ ] 用户不用 Pilot 可以创建、编辑、预览、发布自由页面。
- [ ] 商品和文章组件使用数据绑定，不保存硬编码副本。
- [ ] 页面在桌面、平板、手机预览中可用。
- [ ] 后台可以查看 Pilot 修改并继续编辑。
- [ ] 历史记录可识别来源并回滚。
- [ ] 保存草稿不会改变线上版本。

### 21.3 Pilot

- [ ] 用户全程只通过对话创建页面。
- [ ] AI 先发现能力，不猜测接口。
- [ ] AI 默认创建草稿。
- [ ] AI 返回预览和结构化变更摘要。
- [ ] 未经明确确认不能发布。
- [ ] 人工中途修改时 AI 不覆盖并能处理冲突。
- [ ] Pilot 创建的页面可在后台完整编辑。

### 21.4 互动应用安全

- [ ] 应用不能读取后台 Cookie 或 API Key。
- [ ] 应用默认不能访问外部网络。
- [ ] Zip Slip、压缩炸弹和符号链接被拒绝。
- [ ] CSP 为强制模式。
- [ ] iframe 不具备同源权限。
- [ ] Bridge 只开放已批准能力。
- [ ] 页面撤销能力后调用立即失败。
- [ ] 上一个发布版本在新构建失败时继续可用。

### 21.5 发布与 Cloudflare

- [ ] 源站和 Cloudflare 渲染结果语义一致。
- [ ] 自由页面资源完整导出。
- [ ] 数据绑定部署快照可追溯。
- [ ] 发布失败不产生半发布状态。
- [ ] 回滚恢复页面修订和对应资源。

---

## 22. 测试矩阵

| 维度 | 最低覆盖 |
|---|---|
| 数据库 | 新库、旧库、重复迁移、失败回滚、多站点 |
| 页面模式 | standard、composition、app |
| 来源 | 后台、单站技能包、平台技能包、Pilot |
| 并发 | 后台→Pilot、Pilot→后台、两个 API 请求 |
| 状态 | 草稿、已发布、有未发布修改、失败构建 |
| 语言 | 单语、多语、缺失译文 |
| 主题 | 内容类主题、通用主题、深色主题、旧主题 |
| 视口 | 跟随主题、1080、1200、1240、1360、1440、平板、手机 |
| 发布 | 源站、Worker Assets、Cloudflare Pages |
| 安全 | HTML、URL、SVG、Zip、CSP、Bridge、外部网络 |
| 兼容 | 老 API、新 API、老技能包、新技能包、旧服务器 |

---

## 23. 实现禁区

以下实现即使能快速展示，也不得合并：

1. 把自由页面 JSON、HTML 或 JS 存入 `posts.content`。
2. 让 `composition` 接受任意全局 CSS 或脚本。
3. 在 gcms 服务器执行 AI 生成的构建脚本。
4. Pilot 通过浏览器点击后台代替 API。
5. API 写入不带基准修订或允许静默覆盖。
6. 把保存草稿自动当成正式发布。
7. 自动转换老页面。
8. 修改公共主题 CSS 导致老页面视觉变化。
9. 应用 iframe 使用同源权限并继承后台 Cookie。
10. 把密钥写入 Manifest、应用包、预览 URL或对话日志。
11. 静态发布时忽略不支持的能力。
12. 删除被已发布修订引用的资源。
13. 让前端和后端各自维护一套组件 Schema。
14. 在多个文件散落组件、资源和请求大小限制。

---

## 24. 决策记录

### ADR-001：标准页面与自由页面共享 `posts`

决定：共享公共页面记录，自由能力放在新增工程表。

原因：

- 保留 URL、SEO、多语言和现有列表能力；
- 避免复制页面身份；
- 不改变老数据；
- 后台和 Pilot 可以统一引用 `page_id`。

### ADR-002：自由编排使用 Manifest

决定：宣传页优先使用结构化组件 Manifest，不使用任意 HTML。

原因：

- 可视化编辑；
- 响应式可控；
- 可绑定后台数据；
- 主题可继承；
- 安全、迁移和 AI 修改更可靠。

### ADR-003：互动应用采用静态包 + 沙箱

决定：小游戏等使用静态 HTML/CSS/JS 包，由 iframe 和能力 Bridge 隔离。

原因：

- 能表达程序逻辑；
- 不引入服务器任意代码执行；
- 可离线验证和内容寻址；
- 适合 Cloudflare 静态发布。

### ADR-004：Pilot 只做对话入口

决定：Pilot 不复制后台编辑器，通过技能包操作同一领域 API。

原因：

- 避免两套产品和两份状态；
- 人工与 AI 能交替工作；
- Pilot 保持“说目标即可”的价值；
- 后台承担精细手工控制。

### ADR-005：发布指定不可变修订

决定：预览、确认和发布都绑定明确的 `revision_id`。

原因：

- 防止确认后内容变化；
- 支持并发冲突；
- 审计和回滚清晰；
- Cloudflare 构建可追溯。

---

## 25. 后续修改本文的规则

需要修改以下任一内容时，必须新增 ADR：

- 页面模式数量或语义；
- 是否共享 `posts`；
- Manifest 与应用包边界；
- Pilot 是否仍为纯对话；
- 发布确认规则；
- 应用沙箱边界；
- 老页面迁移方式；
- 服务端是否引入构建运行时；
- Cloudflare 对互动能力的承载方式。

小范围字段、端点命名和 UI 文案可以直接修订，但要更新文档版本和日期。
