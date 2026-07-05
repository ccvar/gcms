# gcms Pilot —— GitHub 打包 + 在线自动升级

跟 gcms 服务端一样「打 tag → CI 打包 → 发布到公开 releases 仓」，外加桌面端自动升级（Tauri updater，ed25519 验签）。当前为**未签名版**（没走 Apple 代码签名/公证）。

## 机制

- **打包**：推 `pilot-v*` tag → `.github/workflows/pilot-release.yml` 在 macOS runner 上 `tauri build`（aarch64），产出 `.dmg`（首装）+ `.app.tar.gz`+`.sig`（升级包）+ `latest.json`（更新清单），发布到 `ccvar/gcms-pilot-releases`（make_latest）。
- **升级**：app 里「连接与模型 → 关于 → 检查更新」调用 Tauri updater，拉 `.../releases/latest/download/latest.json`，比版本 → ed25519 验签 → 下载安装 → 重启。公钥已内置在 `src-tauri/tauri.conf.json` 的 `plugins.updater.pubkey`。

## 一次性配置（只做一次）

1. **建公开仓** `ccvar/gcms-pilot-releases`（空仓即可，跟 `gcms-releases` 分开，避免 updater 混淆）。
2. **在源码仓 `ccvar/gcms` 加两个 Secret**（Settings → Secrets and variables → Actions）：
   - `TAURI_SIGNING_PRIVATE_KEY` = 更新签名**私钥**内容（我已在本机生成，见下方「私钥」；私钥无密码，所以 workflow 里 `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` 留空）。
   - `PILOT_RELEASES_TOKEN` = 一个 PAT，对 `ccvar/gcms-pilot-releases` 有 **Contents: Read and write**（fine-grained 选该仓 Contents 读写，或经典 token 勾 `repo`）。
3. 公钥不用管，已经写进 `tauri.conf.json` 了。

> ⚠️ 私钥是唯一的签名凭据，丢了就没法再签更新包（老用户升不了级）。请只放进 GitHub Secret，本机的 `scratchpad/pilot-updater.key` 存一份备份后即可删。

## 每次发版

```bash
# 1. 改版本号（两处要一致）
#    desktop/src-tauri/tauri.conf.json  -> "version": "0.1.1"
#    desktop/package.json               -> "version": "0.1.1"
# 2. 提交后打 tag（注意是 pilot-v 前缀，跟服务端 v* 分开）
git tag pilot-v0.1.1
git push origin pilot-v0.1.1
```

CI 跑完后，`gcms-pilot-releases` 会出一个 `pilot-v0.1.1` release，带 `.dmg`/`.app.tar.gz`/`latest.json`。老用户点「检查更新」即可升级。

## 用户侧

- **首次安装**（未签名）：下载 `.dmg` → 拖入 Applications → 首次打开右键「打开」一次绕过 Gatekeeper（或 `xattr -dr com.apple.quarantine "/Applications/gcms Pilot.app"`）。
- **之后升级**：app 内「检查更新」自动完成，无需再绕 Gatekeeper。

## 以后要「双击即开 + 静默」

补 Apple 代码签名 + 公证：Apple Developer 账号（$99/年）→ Developer ID 证书 → 把 `APPLE_CERTIFICATE`/`APPLE_CERTIFICATE_PASSWORD`/`APPLE_SIGNING_IDENTITY`/`APPLE_ID`/`APPLE_PASSWORD`/`APPLE_TEAM_ID` 加成 Secret，`tauri build` 会自动签名公证。这与上面的 ed25519 更新签名是两套，互不替代。

## ToS

Pilot 只驱动本机订阅版 `claude`/`codex` CLI，不碰任何人的 OAuth token；分发给别人时 app 不带任何登录凭据，每个用户装完用自己的 CLI 登录。
