# GCMS Pilot

本地 AI 内容驾驶舱 —— macOS 桌面客户端（Tauri 2 + Svelte 5）。导入 gcms 平台技能包，
用**你自己本地的** Claude Code CLI / OpenAI Codex CLI（订阅账户，无额外 API 计费）为
平台下的多个站点批量生产内容。

## 功能

- **导入技能包**：支持嵌密钥包与原始包（原始包导入时提示粘贴 `gcmsp_` 密钥）。
  密钥只进 **macOS 钥匙串**，绝不落盘、绝不进 WebView。
- **站点发现**：读取平台发现契约，展示所有可管理站点（含 Logo / 域名）。
- **本地大脑**：自动检测 claude / codex CLI 的安装与登录状态；未登录一键「去授权」
  （打开终端跑官方登录命令，自动带上系统代理）。
- **流水线**：写作 → 可选「AI 主编终审」（替代人工审核）→ 产出策略（草稿 / 直接发布 /
  定时发布，定时由 gcms 服务端到点自动发）。
- **三种任务**：单篇、批量（自动拆成互不重复的多篇）、新站建设（站点资料 + 导航 + 种子内容）。
- **任务台**：运行历史 + 待人工处理队列（终审未通过 / 无终审的草稿），可跳到 gcms 后台处理。
- 关窗隐藏到托盘，后台任务继续跑；完成时系统通知。

## 开发

```bash
npm install
npm run tauri dev
```

依赖：Rust ≥ 1.88、Node ≥ 20、本机已装并登录 `claude`（Claude Code）；`codex` 可选。

## 自用打包（v0，未签名）

```bash
npm run tauri build
```

产物在 `src-tauri/target/release/bundle/`（`.app` 与 `.dmg`）。发布版做了 **ad-hoc 签名**但未公证，
从网上下载后被隔离会误报「已损坏」——最稳的放行是终端一行 `xattr -cr "/Applications/GCMS Pilot.app"`
（详见 [RELEASE.md](RELEASE.md) 用户侧一节）。

## 尚未接入（对外分发前的前置）

以下按冻结方案属于对外分发阶段，v0 自用不含，需要外部凭据/仓库，未在本仓接线：

- **自动更新**：`pilot-v*` tag 触发独立 `pilot-release.yml` → 独立 `ccvar/gcms-pilot-releases`
  仓库（**不可**与 `gcms-releases` 混用，否则两个 updater 的 `releases/latest` 互相顶掉）。
  需要 `PILOT_RELEASES_TOKEN` PAT + tauri updater 的独立 ed25519 签名密钥
  （`tauri signer generate`，与 gcms 的 RSA 发布密钥无关）。
- **代码签名 / 公证**：Apple Developer 证书 + notarytool。
- **首个一等公民 API-Key 模式**：对外分发前必须补上（ToS：消费订阅 OAuth 令牌仅限官方
  Claude Code / claude.ai 使用；Pilot 通过驱动官方 CLI 合规，不提取/存储 OAuth 令牌，
  但对外分发前应提供 API-Key 直连模式与说明）。

## 安全 / 合规要点

- 密钥仅存 macOS 钥匙串（`keyring-core` + `apple-native-keyring-store`）。
- 取消任务时对整个进程组 SIGKILL，避免 node/bash 孙进程带着密钥继续写 CMS。
- 直接发布无终审时有显式风险提示；默认策略为「草稿」。
