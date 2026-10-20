# GCMS Pilot —— GitHub 打包 + 在线自动升级

跟 gcms 服务端一样「打 tag → CI 打包 → 发布到公开 releases 仓」，外加桌面端自动升级（Tauri updater，ed25519 验签）。当前做了 **ad-hoc 签名**（`bundle.macOS.signingIdentity = "-"`，免 Apple 证书）但**未公证**——首次从网上下载安装仍需手动放行一次（见下方「用户侧」）。

## 机制

- **打包**：推 `pilot-v*` tag → `.github/workflows/pilot-release.yml` 分别构建 macOS arm64 和 Windows x64；macOS 产出 `.dmg` 与签名更新包，Windows 产出签名 NSIS 安装器。两个平台完成后合并 `latest.json`，发布到 `ccvar/gcms-pilot-releases`。
- **升级**：app 里「连接与模型 → 关于 → 检查更新」调用 Tauri updater，拉 `.../releases/latest/download/latest.json`，比版本 → ed25519 验签 → 下载安装 → 重启。公钥已内置在 `src-tauri/tauri.conf.json` 的 `plugins.updater.pubkey`。

## 0.2.63（2026-10-20）

- 托管卡片的模式标签支持下拉切换「计划托管 ↔ 增长托管」，单站编辑与批量设置同步支持；切换前二次确认，取消或保存失败不提前改变界面状态。
- 切换保留现有任务 ID、排期、启停、模型、预算、运营计划、草稿与历史记录，并分别保存、恢复各模式的自定义提示词；兼容旧版两条配套任务。
- 任务运行期间禁止切换，调度器与手动运行读取切换后的最新任务。切换本身不立即执行任务；保存托管记录失败时回滚配套任务文件。

## 0.2.62（2026-10-20）

- 修复 Claude API 旧会话续聊及截断恢复时的 `Invalid signature in thinking block`：恢复提示追加为新消息，不再修改思考签名绑定的系统说明。
- 保存有效系统说明与工具定义的指纹。旧会话升级或定义变化时仅一次性移除失效的思考块，保留正文、工具调用与结果；未变化的会话完整保留思考签名，不重复执行已完成操作。
- 再次触及输出上限时清理半截文本并结束恢复状态。新增严格签名绑定回归测试及独立、显式启用的官方 API 实测；发版前已验证真实 Opus 5.5 续聊、同款 400 负对照及迁移后的恢复。

## 0.2.61（2026-10-20）

- API 模型支持按协议与模型选择思考等级；新对话、现有会话、定时任务及托管共用设置，旧的隐藏档位保持服务商默认，不自动启用。
- Claude API 确认达到输出上限后，在原预算内恢复一次；撤回首次截断正文，不执行不完整工具调用，也不重复已完成的工具操作。网络异常等不确定结果不会自动重试。
- 完整工具结果及时保存；符合完整性检查的旧会话可安全继续。新建 Claude API 服务默认输出上限调整为 32768，已有连接与手动设置不变。

## 一次性配置（只做一次）

1. **建公开仓** `ccvar/gcms-pilot-releases`（空仓即可，跟 `gcms-releases` 分开，避免 updater 混淆）。
2. **在源码仓 `ccvar/gcms` 加两个 Secret**（Settings → Secrets and variables → Actions）：
   - `TAURI_SIGNING_PRIVATE_KEY` = 更新签名**私钥**内容（我已在本机生成，见下方「私钥」；私钥无密码，所以 workflow 里 `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` 留空）。
   - `PILOT_RELEASES_TOKEN` = 一个 PAT，对 `ccvar/gcms-pilot-releases` 有 **Contents: Read and write**（fine-grained 选该仓 Contents 读写，或经典 token 勾 `repo`）。
3. 公钥不用管，已经写进 `tauri.conf.json` 了。

> ⚠️ 私钥是唯一的签名凭据，丢了就没法再签更新包（老用户升不了级）。请只放进 GitHub Secret，本机的 `scratchpad/pilot-updater.key` 存一份备份后即可删。

## 每次发版

```bash
# 1. 改版本号（三处要一致）
#    desktop/src-tauri/tauri.conf.json  -> "version": "0.1.1"
#    desktop/package.json               -> "version": "0.1.1"
#    desktop/package-lock.json          -> 根版本及 packages[""].version
# 2. 提交后打 tag（注意是 pilot-v 前缀，跟服务端 v* 分开）
git tag pilot-v0.1.1
git push origin pilot-v0.1.1
```

CI 跑完后，`gcms-pilot-releases` 会出一个 `pilot-v0.1.1` release，带 `.dmg`/`.app.tar.gz`/`latest.json`。老用户点「检查更新」即可升级。

## 用户侧（首次安装：绕过「已损坏」）

未公证的 app 从网上下载后会被打上 `com.apple.quarantine` 隔离属性，Apple Silicon 上 Gatekeeper 会误报 **「"GCMS Pilot" 已损坏，无法打开」**（并非真的损坏）。已做 ad-hoc 签名缓解，但下载分发仍需**下载者首次手动放行一次**，三选一（从稳到弱）：

- **最稳（终端一行，必成）**：把 app 拖进「应用程序」后执行，然后正常双击打开：
  ```bash
  xattr -cr "/Applications/GCMS Pilot.app"
  ```
- **系统设置放行**（macOS 15+）：双击被拦 → 系统设置 → 隐私与安全性 → 底部「仍要打开」。
- **右键打开**（macOS 14 及更早）：Finder 里右键 app →「打开」→ 再点「打开」。

> 分发给别人时，把上面那条 `xattr -cr …` 命令一起发给对方最省事。

- **之后升级**：app 内「检查更新」自动完成（updater 装的包不带隔离属性），**无需再放行**。
- **彻底免放行**：只能靠 Apple 代码签名 + 公证（见下节），ad-hoc 签名做不到。

## 以后要「双击即开 + 静默」

补 Apple 代码签名 + 公证：Apple Developer 账号（$99/年）→ Developer ID 证书 → 把 `APPLE_CERTIFICATE`/`APPLE_CERTIFICATE_PASSWORD`/`APPLE_SIGNING_IDENTITY`/`APPLE_ID`/`APPLE_PASSWORD`/`APPLE_TEAM_ID` 加成 Secret，`tauri build` 会自动签名公证。这与上面的 ed25519 更新签名是两套，互不替代。

## ToS

Pilot 支持本机 `claude` / `codex` / `grok` 客户端以及用户自备 API Key 的模型服务，不提取客户端的 OAuth token。API Key 保存在系统凭据库，API 调用由服务商独立计费。分发包不包含任何登录凭据，每位用户自行登录客户端或配置 API 服务。
