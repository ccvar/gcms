# Cloudflare 部署接入

gcms 的「Cloudflare部署」不依赖公共中间服务。每个站点都使用自己的 Cloudflare API Token，密钥只保存在当前站点数据库中，不会写入发布包，也不会发送到 ccvar.com。

## 推荐流程

1. 打开后台「设置 -> Cloudflare部署」。
2. 点击「去 Cloudflare 获取授权 Token」。
3. Cloudflare 会打开官方 Token 创建页，确认权限后生成 Token。
4. 复制 Token，回到 gcms 粘贴，点击「保存配置」；系统会先验证 Token 是否有效。
5. 保存成功后可以立即部署，也可以稍后在「部署状态」里点击「立即部署」。gcms 会导出完整前台静态站，上传到 Worker Assets 或 Cloudflare Pages；如果前台域名所在 Zone 属于这个 Cloudflare 账号，会继续自动绑定域名、DNS 并清理缓存。

推荐 Token 的权限范围：

- Account -> Workers Scripts -> Edit
- Zone -> Workers Routes -> Edit
- Account -> Cloudflare Pages -> Edit
- Zone -> DNS -> Edit
- Zone -> Cache Purge -> Purge
- Zone -> Zone -> Read
- Account -> Account Settings -> Read

如果自动识别失败，可以展开「高级设置：路由、缓存与手动字段」，补充 Account ID 或 Zone ID；如果域名不在当前 Cloudflare 账号，则需要在对应 DNS 服务商手动配置解析。

## 域名与托管方式

- 「主域名」是访客最终使用的前台入口，例如 `ccvar.com`。
- 「别名域名」可以填写多个，例如 `www.ccvar.com`；勾选跳转后会 301 到主域名。
- Worker Assets 是默认方式：由 Worker 承载完整静态站，适合统一入口控制。
- Cloudflare Pages 是可选方式：Cloudflare 后台能看到项目、部署记录和自定义域名。
- 部署后的前台是纯静态站；后台、数据库、自动化接口和草稿预览仍留在当前 gcms 服务。

## 本机保存的内容

Token 方式会在当前 gcms 数据库中保存：

- API Token
- Worker 名称
- Pages 项目名称
- 前台访问域名
- 可显示的 Account/Zone 名称与 ID

这些内容不会写入公开发布包，也不会发送到 ccvar.com 或其他公共连接服务。

## 当前 gcms 端点

- `POST /admin/settings/cloudflare`：验证并保存 Token、域名、项目名称和托管方式等配置；推荐卡片会保存后询问是否立即部署。
- `POST /admin/settings/cloudflare/deploy`：导出前台静态站，并发布到 Worker Assets 或 Cloudflare Pages。
- `POST /admin/settings/cloudflare/unpublish`：取消 Cloudflare 公开入口绑定；项目和已上传静态资源保留，DNS 不删除。
- `POST /admin/settings/cloudflare/purge`：清理 Cloudflare 缓存；需要已识别或手动填写 Zone ID。
- `POST /admin/settings/cloudflare/reset`：清空本地保存的 Cloudflare Token、域名和识别结果；不会删除 Cloudflare 上的项目或 DNS。

## 安全约束

- API Token 是敏感凭据，请保护好数据库和服务器权限。
- Token 页面不会回显完整值，Cloudflare 生成完成页也通常只显示一次，请创建后立即复制。
- 生产环境建议使用 HTTPS 后台地址。
- 怀疑泄露时，在 Cloudflare 后台撤销 Token，然后在 gcms 中重新创建并粘贴新的 Token。
