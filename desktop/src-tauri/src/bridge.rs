//! AI 桥：让 AI 在远程机器上跑命令，但**永远拿不到 SSH 凭据**。
//!
//! ```text
//! AI 子进程 → node <tools/ssh.js> '<命令>' → 写 <租约目录>/<id>.req.json
//!           → Pilot（本模块）轮询到 → 按档位弹卡确认 → 用钥匙串凭据在已建会话上执行
//!           → 写回 <id>.resp.json → 脚本打印 JSON 给 AI
//! ```
//!
//! **为什么不学 gcms/CF 把凭据塞进子进程 env**：那对一个 API key 可以接受，对一台机器的
//! root shell 不成立。这里凭据只在 Rust + 钥匙串里，每条命令都过 Pilot ——
//! 可确认、可拦、可记录，且 AI 改不了这个闸（它在 Rust 里，不在 AI 能写的脚本里）。
//!
//! **为什么用文件而不是 localhost 端口**：与既有的权限钩子协议同构（那套已经在跑、有测试），
//! 不用监听端口、不碰防火墙、不新增依赖。租约目录放在**本轮 cwd（工作区）之下**是必须的 ——
//! codex 的 `workspace-write` 沙箱只允许写 cwd，放 data_dir 下 codex 会被沙箱挡住。
//!
//! 租约（Lease）＝一轮对话的授权：进程 env 里给脚本 `GCMS_SSH_DIR` + `GCMS_SSH_TOKEN`，
//! 回合结束（含出错/取消）Drop 即撤销 + 删目录。

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use crate::pack::Connection;
use crate::permit::{self, PermMode};
use crate::ssh::SshSessions;

/// 脚本心跳超过这个岁数 = AI 那侧的 Bash 调用已经死了（工具超时/被杀/回合取消）。
/// 此时**绝不能再执行**：否则命令会在「AI 已经放弃这次调用」之后偷偷在服务器上跑起来。
const ALIVE_STALE_MS: u128 = 4_000;
/// 等用户点批准卡的上限（与权限钩子一致，超时 = 拒绝，fail-closed）。
const APPROVE_TIMEOUT: Duration = Duration::from_secs(15 * 60);
/// 轮询间隔（与钩子的 300ms 同量级；目录通常是空的，开销可忽略）。
const POLL: Duration = Duration::from_millis(200);
/// 单条命令的默认/最大执行时限（秒）。长任务应当 nohup/systemd 放后台，别占着通道。
const EXEC_TIMEOUT_DEFAULT: u64 = 240;
const EXEC_TIMEOUT_MAX: u64 = 900;

fn now_ms() -> u128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0)
}

/// 先写临时文件再 rename：读方（脚本/Pilot）永远看不到写了一半的 JSON。
fn write_atomic(path: &Path, body: &[u8]) -> std::io::Result<()> {
    let tmp = path.with_extension("tmp");
    std::fs::write(&tmp, body)?;
    std::fs::rename(&tmp, path)
}

/// 一轮对话对某个 SSH 连接的授权。Drop = 撤销（停轮询 + 删目录）。
pub struct Lease {
    dir: PathBuf,
    token: String,
    stop: Arc<AtomicBool>,
}

/// 轮询任务需要的一切（不能持有 Lease 本身，否则 Drop 永远不触发）。
struct Ctx {
    dir: PathBuf,
    token: String,
    stop: Arc<AtomicBool>,
    conn: Connection,
    conv_id: String,
    mode: PermMode,
    pending_dir: PathBuf,
    ssh: SshSessions,
}

impl Lease {
    /// 给本轮开一个租约。非 ssh 连接返回 None（其它 kind 没有远程机器可跑）。
    /// `work_dir` 必须是本轮 cwd —— 租约目录建在它下面，codex 沙箱才写得进去。
    pub fn start(
        conn: &Connection,
        work_dir: &str,
        conv_id: &str,
        mode: PermMode,
        pending_dir: PathBuf,
        ssh: SshSessions,
    ) -> Option<Lease> {
        if conn.kind != "ssh" {
            return None;
        }
        // 目录名和令牌是**两个**随机数：目录躺在工作区里，谁都能 ls 到；
        // 令牌只在本轮 AI 子进程的 env 里。用同一个值就等于把令牌写在门牌上，
        // 别的对话（比如被网页注入的 CF 会话）扫一眼目录就能伪造请求打你的服务器。
        let slot = uuid::Uuid::new_v4().simple().to_string();
        let token = uuid::Uuid::new_v4().simple().to_string();
        let dir = Path::new(work_dir).join(".pilot-ssh").join(&slot);
        if let Err(e) = std::fs::create_dir_all(&dir) {
            eprintln!("[ssh-bridge] 建租约目录失败，AI 将无法执行远程命令: {e}");
            return None;
        }
        let stop = Arc::new(AtomicBool::new(false));
        let ctx = Ctx {
            dir: dir.clone(),
            token: token.clone(),
            stop: stop.clone(),
            conn: conn.clone(),
            conv_id: conv_id.to_string(),
            mode,
            pending_dir,
            ssh,
        };
        tokio::spawn(poll_loop(ctx));
        Some(Lease { dir, token, stop })
    }

    /// 注入给 AI 子进程的环境变量：脚本据此找到租约目录并证明自己是本轮的 AI。
    /// token 让**别的对话**（比如被网页注入的 CF 会话）没法伪造请求打你的服务器。
    pub fn env(&self) -> [(&'static str, String); 2] {
        [
            ("GCMS_SSH_DIR", self.dir.to_string_lossy().into_owned()),
            ("GCMS_SSH_TOKEN", self.token.clone()),
        ]
    }
}

impl Drop for Lease {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

/// 扫租约目录 → 抢到一条请求就丢给一个任务去办（并发：AI 可能同时发几条命令）。
async fn poll_loop(ctx: Ctx) {
    let ctx = Arc::new(ctx);
    loop {
        tokio::time::sleep(POLL).await;
        if ctx.stop.load(Ordering::Relaxed) {
            return;
        }
        let Ok(rd) = std::fs::read_dir(&ctx.dir) else { continue };
        for e in rd.flatten() {
            let p = e.path();
            let Some(id) = p
                .file_name()
                .and_then(|n| n.to_str())
                .and_then(|n| n.strip_suffix(".req.json"))
                .map(|s| s.to_string())
            else {
                continue;
            };
            // id 来自脚本写的文件名 → 当不可信输入消毒（要用来拼路径和批准卡 id）。
            if !permit::safe_id(&id) {
                let _ = std::fs::remove_file(&p);
                continue;
            }
            // 原子认领：rename 成功的那个才办。否则下一轮扫描会把同一条再办一遍（重复执行）。
            let busy = ctx.dir.join(format!("{id}.busy"));
            if std::fs::rename(&p, &busy).is_err() {
                continue;
            }
            let ctx2 = ctx.clone();
            tokio::spawn(async move { handle(ctx2, id, busy).await });
        }
    }
}

async fn handle(ctx: Arc<Ctx>, id: String, busy: PathBuf) {
    let body = match run_one(&ctx, &id, &busy).await {
        Ok(o) => serde_json::json!({
            "ok": true, "code": o.code, "stdout": o.stdout, "stderr": o.stderr, "truncated": o.truncated
        }),
        Err(e) => serde_json::json!({ "ok": false, "error": e }),
    };
    let resp = ctx.dir.join(format!("{id}.resp.json"));
    if let Ok(v) = serde_json::to_vec(&body) {
        let _ = write_atomic(&resp, &v);
    }
    let _ = std::fs::remove_file(&busy);
}

async fn run_one(ctx: &Ctx, id: &str, busy: &Path) -> Result<crate::ssh::ExecOut, String> {
    let raw = std::fs::read(busy).map_err(|e| format!("读请求失败: {e}"))?;
    let v: serde_json::Value = serde_json::from_slice(&raw).map_err(|e| format!("请求不是合法 JSON: {e}"))?;

    // 令牌不对 = 不是本轮的 AI 写的（别的对话/别的进程想借道）→ 直接拒。
    let token = v.get("token").and_then(|x| x.as_str()).unwrap_or_default();
    if token != ctx.token {
        return Err("令牌无效：这个对话没有这台机器的执行授权。".into());
    }
    let cmd = v.get("cmd").and_then(|x| x.as_str()).unwrap_or_default().trim().to_string();
    if cmd.is_empty() {
        return Err("命令是空的。".into());
    }
    let timeout = v
        .get("timeout")
        .and_then(|x| x.as_u64())
        .filter(|t| *t > 0)
        .unwrap_or(EXEC_TIMEOUT_DEFAULT)
        .min(EXEC_TIMEOUT_MAX);

    let alive = ctx.dir.join(format!("{id}.alive"));
    gate(ctx, id, &cmd, &alive).await?;

    // 批准之后、下发之前确认脚本还活着：用户可能盯着卡片想了 5 分钟，
    // 那边 AI 的 Bash 工具早超时放弃了 —— 这时候执行就是「没人要的命令偷偷跑」。
    still_wanted(ctx, &alive)?;

    let auth = crate::ssh::auth_for(&ctx.conn)?;
    let expect = (!ctx.conn.ssh_fingerprint.is_empty()).then(|| ctx.conn.ssh_fingerprint.clone());
    ctx.ssh
        .ensure(&ctx.conn.id, &ctx.conn.ssh_host, ctx.conn.ssh_port, &auth, expect)
        .await?;
    // ★ 再查一次，而且必须在这里查：ensure 可能刚跟一台连不上的机器耗了一分多钟
    //（TCP 连不上没有超时上限），这期间 AI 早放弃了、或者用户按了停止。
    // 「查完就下发」中间不能再插任何 await，否则窗口又开回来。
    still_wanted(ctx, &alive)?;
    ctx.ssh.exec(&ctx.conn.id, &cmd, timeout).await
}

/// 这条命令现在还有人要吗：回合没结束、脚本还在跳。任一为否 = 不执行（fail-closed）。
fn still_wanted(ctx: &Ctx, alive: &Path) -> Result<(), String> {
    if ctx.stop.load(Ordering::Relaxed) {
        return Err("回合已结束，命令未执行。".into());
    }
    if !alive_fresh(alive) {
        return Err("调用已放弃（AI 那侧的命令超时了），本条未执行。".into());
    }
    Ok(())
}

/// 档位闸：plan 不动手；full 直放；ask/auto **每条远程命令都弹卡**
/// （SSH 没有「本地」可言——每条都对线上生效，这正是 auto 档 DANGER 清单要拦的那一类）。
async fn gate(ctx: &Ctx, id: &str, cmd: &str, alive: &Path) -> Result<(), String> {
    match ctx.mode {
        PermMode::Full => return Ok(()),
        PermMode::Plan => {
            return Err("当前是「计划」档：只看不动手。要真正执行，请把权限档位切到「询问」或更高。".into())
        }
        PermMode::Ask | PermMode::Auto => {}
    }
    let card_id = format!("ssh-{id}");
    let card = serde_json::json!({
        "id": card_id,
        "conv": ctx.conv_id,
        "tool": "SSH",
        "cmd": cmd,
        "desc": "在远程服务器上执行命令",
        "arg": format!("{}@{}:{}", ctx.conn.ssh_user, ctx.conn.ssh_host, ctx.conn.ssh_port),
        "dangerous": permit::is_dangerous(cmd),
        "mode": ctx.mode.as_str(),
        "ts": now_ms() as u64,
    });
    let req = ctx.pending_dir.join(format!("{card_id}.req.json"));
    let resp = ctx.pending_dir.join(format!("{card_id}.resp.json"));
    std::fs::create_dir_all(&ctx.pending_dir).map_err(|e| format!("建待批目录失败: {e}"))?;
    write_atomic(&req, &serde_json::to_vec(&card).map_err(|e| e.to_string())?)
        .map_err(|e| format!("写批准请求失败: {e}"))?;

    let cleanup = || {
        let _ = std::fs::remove_file(&req);
        let _ = std::fs::remove_file(&resp);
    };
    let deadline = std::time::Instant::now() + APPROVE_TIMEOUT;
    loop {
        tokio::time::sleep(POLL).await;
        if ctx.stop.load(Ordering::Relaxed) {
            cleanup();
            return Err("回合已结束，命令未执行。".into());
        }
        // 脚本没心跳了 = AI 放弃了这次调用 → 顺手把卡片撤掉，别留幽灵卡给用户点。
        if !alive_fresh(alive) {
            cleanup();
            return Err("调用已放弃（AI 那侧的命令超时了），本条未执行。".into());
        }
        if let Ok(raw) = std::fs::read(&resp) {
            let allow = serde_json::from_slice::<serde_json::Value>(&raw)
                .ok()
                .and_then(|v| v.get("decision").and_then(|d| d.as_str()).map(|s| s == "allow"))
                .unwrap_or(false);
            cleanup();
            return if allow { Ok(()) } else { Err("用户拒绝了这条命令。".into()) };
        }
        // 请求卡被清了（回合收尾 sweep_conv / 用户停了这一轮）→ 没人会来批了。
        if !req.exists() {
            cleanup();
            return Err("批准请求已失效，命令未执行。".into());
        }
        if std::time::Instant::now() >= deadline {
            cleanup();
            return Err("15 分钟没人确认，已按拒绝处理。".into());
        }
    }
}

/// 心跳文件里是脚本写的毫秒时间戳（同机同钟，直接比）。文件不存在＝脚本已死
/// （脚本约定：先写心跳再写请求，所以我们看得到请求时心跳一定已经存在过）。
fn alive_fresh(alive: &Path) -> bool {
    let Ok(s) = std::fs::read_to_string(alive) else { return false };
    let Ok(ts) = s.trim().parse::<u128>() else { return false };
    now_ms().saturating_sub(ts) < ALIVE_STALE_MS
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp() -> PathBuf {
        let d = std::env::temp_dir().join(format!("bridge-t-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    fn ssh_conn() -> Connection {
        Connection {
            id: "c1".into(),
            name: "s".into(),
            kind: "ssh".into(),
            api_base: String::new(),
            skill_dir: String::new(),
            key_prefix: String::new(),
            key_kind: String::new(),
            account_id: String::new(),
            ssh_host: "h".into(),
            ssh_port: 22,
            ssh_user: "root".into(),
            ssh_auth: "password".into(),
            ssh_key_path: String::new(),
            ssh_fingerprint: "SHA256:x".into(),
            pack_version: String::new(),
            created_at: String::new(),
        }
    }

    #[test]
    fn lease_only_for_ssh_kind() {
        let d = tmp();
        let mut conn = ssh_conn();
        conn.kind = "cloudflare".into();
        // 非 ssh 连接不给租约（AI 拿不到任何远程执行能力）
        assert!(Lease::start(&conn, &d.to_string_lossy(), "cv", PermMode::Auto, d.join("p"), SshSessions::new()).is_none());
        std::fs::remove_dir_all(&d).ok();
    }

    #[test]
    fn alive_freshness() {
        let d = tmp();
        let a = d.join("x.alive");
        assert!(!alive_fresh(&a)); // 不存在 = 已死
        std::fs::write(&a, now_ms().to_string()).unwrap();
        assert!(alive_fresh(&a));
        std::fs::write(&a, (now_ms() - 10_000).to_string()).unwrap();
        assert!(!alive_fresh(&a)); // 10 秒前的心跳 = 已死
        std::fs::write(&a, "garbage").unwrap();
        assert!(!alive_fresh(&a));
        std::fs::remove_dir_all(&d).ok();
    }

    #[test]
    fn write_atomic_leaves_no_tmp() {
        let d = tmp();
        let p = d.join("a.resp.json");
        write_atomic(&p, b"{}").unwrap();
        assert_eq!(std::fs::read_to_string(&p).unwrap(), "{}");
        assert!(!d.join("a.resp.tmp").exists());
        std::fs::remove_dir_all(&d).ok();
    }

    // 租约的 env 契约：脚本靠这两个变量找目录 + 证明身份。
    #[tokio::test]
    async fn lease_env_and_drop_cleans_dir() {
        let d = tmp();
        let conn = ssh_conn();
        let lease = Lease::start(&conn, &d.to_string_lossy(), "cv", PermMode::Auto, d.join("p"), SshSessions::new()).unwrap();
        let env = lease.env();
        assert_eq!(env[0].0, "GCMS_SSH_DIR");
        assert_eq!(env[1].0, "GCMS_SSH_TOKEN");
        assert!(!env[1].1.is_empty());
        let dir = PathBuf::from(&env[0].1);
        assert!(dir.exists());
        drop(lease);
        assert!(!dir.exists(), "租约 Drop 必须删掉目录（令牌随之作废）");
        std::fs::remove_dir_all(&d).ok();
    }

    /// plan 档不执行：连闸都过不去。
    #[tokio::test]
    async fn plan_mode_refuses() {
        let d = tmp();
        let ctx = Ctx {
            dir: d.clone(),
            token: "t".into(),
            stop: Arc::new(AtomicBool::new(false)),
            conn: ssh_conn(),
            conv_id: "cv".into(),
            mode: PermMode::Plan,
            pending_dir: d.join("p"),
            ssh: SshSessions::new(),
        };
        let err = gate(&ctx, "s1", "ls", &d.join("s1.alive")).await.unwrap_err();
        assert!(err.contains("计划"), "{err}");
        std::fs::remove_dir_all(&d).ok();
    }

    /// full 档直放（用户明确选了全自动）。
    #[tokio::test]
    async fn full_mode_allows_without_card() {
        let d = tmp();
        let ctx = Ctx {
            dir: d.clone(),
            token: "t".into(),
            stop: Arc::new(AtomicBool::new(false)),
            conn: ssh_conn(),
            conv_id: "cv".into(),
            mode: PermMode::Full,
            pending_dir: d.join("p"),
            ssh: SshSessions::new(),
        };
        assert!(gate(&ctx, "s1", "rm -rf /", &d.join("s1.alive")).await.is_ok());
        assert!(!d.join("p").exists(), "full 档不该写批准卡");
        std::fs::remove_dir_all(&d).ok();
    }

    /// ask 档：写卡 → 用户批准 → 放行，且卡片文件被清干净（不留幽灵卡）。
    #[tokio::test]
    async fn ask_mode_cards_and_honors_approval() {
        let d = tmp();
        let pend = d.join("p");
        let alive = d.join("s1.alive");
        std::fs::write(&alive, now_ms().to_string()).unwrap();
        let ctx = Ctx {
            dir: d.clone(),
            token: "t".into(),
            stop: Arc::new(AtomicBool::new(false)),
            conn: ssh_conn(),
            conv_id: "cv".into(),
            mode: PermMode::Ask,
            pending_dir: pend.clone(),
            ssh: SshSessions::new(),
        };
        let h = tokio::spawn(async move { gate(&ctx, "s1", "apt install nginx", &alive).await });
        // 等卡片出现
        let card = pend.join("ssh-s1.req.json");
        for _ in 0..50 {
            if card.exists() { break }
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        assert!(card.exists(), "ask 档必须弹卡");
        let v: serde_json::Value = serde_json::from_slice(&std::fs::read(&card).unwrap()).unwrap();
        assert_eq!(v["tool"], "SSH");
        assert_eq!(v["cmd"], "apt install nginx");
        assert_eq!(v["arg"], "root@h:22");
        permit::respond(&pend, "ssh-s1", true).unwrap();
        assert!(h.await.unwrap().is_ok(), "批准后应放行");
        assert!(!card.exists(), "批准后要清掉卡片");
        std::fs::remove_dir_all(&d).ok();
    }

    /// 拒绝 → 报错（AI 收到「用户拒绝」，不会以为跑成功了）。
    #[tokio::test]
    async fn ask_mode_honors_denial() {
        let d = tmp();
        let pend = d.join("p");
        let alive = d.join("s2.alive");
        std::fs::write(&alive, now_ms().to_string()).unwrap();
        let ctx = Ctx {
            dir: d.clone(), token: "t".into(), stop: Arc::new(AtomicBool::new(false)),
            conn: ssh_conn(), conv_id: "cv".into(), mode: PermMode::Ask,
            pending_dir: pend.clone(), ssh: SshSessions::new(),
        };
        let h = tokio::spawn(async move { gate(&ctx, "s2", "rm -rf /", &alive).await });
        let card = pend.join("ssh-s2.req.json");
        for _ in 0..50 {
            if card.exists() { break }
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        // 危险命令要标红
        let v: serde_json::Value = serde_json::from_slice(&std::fs::read(&card).unwrap()).unwrap();
        assert_eq!(v["dangerous"], true);
        permit::respond(&pend, "ssh-s2", false).unwrap();
        assert!(h.await.unwrap().unwrap_err().contains("拒绝"));
        std::fs::remove_dir_all(&d).ok();
    }

    /// ★ 孤儿执行防线：脚本心跳停了（AI 的 Bash 超时被杀）→ 即使卡片还在也不执行，卡片自撤。
    #[tokio::test]
    async fn dead_script_cancels_card_and_refuses() {
        let d = tmp();
        let pend = d.join("p");
        let alive = d.join("s3.alive");
        std::fs::write(&alive, (now_ms() - 60_000).to_string()).unwrap(); // 早就没心跳了
        let ctx = Ctx {
            dir: d.clone(), token: "t".into(), stop: Arc::new(AtomicBool::new(false)),
            conn: ssh_conn(), conv_id: "cv".into(), mode: PermMode::Ask,
            pending_dir: pend.clone(), ssh: SshSessions::new(),
        };
        let err = gate(&ctx, "s3", "reboot", &alive).await.unwrap_err();
        assert!(err.contains("放弃"), "{err}");
        assert!(!pend.join("ssh-s3.req.json").exists(), "脚本死了就该撤卡");
        std::fs::remove_dir_all(&d).ok();
    }

    /// ★ 端到端：真的用 node 跑随附的 ssh.js，走完整往返（脚本写请求 → 轮询任务认领 →
    /// 令牌校验 → 闸 → 打到 SSH 层 → 结果写回 → 脚本打印 JSON）。
    /// 这里没有真服务器，所以终点必然是连接失败——但那恰好证明**整条链路是通的**
    /// （错误来自 ssh 层，不是令牌/协议）。手动跑：cargo test -- --ignored bridge_roundtrip
    #[tokio::test]
    #[ignore]
    async fn bridge_roundtrip_with_real_node() {
        let base = tmp();
        let ssh_js = crate::tools::ensure_ssh(&base).unwrap();
        let work = base.join("work");
        std::fs::create_dir_all(&work).unwrap();
        // full 档：不弹卡，直奔执行（本测试要验的是链路，不是闸——闸另有单测）
        let lease = Lease::start(
            &ssh_conn(), &work.to_string_lossy(), "cv", PermMode::Full, base.join("pending"), SshSessions::new(),
        )
        .unwrap();
        let env = lease.env();
        let out = tokio::task::spawn_blocking(move || {
            std::process::Command::new("node")
                .arg(&ssh_js)
                .arg("echo hi")
                .env(env[0].0, &env[0].1)
                .env(env[1].0, &env[1].1)
                .output()
                .expect("run node")
        })
        .await
        .unwrap();
        let s = String::from_utf8_lossy(&out.stdout);
        let v: serde_json::Value = serde_json::from_str(s.trim()).unwrap_or_else(|e| panic!("脚本没输出合法 JSON: {s} ({e})"));
        assert_eq!(v["ok"], false, "没有真服务器，应当失败: {s}");
        let err = v["error"].as_str().unwrap_or("");
        // 令牌/协议若有问题会是这两条——出现即说明链路断在 Pilot 侧而不是 SSH 侧
        assert!(!err.contains("令牌无效"), "令牌契约断了: {err}");
        assert!(!err.contains("这个对话没有连接远程机器"), "env 契约断了: {err}");
        assert!(!err.is_empty());
        drop(lease);
        std::fs::remove_dir_all(&base).ok();
    }

    /// ★ 令牌不能等于目录名：目录躺在工作区里谁都 ls 得到，令牌只在 env 里。
    #[tokio::test]
    async fn token_is_not_the_directory_name() {
        let d = tmp();
        let lease = Lease::start(
            &ssh_conn(), &d.to_string_lossy(), "cv", PermMode::Auto, d.join("p"), SshSessions::new(),
        )
        .unwrap();
        let env = lease.env();
        let dir_name = std::path::Path::new(&env[0].1).file_name().unwrap().to_string_lossy().into_owned();
        assert_ne!(dir_name, env[1].1, "令牌等于目录名＝把令牌写在门牌上");
        std::fs::remove_dir_all(&d).ok();
    }

    /// ★ 孤儿执行防线之二：回合已结束（Lease 被 Drop / 用户按了停止）→ 即使批准过也不下发。
    #[tokio::test]
    async fn stopped_turn_refuses_before_exec() {
        let d = tmp();
        let alive = d.join("s5.alive");
        std::fs::write(&alive, now_ms().to_string()).unwrap();
        let ctx = Ctx {
            dir: d.clone(), token: "t".into(), stop: Arc::new(AtomicBool::new(true)), // 已停
            conn: ssh_conn(), conv_id: "cv".into(), mode: PermMode::Full,
            pending_dir: d.join("p"), ssh: SshSessions::new(),
        };
        let err = still_wanted(&ctx, &alive).unwrap_err();
        assert!(err.contains("回合已结束"), "{err}");
        std::fs::remove_dir_all(&d).ok();
    }

    /// 令牌不对 = 别的对话/别的进程伪造请求 → 拒。
    #[tokio::test]
    async fn wrong_token_refused() {
        let d = tmp();
        let ctx = Ctx {
            dir: d.clone(), token: "real".into(), stop: Arc::new(AtomicBool::new(false)),
            conn: ssh_conn(), conv_id: "cv".into(), mode: PermMode::Full,
            pending_dir: d.join("p"), ssh: SshSessions::new(),
        };
        let busy = d.join("s4.busy");
        std::fs::write(&busy, r#"{"token":"stolen","cmd":"whoami"}"#).unwrap();
        let err = run_one(&ctx, "s4", &busy).await.unwrap_err();
        assert!(err.contains("令牌无效"), "{err}");
        std::fs::remove_dir_all(&d).ok();
    }
}
