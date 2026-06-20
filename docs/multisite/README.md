# 多站点后台设计记录

本文记录把当前单站 CMS 升级为多站点 / 站群管理的第一版设计与分阶段落地方案。

当前代码已落地 `system.db`、默认站点兼容登记、平台管理员凭据和平台会话、站点运行时池、Host 匹配、站点 / 域名管理、平台 `site_id` 自动化入口、平台 AI 接入包、登录态后台预览入口，以及 `/admin/sites/{site_id}/...` 到现有后台路径的兼容跳转。可选目录整理、完整表单前缀化和角色权限仍作为后续增强。

本版设计已经移除路径型站点，只考虑通过域名或子域名绑定站点。

## 设计目标

当前系统是一套后台管理一个前台站点。目标是升级为：

- 登录后先进入综合管理后台，管理多个站点。
- 每个站点都有完整的前台页面能力、内容、设置、主题、上传文件和自动化接口。
- 自动化接口对每个站点只有**一套 Key**（存在站点自己的 `cms.db`），但提供两个访问入口：按站点域名的公开入口、以及按 `site_id` 的平台入口（给未绑定域名的站点用）。
- 可以新增多个站点。
- 可以启用或关闭某个站点。
- 可以把任意启用站点设置为默认站点。
- 默认站点使用当前系统默认前台地址。
- 非默认站点如需公开前台访问，必须配置独立访问域名或子域名。
- 不支持通过路径区分站点，例如 `/sites/blog`、`/blog` 这类路径型站点。
- 未绑定域名的站点可以在登录态下通过后台预览入口查看前台效果，但不提供公开前台入口。
- 未绑定域名的站点没有公开 API Base；如需 AI 管理内容，开启该站点的平台入口即可（按 `site_id` 访问），用的仍是站点自己的同一套 Key。

## 核心分层

系统分成两层：

```text
平台后台
  管理站点列表、绑定域名、默认站点、启用状态、各站点「平台自动化入口」开关

站点后台
  管理某一个站点的文章、链接、页面、分类、主题、导航、上传、自动化接口 Key
```

后台路径建议：

```text
/admin
  综合管理后台

/admin/sites/{site_id}
  某个站点的后台概览

/admin/sites/{site_id}/posts
/admin/sites/{site_id}/links
/admin/sites/{site_id}/pages
/admin/sites/{site_id}/settings
  站点级后台功能
```

说明：

- `/admin` 永远是平台综合管理后台。
- 现有单站后台功能迁移到 `/admin/sites/{site_id}/...`。
- 后续如果做角色权限，可以在这个路径结构上区分平台管理员和站点管理员。

实施提醒：

- `/admin/sites/{site_id}/...` 是最终路径形态，不建议低估它的改造量。
- 当前后台大量表单 action、redirect、分页链接、上传入口、设置页链接都默认从 `/admin/...` 开始，完整前缀化会牵动整个 admin 层。
- 第一阶段可以采用过渡方案：平台会话里保存 `current_site_id`，现有 `/admin/posts`、`/admin/settings` 等后台先读取当前站点上下文运行。
- 等多站点运行时和数据隔离稳定后，再逐步把站点级后台路径规范为 `/admin/sites/{site_id}/...`。

## 数据模型

建议引入一个平台级数据库 `system.db`，只保存站点和绑定域名等平台信息。每个站点继续使用独立的 `cms.db`。

### Site

```text
Site
- id
- slug
- name
- status: enabled / disabled
- is_default
- management_automation_enabled
- admin_note
- db_path
- upload_dir
- created_at
- updated_at
```

说明：

- `slug` 用作内部标识，例如 `main`、`blog`、`docs`。
- `status=disabled` 时，该站点所有前台访问返回 404 或维护页；第一版建议返回 404。
- `is_default=true` 的站点只能有一个。
- `management_automation_enabled` 控制该站点是否允许通过平台入口（按 `site_id`）访问自动化接口，独立于公开前台启用状态。
- 站点可以没有绑定域名；这种站点没有公开前台入口，但可以通过登录态后台预览查看。
- `db_path` 和 `upload_dir` 不建议用户自由填写：建站时由系统按当时 `slug` 在 `data/sites/{slug}/` 下生成并**存库**，此后以存库路径为准。
- `slug` 可改名（只改内部标识与展示），但**不移动已有物理目录**；改名后磁盘目录名可能与新 `slug` 不一致，一切以存库的 `db_path` / `upload_dir` 为准。
- `db_path` 和 `upload_dir` 必须校验在 `data/sites/` 目录内，避免越权访问其他文件。

### SiteDomain

```text
SiteDomain
- id
- site_id
- scheme: https / http
- host
- is_primary
- redirect_to_primary
- enabled
- created_at
- updated_at
```

说明：

- `host` 是规范化后的域名或子域名，例如 `blog.example.com`。
- `scheme` 用于生成 canonical、RSS、sitemap、公开 OpenAPI 等绝对地址。
- `is_primary` 表示该站点的主域名，canonical、RSS、sitemap、公开 OpenAPI 地址优先使用它。
- `redirect_to_primary` 表示该域名只是别名，访问时跳转到主域名。
- `enabled=false` 时，该域名不参与前台匹配。
- 同一个站点可以绑定多个域名，但同一站点最多一个主域名。
- 同一个域名只能绑定到一个启用站点。
- 非默认站点不得绑定 `BASE_URL` 的 Host。
- `BASE_URL` 的 Host 保留给默认站点；默认站点可以不显式保存这条域名绑定。
- 如果默认站点显式保存 `BASE_URL` Host，则只能由默认站点持有。
- Host 需要规范化后比较：小写、去空白、保留端口、必要时处理 IDN / punycode。
- `is_default` 和 `is_primary` 必须通过数据库约束或事务保证唯一性。

### PlatformAdmin

```text
PlatformAdmin
- id
- username
- password_hash
- created_at
- updated_at
```

说明：

- 平台登录账号属于 `system.db`，不属于任何一个站点的 `cms.db`。
- 当前单站的 `admin_user`、`admin_password_hash` 升级时应迁移到 `system.db`。
- 后续如果做多角色权限，可以在此基础上扩展平台管理员、站点管理员、编辑员。

### PlatformSession

```text
PlatformSession
- token_hash
- admin_id
- csrf
- current_site_id
- expires_at
- created_at
- updated_at
```

说明：

- 平台会话属于 `system.db`。
- `current_site_id` 用于第一阶段过渡：进入某个站点后台后，现有 `/admin/posts` 等后台功能可读取当前站点上下文。
- 最终路径完全前缀化后，`current_site_id` 仍可用于后台导航、最近访问站点等体验优化，但不应作为唯一权限边界。

### 自动化 Key 放在站点库（不新增平台 Key 表）

自动化 Key **不在 `system.db` 里另起一张表**，仍然存在每个站点自己的 `cms.db`（沿用现有的 `automation_keys`）。两个访问入口（公开域名入口、平台 `site_id` 入口）校验的是**同一批 Key**：

- 公开入口按 Host 定位站点 → 打开该站 `cms.db` → 校验 Key。
- 平台入口按 `site_id` 定位站点 → 打开该站 `cms.db` → 校验同一批 Key。
- 平台入口干活本来就要打开站点 `cms.db`，所以**没必要**把 Key 集中到 `system.db`，避免「两套密钥体系」的重复维护面。
- 是否允许平台入口，由 `Site.management_automation_enabled` 控制（见上）。
- 若想要「只能走平台入口」的 Key，把它做成 Key 上的一个 scope（如 `via_platform`），而不是另一套密钥。
- 审计日志仍需记录 `site_id`，方便排查。

## 存储结构

推荐结构：

```text
data/
  system.db
  sites/
    main/
      cms.db
      uploads/
    blog/
      cms.db
      uploads/
    docs/
      cms.db
      uploads/
```

原则：

- 平台信息放在 `system.db`。
- 每个站点内容数据放在自己的 `cms.db`。
- 每个站点上传文件放在自己的 `uploads/`。
- 站点之间不共享内容库和上传目录。
- 当前已有单站数据升级后应迁移为默认站点。
- 当前单站数据迁移时需要先完整备份原 `cms.db` 和 `uploads/`。

## 站点运行时与缓存隔离

这是多站点实现的最大风险点。

当前单站实现里，`Server` 绑定单个 `store`、单个 `baseURL`、单个 `uploadDir`，并持有全局内容缓存和端点缓存。多站点后必须改成按站点隔离的运行时。

建议模型：

```text
SiteRuntime
- site
- primary_domain
- aliases
- store
- upload_dir
- asset_version
- content_cache
- endpoint_cache
- settings_cache
- updated_at
```

运行规则：

- 请求进入后先解析出当前站点，再从运行时池取得对应 `SiteRuntime`。
- 每个站点有独立 `store`，不能复用默认站点的 `store`。
- `content_cache` 必须按站点隔离，避免文章 HTML、TOC、Markdown 渲染结果串站。
- `endpoint_cache` 必须按站点隔离，避免 sitemap、RSS、OpenAPI 等端点串站。
- `settings_cache` 如后续引入，也必须按站点隔离。
- 清理缓存时只清理当前站点；平台级配置变更才清理站点映射或运行时池。
- 站点关闭、删除、迁移或数据库路径变化时，要关闭对应 `store` 并移除运行时。

运行时池建议：

```text
SiteRuntimePool
- system_store
- map[site_id]*SiteRuntime
- map[host]*site_id
- default_site_id
```

注意：

- Host 到站点的映射来自 `system.db`。
- 站点列表、默认站点、域名绑定变更后，需要刷新 Host 映射。
- 如果站点很多，运行时池可以做懒加载和空闲关闭；第一版也可以全部打开，先保证正确性。

## 后台预览入口

未绑定域名或未上线站点，需要一个登录态下的后台预览入口。这不是路径型公开站点，而是后台功能。

建议路径：

```text
/admin/sites/{site_id}/preview/
/admin/sites/{site_id}/preview/zh/
/admin/sites/{site_id}/preview/zh/posts/{slug}
/admin/sites/{site_id}/preview/uploads/{file}
```

规则：

- 必须登录后台。
- 服务端根据 URL 中的 `site_id` 直接选择站点运行时，不依赖 Host。
- 强制输出 `X-Robots-Tag: noindex, nofollow`。
- 页面 meta robots 也应强制 `noindex, nofollow`。
- 不生成公开 canonical；如必须输出 canonical，应指向站点主域名，未绑定主域名时不输出 canonical。
- 预览入口只用于后台查看，不允许作为公开访问地址配置。
- 预览上传文件也必须按站点读取，不能穿透到其他站点上传目录。
- **页面内的站内链接和资源要复用「按站点注入 URL-base」的同一套机制**：前台模板生成的 `/zh/posts/a`、`/uploads/x`、`/assets/...` 在预览前缀下会指到平台根，所以预览渲染时 base 要设为预览前缀 `/admin/sites/{site_id}/preview`（或在页面注入 `<base href>`），否则点链接会跳出预览、图片 404。这正是删掉「路径型站点」省掉的前缀逻辑，在预览这里需要小范围复用一次。

这样可以解决“未上线、零域名站点”的预览问题，同时不引入路径型公开站点。

## 域名绑定规则

第一版只支持 Host 型访问。

示例：

```text
https://cms.example.com       -> 默认站点
https://blog.example.com      -> 博客站点
https://docs.example.com      -> 文档站点
https://client-a.example.com  -> 客户 A 站点
```

不支持：

```text
https://cms.example.com/sites/blog
https://cms.example.com/blog
https://cms.example.com/docs
```

这样做的好处：

- 前台 URL 结构保持当前形态，不需要给所有链接加站点路径前缀。
- 上传文件路径保持 `/uploads/...`，不需要变成 `/sites/blog/uploads/...`。
- sitemap、RSS、canonical、公开 OpenAPI 规则更清楚。
- Caddy 只负责把域名代理进应用，应用按 Host 匹配站点。
- 后续 SEO 和缓存策略更简单。

校验规则：

- 绑定域名不能为空。
- 绑定域名不能包含路径，例如不允许 `blog.example.com/foo`。
- 绑定域名规范化后不能和其他启用域名重复。
- 非默认站点不能绑定 `BASE_URL` 的 Host。
- `BASE_URL` Host 是默认站点入口，切换默认站点时由新默认站点接管。
- 绑定、解绑、设置主域名、设置默认站点必须在事务内完成。
- 如果使用 SQLite，建议用部分唯一索引保证约束：

```sql
-- 全局只能有一个默认站点
CREATE UNIQUE INDEX idx_sites_one_default
ON sites(is_default)
WHERE is_default = 1;

-- 同一个启用 host 只能出现一次
CREATE UNIQUE INDEX idx_site_domains_enabled_host
ON site_domains(host)
WHERE enabled = 1;

-- 每个站点最多一个主域名
CREATE UNIQUE INDEX idx_site_domains_one_primary
ON site_domains(site_id)
WHERE is_primary = 1;
```

## 默认站点规则

默认站点使用当前系统默认前台地址，也就是 `BASE_URL` 代表的地址。

示例：

```text
BASE_URL=https://cms.example.com
默认站点=main
```

访问：

```text
https://cms.example.com/zh/
```

进入默认站点。

切换默认站点时：

- 新默认站点必须是 `enabled`。
- 原默认站点自动取消默认。
- 新默认站点接管 `BASE_URL` 对应地址。
- 原默认站点如果没有其他绑定域名，则没有公开前台入口，但仍可从后台管理和预览。
- 如原默认站点已有其他绑定域名，则继续保留。

## 前台匹配规则

请求进入系统后，建议按以下顺序匹配：

1. **平台专属前缀 `/admin` 和 `/api/platform/` 只在平台 Host（即 `BASE_URL` 的 Host）提供**；其它站点 Host 命中这两个前缀一律 404 或 302 到平台 Host。这样避免每个站点域名都暴露一个登录页，也避免平台会话 cookie 的跨域名混乱。
2. 平台 Host 上：`/admin/...` → 综合 / 站点后台；`/admin/sites/{site_id}/...` → 某站后台；`/api/platform/v1/sites/{site_id}/...` → 平台自动化入口。
3. 其余请求按 Host 定位前台站点：
   - Host 匹配到启用的 `SiteDomain.host` 且站点 `enabled` → 进入该站点。
   - Host 未匹配绑定域名，但等于 `BASE_URL` 的 Host → 进入默认站点。
   - 其他未知 Host → 404。

注意：

- 平台 Host 上同时跑着 `/admin`、`/api/platform/`、默认站点自己的公开 API `/api/admin/v1/`、以及默认站点前台，**这几个前缀必须在「落到默认站前台」之前先匹配**；默认站点的内容 slug 不能占用这些前缀。
- 这个规则也避免未知域名直接暴露默认站点。

## URL 生成规则

站点数据库里仍然保存站点内路径，不保存域名。

例如内容里保存：

```text
/uploads/cover.webp
/zh/posts/a
```

渲染时根据当前站点的主域名或当前请求域名生成完整地址。

示例：

```text
https://blog.example.com/uploads/cover.webp
https://blog.example.com/zh/posts/a
```

原则：

- canonical 使用站点主域名。
- sitemap 使用站点主域名。
- RSS 使用站点主域名。
- 公开站点 OpenAPI 和公开 AI 接入包使用当前站点的公开 API Base，也就是站点主域名。
- 平台入口的 OpenAPI 和管理型 AI 接入包使用平台 API Base，也就是 `BASE_URL` 下的 `/api/platform/v1/sites/{site_id}`。
- 如果通过别名域名访问，并且 `redirect_to_primary=true`，应先跳转到主域名。
- 如果通过别名域名访问且不跳转，canonical 仍然指向主域名。
- 全局 `BASE_URL` 只代表平台默认入口 / 默认站点入口，不再作为所有站点的 SEO base。

## 上传文件规则

每个站点独立上传目录：

```text
data/sites/blog/uploads/
data/sites/docs/uploads/
```

前台 URL 仍然是：

```text
/uploads/file.webp
```

但不同域名下读取的是不同站点的上传目录。

示例：

```text
https://blog.example.com/uploads/cover.webp
https://docs.example.com/uploads/cover.webp
```

这两个 URL 可以对应两个不同站点里的不同文件。

站点 A 不能访问站点 B 的上传文件。

## 自动化接口规则

每个站点只有**一套 Key**（存在站点自己的 `cms.db`，沿用现有 `automation_keys`），但有**两个访问入口**。两个入口校验的是同一批 Key，互通。

### 入口①：公开域名入口（按 Host）

适合已经绑定公开域名的站点。

```text
https://blog.example.com/api/admin/v1/posts
https://docs.example.com/api/admin/v1/posts
```

- 请求按 Host 定位站点 → 打开该站 `cms.db` → 校验 Key。
- 公开 AI 接入包从站点后台下载，写入该站点的公开 API Base（站点主域名）。
- 默认站点没有显式绑定域名时，公开 API Base 用 `BASE_URL`。
- 未绑定域名的站点没有公开 API Base，也不生成可直接外部访问的公开接入包——它们用入口②。

### 入口②：平台入口（按 `site_id`）

入口在平台 Host 上，用 URL 中的 `site_id` 指定站点，**不依赖站点自己的绑定域名**，适合未上线、未绑定域名、本地测试或只需后台管理的站点。

```text
https://cms.example.com/api/platform/v1/sites/{site_id}/posts
https://cms.example.com/api/platform/v1/sites/{site_id}/links
https://cms.example.com/api/platform/v1/sites/{site_id}/media
```

其中 `https://cms.example.com` 来自平台 `BASE_URL`。

- 请求只需命中平台 Host；按 URL 里的 `site_id` 选站点 → 打开该站 `cms.db` → 校验**同一批 Key**。
- 是否放行入口②，由 `Site.management_automation_enabled` 控制（按站点开关）。
- Key 只对自己所属站点有效：即使路径里手动改成别的 `site_id` 也必须失败（权限边界以 Key 所属站点为准，而非 URL）。
- 想要「只能走入口②、不在公开域名暴露」的 Key，加一个 scope（如 `via_platform`）即可，不必另起一套密钥。
- 站点 `disabled`（公开前台关闭）时是否仍可被入口② 管理，由 `management_automation_enabled` 决定。
- 平台管理型 AI 接入包从综合后台的站点管理页下载，写入平台 API Base + 固定 `site_id`；其 OpenAPI `servers.url` 用 `BASE_URL` 下的 `/api/platform/v1/sites/{site_id}`。
- 未绑定域名站点上传的媒体只有站内相对路径（`/uploads/...`），没有可外部直达的绝对 URL，预览态可见。
- 平台入口上传媒体返回 `url=/uploads/...` 作为内容引用；可选返回登录态 `preview_url` 仅供后台或 AI 检查，不作为公开 URL。

第一版不做跨站批量入口：一个 Key 仍只对应一个站点，没有「一把 key 管所有站点」。

## SEO 规则

主域名：

- 可索引。
- canonical、sitemap、RSS 使用该主域名。
- Open Graph、JSON-LD、hreflang、公开 OpenAPI 地址都使用该主域名。

别名域名：

- 默认建议跳转到主域名。
- 如果不跳转，应明确 canonical 到主域名，避免重复收录。

关闭站点：

- 第一版建议前台返回 404。
- 后续可增加维护页。

未绑定域名的站点：

- 没有公开前台入口。
- 只能在后台管理。
- 不生成可公开访问的 sitemap、RSS、canonical。
- 可以通过后台预览入口查看，但强制 `noindex, nofollow`。

SEO 实现要求：

- SEO 层必须从“全局 `BASE_URL`”切换为“当前站点主域名”。
- sitemap 缓存 key 必须包含 `site_id`。
- RSS 缓存 key 必须包含 `site_id` 和语种。
- 公开 OpenAPI 文档和公开 AI 接入包的 API Base 必须使用当前站点主域名。
- 平台入口的 OpenAPI 文档和管理型 AI 接入包的 API Base 必须使用 `BASE_URL` 下的 `/api/platform/v1/sites/{site_id}`。
- alias host 访问时，如不跳转，页面 canonical 必须指向 primary host。

## 本地测试规则

因为第一版不支持路径型站点，本地测试通过本地域名完成。

推荐方式：

```text
127.0.0.1 main.test
127.0.0.1 blog.test
127.0.0.1 docs.test
```

然后在系统中绑定：

```text
http://main.test:8080
http://blog.test:8080
http://docs.test:8080
```

也可以通过 Caddy 本地代理：

```text
main.test  -> 127.0.0.1:8080
blog.test  -> 127.0.0.1:8080
docs.test  -> 127.0.0.1:8080
```

或者开发调试时直接指定 Host Header：

```text
curl -H "Host: blog.test:8080" http://127.0.0.1:8080/zh/
```

说明：

- 暂未上线站点如果需要模拟真实域名访问，可以绑定一个本地域名或内网域名。
- 如果不绑定任何域名，该站点仍可通过后台预览入口查看。
- 后台预览不依赖 Host，且强制 `noindex, nofollow`。
- 如果不绑定任何域名但需要 AI 管理内容，应启用平台入口（按 `site_id`），而不是公开域名入口。

## 迁移与系统更新

多站点后，迁移分为两类：

```text
system.db migration
site cms.db migration
```

启动时建议顺序：

1. 打开或创建 `system.db`。
2. 执行 `system.db` 迁移。
3. 如果检测到旧单站结构，创建默认站点并登记原 `cms.db`、`uploads/` 路径，同时复制管理员凭据到平台库。
4. 读取所有站点记录。
5. 逐站打开 `cms.db` 并执行站点库迁移。
6. 构建 Host 映射和运行时池。

系统更新时也必须逐站 migrate：

```text
migrate system.db
for each site in sites:
  migrate site.cms.db
```

规则：

- `system.db` 和每个站点 `cms.db` 都要有独立、幂等、可重复执行的迁移。
- 站点无论 enabled 或 disabled，只要还在系统里，都应能被迁移，避免未来重新启用时结构落后。
- 系统更新前备份范围必须从“一个 cms.db”扩大为：
  - `system.db`
  - 所有站点 `cms.db`
  - 必要的运行状态文件
  - 上传目录不一定每次完整备份，但路径和版本切换逻辑不能破坏上传目录
- 迁移失败要**分级**，不能静默跳过：`system.db` 迁移失败属平台级，应阻止启动；**某个站点 `cms.db` 迁移失败，只把该站点标记为降级 / 维护并返回 503，其它站点照常服务**，并在平台后台高亮报错——别让一个坏库拖垮整个平台。
- 回滚时要同时考虑 `system.db` 和各站点库的一致性。

## 背景任务

当前单站后台任务需要扩展为逐站执行。

需要 fan-out 的任务：

- 定时发布。
- sitemap / RSS 相关缓存清理。
- Cloudflare 同步或静态导出。
- 后续可能增加的备份、健康检查、数据清理任务。

规则：

- 后台任务遍历所有 enabled 站点。
- 如果任务会影响站点公开内容，应使用该站点自己的 `store`、上传目录和主域名。
- 某站点任务失败不应导致其他站点任务全部停止，但应记录平台级日志。
- 任务日志需要包含 `site_id` 和站点名称，方便排查。
- 如果站点 disabled，默认不执行公开内容任务；迁移类任务除外。

## Caddy 与应用的边界

Caddy 负责：

- 域名解析后的请求入口。
- TLS。
- 反向代理。

应用负责：

- 根据 Host 判断站点归属。
- 对平台入口（`/api/platform`），根据 URL 中的 `site_id` 判断站点归属。
- 根据站点生成 canonical、RSS、sitemap、公开 OpenAPI 或平台管理 OpenAPI 地址。
- 根据站点选择数据库和上传目录。
- 判断站点是否启用。

因此 Caddy 不负责决定“哪个站点”，它只把请求转发给应用；公开访问的站点归属由应用按 Host 完成，平台入口的站点归属由应用按 `site_id` 完成。

## 具体执行方案

第一版落地建议按阶段推进，目标是先保证旧单站用户无感升级，再逐步打开多站能力。不要一开始就把整个后台全部改成 `/admin/sites/{site_id}/...`，否则改动面和回归风险会过大。

### 阶段 0：准备与基线保护

目标：先把现有单站行为固定住，避免多站改造时破坏旧功能。

要做：

- 补齐现有后台和自动化接口的基线测试：
  - 后台登录、设置页保存、文章 / 链接 / 页面 CRUD。
  - 上传图片 / 媒体。
  - 自动化 Key 创建、吊销、重生成、权限校验。
  - `/api/admin/v1/openapi.json`、AI 接入包、草稿预览、预览链接。
  - sitemap、RSS、canonical、uploads。
- 给关键 URL 生成逻辑补测试，覆盖 `BASE_URL`、反向代理 Host、公开 API Base。
- 明确保留旧路径：
  - `/admin/...` 继续可用。
  - `/api/admin/v1/...` 继续可用。
  - 旧 AI 接入包里的 `GCMS_API_BASE` 不失效。

风险控制：

- 本阶段不改数据结构，不迁移文件。
- 只建立测试网和兼容承诺。

### 阶段 1：引入 `system.db`，但默认站点继续兼容旧路径

目标：建立平台层数据模型，但让旧用户升级后前台、后台、API 尽量无感。

要做：

- 新增 `system.db` 及迁移机制。
- 新增 `sites`、`site_domains`、`platform_admins`、`platform_sessions` 等平台表。
- 首次升级时自动创建默认站点：
  - `slug=main`
  - `is_default=true`
  - `status=enabled`
  - `management_automation_enabled=true`
  - `db_path` 指向当前旧 `CMS_DB`，第一阶段建议**不移动原 `data/cms.db`**。
  - `upload_dir` 指向当前旧 `UPLOAD_DIR`，第一阶段建议**不移动原上传目录**。
- 把旧 `admin_user`、`admin_password_hash` 复制 / 迁移到 `system.db` 的平台管理员表。
- 后台 session 改用 `system.db`。
- 第一阶段保留旧路径；最终标准结构是 `data/sites/main/`，后续通过可选维护动作迁移。

风险控制：

- 第一阶段不要强制把旧数据移动到 `data/sites/main/`，避免升级失败后路径变化导致回滚困难。
- 如果未来要整理目录，单独做“迁移到标准目录”的可选维护动作，而不是随多站升级自动执行。
- 升级前备份：
  - 当前 `cms.db`
  - 当前上传目录
  - 即将创建的 `system.db`

对旧用户影响：

- 前台 URL 不变。
- 后台仍能从 `/admin` 进入。
- 原自动化 Key 继续保存在默认站点 `cms.db`，仍可访问 `/api/admin/v1/...`。
- 可能需要重新登录，因为后台会话从站点库迁移到平台库。

### 阶段 2：站点运行时池与请求上下文

目标：把当前 `Server` 从“单 store”改造成“平台 + 多站运行时”，但业务页面先尽量复用原 handler。

要做：

- 引入 `SiteRuntime`：
  - `site`
  - `store`
  - `upload_dir`
  - `primary_domain`
  - `aliases`
  - `content_cache`
  - `endpoint_cache`
  - `settings_cache`
- 引入 `SiteRuntimePool`：
  - `system_store`
  - `map[site_id]*SiteRuntime`
  - `map[host]*site_id`
  - `default_site_id`
- 增加统一入口函数，例如：

```text
withSiteRuntime(r) -> SiteRuntime
withSiteRuntimeByID(site_id) -> SiteRuntime
```

- 公开前台和公开 API 通过 Host 选择 runtime。
- 平台入口 `/api/platform/v1/sites/{site_id}/...` 通过 `site_id` 选择 runtime。
- 现有业务代码逐步从 `s.store` / `s.uploadDir` / `s.baseURL` 改成当前 runtime。

风险控制：

- 禁止业务 handler 自己随手拿全局 `s.store`。
- 缓存 key 必须带 `site_id`，或直接放进 `SiteRuntime` 内部。
- 清缓存只清当前站点；平台域名 / 默认站点变更才刷新 Host 映射和运行时池。
- 为串站风险补测试：
  - A 站文章不会出现在 B 站。
  - A 站上传文件 B 站不能访问。
  - A 站 sitemap / OpenAPI 不使用 B 站域名。

### 阶段 3：平台综合后台与当前站点过渡

目标：先把多站管理入口做出来，同时减少一次性重写整个后台的风险。

要做：

- `/admin` 改成平台综合后台：
  - 站点列表。
  - 新建站点。
  - 启用 / 关闭站点。
  - 设置默认站点。
  - 域名绑定、主域名、别名域名。
  - 平台入口开关 `management_automation_enabled`。
- 平台会话保存 `current_site_id`。
- 第一阶段过渡保留现有后台路径：
  - `/admin/posts`
  - `/admin/links`
  - `/admin/pages`
  - `/admin/settings`
- 进入某站点后台时设置 `current_site_id`，现有后台功能读取当前站点 runtime。
- 页面上明确显示“当前站点”，避免管理员误操作。

风险控制：

- 不在这一阶段强制把所有表单 action 改成 `/admin/sites/{site_id}/...`。
- 所有后台写操作必须从会话或路径解析出当前站点，不允许默认回落到第一个站点。
- 如果 `current_site_id` 丢失，应跳回站点选择页，而不是静默操作默认站点。

### 阶段 4：公开入口与平台入口自动化 API

目标：保留原公开自动化入口，同时新增平台 `site_id` 入口，并复用同一套站点 Key。

要做：

- 公开入口保持：

```text
/api/admin/v1/...
```

- 新增平台入口：

```text
/api/platform/v1/sites/{site_id}/...
```

- 两个入口复用现有自动化 handler 的业务逻辑：
  - 入口层先选 `SiteRuntime`。
  - 认证层用该 runtime 的 `store.GetAutomationKeyByToken()` 校验 Key。
  - 权限层沿用现有 scopes。
- 平台入口额外检查：
  - Host 必须是平台 Host，也就是 `BASE_URL` 的 Host。
  - `Site.management_automation_enabled=true`。
  - Key 必须存在于 URL 中 `site_id` 对应站点的 `cms.db`。
  - 如 Key 带 `via_platform` scope，则只允许平台入口。
- 生成两类 OpenAPI / AI 接入包：
  - 公开包：`https://blog.example.com/api/admin/v1`
  - 平台包：`https://cms.example.com/api/platform/v1/sites/{site_id}`

风险控制：

- 平台入口权限边界以“Key 所属站点”为准，不以 URL 文本为准。
- A 站 Key 请求 `/api/platform/v1/sites/B/...` 必须失败。
- 未绑定域名站点只显示平台入口包，不显示公开 API Base。
- 旧 AI 接入包继续可用，不改变 `/api/admin/v1`。

### 阶段 5：后台预览入口

目标：让未绑定域名的站点可以在登录态下预览完整前台效果。

要做：

- 增加：

```text
/admin/sites/{site_id}/preview/
/admin/sites/{site_id}/preview/{lang}/
/admin/sites/{site_id}/preview/{lang}/posts/{slug}
/admin/sites/{site_id}/preview/uploads/{file}
```

- 强制：
  - 必须登录。
  - `X-Robots-Tag: noindex, nofollow`。
  - meta robots `noindex, nofollow`。
- 预览渲染使用对应站点 runtime。
- 页面内链接、上传资源、静态资源要能在预览前缀下工作。

风险控制：

- 预览入口不能作为公开域名配置。
- 未绑定主域名时不输出公开 canonical。
- 预览上传文件只读当前站点上传目录。

### 阶段 6：后台路径完全前缀化

目标：在多站运行时稳定后，再把站点后台规范化为最终路径。

要做：

- 最终路径：

```text
/admin/sites/{site_id}/posts
/admin/sites/{site_id}/links
/admin/sites/{site_id}/pages
/admin/sites/{site_id}/settings
```

- 系统性替换：
  - 后台导航链接。
  - 表单 action。
  - 删除 / 发布 / 置顶 / 上传 / 预览等 POST 地址。
  - 分页、筛选、语言切换、设置页分区链接。
  - 重定向目标。
- 保留旧 `/admin/posts` 等路径作为短期兼容：
  - 如果有 `current_site_id`，302 到新路径。
  - 如果没有，跳站点选择页。

风险控制：

- 这一步必须在测试覆盖足够后做。
- 不建议和阶段 1-4 混在一个大版本里一起上。

### 阶段 7：可选目录整理与后续增强

目标：把旧默认站点物理目录整理到标准结构，但不作为多站升级的硬要求。

可选动作：

- 将默认站点从旧路径整理到：

```text
data/sites/main/cms.db
data/sites/main/uploads/
```

- 更新 `system.db` 中默认站点的 `db_path`、`upload_dir`。
- 提供后台维护动作或命令行工具执行。

风险控制：

- 必须先备份。
- 失败可回滚到旧路径。
- 不影响前台 URL。

### 建议发布节奏

```text
版本 A：system.db + 默认站点兼容 + 平台 session（已落地）
版本 B：站点运行时池 + Host 匹配 + 站点列表 / 域名管理（已落地）
版本 C：平台入口自动化 API + 管理型 AI 接入包（已落地）
版本 D：后台预览入口（已落地）
版本 E：后台路径前缀兼容（已落地；完整表单前缀化后续增强）
版本 F：可选目录整理、跨站增强、角色权限（后续增强）
```

这样拆的好处是每个版本都有可验证目标，旧用户升级风险也最低。

## 第一版实施范围建议

第一版建议包含：

- 创建 `system.db`。
- `system.db` 自身迁移。
- 平台级管理员和平台级会话。
- 自动把当前单站数据迁移成默认站点。
- 综合管理后台站点列表。
- 新建站点。
- 编辑站点名称、slug、备注。
- 启用 / 关闭站点。
- 设置默认站点。
- 配置域名或子域名。
- 配置主域名和别名域名。
- 站点运行时池。
- 每站点独立缓存。
- 进入某个站点后台。
- 登录态后台预览入口。
- 前台按 Host / 默认站点匹配数据源。
- 每站点独立数据库和上传目录。
- 每站点一套自动化 API Key（存站点 `cms.db`）+ 公开域名入口 + 公开 AI 接入包。
- 每站点可选启用的平台入口（按 `site_id`，复用同一套 Key）。
- 平台入口的 OpenAPI 描述文件和管理型 AI 接入包。
- 逐站迁移。
- 定时发布 fan-out 到所有启用站点。

第一版暂不建议包含：

- 路径型站点。
- 站点克隆。
- 站点删除后的复杂回收站。
- 跨站批量平台 API。
- 多角色权限系统。
- 维护页。
- 未登录公开预览入口。
- 任意自定义站点路径前缀。

## 后续增强方向

- 克隆站点。
- 导出 / 导入某个站点。
- 每站点备份与恢复。
- 总管理员、站点管理员、编辑员权限。
- 批量升级站点数据结构。
- 维护页。
- 跨站批量自动化接口。
- 域名 DNS / Caddy 配置检测。
- 站点级任务日志页面。
- 运行时池空闲回收。
- 多角色权限。

## 待确认问题

1. 未绑定域名的站点是否完全没有公开前台入口？
   - 当前建议：是；但提供登录态后台预览入口。
   - 自动化接口补充：未绑定域名没有公开 API Base，但可以按站点开启平台入口（按 `site_id`）。

2. 本地测试是否统一使用本地域名或 Host Header？
   - 当前建议：是；零域名预览用后台预览入口。

3. 关闭站点时前台返回 404，还是显示维护页？
   - 第一版建议 404，后续再做维护页。

4. 切换默认站点后，原默认站点如果没有其他域名，是否允许只从后台管理？
   - 建议允许。

5. 一个站点是否允许多个域名？
   - 建议允许，一个主域名，多个别名。

6. 别名域名是否默认跳转到主域名？
   - 建议默认跳转，可配置关闭。

7. 当前单站数据迁移时，是移动到 `data/sites/main/`，还是保留原路径作为默认站点？
   - 第一阶段保留旧路径，降低升级和回滚风险。
   - 推荐最终标准结构移动到 `data/sites/main/`，但应通过后续可选维护动作执行，并在迁移前自动备份。

8. 站点域名配置是否允许填写端口？
   - 建议允许，方便本地测试，例如 `blog.test:8080`。

9. 第一阶段站点后台是否采用 `current_site_id` 过渡？
   - 建议采用，降低一次性前缀化整个 admin 层的风险。

10. 站点运行时池第一版是全部打开，还是懒加载？
   - 建议第一版优先正确性；站点数量不大时可全部打开，后续再做空闲回收。

11. 迁移失败时是否阻止服务启动？
   - 当前建议：分级处理。`system.db` 迁移失败应阻止启动；单个站点 `cms.db` 迁移失败时，只让该站点降级 / 维护并返回 503，其它站点继续服务，同时在平台后台高亮报错。

12. 平台入口是否复用站点自己的 API Key？
   - 当前建议：**复用**。Key 仍存站点 `cms.db`；公开域名入口与平台 `site_id` 入口校验同一批 Key，不新增 `system.db` 密钥表。需要「仅平台入口可用」的 Key 时，用 Key 上的 scope（如 `via_platform`）区分。

13. 未绑定域名的站点能否被 AI 管理？
   - 当前建议：可以，但只能通过平台入口（按 `site_id`）；它没有公开 API Base。
