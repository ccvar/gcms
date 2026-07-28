# GCMS Preview 更新通道

GCMS 过渡版提供一个最小内部灰度通道。普通安装始终使用 `stable`；只有后台管理员在已登录状态访问带激活码的内部地址后，整个 GCMS 实例才切换到 `preview`。

## 发布边界

- Stable：`ccvar/gcms-releases`，保持现有公开更新行为。
- Preview：`ccvar/gcms-preview-releases`，单独发布 `manifest.json`、签名、校验和与平台安装包。
- Pilot 暂不切换 Preview。Preview GCMS 及其技能包必须继续兼容当前 Stable Pilot。

Preview Manifest 不可达时会故障关闭，不回退到 Stable Manifest。后台检查更新和真正执行升级时使用同一个、已经选定的 Manifest URL。

## 构建过渡版

生成至少 32 个随机字节的激活码，只把 SHA-256 十六进制哈希交给构建：

```sh
PREVIEW_ACTIVATION_CODE_HASH=<64位SHA-256十六进制哈希> \
PREVIEW_RELEASE_REPO=ccvar/gcms-preview-releases \
VERSION=1.3.58 ./scripts/package.sh linux amd64
```

原始激活码不得写入仓库、Manifest、安装包配置或日志。未注入合法哈希时，激活地址固定返回 404。

当前约定版本为：

- Stable 过渡版：`v1.3.58`
- 首个 Preview：`v1.3.59-preview.1`

`v1.3.58` 的 `scripts/cms.sh` 必须与 `v1.3.57` 保持相同总行数和总字节数，
并让 `install_root_files` 调用前的字节偏移一致。原因是旧升级器会在自身仍在执行时
覆盖根目录脚本；改变偏移会导致 macOS `/bin/sh` 在升级成功后继续读取到错位内容。
发布前必须用运行中的真实 `v1.3.57` 安装目录执行一次停机、切换、重启和健康检查，
不能只测试新脚本自身。

`.github/workflows/release.yml` 会按标签自动选发布仓库：普通 `v*` 标签发布到
`ccvar/gcms-releases`，`v*-preview.N` 标签发布到
`ccvar/gcms-preview-releases`。无论构建哪个通道，二进制都会同时内置 Stable 与
Preview 仓库地址，所以从 Preview 切回 Stable 后不会继续读取 Preview 清单。

源码仓库需要配置：

- `GCMS_RELEASE_TOKEN`：Stable 发布仓库写权限；
- `GCMS_PREVIEW_RELEASE_TOKEN`：Preview 发布仓库写权限；
- `GCMS_RELEASE_SIGNING_KEY`：两个通道共用的 Manifest RSA 签名私钥；
- `GCMS_RELEASE_PUBLIC_KEY`：与签名私钥匹配、内置进安装包的公钥；
- `GCMS_PREVIEW_ACTIVATION_CODE_HASH`：原始激活码的 SHA-256 十六进制哈希。

本地打包可通过 `GCMS_RELEASE_PUBLIC_KEY_FILE=/绝对路径/public.pem` 指定公钥，
不需要把公钥临时复制进源码目录。

## 激活

用户先登录自己的 GCMS 后台，再访问：

```text
https://用户域名/admin/pre-active?code=<原始激活码>
```

成功后：

1. 平台数据库写入 `system.update_channel=preview`；
2. 记录 `system.preview_activated_at`；
3. 303 跳转到 `/admin/updates?preview=active`；
4. 更新页显示 `Preview` 标记并从 Preview Manifest 检查版本。

未登录请求只会跳转登录页，不会激活。错误 code 返回 404。重复访问是幂等的。

## 停止接收 Preview

内部恢复地址使用相同激活码：

```text
https://用户域名/admin/pre-stable?code=<原始激活码>
```

该操作只把后续更新通道改回 Stable，不会降级当前程序或数据库。应等待 Stable 版本追上当前 Preview 后再完成正式回归。

## 安全说明

- 两个内部地址都使用现有后台会话与默认密码修改拦截。
- 激活尝试按来源 IP 限流。
- code 使用恒定时间哈希比较。
- 响应禁止缓存、禁止 Referer、禁止搜索引擎索引。
- 应避免反向代理访问日志记录完整查询字符串；共享 code 被转发后，其他已安装过渡版且拥有后台权限的用户也能激活。
- 这是内部过渡方案，不等同于正式的用户级授权系统；将来可把本地哈希校验替换为 Worker 邀请兑换，通道和升级链路无需重做。

## 验收清单

- 旧版访问激活地址为 404。
- 过渡版未激活时始终检查 Stable。
- 未登录、错误 code 和未配置哈希均不能切换通道。
- 正确 code 只修改平台级通道。
- Preview 检查与升级脚本使用同一 Manifest。
- `preview.1 < preview.2 < preview.10 < 正式版`。
- Manifest 签名、SHA-256、备份、原子切换和失败回滚保持有效。
- Preview GCMS 可以继续连接 Stable Pilot。
