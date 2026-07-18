//! 对话轮次执行器：把一轮用户消息交给本地 claude / codex 跑，
//! 边跑边把助手文本增量、工具调用经 Channel 推给前端，收尾返回本轮结果。
//! 多轮机制（已在真机验证）：
//!   claude —— 首轮 `--session-id <uuid>`，续轮 `--resume <uuid>`；
//!   codex  —— 首轮 `exec` 从 thread.started 取 thread_id，续轮 `exec resume <id>`。

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::PathBuf;
use std::process::Stdio;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use tauri::ipc::Channel;
use tokio::io::{AsyncBufReadExt, AsyncRead, BufReader};
use tokio::process::Command;

use crate::convo::{TaskProposal, ToolCall};
use crate::keychain;
use crate::pack::Connection;
use crate::permit::{self, PermMode};

/// 本轮的权限设置：档位 + 钩子资产落盘位置（claude 用）。
struct PermSpec {
    mode: PermMode,
    conv_id: String,
    gen_dir: PathBuf,
    pending_dir: PathBuf,
    /// AI 桥脚本路径：钩子要靠它认出「桥命令」并放行（桥自己会弹卡），见 permit::is_bridge_cmd。
    ssh_js: PathBuf,
}

#[derive(Clone, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum TurnEvent {
    Delta { text: String },
    Tool { label: String, detail: String },
    Done { ok: bool, error: String },
}

pub struct TurnResult {
    pub ok: bool,
    pub text: String,
    pub tools: Vec<ToolCall>,
    pub error: String,
    pub session_ref: String,
    /// AI 在本轮提议的定时任务（PILOT_TASK 行解析而来），供前端弹确认卡。
    pub proposal: Option<TaskProposal>,
    /// 本轮 token 用量（从 CLI 流里抽出）；用于「会话大小/累计用量」。拿不到则 None。
    pub usage: Option<TurnUsage>,
    /// 本轮因订阅额度/限流失败：Some(恢复时间戳秒)，拿不到恢复时间为 Some(0)；非限额错误 None。
    pub limit_reset: Option<i64>,
}

/// 识别「订阅额度耗尽/限流」类报错。claude 触顶输出形如
/// "Claude AI usage limit reached|1736600400"（重置时间戳可选）；codex/OpenAI 是
/// 429 / usage limit / rate limit 一族措辞；grok 欠费/触顶是 402 Payment Required +
/// "personal-team-blocked:spending-limit: You have run out …"（实测 0.2.101）。
/// 只对失败回合的错误材料调用（正文里聊到 "rate limit" 不会误判——ok 回合不进这里）。
pub(crate) fn detect_usage_limit(material: &str) -> Option<i64> {
    let low = material.to_ascii_lowercase();
    let hit = ["usage limit", "rate limit", "limit reached", "too many requests", "insufficient_quota", "quota exceeded", "resets at", "spending-limit", "payment required"]
        .iter()
        .any(|p| low.contains(p))
        || low.contains("(429")
        || low.contains(" 429")
        || low.contains("status 429")
        || low.contains("429 ");
    if !hit {
        return None;
    }
    // claude 风格：…limit reached|<epoch秒>
    if let Some(i) = low.find("limit reached|") {
        let rest = &material[i + "limit reached|".len()..];
        let digits: String = rest.chars().take_while(|c| c.is_ascii_digit()).collect();
        if let Ok(ts) = digits.parse::<i64>() {
            // 秒级时间戳合理区间（2020–2100），毫秒级的除以 1000
            if (1_577_836_800..4_102_444_800).contains(&ts) {
                return Some(ts);
            }
            if (1_577_836_800_000..4_102_444_800_000).contains(&ts) {
                return Some(ts / 1000);
            }
        }
    }
    Some(0)
}

/// 一轮的 token 用量。input+cache_read+cache_create ≈ 模型这轮读入的整段上下文（＝当前会话大小）。
#[derive(Clone, Default)]
pub struct TurnUsage {
    pub input: u64,
    pub output: u64,
    pub cache_read: u64,
    pub cache_create: u64,
}

// ---- 取消注册表（按 turn id）----

#[derive(Clone, Default)]
pub struct RunRegistry {
    inner: Arc<Mutex<HashMap<String, (Arc<AtomicBool>, Option<tokio::sync::oneshot::Sender<()>>)>>>,
}

impl RunRegistry {
    pub(crate) fn register(&self, id: &str) -> (Arc<AtomicBool>, tokio::sync::oneshot::Receiver<()>) {
        let canceled = Arc::new(AtomicBool::new(false));
        let (tx, rx) = tokio::sync::oneshot::channel();
        self.inner
            .lock()
            .unwrap()
            .insert(id.to_string(), (canceled.clone(), Some(tx)));
        (canceled, rx)
    }
    pub(crate) fn unregister(&self, id: &str) {
        self.inner.lock().unwrap().remove(id);
    }
    pub fn cancel(&self, id: &str) -> bool {
        if let Some((flag, tx)) = self.inner.lock().unwrap().get_mut(id) {
            flag.store(true, Ordering::SeqCst);
            if let Some(tx) = tx.take() {
                let _ = tx.send(());
            }
            true
        } else {
            false
        }
    }
    pub(crate) fn is_canceled(&self, id: &str) -> bool {
        self.inner
            .lock()
            .unwrap()
            .get(id)
            .map(|(f, _)| f.load(Ordering::SeqCst))
            .unwrap_or(false)
    }
}

// ---- 系统提示词（角色框架）----

pub fn system_prompt(task_type: &str, site_slug: &str, site_name: &str) -> String {
    let base = format!(
        "你是站点「{name}」(slug: {slug}) 的 AI 内容助手，通过运行 `node scripts/gcms.js` 操作 gcms 平台。\
先阅读当前目录的 SKILL.md、AI助手说明.md（如存在）了解可用命令。\n\
硬性规则：目标站点固定用 `--site {slug}`；slug 只用 ASCII 小写字母/数字/连字符；\
时间字段一律带时区偏移的 RFC3339；图片先转 WebP 再上传；未经用户明确同意不要发布内容（默认建草稿）。\n\
【扩展内容类型】站点能做的不只文章/链接/页面——先 `node scripts/gcms.js types --site {slug}` 看本站启用的\
扩展类型（产品/文档/活动/图库/自定义），返回的字段 schema 就是操作契约：list/create/update 对\
扩展集合同样可用，自定义字段放 `fields:{{...}}`。需要新的内容形态（如案例库/菜谱/招聘岗位）时，\
先把内容模型（类型名+字段清单）讲给用户、征得同意后再 type-create；类型是站点级结构，别随手建。\n\
交互方式：以对话推进——先理解用户意图，必要时提问澄清，给出简短方案并征得同意后再动手；\
动手时边做边用一两句话说明你在做什么；每完成一步给出结果（如新建内容的 ID、预览或后台链接，可用 `preview-url`）。\
回答用中文，简洁自然，不要长篇大论。运行命令（Bash）时 description 字段一律用中文写清这条命令做什么、\
会影响什么——它会原样展示给非技术用户做批准决定。\n\
【定时发布 vs 定时任务，区分清楚】\n\
- 如果用户只是要把某篇内容**定时发布**：直接用 gcms.js 建 status=scheduled 的内容并设 published_at 即可，这属于内容操作。\n\
- 如果用户希望你**周期性地自动**做某事（比如每天自动写一篇、每周巡检）：**你无法、也绝不要自己搭建任何定时或常驻机制**——不要写或安装 cron / launchd / 系统定时器，不要准备「长期运行的脚本或环境」，不要建后台守护进程或自触发循环，也不要在当前目录留调度脚本。这类循环任务**只能**由 GCMS Pilot 客户端调度。你唯一要做的是：用一句话告诉用户你准备了一个定时任务建议、请在下方卡片确认，然后在回复的**最后单独打印一行**（只打印一次）：\n\
PILOT_TASK: {{\"title\":\"简短任务名\",\"prompt\":\"每次到点要执行的完整指令，写清站点、语言、产出要求\",\"every_minutes\":周期分钟数,\"first_run\":\"可选，首次运行时间，带时区的RFC3339\"}}\n\
打印这行后就停下、等用户在卡片里确认；确认后由 Pilot 到点自动开新对话执行你写的 prompt。其中 every_minutes：1440=每天、10080=每周、60=每小时、360=每6小时。",
        name = if site_name.is_empty() { site_slug } else { site_name },
        slug = site_slug
    );
    let role = match task_type {
        "sitebuild" => "\n\n本次目标：新站建设。帮用户从零把这个站点搭起来——先了解定位、目标读者、栏目方向，\
**并聊清内容模型**：这个站需要哪些内容形态（文章之外是否要产品库/文档/活动/图库/自定义类型）；\
给出建站方案；达成一致后依次完善站点资料（site-profile-update）、配置前台导航（navigation-update）、\
启用或创建所需内容类型（types / type-enable / type-create，模型需用户确认）、创建若干种子内容。\
每一步做完都向用户汇报，让他确认后再进行下一步。",
        "article" => "\n\n本次目标：内容创作。帮用户策划并创作文章——先明确主题、角度、语言与要求，\
需要时给出提纲让用户确认，再撰写并在站点创建（默认草稿）。用户可以在对话里让你修改、扩写或换角度。\n\
【文章质量强制规则】每篇文章必须有真实、贴切且与正文对应的配图，并填写准确 alt；\
涉及后台、网页、软件或产品操作时必须使用真实系统截图，发布前遮盖 Token、账号、邮箱、Cookie 和内部 URL 等敏感信息，禁止伪造截图。\
每篇文章围绕一个明确且真实的搜索意图和目标读者展开，内容必须原创、可验证、可执行；禁止关键词堆砌、模板拼接、空话、虚构案例、虚构数据和伪造引用。\
时效性事实优先引用官方或一手来源；不能验证的事实要标注不确定，不得编造。",
        _ => "\n\n以对话方式协助用户完成关于这个站点的各类内容运营工作。",
    };
    format!("{base}{role}")
}

/// 多站会话的系统提示词：AI 可操作清单内任意站点（平台密钥 gcms.js 本就支持 --site 任意站）。
pub fn multi_site_system_prompt(slugs: &[String], names: &[String]) -> String {
    let mut list = String::new();
    for (i, s) in slugs.iter().enumerate() {
        let n = names.get(i).map(String::as_str).unwrap_or(s);
        list.push_str(&format!("- {s}（{n}）\n"));
    }
    format!(
        "你是跨站点的 AI 内容助手，通过运行 `node scripts/gcms.js` 同时管理下列 {count} 个站点。\
先阅读当前目录的 SKILL.md、AI助手说明.md（如存在）了解可用命令。\n\
【站点清单】\n{list}\
硬性规则：**每条命令都必须显式带 `--site <slug>`**，slug 只能取自上面清单；\
时间字段一律带时区偏移的 RFC3339；图片先转 WebP 再上传；未经用户明确同意不要发布内容（默认建草稿）。\n\
【统计/巡检/对比类需求】按清单逐站查询后汇总作答，给出分站明细 + 合计；站点较多时边查边简要汇报进度。\n\
【扩展内容类型】各站独立：先 `types --site <slug>` 看该站启用的类型，字段 schema 即操作契约。\n\
【批量重活分发给 Pilot】如果用户要求对**每个站独立完成一件重活**（如各写一篇文章）：不要在本会话里逐站硬干\
（串行慢、上下文膨胀、质量随长度下降）。改为提议一个多站定时任务：用一句话说明后，在回复最后单独打印一行\n\
PILOT_TASK: {{\"title\":\"简短任务名\",\"prompt\":\"每个站点各自执行的完整指令（写清语言、产出要求；站点由任务配置指定，不要写死 slug）\",\"every_minutes\":周期分钟数,\"first_run\":\"可选，RFC3339\"}}\n\
Pilot 会预填本会话的全部站点，用户确认后并发分站执行；一次性的活让用户创建后点「立即运行」即可。\n\
交互方式：先理解意图、必要时澄清，动手时边做边简述；回答用中文，简洁自然。\
运行命令（Bash）时 description 一律用中文写清这条命令做什么、影响哪个站点。",
        count = slugs.len(),
    )
}

/// Cloudflare 建站助手的系统提示词。cwd＝项目目录；token 已注入 env。
///
/// `seeded`＝项目目录里已经有文件（引用了起始模板，或上一轮已经建过）。**必须告诉模型**：
/// 以前 use_template 把模板拷进去之后没人吭声，模型视角永远是「空目录、从白纸开始」，
/// 于是那份精心设计的起点直接被推倒重写 —— 内置模板等于白做。
pub fn cf_system_prompt(project: &str, account_id: &str, seeded: bool) -> String {
    let acct = if account_id.is_empty() { "(未指定)".to_string() } else { account_id.to_string() };
    let seed = if seeded {
        "【当前目录已有起点（重要）】项目目录里已经有文件了（多半是你选的起始模板，或上一轮建的）。\
**先 `ls` 看一眼、把 index.html 读完再动手**：它的 `:root` 里已经有整套设计变量、排版与间距刻度。\
你的活是**在它上面改**——换文案、按需增删区块、调色板（改 `:root` 的变量即可）。\
**别推倒重写、别新建一套并行的 CSS。**\n"
    } else {
        ""
    };
    format!(
        "你是用户的「建站 + 部署」助手，在当前目录里为用户搭建网站，并用 wrangler 部署到 Cloudflare。\n\
{seed}\
项目 slug：{project}（部署到 Cloudflare Pages 时用它当 --project-name）。Cloudflare 账号 id：{acct}\
（已通过 CLOUDFLARE_API_TOKEN / CLOUDFLARE_ACCOUNT_ID 环境变量注入 wrangler，无需登录）。\n\
【工作方式】以对话推进：先理解用户想要什么样的站（定位 / 风格 / 页面 / 是否要后端与数据库），\
给一个简短方案征得同意，再动手；边做边用一两句话说明你在做什么；每完成一步告诉用户结果。\n\
【技术约定】\n\
- 纯前端站：写静态文件或用轻量框架，构建产物放 ./dist；本地预览由 Pilot 用 `wrangler pages dev`（或你在 package.json 写的 dev 脚本）起。\n\
- 要后端 / 表单 / 数据库：用 Cloudflare Pages Functions 或 Worker + D1；`wrangler d1 create <name>` 建库，\
在 wrangler.toml 声明绑定，本地 `wrangler pages dev` 会自动模拟绑定。\n\
- 在项目根写一个 pilot.json 描述项目：{{\"dev\":\"本地预览命令\",\"port\":本地预览端口,\"build\":\"构建命令\",\"out\":\"构建产物目录\",\"bindings\":[\"d1:数据库名\"]}}；\
Pilot 用它来起预览 / 构建（没有 pilot.json 时默认 `wrangler pages dev .` 跑在 8788）。**若你的 dev 命令监听的端口不是 8788，务必在 pilot.json 里写对 \"port\"，否则预览窗打不开。**\n\
- 绝不要把任何密钥写进代码或文件；token 已在环境变量里。\n\
【设计底座（硬性）】视觉质量的下限靠纪律保证，不靠发挥。这几条任何时候都不许破：\n\
- 颜色/圆角**只从 `:root` 的设计变量取**（`--bg/--surface/--text/--muted/--accent/--radius`），正文里不许硬编码 hex；\
间距只用刻度值（4/8/12/16/24/40/64）；色板 = 中性色 + **一个**强调色；正文对比度 ≥4.5:1。\n\
- 字号成体系（14/16/20/28/40，别只有两档），正文行高 1.6-1.8，中文用系统字体栈，别引一堆字体。\n\
- 内容有最大宽度容器（680-1100px）居中；可点元素都有 hover 态；补 favicon 与 <title>。\n\
- 不确定怎么好看时，宁可更简单：减色、减框、加留白。\n\
**完整设计规范**（排版/组件/状态约定、成品自检清单、常见翻车点）见随附技能 `web-design`——\
**每次要写 CSS、调版式、改配色前先把它读一遍**，别凭记忆发挥。\n\
【视觉自检（硬性）】你写的页面必须**亲眼看过**才算完成——首次建完和每次大改版式后：\n\
1. 用随附截图工具截两张：桌面 `--width 1280` 和手机 `--width 390 --height 844`。\
本地预览在跑就截 `http://127.0.0.1:<端口>`；纯静态页可以直接截 `file://<项目绝对路径>/index.html`（不需要起服务）；\
有 Functions/D1 的动态页且预览没在跑，就请用户点一下「预览」再截。\n\
2. Read 打开截图逐项检查：横向溢出/滚动条、文字对比度、间距节奏是否均匀、字体是否真的生效、\
图片是否加载（裂图/占位）、按钮悬停态、手机端是否挤爆或字过小。\n\
3. 发现问题先修掉再截一轮确认（至少一轮），完成后把截图路径告诉用户并简述你修了什么。别把没看过的页面交给用户。\n\
【参考图】用户贴参考截图时：提取它的**布局结构、配色气质、字重对比、密度**来做设计，\
不要抄它的文字内容、logo 或品牌名；做完对照参考图自查气质是否接近。\n\
【部署】用户明确要部署时：纯前端 `wrangler pages deploy <产物目录> --project-name {project}`；\
要绑定自定义域名再配 DNS / 自定义域名。部署、改 DNS、写远端 D1 这类对线上生效的操作，\
Pilot 会让用户确认一次，你正常执行命令即可。\n\
回答用中文，简洁自然，先给方案别一上来就大动干戈。运行命令（Bash）时 description 字段一律用中文\
写清这条命令做什么、会影响什么——它会原样展示给非技术用户做批准决定。"
    )
}

// 远程连接 × Codex 的**真实边界**（用户拍板放开，UI 上有醒目警告）：
// - 走 AI 桥的命令**照样逐条弹卡**（闸在 bridge.rs，与厂商无关）—— 这条对 codex 一样有效；
// - 但 codex 无头下没有逐工具闸（claude 靠 PreToolUse 钩子、grok 靠 ACP 权限回调，
//   codex 只有粗粒度 sandbox），所以**拦不住它绕开桥**：直接调系统 ssh/scp + 用户自己的
//   ~/.ssh 密钥打同一台机器 —— 那条路（claude 靠 DANGER 清单弹卡）对 codex 一张卡都不弹。
// 所以这个组合能用，但「每条命令都确认」对它是打折的，UI 必须把这句话摆在用户眼前。
// ★ 配套硬性要求见 lib.rs::apply_brain_switch：**绝不能把 ssh 对话的 ask/auto 静默改写成 full** ——
//   桥那道卡是 codex 仅剩的闸，把它也拿掉就是彻底的无人确认。

/// 远程机器助手的系统提示（kind=ssh 的对话）。
///
/// 契约要和三处对齐，改这里必须同步看：`tools.rs::SSH_JS`（脚本参数）、
/// `bridge.rs`（执行/确认）、`permit.rs::is_bridge_cmd`（钩子放行的形状——**多一个尾巴就会多弹一张卡**）。
pub fn ssh_system_prompt(user: &str, host: &str, port: u16, ssh_js: &str) -> String {
    format!(
        "你是用户这台远程服务器（{user}@{host}:{port}）的运维助手：帮 TA 装软件、配服务、排障、做加固。\n\
【怎么在服务器上执行命令】用随附工具（**只有这一条路**）：\n\
`node \"{ssh_js}\" '<命令>'`，需要更长时限时 `node \"{ssh_js}\" --timeout <秒> '<命令>'`（默认 240 秒，最大 900）。\n\
- 命令**必须**用一对引号整个包起来，且引号后不许再挂任何东西（`; cmd`、`&& cmd`、`> file`、管道都不行）——\
多步请写成一条：`node \"{ssh_js}\" 'cd /etc/nginx && ls'`。\n\
- 输出是 JSON：`{{\"ok\":true,\"code\":<远端退出码>,\"stdout\":\"…\",\"stderr\":\"…\"}}`；\
`ok:false` 时 `error` 是原因（比如用户拒绝了这条命令）。**看 code 判断成败**，别只看有没有报错。\n\
- 凭据在 Pilot 手里，你拿不到也不需要——别去找密码/私钥，别读 ~/.ssh，别自己调系统的 ssh/scp/rsync：\
那些用的是用户自己的密钥、绕开了确认闸，会被拦下来。\n\
【这条通道的脾气】\n\
- **每条命令是独立的一次性 shell**：`cd` 不会留到下一条，环境变量也不会；要连续动作就用 `&&` 串在同一条里。\n\
- **没有 PTY**：交互式提示符没法应答。装包加 `DEBIAN_FRONTEND=noninteractive` 和 `-y`；\
`sudo` 必须免密（`sudo -n`），要输密码的一律会挂住直到超时。\n\
- 长任务（大编译、大下载）别占着通道：用 `nohup … > /tmp/x.log 2>&1 &` 或 systemd 放后台，再分次看日志。\n\
- 每条命令**用户都要点一次确认**（除非 TA 选了全自动）。所以：把相关步骤合并成一条，别拆成几十条来回；\
但也别把「装依赖」和「改配置删数据」混在一条里——用户要看得懂自己在批准什么。\n\
【干活的规矩】这是**用户的真机**，不是沙箱：\n\
- 先看后改：动配置前先 `cat` 出来看、先备份（`cp x x.bak.$(date +%s)`）；改完验证（`nginx -t`、`systemctl status`）。\n\
- 破坏性动作（删数据、`rm -rf`、改防火墙规则、重启机器、动 sshd 配置）**先说清楚后果、征得同意再做**；\
尤其小心把自己关在门外：改 sshd/防火墙前先确认新规则不会切断当前连接。\n\
- 装东西前先探明环境（`cat /etc/os-release`、`uname -m`、有没有 docker/systemd、磁盘和内存够不够），别猜。\n\
- 报告用中文，先说结论（成了/没成、影响是什么），再给关键输出；命令失败就把 stderr 原文给用户看，别自己编原因。"
    )
}

/// 网页截图能力的系统提示补充（shot.js 由 Pilot 生成在数据目录，见 tools.rs）。
/// gcms 会话教「截图→确认→转 WebP→上传→插入文章」；CF 会话教「截本地预览自查 / 截参考站」。
pub fn shot_prompt(shot_path: &str, is_cf: bool) -> String {
    let common = format!(
        "【网页截图】需要网页截图时用随附的无头截图工具（后台渲染、不弹窗）：\n\
`node \"{shot_path}\" --url <URL> --out shots/名字.png [--width 1280] [--full-page] [--wait 3000]`\n\
成功输出 JSON（含文件路径），失败会给出原因。截完**必须用 Read 打开图片确认**内容正确\
——打不开的页面 Chrome 会把自己的错误页截下来，所以要看图排除：错误页 / 验证码 / 空白页 / Cookie 弹窗。\
不对就加大 --wait 重截或换 URL；需要登录或反爬的页面截不了就直说，别硬试。"
    );
    if is_cf {
        format!(
            "{common}\n\
用途：截 `http://127.0.0.1:<预览端口>` 检查你搭的页面实际效果（先确认本地预览在跑）；也可截参考网站找样式灵感。"
        )
    } else {
        format!(
            "{common}\n\
用途：给文章配网页截图——确认无误后转 WebP（`cwebp 输入.png -o 输出.webp`，macOS 也可 \
`sips -s format webp 输入.png --out 输出.webp`），用 `node scripts/gcms.js upload` 上传拿 url，\
以 Markdown 图片插入文章。注意版权：优先截步骤 / 界面等事实性画面，不要整版搬运他人内容。"
        )
    }
}

/// codex 专用兜底：它没有 `--plugin-dir`，吃不到随附的设计技能，只能在提示词里给绝对路径让它自己读。
/// claude/grok 走 plugin 注册（渐进披露、任意轮可重读），不需要这段。
pub fn design_prompt(skill_path: &str) -> String {
    format!(
        "【设计规范文件】完整建站设计规范在这个文件里：\n`{skill_path}`\n\
**每次要写 CSS、调版式、改配色、加区块之前，先用 Read 打开它读一遍**（它有设计变量契约、\
排版与间距刻度、组件与状态约定、成品自检清单，以及最容易翻车的那些点）。别凭记忆发挥。"
    )
}

// ---- 运行一轮 ----

/// 杀整棵进程树（进程组/子孙），别留下带着密钥继续写 CMS 的孙进程（node/bash 等）。
/// spawn 前需已设 process_group(0)（unix）；Windows 用 taskkill /T。
pub(crate) fn kill_tree(pid: Option<u32>) {
    #[cfg(unix)]
    if let Some(pid) = pid {
        let _ = std::process::Command::new("kill").args(["-9", &format!("-{pid}")]).status();
    }
    #[cfg(windows)]
    if let Some(pid) = pid {
        use std::os::windows::process::CommandExt;
        let _ = std::process::Command::new("taskkill").args(["/T", "/F", "/PID", &pid.to_string()]).creation_flags(0x0800_0000).status();
    }
}

#[allow(clippy::too_many_arguments)]
pub async fn run_turn(
    registry: RunRegistry,
    conn: Connection,
    work_dir: String,
    brain: String,
    model: String,
    perm_mode: String,
    effort: String,
    data_dir: PathBuf,
    ssh: crate::ssh::SshSessions,
    session_ref: String,
    is_first: bool,
    system: Option<String>,
    message: String,
    turn_id: String,
    channel: Channel<TurnEvent>,
) -> TurnResult {
    // ssh 连接没有 API key（密码/口令是给 Pilot 连机器用的，绝不进子进程），
    // 且无口令私钥根本没有钥匙串条目 —— 这里拿不到不算错。
    let api_key = if conn.kind == "ssh" {
        String::new()
    } else {
        match keychain::get_key(&conn.id) {
            Ok(k) => k,
            Err(e) => {
                let _ = channel.send(TurnEvent::Done { ok: false, error: e.clone() });
                return TurnResult { ok: false, text: String::new(), tools: vec![], error: e, session_ref, proposal: None, usage: None, limit_reset: None };
            }
        }
    };

    let permit_base = data_dir.join("permit");
    let mode = PermMode::parse(&perm_mode);
    let perm = PermSpec {
        mode,
        conv_id: turn_id.clone(),
        gen_dir: permit_base.join("hooks").join(&turn_id),
        pending_dir: permit_base.join("pending"),
        ssh_js: crate::tools::ssh_js_path(&data_dir),
    };

    let work_dir = if work_dir.trim().is_empty() { conn.skill_dir.clone() } else { work_dir };
    // AI 桥租约：只有 ssh 连接才有。Drop（本函数任何出口）即撤销令牌 + 删目录 → 回合一结束，
    // 残留的 AI 进程也再没法在服务器上跑任何东西。
    let lease = crate::bridge::Lease::start(
        &conn, &work_dir, &turn_id, mode, perm.pending_dir.clone(), ssh.clone(),
    );
    // 随附的建站设计技能：只给 CF 会话（内容站/远程运维用不上它，白占上下文）。
    // claude 和 grok 吃同一个 plugin 目录格式；codex 没有 --plugin-dir，走 design_prompt 兜底。
    let plugin_dir = (conn.kind == "cloudflare").then(|| crate::tools::design_plugin_dir(&data_dir));
    // grok 走 ACP（JSON-RPC over stdio）——协议不同族，整轮交给 acp 模块（事件/结果契约一致）。
    if brain == "grok" {
        return crate::acp::run_turn(
            registry, conn, work_dir, model, mode, effort, perm.pending_dir.clone(), perm.ssh_js.clone(),
            lease.as_ref(), session_ref, is_first, system, message, turn_id, channel, api_key,
            plugin_dir,
        )
        .await;
    }
    let build = if brain == "codex" {
        build_codex(&conn, &model, &effort, &session_ref, is_first, system.as_deref(), &message, &api_key, &work_dir, mode, lease.as_ref())
    } else {
        build_claude(&conn, &model, &effort, &session_ref, is_first, system.as_deref(), &message, &api_key, &work_dir, &perm, lease.as_ref(), plugin_dir.as_deref())
    };
    let mut cmd = match build {
        Ok(c) => c,
        Err(e) => {
            let _ = channel.send(TurnEvent::Done { ok: false, error: e.clone() });
            return TurnResult { ok: false, text: String::new(), tools: vec![], error: e, session_ref, proposal: None, usage: None, limit_reset: None };
        }
    };

    #[cfg(unix)]
    cmd.process_group(0);
    #[cfg(windows)]
    cmd.creation_flags(0x0800_0000); // CREATE_NO_WINDOW：跑 CLI 不弹控制台

    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => {
            let msg = format!("启动 {brain} 失败（确认已安装并登录）: {e}");
            let _ = channel.send(TurnEvent::Done { ok: false, error: msg.clone() });
            return TurnResult { ok: false, text: String::new(), tools: vec![], error: msg, session_ref, proposal: None, usage: None, limit_reset: None };
        }
    };

    let stdout = child.stdout.take();
    let stderr = child.stderr.take();
    let pid = child.id();
    let (_canceled, mut kill_rx) = registry.register(&turn_id);

    let collect = Arc::new(Mutex::new(Collect {
        text: String::new(),
        tools: Vec::new(),
        session_ref: session_ref.clone(),
        is_error: false,
        usage: None,
        raw_tail: String::new(),
        images: Vec::new(),
    }));
    let is_codex = brain == "codex";
    let out_task = stdout.map(|s| {
        parse_stream(s, is_codex, channel.clone(), collect.clone())
    });
    let err_buf = Arc::new(Mutex::new(String::new()));
    let err_task = stderr.map(|s| collect_lines(s, err_buf.clone()));

    let status = tokio::select! {
        s = child.wait() => s.ok(),
        _ = &mut kill_rx => {
            kill_tree(pid);
            let _ = child.kill().await;
            child.wait().await.ok()
        }
    };
    let _ = pid;
    if let Some(t) = out_task { let _ = t.await; }
    if let Some(t) = err_task { let _ = t.await; }
    // 必须在 unregister 之前读取取消标记——移除句柄后 is_canceled 恒为 false。
    let canceled = registry.is_canceled(&turn_id);
    registry.unregister(&turn_id);
    // 清掉本会话遗留的待批请求（取消时钩子被杀不会自删，否则下次冒幽灵批准卡）。
    permit::sweep_conv(&perm.pending_dir, &turn_id);

    let c = collect.lock().unwrap().clone();
    let err_text = err_buf.lock().unwrap().clone();
    let proc_ok = status.map(|s| s.success()).unwrap_or(false);
    let ok = proc_ok && !c.is_error && !canceled;

    let error = if canceled {
        "已停止".to_string()
    } else if !ok {
        // 诊断优先级：stderr 最后一行 → stdout 里最后一条非 JSON 行（CLI 崩溃常打在这）→ 兜底（带退出码 + 自救指引）。
        last_nonempty(&err_text)
            .or_else(|| {
                let t = c.raw_tail.trim();
                if t.is_empty() { None } else { Some(t.to_string()) }
            })
            .or_else(|| {
                if c.text.trim().is_empty() {
                    let code = status
                        .and_then(|s| s.code())
                        .map(|n| n.to_string())
                        .unwrap_or_else(|| "未知/被信号终止".into());
                    Some(format!(
                        "模型没有产生输出（进程退出码：{code}）。若重试仍然如此，多半是这条会话的底层状态损坏——\
点「重建继续」换一个新会话原地续跑（历史和项目文件都会保留）。"
                    ))
                } else {
                    None
                }
            })
            .unwrap_or_default()
    } else {
        String::new()
    };

    // 从助手文本里剥出 PILOT_TASK 提议（并把那行从展示文本移除）。
    let (clean_text, proposal) = extract_proposal(&c.text);
    // 生图产物：只补真实存在的文件（模型可能提了没写出来的路径）。
    let clean_text = if ok && !c.images.is_empty() {
        let existing: Vec<String> = c
            .images
            .iter()
            .filter(|p| std::path::Path::new(p.as_str()).is_file())
            .cloned()
            .collect();
        append_generated_images(&clean_text, &existing)
    } else {
        clean_text
    };

    // 限额识别只看失败回合的错误材料（stderr + 结构化错误 + stdout 尾行）。
    let limit_reset = if ok {
        None
    } else {
        detect_usage_limit(&format!("{error}\n{err_text}\n{}", c.raw_tail))
    };

    let _ = channel.send(TurnEvent::Done { ok, error: error.clone() });
    TurnResult {
        ok,
        text: clean_text,
        tools: c.tools,
        error,
        session_ref: c.session_ref,
        proposal,
        usage: c.usage,
        limit_reset,
    }
}

/// 从文本里找 `PILOT_TASK: {json}` 行，解析成 TaskProposal 并把该行从展示文本移除。
pub(crate) fn extract_proposal(text: &str) -> (String, Option<TaskProposal>) {
    let mut found: Option<TaskProposal> = None;
    let mut kept: Vec<&str> = Vec::new();
    for line in text.lines() {
        if found.is_none() {
            let t = line.trim();
            if let Some(idx) = t.find("PILOT_TASK:") {
                let rest = &t[idx + "PILOT_TASK:".len()..];
                if let Some(b) = rest.find('{') {
                    let mut de = serde_json::Deserializer::from_str(&rest[b..]);
                    if let Ok(p) = TaskProposal::deserialize(&mut de) {
                        if p.every_minutes >= 1 && !p.prompt.trim().is_empty() {
                            found = Some(p);
                            continue; // 丢掉这一整行
                        }
                    }
                }
            }
        }
        kept.push(line);
    }
    (kept.join("\n").trim().to_string(), found)
}

#[derive(Clone)]
struct Collect {
    text: String,
    tools: Vec<ToolCall>,
    session_ref: String,
    is_error: bool,
    usage: Option<TurnUsage>,
    /// stdout 里最后一条**非 JSON** 行：CLI 崩溃/报错时常直接打纯文本到 stdout，
    /// 不留这个的话失败原因会被静默吞掉，只剩一句"模型没有产生输出"。
    raw_tail: String,
    /// codex 生图工具的产物路径（事件流里带 generated_images 目录的图片路径）：
    /// 工具结果不进对话正文，收集后在回合末尾补成「生成的图片」清单给前端渲染缩略图。
    images: Vec<String>,
}

/// 递归扫事件 JSON 的字符串值，抽生图产物路径：含 generated_images 目录 + 图片扩展名
/// 结尾的 token。只认生图目录，不把命令输出里的普通路径当图。
fn extract_generated_images(v: &serde_json::Value, out: &mut Vec<String>) {
    const IMG_EXTS: [&str; 8] = [".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".svg", ".avif"];
    match v {
        serde_json::Value::String(s) if s.contains("generated_images") => {
            for tok in s.split(|ch: char| {
                ch.is_whitespace() || matches!(ch, '"' | '\'' | '(' | ')' | '[' | ']' | '`' | ',' | ';' | '<' | '>')
            }) {
                let t = tok.trim_end_matches(['.', '。', '，', '：', ':']);
                if t.contains("generated_images")
                    && IMG_EXTS.iter().any(|e| t.to_ascii_lowercase().ends_with(e))
                    && !out.iter().any(|x| x == t)
                {
                    out.push(t.to_string());
                }
            }
        }
        serde_json::Value::Array(a) => {
            for x in a {
                extract_generated_images(x, out);
            }
        }
        serde_json::Value::Object(o) => {
            for x in o.values() {
                extract_generated_images(x, out);
            }
        }
        _ => {}
    }
}

/// 把正文没提到的生图产物补成「生成的图片」清单（前端按此标记渲染缩略图）。
/// images 需由调用方先过滤成真实存在的文件。
fn append_generated_images(text: &str, images: &[String]) -> String {
    let fresh: Vec<&String> = images.iter().filter(|p| !text.contains(p.as_str())).take(12).collect();
    if fresh.is_empty() {
        return text.to_string();
    }
    let mut out = text.trim_end().to_string();
    if !out.is_empty() {
        out.push_str("\n\n");
    }
    out.push_str("生成的图片：");
    for p in fresh {
        out.push_str("\n- ");
        out.push_str(p);
    }
    out
}

/// 从 usage 对象抽 token 数。兼容 Anthropic（input_tokens/…）与 OpenAI 风格（prompt/completion_tokens）。缺失按 0。
fn parse_usage(v: &serde_json::Value) -> Option<TurnUsage> {
    if !v.is_object() {
        return None;
    }
    let g = |keys: &[&str]| keys.iter().find_map(|k| v.get(*k).and_then(|x| x.as_u64())).unwrap_or(0);
    let u = TurnUsage {
        input: g(&["input_tokens", "prompt_tokens"]),
        output: g(&["output_tokens", "completion_tokens"]),
        cache_read: g(&["cache_read_input_tokens", "cached_input_tokens"]),
        cache_create: g(&["cache_creation_input_tokens"]),
    };
    if u.input + u.output + u.cache_read + u.cache_create == 0 {
        None
    } else {
        Some(u)
    }
}

/// 按连接类型注入凭据 env + 设置 cwd：
///   cloudflare → CLOUDFLARE_API_TOKEN/ACCOUNT_ID，cwd=项目目录；
///   gcms       → GCMS_API_BASE/KEY，cwd=技能包目录；
///   ssh        → **不注入任何凭据**（那是一台机器的 root shell，不是一个 API key），
///                只给本轮租约的目录+令牌，让 ssh.js 能把命令递给 Pilot 代跑。见 bridge.rs。
pub(crate) fn apply_env_cwd(
    cmd: &mut Command,
    conn: &Connection,
    work_dir: &str,
    api_key: &str,
    lease: Option<&crate::bridge::Lease>,
) {
    cmd.current_dir(work_dir);
    if conn.kind == "ssh" {
        if let Some(l) = lease {
            for (k, v) in l.env() {
                cmd.env(k, v);
            }
        }
        return;
    }
    if conn.kind == "cloudflare" {
        cmd.env("CLOUDFLARE_API_TOKEN", api_key);
        if !conn.account_id.is_empty() {
            cmd.env("CLOUDFLARE_ACCOUNT_ID", &conn.account_id);
        }
    } else {
        cmd.env("GCMS_API_BASE", &conn.api_base)
            .env("GCMS_API_KEY", api_key);
    }
}

#[allow(clippy::too_many_arguments)]
fn build_claude(
    conn: &Connection,
    model: &str,
    effort: &str,
    session_ref: &str,
    is_first: bool,
    system: Option<&str>,
    message: &str,
    api_key: &str,
    work_dir: &str,
    perm: &PermSpec,
    lease: Option<&crate::bridge::Lease>,
    plugin_dir: Option<&std::path::Path>,
) -> Result<Command, String> {
    // 空 → 默认档位；别名（sonnet/opus/haiku）或完整模型 ID（如 claude-opus-4-8）都放行，
    // 只挡形似参数/含空白的非法值。claude --model 同时接受别名与完整 ID。
    let model = model.trim();
    let model = if model.is_empty() {
        "sonnet"
    } else if model.starts_with('-') || model.contains(char::is_whitespace) {
        return Err(format!("无效的模型标识: {model}"));
    } else {
        model
    };
    // 用检测同款的路径解析：Windows 上裸名找不到 .cmd/.exe 之外的安装形态。
    let mut cmd = Command::new(crate::brains::resolve_bin("claude"));
    cmd.arg("-p").arg(message);
    if is_first {
        cmd.args(["--session-id", session_ref]);
        if let Some(sys) = system {
            cmd.args(["--append-system-prompt", sys]);
        }
    } else {
        cmd.args(["--resume", session_ref]);
    }
    // ★ 每轮都带（包括 --resume）：技能的价值就在「任意一轮能重新拉取」。只在首轮给等于白做——
    // 系统提示词首轮就有的东西，第 20 轮改版式时早漂没了，那正是要治的病。
    if let Some(p) = plugin_dir {
        cmd.args(["--plugin-dir", &p.to_string_lossy()]);
    }
    cmd.args(["--output-format", "stream-json"])
        .arg("--verbose")
        .arg("--include-partial-messages")
        .args(["--model", model]);
    // 权限档位 → claude 参数（plan/ask/auto/full）；ask/auto 会生成 PreToolUse 钩子 + settings。
    let perm_flags =
        permit::claude_flags(perm.mode, &perm.conv_id, &perm.gen_dir, &perm.pending_dir, &perm.ssh_js)?;
    cmd.args(&perm_flags);
    // 思考等级 → MAX_THINKING_TOKENS（无头模式实测能开出 thinking 块）；空＝跟随模型默认。
    let budget = match effort {
        "low" => "4096",
        "medium" => "16384",
        "high" => "32000",
        _ => "",
    };
    if !budget.is_empty() {
        cmd.env("MAX_THINKING_TOKENS", budget);
    }
    apply_env_cwd(&mut cmd, conn, work_dir, api_key, lease);
    cmd.stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    Ok(cmd)
}

#[allow(clippy::too_many_arguments)]
fn build_codex(
    conn: &Connection,
    model: &str,
    effort: &str,
    session_ref: &str,
    is_first: bool,
    system: Option<&str>,
    message: &str,
    api_key: &str,
    work_dir: &str,
    perm: PermMode,
    lease: Option<&crate::bridge::Lease>,
) -> Result<Command, String> {
    // codex 无头没有逐工具回传 UI 的能力，权限档位只能落到 sandbox 粗粒度（精细批准以 claude 为主）。
    // plan＝只读；full＝完全放开；ask/auto＝可写工作区（无法逐命令确认）。
    let sandbox = match perm {
        PermMode::Plan => "read-only",
        PermMode::Full => "danger-full-access",
        PermMode::Ask | PermMode::Auto => "workspace-write",
    };
    // codex 没有独立系统提示位：首轮把角色框架并进消息（前端只展示用户原话）。
    let prompt = if is_first {
        match system {
            Some(sys) => format!("{sys}\n\n——\n用户：{message}"),
            None => message.to_string(),
        }
    } else {
        message.to_string()
    };
    // 同上：npm 装的 codex 在 Windows 上是 codex.cmd，必须用完整路径 spawn。
    let mut cmd = Command::new(crate::brains::resolve_bin("codex"));
    cmd.arg("exec");
    if is_first {
        cmd.arg("--json")
            .args(["--sandbox", sandbox])
            .args(["-c", "sandbox_workspace_write.network_access=true"])
            .arg("--skip-git-repo-check");
    } else {
        cmd.arg("resume")
            .arg(session_ref)
            .arg("--json")
            .arg("--skip-git-repo-check")
            .args(["-c", &format!("sandbox_mode={sandbox}")])
            .args(["-c", "sandbox_workspace_write.network_access=true"]);
    }
    // 自定义模型 ID（可选）：非空则用 -c model=<id> 覆盖 codex 本地默认；留空用其默认。
    // 与既有 -c sandbox_* 一致用裸字符串；含空白/形似参数的非法值直接拒。
    let model = model.trim();
    if !model.is_empty() {
        if model.starts_with('-') || model.contains(char::is_whitespace) {
            return Err(format!("无效的模型标识: {model}"));
        }
        cmd.args(["-c", &format!("model={model}")]);
    }
    // 思考等级（实测 0.144：-c model_reasoning_effort=low|medium|high）；空＝跟随 codex 默认。
    if matches!(effort, "low" | "medium" | "high") {
        cmd.args(["-c", &format!("model_reasoning_effort={effort}")]);
    }
    cmd.arg(&prompt);
    apply_env_cwd(&mut cmd, conn, work_dir, api_key, lease);
    cmd.stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    Ok(cmd)
}

fn parse_stream(
    reader: impl AsyncRead + Unpin + Send + 'static,
    is_codex: bool,
    ch: Channel<TurnEvent>,
    collect: Arc<Mutex<Collect>>,
) -> tauri::async_runtime::JoinHandle<()> {
    tauri::async_runtime::spawn(async move {
        let mut r = BufReader::new(reader);
        let mut buf = Vec::new();
        loop {
            buf.clear();
            match r.read_until(b'\n', &mut buf).await {
                Ok(0) => break,
                Ok(_) => {
                    let cow = String::from_utf8_lossy(&buf);
                    let line = cow.trim_end_matches(['\n', '\r']);
                    let Ok(ev) = serde_json::from_str::<serde_json::Value>(line) else {
                        // CLI 崩溃/报错常直接打纯文本到 stdout；留住最后一条非 JSON 行当诊断线索。
                        let t = line.trim();
                        if !t.is_empty() {
                            let mut c = collect.lock().unwrap();
                            c.raw_tail = t.chars().take(300).collect();
                        }
                        continue;
                    };
                    if is_codex {
                        parse_codex(&ev, &ch, &collect);
                    } else {
                        parse_claude(&ev, &ch, &collect);
                    }
                }
                Err(_) => break,
            }
        }
    })
}

fn parse_claude(ev: &serde_json::Value, ch: &Channel<TurnEvent>, collect: &Arc<Mutex<Collect>>) {
    match ev.get("type").and_then(|t| t.as_str()) {
        Some("stream_event") => {
            let e = &ev["event"];
            if e.get("type").and_then(|t| t.as_str()) == Some("content_block_delta") {
                if e["delta"].get("type").and_then(|t| t.as_str()) == Some("text_delta") {
                    if let Some(t) = e["delta"].get("text").and_then(|t| t.as_str()) {
                        collect.lock().unwrap().text.push_str(t);
                        let _ = ch.send(TurnEvent::Delta { text: t.to_string() });
                    }
                }
            }
        }
        Some("assistant") => {
            if let Some(u) = parse_usage(&ev["message"]["usage"]) {
                collect.lock().unwrap().usage = Some(u); // 每条 assistant 消息刷新，最后一条＝本轮最终上下文
            }
            if let Some(blocks) = ev["message"]["content"].as_array() {
                for b in blocks {
                    if b.get("type").and_then(|t| t.as_str()) == Some("tool_use") {
                        let name = b.get("name").and_then(|n| n.as_str()).unwrap_or("tool");
                        let detail = tool_detail(name, &b["input"]);
                        collect.lock().unwrap().tools.push(ToolCall { label: name.into(), detail: detail.clone() });
                        let _ = ch.send(TurnEvent::Tool { label: name.into(), detail });
                    }
                }
            }
        }
        Some("result") => {
            let mut c = collect.lock().unwrap();
            // 优先用最后一条 assistant 消息的 usage（＝最终那次模型调用读入的整段上下文＝当前会话大小）；
            // result.usage 在多步回合里可能是各步汇总，会高估上下文，故仅在没抓到 assistant usage 时兜底。
            if c.usage.is_none() {
                if let Some(u) = parse_usage(&ev["usage"]) {
                    c.usage = Some(u);
                }
            }
            if ev.get("is_error").and_then(|b| b.as_bool()).unwrap_or(false) {
                c.is_error = true;
            }
            if c.text.trim().is_empty() {
                if let Some(r) = ev.get("result").and_then(|r| r.as_str()) {
                    c.text = r.to_string();
                }
            }
        }
        _ => {}
    }
}

fn parse_codex(ev: &serde_json::Value, ch: &Channel<TurnEvent>, collect: &Arc<Mutex<Collect>>) {
    // 生图工具的产物只出现在工具结果事件里、不进对话正文——从所有事件里顺手收集。
    {
        let mut imgs = Vec::new();
        extract_generated_images(ev, &mut imgs);
        if !imgs.is_empty() {
            let mut c = collect.lock().unwrap();
            for i in imgs {
                if !c.images.contains(&i) {
                    c.images.push(i);
                }
            }
        }
    }
    // codex 某些版本在事件里带 token 统计，尽力抽一下（拿不到就 None，前端会隐藏上下文条）。
    if let Some(u) = parse_usage(&ev["usage"])
        .or_else(|| parse_usage(&ev["item"]["usage"]))
        .or_else(|| parse_usage(&ev["info"]["usage"]))
    {
        collect.lock().unwrap().usage = Some(u);
    }
    match ev.get("type").and_then(|t| t.as_str()) {
        Some("thread.started") => {
            if let Some(id) = ev.get("thread_id").and_then(|i| i.as_str()) {
                collect.lock().unwrap().session_ref = id.to_string();
            }
        }
        Some("item.completed") => {
            let it = &ev["item"];
            match it.get("type").and_then(|t| t.as_str()) {
                Some("agent_message") => {
                    if let Some(t) = it.get("text").and_then(|t| t.as_str()) {
                        let mut c = collect.lock().unwrap();
                        if !c.text.is_empty() {
                            c.text.push_str("\n\n");
                        }
                        c.text.push_str(t);
                        let _ = ch.send(TurnEvent::Delta { text: t.to_string() });
                    }
                }
                Some("command_execution") => {
                    let d = it.get("command").and_then(|c| c.as_str()).unwrap_or("").chars().take(200).collect::<String>();
                    collect.lock().unwrap().tools.push(ToolCall { label: "exec".into(), detail: d.clone() });
                    let _ = ch.send(TurnEvent::Tool { label: "exec".into(), detail: d });
                }
                Some("error") => {
                    collect.lock().unwrap().is_error = true;
                }
                _ => {}
            }
        }
        Some("error") | Some("turn.failed") => {
            collect.lock().unwrap().is_error = true;
        }
        _ => {}
    }
}

fn tool_detail(name: &str, input: &serde_json::Value) -> String {
    if name == "Bash" {
        if let Some(cmd) = input.get("command").and_then(|c| c.as_str()) {
            return cmd.chars().take(200).collect();
        }
    }
    // Skill＝加载随附技能（如建站设计规范）。不特判的话工具行会显示成一坨裸 JSON。
    if name == "Skill" {
        if let Some(s) = input
            .get("command")
            .or_else(|| input.get("skill"))
            .or_else(|| input.get("name"))
            .and_then(|c| c.as_str())
        {
            return s.chars().take(200).collect();
        }
    }
    input.to_string().chars().take(200).collect()
}

fn collect_lines(
    reader: impl AsyncRead + Unpin + Send + 'static,
    sink: Arc<Mutex<String>>,
) -> tauri::async_runtime::JoinHandle<()> {
    tauri::async_runtime::spawn(async move {
        let mut r = BufReader::new(reader);
        let mut buf = Vec::new();
        loop {
            buf.clear();
            match r.read_until(b'\n', &mut buf).await {
                Ok(0) => break,
                Ok(_) => {
                    let cow = String::from_utf8_lossy(&buf);
                    let mut s = sink.lock().unwrap();
                    s.push_str(cow.trim_end_matches(['\n', '\r']));
                    s.push('\n');
                }
                Err(_) => break,
            }
        }
    })
}

pub(crate) fn last_nonempty(s: &str) -> Option<String> {
    s.lines().rev().find(|l| !l.trim().is_empty()).map(|l| l.trim().chars().take(200).collect())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn ch() -> Channel<TurnEvent> {
        Channel::new(|_| Ok(()))
    }

    /// 目录非空时必须明确告诉模型「先读再改、别推倒重写」——这是内置模板能不能生效的**唯一**开关。
    /// 以前 use_template 把模板拷进去后没人吭声，模型视角永远是空目录，那份起点直接被重写掉。
    #[test]
    fn cf_prompt_tells_model_about_the_seed() {
        let seeded = cf_system_prompt("coffee", "acct", true);
        assert!(seeded.contains("已有起点"), "非空目录必须提示已有起点");
        assert!(seeded.contains("别推倒重写"), "得明说别重写，否则等于没提示");
        assert!(seeded.contains("index.html"), "要点名先读 index.html");

        let blank = cf_system_prompt("coffee", "acct", false);
        assert!(!blank.contains("已有起点"), "空目录别瞎说有起点");

        // 两种情况都得带上设计底座与技能指针（这条是所有厂商共吃的地板）
        for p in [&seeded, &blank] {
            assert!(p.contains("设计底座"));
            assert!(p.contains("web-design"), "要指向随附技能，否则规范只活第一轮");
        }
    }

    /// codex 吃不到 --plugin-dir，只能靠提示词里的绝对路径自己去读。
    #[test]
    fn design_prompt_points_at_the_skill_file() {
        let p = design_prompt("/data/plugins/pilot-design/skills/web-design/SKILL.md");
        assert!(p.contains("/data/plugins/pilot-design/skills/web-design/SKILL.md"));
        assert!(p.contains("Read"), "得让它用 Read 打开");
    }

    #[test]
    fn claude_text_delta_accumulates() {
        let c = Arc::new(Mutex::new(Collect { text: String::new(), tools: vec![], session_ref: "s".into(), is_error: false, usage: None, raw_tail: String::new(), images: vec![] }));
        let channel = ch();
        parse_claude(&json!({"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"你好"}}}), &channel, &c);
        parse_claude(&json!({"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"世界"}}}), &channel, &c);
        assert_eq!(c.lock().unwrap().text, "你好世界");
    }

    #[test]
    fn claude_tool_use_captured() {
        let c = Arc::new(Mutex::new(Collect { text: String::new(), tools: vec![], session_ref: "s".into(), is_error: false, usage: None, raw_tail: String::new(), images: vec![] }));
        parse_claude(&json!({"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"node scripts/gcms.js list posts"}}]}}), &ch(), &c);
        let g = c.lock().unwrap();
        assert_eq!(g.tools.len(), 1);
        assert!(g.tools[0].detail.contains("gcms.js"));
    }

    #[test]
    fn codex_thread_and_message() {
        let c = Arc::new(Mutex::new(Collect { text: String::new(), tools: vec![], session_ref: String::new(), is_error: false, usage: None, raw_tail: String::new(), images: vec![] }));
        parse_codex(&json!({"type":"thread.started","thread_id":"tid-123"}), &ch(), &c);
        parse_codex(&json!({"type":"item.completed","item":{"type":"agent_message","text":"明白"}}), &ch(), &c);
        let g = c.lock().unwrap();
        assert_eq!(g.session_ref, "tid-123");
        assert_eq!(g.text, "明白");
    }

    #[test]
    fn result_error_flag() {
        let c = Arc::new(Mutex::new(Collect { text: String::new(), tools: vec![], session_ref: "s".into(), is_error: false, usage: None, raw_tail: String::new(), images: vec![] }));
        parse_claude(&json!({"type":"result","is_error":true,"result":"boom"}), &ch(), &c);
        assert!(c.lock().unwrap().is_error);
    }

    /// 限额识别：claude 带重置时间戳、codex/429 无时间戳给 0、普通错误 None。
    #[test]
    fn detect_usage_limit_variants() {
        assert_eq!(detect_usage_limit("Claude AI usage limit reached|1736600400"), Some(1736600400));
        assert_eq!(detect_usage_limit("blah limit reached|1736600400000 tail"), Some(1736600400)); // 毫秒级归一
        assert_eq!(detect_usage_limit("HTTP 429 Too Many Requests"), Some(0));
        assert_eq!(detect_usage_limit("You've hit your usage limit."), Some(0));
        assert_eq!(detect_usage_limit("insufficient_quota: upgrade your plan"), Some(0));
        assert_eq!(detect_usage_limit("没找到这个文件"), None);
        assert_eq!(detect_usage_limit("connection reset by peer"), None); // reset≠限额
    }

    /// 生图产物收集：工具结果里的 generated_images 路径进 images（去重）；普通路径不收。
    #[test]
    fn codex_generated_images_collected() {
        let c = Arc::new(Mutex::new(Collect { text: String::new(), tools: vec![], session_ref: "s".into(), is_error: false, usage: None, raw_tail: String::new(), images: vec![] }));
        let p = "/Users/x/.codex/generated_images/019f/exec-abc.png";
        parse_codex(&json!({"type":"item.completed","item":{"type":"tool_call","output":format!("saved to {p} done")}}), &ch(), &c);
        parse_codex(&json!({"type":"item.completed","item":{"type":"tool_call","output":{"path": p}}}), &ch(), &c); // 重复不重收
        parse_codex(&json!({"type":"item.completed","item":{"type":"command_execution","command":"cp a.png b.png"}}), &ch(), &c); // 普通路径不收
        let g = c.lock().unwrap();
        assert_eq!(g.images, vec![p.to_string()]);
    }

    /// 「生成的图片」清单：正文没提到的才补；提到过/为空不动原文。
    #[test]
    fn append_generated_images_dedups_against_text() {
        let imgs = vec!["/a/generated_images/x.png".to_string(), "/a/generated_images/y.png".to_string()];
        let out = append_generated_images("已生成 /a/generated_images/x.png", &imgs);
        assert!(out.contains("生成的图片：\n- /a/generated_images/y.png"), "{out}");
        assert_eq!(out.matches("x.png").count(), 1, "正文已有的不重复列");
        assert_eq!(append_generated_images("正文", &[]), "正文");
        let solo = append_generated_images("", &imgs[..1].to_vec());
        assert!(solo.starts_with("生成的图片："), "{solo}");
    }

    #[test]
    fn extract_proposal_parses_and_strips() {
        let text = "好的，我准备了一个定时任务建议，请确认。\nPILOT_TASK: {\"title\":\"每日速写\",\"prompt\":\"写一篇当日科技热点文章存草稿\",\"every_minutes\":1440,\"first_run\":\"2026-07-05T09:00:00+08:00\"}";
        let (clean, p) = extract_proposal(text);
        let p = p.expect("proposal parsed");
        assert_eq!(p.title, "每日速写");
        assert_eq!(p.every_minutes, 1440);
        assert!(!clean.contains("PILOT_TASK"));
        assert!(clean.contains("请确认"));
    }

    #[test]
    fn extract_proposal_none_when_absent() {
        let (clean, p) = extract_proposal("普通回复，没有任务提议。");
        assert!(p.is_none());
        assert_eq!(clean, "普通回复，没有任务提议。");
    }

    #[test]
    fn extract_proposal_rejects_incomplete() {
        // every_minutes 缺失 → 解析失败，不当作提议。
        let (_, p) = extract_proposal("PILOT_TASK: {\"title\":\"x\",\"prompt\":\"y\"}");
        assert!(p.is_none());
    }
}
