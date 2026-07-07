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
    fn register(&self, id: &str) -> (Arc<AtomicBool>, tokio::sync::oneshot::Receiver<()>) {
        let canceled = Arc::new(AtomicBool::new(false));
        let (tx, rx) = tokio::sync::oneshot::channel();
        self.inner
            .lock()
            .unwrap()
            .insert(id.to_string(), (canceled.clone(), Some(tx)));
        (canceled, rx)
    }
    fn unregister(&self, id: &str) {
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
    fn is_canceled(&self, id: &str) -> bool {
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
交互方式：以对话推进——先理解用户意图，必要时提问澄清，给出简短方案并征得同意后再动手；\
动手时边做边用一两句话说明你在做什么；每完成一步给出结果（如新建内容的 ID、预览或后台链接，可用 `preview-url`）。\
回答用中文，简洁自然，不要长篇大论。\n\
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
给出建站方案；达成一致后依次完善站点资料（site-profile-update）、配置前台导航（navigation-update）、\
创建若干种子文章。每一步做完都向用户汇报，让他确认后再进行下一步。",
        "article" => "\n\n本次目标：内容创作。帮用户策划并创作文章——先明确主题、角度、语言与要求，\
需要时给出提纲让用户确认，再撰写并在站点创建（默认草稿）。用户可以在对话里让你修改、扩写或换角度。",
        _ => "\n\n以对话方式协助用户完成关于这个站点的各类内容运营工作。",
    };
    format!("{base}{role}")
}

/// Cloudflare 建站助手的系统提示词。cwd＝项目目录；token 已注入 env。
pub fn cf_system_prompt(project: &str, account_id: &str) -> String {
    let acct = if account_id.is_empty() { "(未指定)".to_string() } else { account_id.to_string() };
    format!(
        "你是用户的「建站 + 部署」助手，在当前目录里为用户搭建网站，并用 wrangler 部署到 Cloudflare。\n\
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
【设计底座（硬性）】视觉质量的下限靠纪律保证，不靠发挥：\n\
- 开工先在 CSS 里定义设计变量并全站只用变量：`--bg/--surface/--text/--muted/--accent/--radius` + 一套间距刻度（如 4/8/12/16/24/40/64px）。\
色板 = 中性色 + **一个**强调色（用户点名多彩除外）；正文对比度必须够（普通文字至少 4.5:1）。\n\
- 字体成对选：标题与正文要么同族不同字重，要么经典搭配（如 无衬线标题+无衬线正文、衬线标题+无衬线正文）；\
中文站用系统字体栈或 Noto 系，别引一堆字体。字号体系化（如 14/16/20/28/40），行高正文 1.6-1.8。\n\
- 内容有最大宽度容器（常用 680-1100px）居中，区块间留白宁大勿小；所有可点元素有 hover 态；补 favicon 与 <title>。\n\
- 不确定怎么好看时，宁可更简单：减色、减框、加留白。\n\
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
回答用中文，简洁自然，先给方案别一上来就大动干戈。"
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

// ---- 运行一轮 ----

#[allow(clippy::too_many_arguments)]
pub async fn run_turn(
    registry: RunRegistry,
    conn: Connection,
    work_dir: String,
    brain: String,
    model: String,
    perm_mode: String,
    permit_base: PathBuf,
    session_ref: String,
    is_first: bool,
    system: Option<String>,
    message: String,
    turn_id: String,
    channel: Channel<TurnEvent>,
) -> TurnResult {
    let api_key = match keychain::get_key(&conn.id) {
        Ok(k) => k,
        Err(e) => {
            let _ = channel.send(TurnEvent::Done { ok: false, error: e.clone() });
            return TurnResult { ok: false, text: String::new(), tools: vec![], error: e, session_ref, proposal: None, usage: None };
        }
    };

    let mode = PermMode::parse(&perm_mode);
    let perm = PermSpec {
        mode,
        conv_id: turn_id.clone(),
        gen_dir: permit_base.join("hooks").join(&turn_id),
        pending_dir: permit_base.join("pending"),
    };

    let work_dir = if work_dir.trim().is_empty() { conn.skill_dir.clone() } else { work_dir };
    let build = if brain == "codex" {
        build_codex(&conn, &model, &session_ref, is_first, system.as_deref(), &message, &api_key, &work_dir, mode)
    } else {
        build_claude(&conn, &model, &session_ref, is_first, system.as_deref(), &message, &api_key, &work_dir, &perm)
    };
    let mut cmd = match build {
        Ok(c) => c,
        Err(e) => {
            let _ = channel.send(TurnEvent::Done { ok: false, error: e.clone() });
            return TurnResult { ok: false, text: String::new(), tools: vec![], error: e, session_ref, proposal: None, usage: None };
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
            return TurnResult { ok: false, text: String::new(), tools: vec![], error: msg, session_ref, proposal: None, usage: None };
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
            // 杀整棵进程树，别留下带着密钥继续写 CMS 的孙进程（node/bash 等）。
            #[cfg(unix)]
            if let Some(pid) = pid {
                let _ = std::process::Command::new("kill").args(["-9", &format!("-{pid}")]).status();
            }
            #[cfg(windows)]
            if let Some(pid) = pid {
                use std::os::windows::process::CommandExt;
                let _ = std::process::Command::new("taskkill").args(["/T", "/F", "/PID", &pid.to_string()]).creation_flags(0x0800_0000).status();
            }
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
        last_nonempty(&err_text)
            .or_else(|| if c.text.trim().is_empty() { Some("模型没有产生输出".into()) } else { None })
            .unwrap_or_default()
    } else {
        String::new()
    };

    // 从助手文本里剥出 PILOT_TASK 提议（并把那行从展示文本移除）。
    let (clean_text, proposal) = extract_proposal(&c.text);

    let _ = channel.send(TurnEvent::Done { ok, error: error.clone() });
    TurnResult {
        ok,
        text: clean_text,
        tools: c.tools,
        error,
        session_ref: c.session_ref,
        proposal,
        usage: c.usage,
    }
}

/// 从文本里找 `PILOT_TASK: {json}` 行，解析成 TaskProposal 并把该行从展示文本移除。
fn extract_proposal(text: &str) -> (String, Option<TaskProposal>) {
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
///   gcms       → GCMS_API_BASE/KEY，cwd=技能包目录。
fn apply_env_cwd(cmd: &mut Command, conn: &Connection, work_dir: &str, api_key: &str) {
    cmd.current_dir(work_dir);
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

fn build_claude(
    conn: &Connection,
    model: &str,
    session_ref: &str,
    is_first: bool,
    system: Option<&str>,
    message: &str,
    api_key: &str,
    work_dir: &str,
    perm: &PermSpec,
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
    let mut cmd = Command::new("claude");
    cmd.arg("-p").arg(message);
    if is_first {
        cmd.args(["--session-id", session_ref]);
        if let Some(sys) = system {
            cmd.args(["--append-system-prompt", sys]);
        }
    } else {
        cmd.args(["--resume", session_ref]);
    }
    cmd.args(["--output-format", "stream-json"])
        .arg("--verbose")
        .arg("--include-partial-messages")
        .args(["--model", model]);
    // 权限档位 → claude 参数（plan/ask/auto/full）；ask/auto 会生成 PreToolUse 钩子 + settings。
    let perm_flags = permit::claude_flags(perm.mode, &perm.conv_id, &perm.gen_dir, &perm.pending_dir)?;
    cmd.args(&perm_flags);
    apply_env_cwd(&mut cmd, conn, work_dir, api_key);
    cmd.stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    Ok(cmd)
}

fn build_codex(
    conn: &Connection,
    model: &str,
    session_ref: &str,
    is_first: bool,
    system: Option<&str>,
    message: &str,
    api_key: &str,
    work_dir: &str,
    perm: PermMode,
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
    let mut cmd = Command::new("codex");
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
    cmd.arg(&prompt);
    apply_env_cwd(&mut cmd, conn, work_dir, api_key);
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
                    let Ok(ev) = serde_json::from_str::<serde_json::Value>(line) else { continue };
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

fn last_nonempty(s: &str) -> Option<String> {
    s.lines().rev().find(|l| !l.trim().is_empty()).map(|l| l.trim().chars().take(200).collect())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn ch() -> Channel<TurnEvent> {
        Channel::new(|_| Ok(()))
    }

    #[test]
    fn claude_text_delta_accumulates() {
        let c = Arc::new(Mutex::new(Collect { text: String::new(), tools: vec![], session_ref: "s".into(), is_error: false, usage: None }));
        let channel = ch();
        parse_claude(&json!({"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"你好"}}}), &channel, &c);
        parse_claude(&json!({"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"世界"}}}), &channel, &c);
        assert_eq!(c.lock().unwrap().text, "你好世界");
    }

    #[test]
    fn claude_tool_use_captured() {
        let c = Arc::new(Mutex::new(Collect { text: String::new(), tools: vec![], session_ref: "s".into(), is_error: false, usage: None }));
        parse_claude(&json!({"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"node scripts/gcms.js list posts"}}]}}), &ch(), &c);
        let g = c.lock().unwrap();
        assert_eq!(g.tools.len(), 1);
        assert!(g.tools[0].detail.contains("gcms.js"));
    }

    #[test]
    fn codex_thread_and_message() {
        let c = Arc::new(Mutex::new(Collect { text: String::new(), tools: vec![], session_ref: String::new(), is_error: false, usage: None }));
        parse_codex(&json!({"type":"thread.started","thread_id":"tid-123"}), &ch(), &c);
        parse_codex(&json!({"type":"item.completed","item":{"type":"agent_message","text":"明白"}}), &ch(), &c);
        let g = c.lock().unwrap();
        assert_eq!(g.session_ref, "tid-123");
        assert_eq!(g.text, "明白");
    }

    #[test]
    fn result_error_flag() {
        let c = Arc::new(Mutex::new(Collect { text: String::new(), tools: vec![], session_ref: "s".into(), is_error: false, usage: None }));
        parse_claude(&json!({"type":"result","is_error":true,"result":"boom"}), &ch(), &c);
        assert!(c.lock().unwrap().is_error);
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
