# GCMS Pilot-first 页面平台设计规格

> 状态：Pilot-first direction confirmed（Phase 0–3 核心代码、Pilot 设计上下文与原生目标绑定确认已落地；真实 Pilot 全流程、全主题视觉和真实 Cloudflare 发布仍是发布门禁）
> 文档版本：0.4
> 更新日期：2026-07-26
> 适用范围：gcms 服务端、gcms 后台、自动化 API、AI 技能包、Pilot、前台渲染与 Cloudflare 静态发布

## 0. 文档用途

这份文档用于冻结“标准页面 + 自由编排页面 + 互动应用”的产品和技术方向，防止后续开发因上下文缺失而重新解释目标。

实施时遵循以下规则：

1. 本文的“必须”“不得”“默认”属于实现约束。
2. 如果实现过程中需要改变核心决策，先更新本文的“决策记录”，再修改代码。
3. 不允许为了快速演示，把自由页面源码塞进现有 `posts.content`，也不允许让 Pilot 通过模拟点击后台完成操作。
4. 每个阶段都必须先通过老数据库升级测试和标准页面视觉回归，才能进入下一阶段。
5. 未在本文授权的能力默认关闭；尤其是外部网络、数据写入、发布和破坏性操作。
6. Pilot 是页面与互动应用的唯一主要创作入口；gcms 后台承担治理、预览、发布、回滚和应急接管，不再以“让普通用户从零手工编排”为产品主路径。
7. “对话完成”必须包含可视化预览、三尺寸检查、修改摘要和明确发布确认，不能退化成 AI 在后台静默写入数据。

### 0.1 实施台账

截至 2026-07-26，Phase 0–3 的核心闭环已经落地：

- 使用独立、事务化、带版本门控的页面平台 Schema；新增页面工程、不可变修订、构建、资源、能力授权、发布记录、候选路由和持久幂等收据。
- 迁移不扫描、不转换、不回填现有标准页面；未来 Schema 版本会在执行旧版 DDL 前拒绝启动，失败迁移不会留下半成品。
- Store 已具备 Canonical JSON、稳定 SHA-256、修订 ETag、乐观并发、`request_id` 幂等、Ready Build 校验、候选路由和原子发布/回滚指针。
- 已开放版本化能力发现与 Page API，单站、平台多站点、后台和 Pilot 复用同一领域数据、校验、修订及发布服务；`available` 与 `granted` 分开表达。
- 老 `content:*` 权限不能继承页面工程、应用源码、资源或运行能力批准权限。
- `composition` 已具备 Manifest v1 严格校验、组件/数据源注册表、真实后台数据绑定、响应式 SSR、三种 Site Shell、素材、联系表单、精确修订预览、发布/回滚及 Cloudflare 静态导出。
- gcms 后台已具备三种页面类型入口、列表筛选、三栏编排器、动态属性/数据绑定、1080/1200/1240/1360/1440 及平板/手机预览、历史、预览、发布和回滚。
- `app` 已具备安全静态包上传、后台文件树与受限文本源码编辑、不可变构建、沙箱 iframe、强制 CSP/Permissions-Policy、实时能力批准/撤销、Bridge 和 Cloudflare 产物导出。
- 标准页、自由编排页和应用预览均使用短期签名票据并绑定精确内容或构建身份；过期、修订/构建/动态数据变化后失效，且强制 `noindex, nofollow`、`no-store`。
- OpenAPI、单站 Skill、平台 Skill 和打包 Skill 已实现同一套页面命令；发布、回滚和互动能力批准由 Pilot 原生密码框完成目标绑定的一次性确认，密码和内部 approval token 不进入 AI 上下文。
- 页面源码、资源、构建产物与数据库一并进入备份/恢复；多站点资源目录和运行时故障隔离均有回归覆盖。
- Cloudflare 静态页上的联系表单只回源到已配置的 GCMS Origin；该 Origin 必须是与静态公开站不同的 HTTPS 源站。服务端仅接受受信公开域名的表单来源，并使用字段白名单、隐私同意、蜜罐、限流和私有保留文件。

当前明确保持关闭或 fail-closed 的能力：

- 数据绑定仅公开 `live`；`release_snapshot` 在没有持久、可追溯快照前拒绝保存，客户端不得伪造支持。
- App Bridge 当前只开放已经实现并批准的能力；任意外部网络和通用表单写入仍不可授权。小游戏和纯客户端互动不受影响。
- 工程原地修改/删除和资源删除尚无完整引用事务，因此能力目录标记不可用；结构或源码变化通过新的不可变修订完成。
- Phase 4 的模板市场、A/B、团队审批、高级框架构建等仍是候选，不属于本次核心闭环。

实现必须继续以能力响应和测试事实为准，不得根据版本号猜测未开放能力。

2026-07-26 产品定位已进一步冻结为 Pilot-first：

- Pilot 负责理解目标、读取站点主题与真实数据、生成页面、完成三尺寸预览与视觉 QA、根据对话迭代，并在用户明确授权后发布。
- gcms 后台保留统一页面列表、状态、历史、审核、发布、下线、回滚、SEO/Slug 等治理能力，以及故障时的高级编辑入口。
- 现有三栏编排器不会删除，但降级为高级/应急工具；后续不得以扩大后台表单数量作为主要产品进展。
- 结构化 Manifest、主题 Token、数据绑定、不可变修订和批准机制继续作为 Pilot 与 gcms 共享的底层协议。

---

## 1. 一句话目标

用户只在 Pilot 中通过自然语言完成页面创作、预览、修改和发布；gcms 作为同一套页面能力、数据、权限、版本与发布流程的所有者，负责主题约束、真实数据、渲染、治理和应急接管。

- Pilot 是唯一主要创作入口，不复制 gcms 的复杂表单，也不要求用户理解组件 Schema、Manifest 或响应式参数。
- Pilot 生成的页面必须继承目标站点主题、绑定 gcms 真实数据，并自动验证桌面、平板和手机表现。
- gcms 后台不是第二个日常创作产品；它保留页面管理、预览、发布、回滚、审计、必要元数据和高级应急编辑。
- Pilot 与后台始终操作同一份页面工程和不可变修订，不产生“后台版”和“AI 版”两套数据。
- 现有客户升级后，旧页面、旧主题、旧 API 和旧技能包行为保持不变。

```text
┌──────────────────────────────────────────────────────────┐
│ Pilot（唯一主要创作入口）                                 │
│ 对话理解 → 主题/数据发现 → 生成 → 三尺寸预览 → 迭代 → 确认 │
└───────────────────────────┬──────────────────────────────┘
                            │ AI 技能包 / API
             ┌──────────────▼────────────────┐
             │ GCMS 页面领域能力与数据层      │
             │ Manifest / 数据 / 版本 / 权限  │
             │ 渲染 / 发布 / 回滚 / 审计      │
             └──────────────┬────────────────┘
                            │
       ┌────────────────────┴────────────────────┐
       │                                         │
┌──────▼────────────────┐              ┌─────────▼──────────┐
│ 站点前台 / Cloudflare │              │ GCMS 后台（治理）   │
│ 主题化响应式页面       │              │ 审核/发布/回滚/应急 │
└───────────────────────┘              └────────────────────┘
```

---

## 2. 已冻结的核心决策

### 2.1 三种页面模式

| 模式 | 内部值 | 主要用途 | 渲染方式 |
|---|---|---|---|
| 标准页面 | `standard` | 关于、帮助、协议、普通内容页 | 保留现有 Markdown/富文本与 `page.html` |
| 自由编排页面 | `composition` | 宣传页、活动页、专题页、产品落地页、轻量计算器 | 结构化 Manifest + gcms 组件渲染器 |
| 互动应用 | `app` | 小游戏、测评、交互演示、复杂计算器 | 隔离的静态 HTML/CSS/JS 应用包 |

### 2.2 Pilot-first 单一主路径、统一能力

- Pilot 是唯一主要创作入口；普通用户不需要进入 gcms 后台完成页面结构、文案、布局或响应式配置。
- gcms 后台保留完整领域能力的治理入口，以及必要时的高级/应急人工接管，不与 Pilot 竞争日常创作体验。
- 后台和 Pilot 调用同一服务层、校验器、版本机制和发布机制。
- Pilot 不通过浏览器自动化模拟后台点击。
- 后台三栏编排器属于非主路径，不作为 Pilot 对话能力不完整时的常规补丁。

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

### 2.8 精美页面来自主题契约

- Pilot 不能靠任意 CSS 或随机“自由发挥”保证页面质量。
- 每个站点向 Pilot 提供版本化主题契约：语义 Token、字体层级、容器、间距、圆角、组件变体、图片比例、Site Shell 和响应式默认值。
- Pilot 只能在主题允许的组件与变体中组合页面；需要突破主题时必须显式说明并保存为受控页面级覆盖。
- “沿用当前主题”是默认行为，不需要用户重复声明。
- 生成后必须经过真实渲染和视觉 QA，不能只依赖 Manifest Schema 校验。

### 2.9 对话不等于无界面

- 用户的操作入口是对话，但 Pilot 必须在对话中展示可视化预览、三尺寸结果、变更摘要、警告和确认卡片。
- 用户可以直接说“首屏紧凑一点”“第二段换成深色”“手机端改成单列”，Pilot 将意图转换为受控 Manifest 变更。
- 对话中不得暴露数据库字段、组件内部键或原始 JSON，除非用户明确进入开发者诊断模式。

---

## 3. 实施前系统基线（历史）

本节保留立项时的基线，便于解释架构选择，不代表当前实现状态。当前落地情况以 0.1 台账、能力发现响应和第 21 节验收证据为准。

### 3.1 当时可以复用的基础

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

### 3.2 当时的缺口（核心项已由 Phase 0–3 关闭）

当前“页面”仍然是固定结构：

- 编辑器只有 Markdown/富文本；
- `posts.content` 表达正文，不表达完整页面结构；
- 前台统一进入固定 `page.html`；
- 扩展内容类型解决“数据字段可扩展”，没有解决“页面布局和交互可扩展”；
- Pilot 的自由建站工作区当时尚未与 gcms 页面、SEO、多语言、版本和发布记录统一；
- 公开页 CSP 当时主要是报告模式，不能作为互动应用的最终安全边界；
- 后台标准页面沿用历史即时保存语义；新增的 `composition`/`app` 已使用“工作修订 → 预览 → 批准 → 发布”，标准页面旧链路继续作为兼容例外。

---

## 4. 产品目标与非目标

### 4.1 产品目标

1. 用户可以全程只在 Pilot 对话中创建、修改、预览和发布标准页面、自由编排页面与互动应用。
2. 用户不需要理解 API、JSON、组件 Schema、响应式参数或文件结构。
3. Pilot 默认读取并继承站点主题，以受控组件和真实内容生成具有品牌一致性的精美页面。
4. 每轮影响视觉的修改都提供桌面、平板、手机预览，并在发布前完成自动视觉 QA。
5. gcms 后台可以治理 Pilot 产物：查看状态与历史、校验、发布、下线、回滚，并在异常时人工接管。
6. 人工和 AI 操作同一页面工程，双方总能看到最新保存的修订，任何一方都不能静默覆盖另一方。
7. 宣传页可以绑定商品、文章、案例等后台真实数据，而不是硬编码展示内容。
8. 互动应用在可控权限下运行，不能获得服务器或后台的隐式访问权。
9. Cloudflare 静态导出包含自由页面及其不可变资源。
10. 老客户升级无须手动迁移，旧页面视觉和行为不变。

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
- 把所有主题都改造成自由页面专用主题；
- 把 gcms 后台编排器建设成与 Pilot 并列的主创作产品；
- 要求普通用户逐项填写组件属性、数据查询或三套断点参数；
- 仅凭模型审美、未经过主题契约与真实渲染检查就把页面称为“精美”。

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
| 主题契约 | Pilot 可读取的版本化设计约束，包括 Token、组件变体、构图、图片与响应式规则 |
| 视觉 QA | 基于真实浏览器渲染，对三尺寸布局、主题、数据异常和可访问性执行的发布前检查 |
| Pilot 会话来源 | 标记某次修订来自哪个连接、对话和请求 |

---

## 6. 产品信息架构

### 6.1 页面列表

现有“页面”列表继续作为统一治理入口，而不是主要创作入口，增加：

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

主要流程由用户在 Pilot 中描述目标，Pilot 自动选择合适模式；用户不必先理解三种页面类型。

gcms 后台的“新建页面”保留为高级/应急入口。点击后可选择：

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

创建成功后进入对应高级编辑器。后台必须提示“推荐在 Pilot 中通过对话创建和修改”，但不得阻止授权管理员应急操作。

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

### 6.4 自由编排页面高级编辑器（非主路径）

现有三栏工作台保留，用于审核 Pilot 产物、故障诊断、精确修正和应急接管：

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

高级/应急人工操作必须支持：

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

该编辑器必须遵循以下产品边界：

- 默认从页面列表的“高级编辑”进入，不作为新建后的推荐下一步；
- 普通治理页优先展示预览、修改摘要、数据绑定、校验问题、修订与发布状态；
- 原始组件字段、逐断点覆盖和 Manifest 诊断收进高级区域；
- 不以“后台可以手工完成”为由降低 Pilot 对话流程的完整度；
- Pilot 保存新修订后，后台刷新即可看到同一修订；后台接管保存后，Pilot 下次操作必须读取最新 ETag。

### 6.5 互动应用编辑器

该界面同样属于高级/应急入口，默认以预览、能力治理和错误处理为主：

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

### 6.7 gcms 后台职责边界

gcms 后台的稳定主职责是：

- 统一页面与应用清单、搜索、筛选和状态查看；
- 查看正式页面、草稿预览、三尺寸截图和 Pilot 修改摘要；
- 管理 Slug、SEO、语言、导航等必要治理元数据；
- 执行校验、发布、下线、回滚和失败重试；
- 查看修订、数据绑定、能力授权、发布与交付审计；
- 在 Pilot 不可用、生成结果异常或需要精确排障时进入高级编辑。

以下不再作为后台主路径指标：

- 普通用户从空白页面逐区块搭建成功率；
- 属性面板可容纳的字段数量；
- 用网页表单覆盖 Pilot 的全部表达能力；
- 把组件 Schema 原样渲染成表单即视为“可用”。

---

## 7. Pilot 的纯对话体验

### 7.1 Pilot 是唯一主要创作入口

Pilot 可以显示：

- 对话；
- 任务进度；
- 可交互预览和桌面/平板/手机截图；
- 变更摘要；
- 确认卡片；
- 主题与真实数据使用摘要；
- 视觉 QA 结果；
- 构建/校验错误；
- 页面链接。

Pilot 不复制：

- 页面属性面板；
- 拖拽画布；
- SEO 完整表单；
- 文件管理后台；
- 导航设置后台。

正常创作不能以“在 gcms 后台打开”作为完成条件。只有治理、审核、排错或用户明确要求高级手工接管时，Pilot 才提供后台深链接。

### 7.2 标准对话流程

```text
用户描述目标
→ Pilot 识别站点、语言、页面目标和是否允许发布
→ Pilot 读取能力、主题契约、导航、内容类型和真实数据
→ Pilot 选择页面模式、结构骨架、组件变体和数据绑定
→ Pilot 输出简短方案；信息足够时直接生成，不要求用户填写技术参数
→ Pilot 创建或修改工作修订
→ GCMS 执行 Manifest/资源/权限/数据校验并生成私有预览
→ Pilot 在真实浏览器渲染桌面、平板、手机
→ Pilot 执行视觉 QA；自动修复可安全修复的问题并重新检查
→ Pilot 在对话中展示预览、修改摘要、数据绑定、QA 结果和风险
→ 用户用自然语言继续修改；每轮都基于最新修订重复预览与检查
→ 用户明确确认发布某个已预览修订
→ Pilot 通过原生确认取得目标绑定授权并发布指定修订
→ Pilot 返回正式 URL、发布摘要和公网交付状态
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
- 已检查桌面、平板和手机布局，无阻断问题
[桌面预览] [平板预览] [手机预览]
```

后续对话：

```text
用户：首屏图片缩小，手机端商品改成单列，再做英文版。
AI：读取最新修订和主题契约后修改，不覆盖期间发生的后台人工调整；完成三尺寸复检后返回新预览。
```

### 7.3 自然语言修改契约

Pilot 必须把用户意图转换为可审计的结构化变更：

| 用户表达 | Pilot 应执行 |
|---|---|
| “首屏紧凑一点” | 在主题允许范围内降低 Hero 高度/间距，保留可读性并重做三尺寸预览 |
| “换成深色的一段” | 选择主题注册的深色 Section 变体，而不是写死颜色 |
| “用最新三个案例” | 查询真实案例数据源，保存筛选、排序和数量绑定 |
| “手机上图片放到文字下面” | 只增加 mobile 响应式覆盖，不破坏 desktop/tablet |
| “整体更高级” | 先说明将调整的构图、层级和组件变体，再以主题契约为边界生成 |
| “恢复上一版” | 展示目标修订与影响，经过对应确认后执行回滚 |

当需求存在多个明显不同且会显著改变页面方向的解释时，Pilot 可以提出一个简短澄清问题；否则应基于站点与上下文作合理选择并直接给出可修改草稿。

### 7.4 人工接管与 AI 交替

必须支持以下流程：

1. Pilot 创建修订 A。
2. 人在 gcms 后台基于 A 保存修订 B。
3. Pilot 再次操作时先读取 B。
4. 如果 Pilot 仍基于 A 提交，API 返回 `409 revision_conflict`。
5. AI 读取最新修订并重新应用意图，不能强制覆盖。

人工接管原则：

- 后台接管是治理和应急能力，不是 Pilot 正常任务的必经步骤；
- 接管前展示当前发布修订、工作修订、来源和未发布修改；
- 后台任何保存都创建新修订，不原地篡改 Pilot 修订；
- 接管后的页面仍可回到 Pilot 对话继续修改，不产生无法由 API 表达的隐式状态；
- 如果高级编辑产生当前 Pilot/技能版本不能理解的结构，能力发现必须返回明确不兼容，Pilot 不得降级覆盖。

### 7.5 Pilot 发布规则

- “看起来不错”“可以了”在任何上下文中都不能自动等同于发布。
- 必须要求用户出现明确动作词：“发布”“上线”“定时发布到……”；不得从满意、结束对话或沉默推断发布授权。
- 发布请求必须携带用户已预览或明确跳过预览的记录。
- 发布指定 `revision_id`，不能只写“发布当前页面”。
- 页面在确认后又发生变化时，旧确认失效，需要重新确认。
- 确认卡必须显示站点、页面、语言、正式 URL、目标修订、主题、数据绑定、能力、视觉 QA 状态和预期公网交付方式。
- Pilot 只能发起发布计划；密码输入和批准凭证由 Pilot 原生 UI 处理，秘密不进入模型上下文。
- “保存草稿”“生成预览”“修复问题”永远不隐含发布。

### 7.6 对话中的预览与反馈

- 每个影响结构、视觉、数据或交互的工作修订至少提供桌面、平板、手机三个预览入口或截图。
- 三个预览绑定同一 `revision_id`、主题版本和数据状态，不能混用不同构建结果。
- Pilot 同时给出一段人能理解的摘要：改了什么、使用了哪些真实数据、哪些内容会随后台更新、仍有哪些警告。
- 用户点击预览后仍在同一对话中继续表达修改，不需要进入后台寻找字段。
- 仅修改不可见元数据时可以不重复完整视觉截图，但仍需执行对应校验并说明未改变页面外观。

### 7.7 Pilot 与现有 Cloudflare 建站工作区

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

### 7.8 Pilot 不可用时的降级

- 已发布页面继续由 gcms/Cloudflare 正常服务，不依赖 Pilot 在线。
- gcms 后台仍可预览、发布已有 Ready 修订、回滚、下线和查看审计。
- 授权管理员可进入高级编辑器做有限应急修复。
- Pilot 恢复后先能力发现并读取最新修订，再继续对话；不能假设离线期间没有人工变化。
- 降级能力不改变 Pilot 作为唯一主要创作入口的产品定位。

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

自由页面默认继承当前主题的版本化主题契约。主题契约是 Pilot 生成精美页面的约束与素材来源，不只是颜色变量集合。

最低语义 Token：

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

每个可用于 Pilot 页面生成的主题还必须声明：

- `theme_id`、契约版本和可追踪哈希；
- 正文与展示字体层级、字重和回退字体；
- 容器宽度、Section 间距、内容密度和栅格规则；
- 按钮、表单、卡片、导航、页脚等基础组件变体；
- Hero、特性、内容列表、FAQ、CTA 等页面区块可用变体；
- 图片建议比例、裁切方式和缺图占位；
- light/dark/surface 等受控表面组合及对比度要求；
- desktop/tablet/mobile 的断点与布局默认值；
- Site Shell 支持范围、固定导航行为和页面底部留白；
- 旧主题缺失某项能力时的核心回退规则。

Pilot 的主题适配契约：

1. 创建或大改页面前读取当前主题契约，不根据主题名称猜测视觉规则。
2. 默认使用 `inherit`，沿用站点现有字体、颜色、圆角、导航和页脚。
3. 优先选择主题注册的组件变体；不得把模型临时生成的颜色、阴影和尺寸散落到页面 Manifest。
4. 用户明确要求特殊视觉时，先选择可表达该意图的受控变体；确需覆盖时保存最小、可校验的页面级 Token，并在摘要中说明。
5. 主题或契约版本变化后，已发布页面保持可渲染；重新编辑和发布前必须重新执行三尺寸预览与视觉 QA。
6. 页面修订或构建记录保存所使用的 `theme_id`、契约版本/哈希和继承模式，保证预览、确认和发布指向同一视觉基线。

“精美”最低质量门槛：

- 信息层级清楚，首屏目标、主要行动和核心内容可辨识；
- 字体、颜色、圆角、阴影和图像风格与站点主题一致；
- Section 之间具有稳定节奏和足够留白，不依赖无意义装饰填满页面；
- 真实内容为空、过长或图片比例异常时仍可控降级；
- 桌面、平板、手机均无重叠、裁切、横向滚动和不可操作控件；
- 文本对比度、键盘焦点、语义标题和交互目标满足组件注册表的可访问性要求。

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
- 若主题契约没有覆盖某个 Pilot 计划使用的区块，Pilot 必须选择核心回退组件或调整方案，不能偷偷注入任意样式绕过契约；

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
- `release_snapshot`（规划中、当前不可用）：发布页面修订时固定内容 ID 和字段快照，后续内容变化不影响该页面，直到重新发布。服务端在持久化可追溯快照前必须拒绝该枚举，能力发现当前只返回 `binding_update_modes: ["live"]`。

后台和 Pilot 必须显示当前模式，不能让用户误以为静态部署会实时变化。

Pilot 创建绑定时必须：

1. 先读取当前站点可用内容类型、字段 Schema、语言、分类和公开状态；
2. 用稳定内容类型/分类标识构造白名单查询，不从用户自然语言直接拼接 SQL 或文件路径；
3. 在保存前预检命中数量、排序、必需字段和图片可用性；
4. 在对话摘要中使用业务语言说明，例如“使用中文站点已发布的智能灯具，按发布时间倒序显示 4 项”；
5. 当用户说“用最新文章/商品/案例”时优先保存真实绑定，不把当前查询结果复制成静态卡片；
6. 如果用户明确要求固定内容，当前版本只能使用手工静态内容或明确说明 `release_snapshot` 尚不可用，不能伪装成已冻结绑定；
7. 三尺寸预览和视觉 QA 使用与目标修订一致的数据模式，并标识预览使用已发布数据还是授权草稿数据。

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
- 预览可以选择“真实已发布数据”或“当前用户有权看的草稿数据”，两者必须标识清楚；
- Pilot 不得为得到“更好看的演示”而以虚构项目、商品或统计数据替换真实查询结果；需要示例占位时必须醒目标记且不得发布。

### 10.3 静态发布语义

- Cloudflare 静态导出时解析绑定并冻结为本次部署的 HTML。
- 后台内容更新不会自动改变已经部署的静态文件，直到下一次同步。
- publication 记录保存发布校验时的页面、数据和构建哈希。
- 当前版本尚未单独持久化“实际部署文件清单 + 部署时数据快照哈希”；`live` 数据可能在 publication 与异步静态部署之间变化，因此不能把 publication 哈希表述为精确的 deployment manifest。需要精确追溯时，后续应增加内容寻址的部署清单。

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
- 对自由编排与互动应用同时绑定 `build_id`、运行时版本和主题；
- 使用签名 Token；
- 默认 `noindex, nofollow`；
- 不改变公开发布状态；
- 对 `app` 使用与正式运行相同的 CSP 和沙箱；
- 显示“草稿预览”和修订号；
- Token 到期后不可继续访问；
- 修订、构建或安全策略变化后，旧预览返回 410 并要求重新生成；
- 预览必须携带所用主题契约版本/哈希、数据模式和视口信息，以便 Pilot 判断三个尺寸是否属于同一视觉基线。

推荐接口返回：

```json
{
  "preview_url": "...",
  "revision_id": 42,
  "build_id": 17,
  "expires_at": "...",
  "render_hash": "...",
  "binding_update_mode": "live",
  "preview_data_scope": "published",
  "viewports": ["desktop", "tablet", "mobile"],
  "warnings": []
}
```

自由编排的 `render_hash` 是稳定的完整呈现契约摘要，至少覆盖页面元数据、
Manifest、真实数据快照、渲染正文、站点资料、当前主题与微调、导航、Site
Shell、布局及运行时版本。上述任一项改变后，旧构建必须返回
`build_stale`，旧预览票据必须返回 410；Pilot 应重新执行
`validate → build → preview`，不能把旧视觉结果继续用于发布确认。

### 12.3 三尺寸预览与视觉 QA

Pilot 在每次影响结构、视觉、数据或交互的修订后，必须使用真实浏览器渲染同一修订的三个产品尺寸：

| 产品尺寸 | 目的 | 最低检查 |
|---|---|---|
| desktop | 主构图和宽屏内容层级 | 容器宽度、导航、栅格、图片、CTA、横向溢出 |
| tablet | 中间宽度重排 | 列数、触控目标、导航收敛、文本换行 |
| mobile | 单手与窄屏使用 | 单列顺序、标题尺度、图片裁切、表单、固定元素 |

后台的 1080/1200/1240/1360/1440 宽度仍作为开发与回归矩阵；普通用户不需要逐个手工配置这些宽度。

自动视觉 QA 至少覆盖：

- 页面级横向滚动、元素越界、重叠、裁切和异常空白；
- 导航与 Site Shell 重复边线、固定位置、遮挡和底部留白；
- 文本溢出、孤行、异常截断、字体加载与回退；
- 图片缺失、比例失真、关键主体被裁切和低清素材；
- 主题 Token 使用、明暗表面对比度和按钮状态；
- 键盘焦点、标题层级、表单标签、触控目标和主要 CTA 可达性；
- 真实数据为空、数量变化、超长标题和缺少图片时的降级表现；
- 互动应用的加载错误、沙箱错误和获批能力可用性。

QA 结果分级：

- `blocking`：会造成不可用、安全边界破坏、关键内容不可见或明确布局损坏；禁止进入发布确认。
- `warning`：不阻止预览，但 Pilot 必须向用户说明并给出修复建议。
- `passed`：当前修订在规定主题、数据模式和三尺寸上通过。

Pilot 可以自动修复确定性且不改变用户意图的问题；每次自动修复都创建新修订、重新渲染并记录修改摘要。模型不能只依据 DOM/Schema 宣称“视觉检查通过”，必须有真实渲染证据。

### 12.4 发布原子性

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

### 12.5 Cloudflare 静态导出

静态导出必须包含：

- `composition` 渲染后的 HTML；
- `app` 的不可变构建产物；
- 页面依赖资源；
- 当前 Site Shell；
- 页面 SEO 和多语言 alternate；
- 数据绑定在本次部署的结果；

当前导出的 HTML/资源本身就是本次静态部署结果，但尚未额外生成并持久化 deployment manifest。publication 中的校验哈希不能替代部署文件清单。

静态页面若包含联系表单，必须配置与 Cloudflare 公开域名（以及本项目 Pages 地址）不同的 HTTPS GCMS Origin。互动应用只有在已批准能力确实需要服务端 Bridge 时才有该要求，并且 Bridge Origin 不能指向任意 `*.pages.dev`；未声明/未批准服务端 Bridge 的纯客户端应用可以不配置 Origin。任一条件不满足时，导出在创建产物前 fail-closed。

不兼容静态环境的能力必须：

- 在发布前阻止并说明；或
- 明确回退到 gcms 受控公网端点。

不得静默发布一个功能失效的页面。

---

## 13. API 设计

### 13.1 总体原则

- gcms 后台和 API 共用领域服务，不复制校验逻辑；
- API 为 Pilot 提供语义操作，不要求 AI 自己拼接数据库结构；
- 所有已开放的页面工程变更请求使用稳定 `Idempotency-Key` 并支持持久幂等重放；修订绑定的只读预检只使用 `If-Match`，不生成或消费幂等键；
- 所有修订写入使用乐观并发；
- 高风险操作支持 `dry_run`、影响预检、明确确认和必要时的短时解锁；
- 平台入口继续使用现有 `site_id` 前缀转发到同一站点能力。
- 能力描述复用现有控制层的操作 ID、scope、风险、确认、dry-run、解锁和可用状态结构，不另造一套不兼容协议。
- OpenAPI 描述请求/响应结构，capabilities 描述当前站点、当前密钥和当前版本实际上能做什么，两者职责分开。

### 13.2 能力发现

新增页面能力描述。以下只摘录关键字段；实际响应还包含完整限制和每个操作各自的 `available`、`granted`、scope、并发、确认及幂等契约：

```json
{
  "page_platform": {
    "version": "1",
    "modes": [
      {"id": "standard", "label": "标准页面", "available": true, "manifest_versions": []},
      {"id": "composition", "label": "自由编排页面", "available": true, "manifest_versions": [1]},
      {"id": "app", "label": "互动应用", "available": true, "manifest_versions": [1]}
    ],
    "manifest_versions": {
      "composition": [1],
      "app": [1]
    },
    "binding_update_modes": ["live"],
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
  },
  "mutation_protocol": {
    "idempotency_header": "Idempotency-Key",
    "concurrency_header": "If-Match",
    "etag_required_on_revision_writes": true,
    "approval_token_revision_bound": true
  }
}
```

限制值来自服务端配置，Pilot 不得自行假设。

这里的 `publish_approval_token` / `approval_token_revision_bound` 描述的是服务端内部批准机制，不代表 AI 可以在请求体中提交 token。公开 OpenAPI 和技能命令只允许 Pilot 原生 UI 注入 `X-GCMS-Control-Unlock`；密码和内部 approval token 都不进入模型上下文。

Pilot-first 客户端还必须分别发现以下能力是否真实可用：主题契约与契约版本、真实浏览器三尺寸预览、视觉 QA、支持的 QA 检查项、自动修复、预览截图和交付状态跟踪。服务端尚未实现的能力必须返回不可用或不出现在能力集中；Pilot 不得因为能生成 Manifest 就声称已经完成主题适配或视觉检查。

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

- 旧内容 API 的单条读取、创建和更新响应已返回强 `ETag`；更新提供 `If-Match` 时执行原子比较更新并在冲突时返回 409；
- 为兼容旧客户端，旧 `/pages`（以及其他旧内容）更新暂不强制 `If-Match`；不带 Header 的历史调用保持原有语义，因此不宣称具有页面工程级的强制乐观并发或幂等保证；
- 新后台编辑器、新技能包和所有页面工程端点必须使用 ETag；
- 新技能包经旧 `/pages` 只修改标准页面草稿，拒绝把状态改成 `published`/`scheduled`，也拒绝修改已发布标准页；这条客户端硬边界不改变旧 API 的兼容合约；
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

构建创建同样使用持久幂等收据。`composition` 的请求身份至少绑定 revision、mode/runtime、规范化 Manifest、渲染哈希和实时数据快照；`app` 还绑定源码/包与产物身份。同一 key 的同一请求身份重试（包括进程重启后）返回原构建，不同身份复用同一 key 返回 `idempotency_conflict`，并发请求只能落一条构建记录。

### 13.5 目标绑定批准凭证

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
- 发布/回滚凭证绑定 operation、site、page、project、revision、build、实时数据快照哈希、ETag、request-id 和主体；任一目标变化立即失效；
- 凭证单次使用并写入审计；
- 后台人工发布由登录会话产生同等级批准记录；
- 正式发布同时要求有效 scope、`If-Match`、`Idempotency-Key` 和由原生 unlock 在服务端内部解析出的批准凭证；公开 OpenAPI/技能合约不向 AI 暴露 token 字段；
- 高风险能力或平台策略可以额外要求短时解锁。

该硬边界适用于所有自由页面发布端点。旧标准页面 API 的历史发布行为按兼容策略保留，目前没有伪装成同等级的原生批准协议：新 Pilot 通过旧标准页面接口时默认只创建或修改草稿；若用户要求发布标准页，必须明确说明它仍走兼容链路。后续若为标准页增加目标绑定批准，应使用新的显式协议版本，不能静默改变旧客户端。

互动应用能力的 `approve` / `deny` 使用同一原生确认边界，但目标改为 operation、site、page、project、revision、capability、规范化 config 哈希、decision、ETag、request-id 和主体。AI 只能提交不含密码/token 的能力决策；首次调用取得 `unlock_required` 与不含秘密的 `page_challenge`，原生 UI 验证后只可原样重试同一逻辑请求。撤销已批准能力不使用批准 token，但仍要求独立 scope、ETag 和幂等键。

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
| `build_stale` | 409 | 构建后的实时绑定数据已变化，必须重新构建 |
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
- Pilot 对话开始、成功生成首版、完成三尺寸预览、完成发布的漏斗；
- 从用户目标到首个可见预览、每轮修改到新预览的耗时；
- 完整主题契约与核心回退契约的使用比例；
- 三尺寸视觉 QA 的 blocking/warning 数量、自动修复率和人工跳过率；
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

Pilot-first 定位不会触发任何存量内容迁移：

- 不把老标准页面自动转换成自由编排 Manifest；
- 不要求老客户启用 Pilot 才能继续查看、编辑或发布原有标准页面；
- 不改变现有主题、导航、页面路由和 Cloudflare 交付结果；
- 不自动给旧 API Key 增加页面工程或发布权限；
- 不隐藏后台原有标准页面编辑器；
- 新增主题契约字段缺失时使用核心回退 Token，且只作用于新建的 `composition` 页面；
- 现有自由页面和应用继续使用其已发布不可变修订，直到用户通过 Pilot 或后台明确创建并发布新修订。

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

- 原 `/pages` CRUD 对标准页面继续工作；页面一旦由自由工程接管，旧写接口返回 `project_api_required`，避免绕过不可变修订和批准边界；
- 原 `editor_mode=markdown|rich` 校验不变；
- 新页面工程使用新增端点；
- 能力发现明确返回服务器是否支持新端点；
- 新技能包连接旧服务器时自动降级为标准页面能力；
- 老技能包连接新服务器时看不到或忽略新能力；
- 不允许 Pilot 根据版本号猜功能，必须读取能力描述。
- 旧 Key 不因升级自动获得应用源码、应用资源、运行时能力或能力批准 scopes。
- Pilot-first 客户端连接不支持页面平台的新服务器或旧服务器时，必须明确降级为标准页面草稿能力；不得用浏览器模拟后台补齐缺失能力。

### 17.5 主题兼容

- 标准页面仍使用旧主题的模板与 CSS；
- 自由页面组件使用新的语义 Token 层；
- 旧主题没有新增 Token 时由核心默认 Token 回退；
- 不批量修改旧主题 CSS；
- 新增组件 CSS 必须在自由页面作用域内；
- 标准页面视觉回归截图必须逐主题通过。
- 主题契约是增量能力：旧主题未声明组件变体、图片规则或断点元数据时，核心契约提供保守回退；不得回写或重排旧主题文件。
- Pilot 必须在预览摘要中标明“完整主题契约”或“核心回退契约”，后者仍需通过三尺寸视觉 QA 才能发布。

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
| 人工和 Pilot 同时编辑页面工程，或新客户端以强 ETag 更新旧内容 | 后提交者收到 `409`，读取新修订/ETag 后重做；不带 `If-Match` 的旧内容客户端仍是兼容例外 |
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

下列“交付/退出条件”是实施顺序与发布门禁的原始定义。Phase 0–3 的核心代码已经接线，不等于所有退出条件均已完成；真实历史库、全主题人工视觉、真实 Pilot 会话和真实 Cloudflare 环境的未完成项统一列在 21.2，发布时以该清单为准。

2026-07-26 起的产品优先级以“Pilot-first 产品化阶段”为当前主线。已落地的后台编排器继续维护，但不再以扩展普通用户手工编排体验为优先事项。

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

- 授权管理员可通过高级/应急路径独立创建并上线一张真实宣传页，证明领域能力不依赖 Pilot 在线；这不是普通用户主路径；
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

- Pilot 创建的页面能在后台治理并在必要时进入高级编辑；
- 后台修改后 Pilot 能继续编辑且不覆盖；
- 未明确发布时只能产生草稿；
- 老技能包/旧服务器兼容测试通过。

### Pilot-first 产品化阶段（当前下一优先级）

交付：

- Pilot 覆盖“理解目标 → 读取主题与数据 → 生成 → 三尺寸预览 → 视觉 QA → 对话修改 → 明确确认 → 发布”的完整主路径；
- 主题契约的能力发现、版本/哈希绑定、核心回退和 Pilot 使用规则；
- 页面目标与组件骨架选择策略，不要求用户先选择模式或逐项填写 Schema；
- 真实数据发现、绑定预检、业务语言摘要和空/长/缺图数据视觉检查；
- 桌面、平板、手机真实浏览器预览，以及可追溯的自动视觉 QA；
- 对话内变更摘要、警告、发布确认卡和公网交付状态；
- gcms 后台信息架构收敛为治理、审核、发布、回滚和应急高级编辑；
- Pilot/后台交替编辑、能力降级和旧服务器兼容。

退出条件：

- 新用户仅通过 Pilot 对话即可创建并上线一张主题一致、绑定真实数据的精美宣传页；
- 用户无需进入 gcms 后台或理解页面模式、组件键、Manifest、断点和数据字段；
- 每轮视觉修改均能查看同一修订的桌面、平板、手机结果；
- 视觉 QA 有真实浏览器证据，阻断问题不能进入发布确认；
- 发布必须由明确动作词和目标绑定原生确认触发，修改后旧确认立即失效；
- Pilot 产物可以在后台治理、回滚和应急接管，接管后仍能回到 Pilot 继续对话；
- 老客户、旧页面、旧主题、旧 API 与旧技能包没有非预期变化。

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

在 Pilot-first 主路径达到上述退出条件前，模板市场、复杂 A/B、后台任意拖拽和更多属性面板不应优先于对话生成质量、主题适配、三尺寸预览与视觉 QA。

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
- 状态、来源、数据绑定和构建筛选；
- 草稿/正式预览与三尺寸 QA 结果；
- 历史、校验和发布；
- 下线、回滚和交付失败重试；
- SEO、Slug、语言和导航治理；
- 非主路径的新建向导与高级编排工作台；
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
- 页面目标、主题契约、真实数据和模式选择的对话任务规范；
- 受控 Manifest/应用包生成与修订；
- 桌面、平板、手机真实预览；
- 自动视觉 QA、确定性修复与证据摘要；
- 自然语言迭代和结构化变更摘要；
- 目标绑定的原生发布确认；
- 冲突重试；
- 治理/应急后台深链接；
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
- publication 校验哈希（独立 deployment manifest 仍为后续项）；
- 缓存与回滚。

---

## 21. 验收标准

勾选只代表已有可重复的自动化证据；真实发布环境、真实历史二进制或人工视觉验收不得用合成测试替代。

### 21.1 已有自动化证据

- [x] 新库、重复迁移、失败回滚和多站点隔离测试通过。
- [x] 合成的“页面平台前 / V1–V5”Schema fixture 可逐代升级到 V6，旧页面关键字段与计数保持一致且不回填 `page_projects`；每代 fixture 会先移除后续版本对象，避免因误留新表而产生假通过。
- [x] 标准页面继续走旧渲染链；页面工程的旧接口写入被阻断，路由所有权冲突双向 fail-closed。
- [x] `composition` 后台/API 可创建不可变修订、绑定真实数据、构建、预览、发布与回滚；绑定数据变化会使旧构建返回 `build_stale`。
- [x] 页面工程 ETag、持久幂等、发布/回滚/能力批准的目标绑定确认和原子 publication 有冲突、重放与失败回滚测试。
- [x] 新技能先发现能力；单站、平台和打包 CLI 的页面协议、降级、原生解锁与 OpenAPI 一致性有自动化测试。
- [x] App 包路径、大小、类型、远程代码、CSP、iframe sandbox、Bridge 授权/撤销及上一个可用版本保护有测试。
- [x] 自由页面资源和 App 构建产物可静态导出；联系表单与 Bridge 缺少独立 HTTPS Origin 时 fail-closed。
- [x] 发布失败不切换公开指针；回滚绑定历史修订和对应的不可变资源/构建。

### 21.2 发布前仍需完成的真实验收

- [ ] 使用至少三个真实受支持历史发布版本生成的数据库完成升级、备份、恢复演练；当前逐代测试仍是合成 Schema fixture，不冒充真实旧二进制。
- [ ] 对全部既有主题，在升级前后以相同内容和视口完成截图对比并人工确认无非预期差异。
- [ ] 在真实浏览器完成后台 1080/1200/1240/1360/1440、平板和手机交互验收。
- [ ] 用真实 Pilot 对话走完“AI 创建 → 后台人工修改 → AI 合并 → 原生确认发布 → 回滚”，核对展示文案与审计串联。
- [ ] 在真实 Cloudflare Worker Assets/Pages 环境比较源站与静态结果，验证 HTTPS 回源、失败重试和上一版本保留。
- [ ] 若产品需要精确追踪“部署时”数据，先实现并验证独立 deployment manifest；当前只持久化 publication 校验哈希，不勾选部署快照可追溯。

### 21.3 Pilot-first 产品验收

- [ ] 用户只描述页面目标、受众和必要内容，Pilot 即可读取主题与真实数据并生成首版，不要求用户先选组件或填写后台表单。
- [ ] Pilot 默认沿用当前主题；预览、确认和发布记录绑定同一主题契约版本/哈希。
- [ ] “使用最新文章/商品/案例”等请求生成真实数据绑定，并在对话中说明查询范围、排序、数量和更新语义。
- [ ] 每次视觉变更返回同一修订的桌面、平板、手机预览；三个尺寸不存在横向滚动、重叠、裁切和关键操作不可达。
- [ ] 自动视觉 QA 基于真实浏览器渲染；存在 `blocking` 问题时无法取得发布确认。
- [ ] 用户可连续用自然语言修改布局、内容、主题变体、数据与响应式表现，无需进入 gcms 后台。
- [ ] “不错”“可以了”或结束对话不会触发发布；只有明确发布动作与目标绑定原生确认可以发布已预览修订。
- [ ] 用户确认后若主题、数据基线、修订或构建发生变化，旧确认失效。
- [ ] gcms 后台可以查看 Pilot 草稿、预览、绑定、QA、历史和交付状态，并可发布、下线、回滚或应急接管。
- [ ] 后台应急接管创建新修订；Pilot 随后能读取该修订继续对话且不会覆盖人工修改。
- [ ] Pilot 服务不可用时，已发布页面不受影响，后台仍能治理和回滚。
- [ ] Pilot-first 功能关闭或未启用时，老客户的旧页面、旧主题、旧 API、旧技能包和后台标准编辑流程保持原样。

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
| 主题 | 内容类主题、通用主题、深色主题、旧主题、完整契约、核心回退契约、主题切换 |
| 视口 | 跟随主题、1080、1200、1240、1360、1440、平板、手机 |
| Pilot 主路径 | 首版生成、连续自然语言修改、三尺寸预览、视觉 QA、明确发布、取消发布 |
| 视觉数据 | 空结果、单项、满额、超长标题、缺图、不同图片比例 |
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
15. 把进入 gcms 后台填写属性面板作为 Pilot 正常页面任务的必要步骤。
16. 绕过主题契约写死品牌颜色、字体、圆角、容器或断点。
17. 为了预览效果硬编码本应来自 gcms 的文章、商品、案例或统计数据。
18. 未经过真实浏览器三尺寸渲染就宣称“视觉 QA 已通过”。
19. 从“不错”“可以了”、结束对话或用户沉默推断正式发布授权。

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
- 对话中可以承载预览、修改摘要、QA 和确认，而不需要复制后台属性表单；
- 人工应急接管与 AI 能交替工作；
- Pilot 保持“说目标即可”的价值；
- 后台承担治理、审核、发布、回滚和精细应急控制。

### ADR-005：发布指定不可变修订

决定：预览、确认和发布都绑定明确的 `revision_id`。

原因：

- 防止确认后内容变化；
- 支持并发冲突；
- 审计和回滚清晰；
- Cloudflare 构建可追溯。

### ADR-006：Pilot-first，后台编排器降级为治理与应急入口

日期：2026-07-26

决定：

- Pilot 是标准页面、自由编排页面和互动应用的唯一主要创作入口；
- 用户通过对话完成生成、预览、修改和明确发布；
- gcms 后台继续操作同一领域模型，但产品职责收敛为治理、审核、发布、下线、回滚和应急接管；
- 已实现的三栏编排器保留，不删除、不破坏 API，也不再作为普通用户从零创作的主路径；
- 页面精美程度由版本化主题契约、受控组件、真实数据、三尺寸预览和视觉 QA 共同保证。

原因：

- 逐字段属性面板把结构化协议直接暴露给普通用户，学习成本高，不能体现 AI 页面平台的核心价值；
- 对话更适合表达目标和连续修改，但必须结合可视化预览而不是只返回文字；
- 主题契约和组件注册表比任意模型样式更能稳定保持品牌一致性、响应式和可维护性；
- 后台治理与应急能力仍然保证审计、发布安全、回滚和 Pilot 不可用时的业务连续性；
- 该调整不改变存储、Manifest、权限、修订、发布和兼容架构，只改变产品入口优先级。

兼容影响：

- 不迁移或转换任何旧页面；
- 不删除标准页面编辑器或现有编排器；
- 不改变老 API、老技能包和旧主题行为；
- 新主题契约缺失时使用核心回退并只影响新建自由页面；
- 后续新增 Pilot 能力仍通过能力发现启用，不能根据版本号猜测。

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
