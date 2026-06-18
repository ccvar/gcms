# Cloudflare 部署接入

gcms 的「Cloudflare部署」不依赖公共中间服务。每个站点都使用自己的 Cloudflare API Token 或 OAuth Client，密钥只保存在当前站点数据库中，不会写入发布包，也不会发送到 ccvar.com。

## 推荐流程

1. 打开后台「设置 -> Cloudflare部署」。
2. 点击「去 Cloudflare 创建专用 Token」。
3. Cloudflare 会打开官方 Token 创建页，确认权限后生成 Token。
4. 复制 Token，回到 gcms 粘贴，点击「保存 Token 并部署」。
5. gcms 会自动识别 Cloudflare Account、Zone 和路由，然后上传 Worker、绑定路由并清理缓存。

推荐 Token 的权限范围：

- Account -> Workers Scripts -> Edit
- Zone -> Workers Routes -> Edit
- Zone -> Cache Purge -> Purge
- Zone -> Zone -> Read
- Account -> Account Settings -> Read

如果自动识别失败，可以展开「高级设置：路由、缓存与手动字段」，补充 Account ID 或 Zone ID。

## 本机保存的内容

Token 方式会在当前 gcms 数据库中保存：

- API Token
- Worker 名称
- 源站地址
- Worker 路由
- 可显示的 Account/Zone 名称与 ID

如果你选择自托管 OAuth，高级流程还会保存：

- OAuth Client ID
- OAuth Client Secret
- OAuth access token
- OAuth refresh token
- token 过期时间

这些内容不会写入公开发布包，也不会发送到 ccvar.com 或其他公共连接服务。

## 高级 OAuth 方式

Token 模板是默认推荐方式。如果你希望做完全 OAuth 授权，也可以自建 Cloudflare OAuth Client：

1. 复制后台页面中的 OAuth 回调地址，通常类似 `https://example.com/admin/settings/cloudflare/callback`。
2. 在 Cloudflare 创建 OAuth Client，并把回调地址填到 Redirect URL。
3. 给 OAuth Client 配置与上方 Token 相同的最小权限。
4. 保存 OAuth Client ID 和 Client Secret 后，走授权回调完成连接。

## 当前 gcms 端点

- `POST /admin/settings/cloudflare`：保存 Token、路由、缓存等配置；推荐卡片会保存后直接启动部署。
- `POST /admin/settings/cloudflare/connect`：保存 OAuth Client 信息，生成 `state`，跳转 Cloudflare 官方 OAuth 授权页。
- `GET /admin/settings/cloudflare/callback`：接收 Cloudflare 返回的 `code`，交换 access/refresh token。
- `POST /admin/settings/cloudflare/deploy`：上传 Worker；OAuth token 过期时会自动刷新，Token 方式会直接使用已保存 Token。
- `POST /admin/settings/cloudflare/purge`：清理 Cloudflare 缓存；部署前会尝试自动识别 Zone ID。

## 安全约束

- API Token、OAuth Client Secret 和 refresh token 都是敏感凭据，请保护好数据库和服务器权限。
- Token 页面不会回显完整值，Cloudflare 生成完成页也通常只显示一次，请创建后立即复制。
- 生产环境建议使用 HTTPS 后台地址。
- 怀疑泄露时，在 Cloudflare 后台撤销 Token 或 OAuth Client，然后在 gcms 中重新连接。
- OAuth `state` 一次性使用，默认 15 分钟过期。
