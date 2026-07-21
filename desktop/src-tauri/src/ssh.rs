//! SSH 远程连接（P1 地基）：russh 客户端 + 密码/密钥双认证 + TOFU 主机指纹 + PTY 交互式 shell。
//!
//! 安全边界（改这个文件前先读）：
//! - 密码 / 密钥口令只在内存里传递，源头是钥匙串；**绝不落盘、绝不进 AI 子进程的环境变量**。
//!   （对比 gcms/CF：那两家把 api key 塞进子进程 env 是可接受的，SSH 口令给一个 shell 就不行。）
//! - 主机密钥 **TOFU**：首连时 `expect=None`，接受并把指纹交给 UI 让用户确认；此后每次连接
//!   都带上已确认的指纹，**不匹配直接拒绝**（防中间人）。绝不做「无条件 Ok(true)」的省事写法。
//! - PTY 原始字节可能不是合法 UTF-8（也含 ANSI 转义），一律 base64 传给前端，由 xterm 解码写入，
//!   避免半个多字节字符把 JSON 序列化炸掉。

use std::collections::HashMap;
use std::sync::{Arc, Mutex as StdMutex};
use std::time::Duration;

use russh::client;
use russh::keys::{load_secret_key, PrivateKeyWithHashAlg};
use russh::ChannelMsg;
use russh_sftp::client::SftpSession;
use serde::Serialize;
use tauri::ipc::Channel;
use tokio::sync::Mutex;

use base64::Engine as _;

/// PTY 会话流事件（前端 xterm 消费）。
#[derive(Clone, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum SshEvent {
    /// PTY 输出：base64 编码的原始字节。
    Data { b64: String },
    /// 会话结束（正常退出 error 为空串）。
    Closed { error: String },
}

/// 试连结果：把主机指纹交给 UI 做 TOFU 确认，并报告认证是否通过。
#[derive(Clone, Serialize)]
pub struct SshProbe {
    /// SHA256:... 形式的主机公钥指纹。
    pub fingerprint: String,
    /// 认证是否成功（指纹拿到但认证失败时为 false，UI 据此提示是密码/密钥的问题）。
    pub auth_ok: bool,
    /// 认证失败原因（auth_ok=true 时为空串）。
    pub error: String,
}

/// 远程目录里的一项。
#[derive(Clone, Serialize)]
pub struct SftpEntry {
    pub name: String,
    pub dir: bool,
    /// 符号链接。注意 sftp 的 readdir 给的是 lstat 语义 → 指向目录的软链这里 dir=false，
    /// 前端点开时再解析（省掉每次列目录都对每个软链多跑一次 stat）。
    pub link: bool,
    /// "rwxr-xr-x" 形式的权限（拿不到权限位时为空串）。
    pub perms: String,
    pub size: u64,
    pub mtime: u64,
}

/// 权限位 → "rwxr-xr-x"。只取低 9 位；setuid/setgid/sticky 按 ls 的写法叠在执行位上。
fn perm_str(mode: u32) -> String {
    let mut s = String::with_capacity(9);
    for (i, group) in [(mode >> 6) & 7, (mode >> 3) & 7, mode & 7]
        .iter()
        .enumerate()
    {
        s.push(if group & 4 != 0 { 'r' } else { '-' });
        s.push(if group & 2 != 0 { 'w' } else { '-' });
        // setuid(04000)/setgid(02000)/sticky(01000) 各自改写对应组的执行位
        let special = match i {
            0 => mode & 0o4000 != 0,
            1 => mode & 0o2000 != 0,
            _ => mode & 0o1000 != 0,
        };
        let x = group & 1 != 0;
        s.push(match (special, x, i) {
            (true, true, 2) => 't',
            (true, false, 2) => 'T',
            (true, true, _) => 's',
            (true, false, _) => 'S',
            (false, true, _) => 'x',
            (false, false, _) => '-',
        });
    }
    s
}

/// 删除目录前的最低限度路径保护。远端路径始终按 POSIX 规则处理，不能用宿主机的
/// `std::path::Path`（Pilot 在 Windows 上运行时会套用 Windows 路径语义）。
fn validate_remove_dir_path(path: &str) -> Result<(), String> {
    let path = path.trim();
    if path.is_empty() || path.trim_matches('/').is_empty() {
        return Err("拒绝删除远端根目录".to_string());
    }
    if path.split('/').any(|part| matches!(part, "." | "..")) {
        return Err("删除路径不能包含 . 或 ..".to_string());
    }
    Ok(())
}

/// 把任意远端路径安全地放进 POSIX shell 单引号。删除目录会在服务器上执行一次
/// `rm -rf`，避免 SFTP 每删一个文件都跨网络往返。
fn posix_shell_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\"'\"'"))
}

/// 由「已存的连接 + 钥匙串」组装认证材料。凭据只在 Rust 侧流转，绝不出这个进程。
/// 无口令私钥不会有钥匙串条目 —— get_key 失败按「无口令」处理，不是错误。
pub fn auth_for(conn: &crate::pack::Connection) -> Result<SshAuth, String> {
    let secret = crate::keychain::get_key(&conn.id).ok();
    Ok(if conn.ssh_auth == "key" {
        SshAuth {
            user: conn.ssh_user.clone(),
            password: None,
            key_path: Some(conn.ssh_key_path.clone()),
            key_pass: secret.filter(|s| !s.is_empty()),
        }
    } else {
        SshAuth {
            user: conn.ssh_user.clone(),
            password: Some(secret.ok_or("钥匙串里没有这个连接的密码，请删除后重新添加")?),
            key_path: None,
            key_pass: None,
        }
    })
}

/// 一次连接所需的认证材料。调用方从钥匙串取出后构造，用完即弃。
#[derive(Clone)]
pub struct SshAuth {
    pub user: String,
    /// 密码认证（与 key_path 二选一）。
    pub password: Option<String>,
    /// 私钥文件路径（只存路径引用，不把用户私钥拷进我们的目录）。
    pub key_path: Option<String>,
    /// 私钥口令（加密私钥才需要）。
    pub key_pass: Option<String>,
}

/// TOFU 主机密钥校验：expect=None 表示探测模式（接受并记录指纹）。
struct Client {
    expect: Option<String>,
    seen: Arc<StdMutex<String>>,
}

impl client::Handler for Client {
    type Error = russh::Error;

    async fn check_server_key(
        &mut self,
        server_public_key: &russh::keys::ssh_key::PublicKey,
    ) -> Result<bool, Self::Error> {
        let fp = server_public_key
            .fingerprint(Default::default())
            .to_string();
        if let Ok(mut g) = self.seen.lock() {
            *g = fp.clone();
        }
        match &self.expect {
            // 探测模式：接受并把指纹交给 UI，由用户确认后才会被存下来。
            None => Ok(true),
            // 已确认过的主机：指纹必须一致，否则可能是中间人 —— 直接拒。
            Some(e) => Ok(e.as_str() == fp),
        }
    }
}

fn cfg() -> Arc<client::Config> {
    Arc::new(client::Config {
        // 交互式 shell 会长时间没流量（比如你盯着 top 看），别让库把连接掐了。
        inactivity_timeout: None,
        keepalive_interval: Some(Duration::from_secs(30)),
        // ★ 关掉 Nagle。russh 默认是**开着**的（它自己的注释：「disabled by default (i.e.
        // Nagle's algorithm is active)」）——那对交互式终端是灾难：每次敲键就几个字节，
        // Nagle 会把小包攒起来等上一个包的 ACK 回来才发，于是每键都可能多压最多 40ms。
        // 所有 ssh 客户端在交互式会话下都开 TCP_NODELAY，这不是可选项。
        nodelay: true,
        ..Default::default()
    })
}

/// 建立已认证的会话。expect=Some(指纹) 时做 TOFU 校验。
/// 返回 (handle, 实际看到的指纹)。
async fn connect_auth(
    host: &str,
    port: u16,
    auth: &SshAuth,
    expect: Option<String>,
) -> Result<(client::Handle<Client>, String), String> {
    let seen = Arc::new(StdMutex::new(String::new()));
    let handler = Client {
        expect: expect.clone(),
        seen: seen.clone(),
    };
    let mut session = client::connect(cfg(), (host, port), handler)
        .await
        .map_err(|e| {
            // 指纹对不上时 russh 报的是通用错误，这里补一句人话（这是安全事件，不能糊弄过去）。
            if expect.is_some() {
                format!("连接失败（若主机指纹变了则可能是中间人攻击，请核实后重新添加）: {e}")
            } else {
                format!("连接 {host}:{port} 失败: {e}")
            }
        })?;
    let fp = seen.lock().map(|g| g.clone()).unwrap_or_default();

    let ok = if let Some(pw) = auth.password.as_deref() {
        session
            .authenticate_password(auth.user.clone(), pw.to_string())
            .await
            .map_err(|e| format!("密码认证出错: {e}"))?
            .success()
    } else if let Some(kp) = auth.key_path.as_deref() {
        let key = load_secret_key(kp, auth.key_pass.as_deref())
            .map_err(|e| format!("读取私钥 {kp} 失败（口令不对？）: {e}"))?;
        let hash = session
            .best_supported_rsa_hash()
            .await
            .map_err(|e| format!("协商 RSA 签名算法失败: {e}"))?
            .flatten();
        session
            .authenticate_publickey(
                auth.user.clone(),
                PrivateKeyWithHashAlg::new(Arc::new(key), hash),
            )
            .await
            .map_err(|e| format!("密钥认证出错: {e}"))?
            .success()
    } else {
        return Err("没有提供密码或私钥".into());
    };
    if !ok {
        return Err("认证失败：用户名/密码或私钥不对".into());
    }
    Ok((session, fp))
}

/// 试连：拿主机指纹 + 验证认证是否可用。首次添加连接时用（expect=None 走 TOFU 探测）。
/// 注意：指纹即使认证失败也会返回，UI 才能区分「指纹要确认」和「密码错了」。
pub async fn probe(
    host: &str,
    port: u16,
    auth: &SshAuth,
    expect: Option<String>,
) -> Result<SshProbe, String> {
    match connect_auth(host, port, auth, expect).await {
        Ok((session, fp)) => {
            // 探测完立刻断开，不留连接。
            let _ = session
                .disconnect(russh::Disconnect::ByApplication, "probe done", "")
                .await;
            Ok(SshProbe {
                fingerprint: fp,
                auth_ok: true,
                error: String::new(),
            })
        }
        Err(e) => Err(e),
    }
}

/// 一条活着的 SSH 连接：一个 handle + 其上的若干通道（PTY / SFTP / exec）。
///
/// 为什么必须留着 `handle`：**开新通道只能通过它**（SFTP 要在同一条连接上再开一路，
/// 否则每次文件操作都要重新握手认证）。它本身也持有 sender，只要它在表里活着，
/// 会话任务就不退出（`Handle::drop` 不 abort，其 JoinHandle 被 drop 在 tokio 里是分离而非杀死）
/// —— 所以**没有 PTY 也能维持连接**，AI 桥/SFTP 不必先开终端。
struct Live {
    /// Arc 是刻意的：开通道要 await 服务端确认（机器失联时能挂满 TCP 超时），
    /// 所以必须**在锁里克隆、出了锁再 await** —— 否则一条连接卡住会连坐整张会话表
    /// （连能救场的「关闭/重连」都拿不到锁）。
    handle: Arc<client::Handle<Client>>,
    /// PTY 通道写半边（读半边在 open_shell 里交给后台任务流给前端；Channel 不能克隆，只能拆两半）。
    /// None = 这条连接上还没开终端（AI 桥或 SFTP 建的）。
    /// Arc 同 handle 的理由：写一次要 await，**每敲一个键都攥着整张会话表**太蠢 ——
    /// 锁里克隆、出了锁再 await。
    ch: Option<Arc<russh::ChannelWriteHalf<client::Msg>>>,
    /// 前端终端的事件通道。留一份克隆，AI 桥就能把「AI 正在跑什么」推到同一个终端里给人看见
    /// （复用 PTY 那条现成的通道，不必另起一套事件系统）。None = 没开终端，AI 干活就不显示。
    on_event: Option<Channel<SshEvent>>,
    /// 当前 PTY 的世代号。PTY 读任务退出时只有「自己仍是当前世代」才动表 ——
    /// 否则重连时旧任务的收尾会把刚建好的新会话删掉（旧代码有这个竞态，靠 connect 的网络延时侥幸不中）。
    pty_gen: u64,
}

/// 远端负载快照（顶栏那三个数）。
///
/// CPU 给的是 **/proc/stat 的累计计数**、不是百分比 —— 那玩意儿是开机以来的累计时间片，
/// 单次读没有意义，必须两次采样求差。差值在前端算（它本来就按连接持有上一次的样本，
/// 后端不必再维护一张「上次读数」的表）。
#[derive(Clone, Serialize, Debug, PartialEq)]
pub struct SshStats {
    /// 所有 CPU 时间片之和（user+nice+system+idle+iowait+…）
    pub cpu_total: u64,
    /// 空闲时间片（idle + iowait）
    pub cpu_idle: u64,
    pub mem_total_kb: u64,
    pub mem_avail_kb: u64,
    pub disk_total_kb: u64,
    pub disk_used_kb: u64,
}

/// 一条命令拿齐三个数。`df -Pk /` 里 -P 是 POSIX 输出（别让长设备名把行折断）、
/// -k 固定 1K 块（有些系统默认 512）。
const STATS_CMD: &str = "head -1 /proc/stat; grep -E '^(MemTotal|MemAvailable|MemFree):' /proc/meminfo; df -Pk / | tail -1";

/// 解析 STATS_CMD 的输出。非 Linux（没有 /proc）会缺行 → Err，UI 就不显示这三个数。
fn parse_stats(s: &str) -> Result<SshStats, String> {
    let mut cpu_total = 0u64;
    let mut cpu_idle = 0u64;
    let (mut mem_total, mut mem_avail, mut mem_free) = (0u64, 0u64, 0u64);
    let (mut disk_total, mut disk_used) = (0u64, 0u64);
    let mut seen_cpu = false;
    let mut seen_df = false;

    for line in s.lines() {
        let f: Vec<&str> = line.split_whitespace().collect();
        if f.first() == Some(&"cpu") && !seen_cpu {
            // cpu user nice system idle iowait irq softirq steal guest guest_nice
            let n: Vec<u64> = f[1..].iter().filter_map(|x| x.parse().ok()).collect();
            if n.len() < 5 {
                continue;
            }
            cpu_total = n.iter().sum();
            cpu_idle = n[3] + n[4]; // idle + iowait
            seen_cpu = true;
        } else if let Some(v) = line.strip_prefix("MemTotal:") {
            mem_total = v
                .split_whitespace()
                .next()
                .and_then(|x| x.parse().ok())
                .unwrap_or(0);
        } else if let Some(v) = line.strip_prefix("MemAvailable:") {
            mem_avail = v
                .split_whitespace()
                .next()
                .and_then(|x| x.parse().ok())
                .unwrap_or(0);
        } else if let Some(v) = line.strip_prefix("MemFree:") {
            mem_free = v
                .split_whitespace()
                .next()
                .and_then(|x| x.parse().ok())
                .unwrap_or(0);
        } else if f.len() >= 6 && !seen_df {
            // df: 文件系统 1K块 已用 可用 容量 挂载点 —— 认「最后一列是 /」这一行
            if f[f.len() - 1] == "/" {
                disk_total = f[f.len() - 5].parse().unwrap_or(0);
                disk_used = f[f.len() - 4].parse().unwrap_or(0);
                seen_df = true;
            }
        }
    }
    if !seen_cpu || mem_total == 0 || !seen_df {
        return Err("读不到远端负载（这台机器可能不是 Linux，或没有 /proc）".into());
    }
    Ok(SshStats {
        cpu_total,
        cpu_idle,
        mem_total_kb: mem_total,
        // 老内核（<3.14）没有 MemAvailable，退回 MemFree —— 偏保守但总比没有强。
        mem_avail_kb: if mem_avail > 0 { mem_avail } else { mem_free },
        disk_total_kb: disk_total,
        disk_used_kb: disk_used,
    })
}

/// 一次性命令的执行结果（AI 桥用）。
#[derive(Clone, Serialize, Debug)]
pub struct ExecOut {
    pub stdout: String,
    pub stderr: String,
    /// 远端退出码；拿不到（通道异常关闭）时为 -1。
    pub code: i32,
    /// 输出是否因超过上限被截断。
    pub truncated: bool,
}

/// 单次 exec 收集的输出上限：`cat 大日志` 之类不能把内存吸爆，AI 也读不完。
const EXEC_OUT_MAX: usize = 256 * 1024;

/// 裸 `\n` → `\r\n`（只给终端回显用；喂给 AI 的输出保持原样）。
///
/// **为什么必须做**：exec 开的是不带 PTY 的通道，输出里的换行就是裸 `\n`。
/// 交互式 shell 那条路之所以正常，是因为 PTY 的行规程（ONLCR）替它把 `\n` 补成了 `\r\n`。
/// 不补就是「阶梯状」：`\n` 只下移一行、不回到行首，于是每行都从上一行的结尾处开始。
///
/// `last_cr` 跨块保持：`\r\n` 有可能正好被拆在两块数据里（`\r` 在这块尾、`\n` 在下块头），
/// 只看本块会把它当成裸 `\n` 而多补一个 `\r`。
fn to_crlf(data: &[u8], last_cr: &mut bool) -> Vec<u8> {
    let mut out = Vec::with_capacity(data.len() + 16);
    for &b in data {
        if b == b'\n' && !*last_cr {
            out.push(b'\r');
        }
        out.push(b);
        *last_cr = b == b'\r';
    }
    out
}

/// 往缓冲追加、到上限就停（并标记截断）。stdout/stderr 各自独立计上限。
fn push_capped(buf: &mut Vec<u8>, data: &[u8], truncated: &mut bool) {
    if buf.len() >= EXEC_OUT_MAX {
        *truncated = true;
        return;
    }
    let room = EXEC_OUT_MAX - buf.len();
    if data.len() > room {
        buf.extend_from_slice(&data[..room]);
        *truncated = true;
    } else {
        buf.extend_from_slice(data);
    }
}

/// 按 conn_id 索引的活跃 SSH 会话表。
#[derive(Clone, Default)]
pub struct SshSessions {
    map: Arc<Mutex<HashMap<String, Live>>>,
    /// PTY 世代号发号器：见 Live.pty_gen。
    gen: Arc<std::sync::atomic::AtomicU64>,
}

impl SshSessions {
    pub fn new() -> Self {
        Self::default()
    }

    /// 确保这条连接有一条已认证的会话（没有就建，**不开 PTY**）。
    /// 给 AI 桥/SFTP 用：用户没开过终端也能跑命令、管文件。
    pub async fn ensure(
        &self,
        conn_id: &str,
        host: &str,
        port: u16,
        auth: &SshAuth,
        expect: Option<String>,
    ) -> Result<(), String> {
        {
            let g = self.map.lock().await;
            // 已在表里但连接其实已断（网线拔了/服务端重启）→ 当作没有，下面重建。
            if let Some(live) = g.get(conn_id) {
                if !live.handle.is_closed() {
                    return Ok(());
                }
            }
        } // 先放锁：connect_auth 要 await 很久（网络 + 认证）
        let (session, _fp) = connect_auth(host, port, auth, expect).await?;
        let mut g = self.map.lock().await;
        // 建连期间别人可能已经建好了（两个 exec 同时进来）——那就用它的，扔掉自己这条。
        match g.get(conn_id) {
            Some(live) if !live.handle.is_closed() => {}
            _ => {
                g.insert(
                    conn_id.to_string(),
                    Live {
                        handle: Arc::new(session),
                        ch: None,
                        on_event: None,
                        pty_gen: 0,
                    },
                );
            }
        }
        Ok(())
    }

    /// 开一个交互式 PTY shell，把输出流给前端；同一 conn_id 已有终端时先关掉旧的。
    /// 已有无终端会话（AI 桥/SFTP 建的）时**复用**它，不重新握手。
    #[allow(clippy::too_many_arguments)]
    pub async fn open_shell(
        &self,
        conn_id: &str,
        host: &str,
        port: u16,
        auth: &SshAuth,
        expect: Option<String>,
        cols: u32,
        rows: u32,
        on_event: Channel<SshEvent>,
    ) -> Result<(), String> {
        // 关掉可能存在的旧 PTY（保留连接本身），再确保连接可用。
        self.close_pty(conn_id).await;
        self.ensure(conn_id, host, port, auth, expect).await?;
        // 锁里只做克隆，开通道在锁外 await（见 Live.handle 的注释）。
        let handle = {
            let g = self.map.lock().await;
            g.get(conn_id).ok_or("会话未打开")?.handle.clone()
        };
        let ch = handle
            .channel_open_session()
            .await
            .map_err(|e| format!("打开会话通道失败: {e}"))?;
        ch.request_pty(false, "xterm-256color", cols, rows, 0, 0, &[])
            .await
            .map_err(|e| format!("申请 PTY 失败: {e}"))?;
        ch.request_shell(false)
            .await
            .map_err(|e| format!("启动 shell 失败: {e}"))?;

        // 读端交给后台任务；写端（Live.ch）留在表里给 input/resize 用。
        // russh 的 Channel 不能克隆，这里把它拆成 reader/writer 两半。
        let (mut reader, writer) = ch.split();
        let my_gen = self.gen.fetch_add(1, std::sync::atomic::Ordering::Relaxed) + 1;
        match self.map.lock().await.get_mut(conn_id) {
            Some(live) => {
                live.ch = Some(Arc::new(writer));
                live.on_event = Some(on_event.clone()); // 留一份给 AI 桥回显（见 Live.on_event）
                live.pty_gen = my_gen;
            }
            // 刚 ensure 完就没了（用户同时点了关闭）：让调用方重来，别把半个会话塞回表里。
            None => return Err("会话已关闭，请重试".into()),
        }

        let map = self.map.clone();
        let id = conn_id.to_string();
        tokio::spawn(async move {
            let b64 = base64::engine::general_purpose::STANDARD;
            loop {
                match reader.wait().await {
                    Some(ChannelMsg::Data { data }) => {
                        let _ = on_event.send(SshEvent::Data {
                            b64: b64.encode(&data[..]),
                        });
                    }
                    // stderr 也直接进终端（交互式 shell 本来就混在一起显示）。
                    Some(ChannelMsg::ExtendedData { data, .. }) => {
                        let _ = on_event.send(SshEvent::Data {
                            b64: b64.encode(&data[..]),
                        });
                    }
                    Some(ChannelMsg::ExitStatus { .. }) | Some(ChannelMsg::Eof) => {}
                    Some(_) => {}
                    None => break, // 通道关闭
                }
            }
            // 终端结束（用户敲了 exit / 网络断）＝这条连接的使命结束，整条撤掉；
            // AI 桥下次 exec 会自己 ensure 重连。**只有自己还是当前世代才动表**（见 Live.pty_gen）。
            {
                let mut g = map.lock().await;
                if g.get(&id).is_some_and(|l| l.pty_gen == my_gen) {
                    g.remove(&id);
                }
            }
            let _ = on_event.send(SshEvent::Closed {
                error: String::new(),
            });
        });
        Ok(())
    }

    /// 只关掉 PTY 通道，保留连接本身（重开终端时用；AI 桥/SFTP 不受影响）。
    async fn close_pty(&self, conn_id: &str) {
        let ch = {
            let mut g = self.map.lock().await;
            match g.get_mut(conn_id) {
                Some(live) => {
                    // 换代：旧读任务醒来时发现自己不是当前世代，就不会去删表。
                    live.pty_gen = self.gen.fetch_add(1, std::sync::atomic::Ordering::Relaxed) + 1;
                    live.on_event = None; // 终端要关了，AI 回显没地方去
                    live.ch.take()
                }
                None => None,
            }
        };
        if let Some(ch) = ch {
            let _ = ch.close().await;
        }
    }

    /// 把键盘输入写进 PTY。
    pub async fn input(&self, conn_id: &str, data: &[u8]) -> Result<(), String> {
        let ch = self.pty(conn_id).await?;
        ch.data(data).await.map_err(|e| format!("写入失败: {e}"))
    }

    /// PTY 写半边（锁里克隆，锁外用）。见 Live.ch。
    async fn pty(
        &self,
        conn_id: &str,
    ) -> Result<Arc<russh::ChannelWriteHalf<client::Msg>>, String> {
        let g = self.map.lock().await;
        let live = g.get(conn_id).ok_or("会话未打开")?;
        live.ch.clone().ok_or_else(|| "终端未打开".to_string())
    }

    /// 终端尺寸变化 → 通知远端重排（vim/top 才不会花屏）。
    pub async fn resize(&self, conn_id: &str, cols: u32, rows: u32) -> Result<(), String> {
        let ch = self.pty(conn_id).await?;
        ch.window_change(cols, rows, 0, 0)
            .await
            .map_err(|e| format!("改窗口大小失败: {e}"))
    }

    /// 关闭整条连接（幂等）。
    pub async fn close(&self, conn_id: &str) {
        if let Some(live) = self.map.lock().await.remove(conn_id) {
            if let Some(ch) = live.ch {
                let _ = ch.close().await;
            }
        }
    }

    #[allow(dead_code)]
    /// 会话在表里**且**底层连接还活着（网线拔了/服务端重启后 handle 会 is_closed 但仍留在表里）。
    /// ensure_ssh 用它做快速路径：活着就跳过重连、也不去读钥匙串；死了或没有就交给 ensure 重建。
    pub async fn is_open(&self, conn_id: &str) -> bool {
        self.map
            .lock()
            .await
            .get(conn_id)
            .map(|l| !l.handle.is_closed())
            .unwrap_or(false)
    }

    /// 取一次远端负载。**故意不 ensure**：没有现成会话就直接失败，不为了顶栏那三个数
    /// 偷偷去登一次服务器（多一条登录记录、还可能把用户刚关掉的连接又拉起来）。
    /// echo=false：这是后台探测，不能拿它污染终端。
    pub async fn stats(&self, conn_id: &str) -> Result<SshStats, String> {
        let out = self.exec(conn_id, STATS_CMD, 15, false).await?;
        parse_stats(&out.stdout)
    }

    /// 前端终端的事件通道（克隆一份出来用，别攥着表锁）。没开终端就是 None。
    ///
    /// **回显只能走它**：exec 一开始按 echo 参数取一次，之后所有往终端写字的地方都用那份
    /// （包括收尾的「▸ 完成」）。别再写「用 conn_id 现查通道」的辅助函数 —— 那会绕过 echo 开关，
    /// 后台探测就会每 5 秒往终端吐一行（真出过这个 bug）。
    async fn sink(&self, conn_id: &str) -> Option<Channel<SshEvent>> {
        self.map
            .lock()
            .await
            .get(conn_id)
            .and_then(|l| l.on_event.clone())
    }

    /// 跑一条一次性命令（AI 桥用）。每次开一路新通道 → **每条命令是独立的一次性 shell**
    /// （无 PTY，`cd` 不跨命令保留，交互式提示符也没法应答）。
    ///
    /// `echo_to_term`＝把命令和输出实时回显进用户的终端窗口，让人看得见 AI 在干什么。
    /// 系统探测那种后台调用传 false（别拿噪音污染终端）。
    pub async fn exec(
        &self,
        conn_id: &str,
        cmd: &str,
        timeout_secs: u64,
        echo_to_term: bool,
    ) -> Result<ExecOut, String> {
        // 通道在循环外取一次：每来一块数据都去抢表锁就太蠢了。
        let sink = if echo_to_term {
            self.sink(conn_id).await
        } else {
            None
        };
        if let Some(s) = &sink {
            // 醒目但克制：一行提示 + 命令原文，让人一眼看出这行不是自己敲的。
            let head = format!("\r\n\x1b[38;5;110m▸ AI\x1b[0m \x1b[1m{cmd}\x1b[0m\r\n");
            let _ = s.send(SshEvent::Data {
                b64: base64::engine::general_purpose::STANDARD.encode(&head),
            });
        }
        self.exec_inner(conn_id, cmd, timeout_secs, sink, None)
            .await
    }

    /// 跑一条需要标准输入的一次性命令。标准输入不会拼进命令文本，也不会回显到终端；
    /// 适合传递密码等短暂敏感数据。发送后立即关闭 stdin，让远端程序能可靠读到 EOF。
    pub async fn exec_with_stdin(
        &self,
        conn_id: &str,
        cmd: &str,
        stdin: &[u8],
        timeout_secs: u64,
    ) -> Result<ExecOut, String> {
        self.exec_inner(conn_id, cmd, timeout_secs, None, Some(stdin))
            .await
    }

    async fn exec_inner(
        &self,
        conn_id: &str,
        cmd: &str,
        timeout_secs: u64,
        sink: Option<Channel<SshEvent>>,
        stdin: Option<&[u8]>,
    ) -> Result<ExecOut, String> {
        let handle = {
            let g = self.map.lock().await;
            g.get(conn_id)
                .ok_or("会话未打开：请先连接远程终端")?
                .handle
                .clone()
        }; // 出锁再开通道：同上，别让一台失联的机器把整张会话表锁死
        let ch = handle
            .channel_open_session()
            .await
            .map_err(|e| format!("打开命令通道失败: {e}"))?;
        ch.exec(true, cmd)
            .await
            .map_err(|e| format!("下发命令失败: {e}"))?;

        let (mut reader, writer) = ch.split();
        if let Some(data) = stdin {
            writer
                .data_bytes(data.to_vec())
                .await
                .map_err(|e| format!("写入命令标准输入失败: {e}"))?;
            writer
                .eof()
                .await
                .map_err(|e| format!("关闭命令标准输入失败: {e}"))?;
        }
        let sink2 = sink.clone(); // collect 闭包要借走 sink，收尾那几行得另留一份
        let collect = async {
            let mut out = ExecOut {
                stdout: String::new(),
                stderr: String::new(),
                code: -1,
                truncated: false,
            };
            let (mut so, mut se): (Vec<u8>, Vec<u8>) = (Vec::new(), Vec::new());
            let b64 = base64::engine::general_purpose::STANDARD;
            // 收到一块就往终端推一块 —— 这样 apt 装东西的进度是**滚出来**的，
            // 而不是憋到最后一次性倒出来（那就不叫「看执行过程」了）。
            // 回显要补 \r（见 to_crlf）；喂给 AI 的 stdout/stderr 保持原样，别动它的数据。
            let mut last_cr = false;
            let mut tee = |data: &[u8]| {
                if let Some(s) = &sink {
                    let _ = s.send(SshEvent::Data {
                        b64: b64.encode(to_crlf(data, &mut last_cr)),
                    });
                }
            };
            loop {
                match reader.wait().await {
                    Some(ChannelMsg::Data { data }) => {
                        tee(&data);
                        push_capped(&mut so, &data, &mut out.truncated)
                    }
                    // ext=1 是 stderr（SSH_EXTENDED_DATA_STDERR），别的扩展流忽略。
                    Some(ChannelMsg::ExtendedData { data, ext }) if ext == 1 => {
                        tee(&data);
                        push_capped(&mut se, &data, &mut out.truncated)
                    }
                    Some(ChannelMsg::ExitStatus { exit_status }) => out.code = exit_status as i32,
                    Some(_) => {}
                    None => break,
                }
            }
            // 远端输出不保证是合法 UTF-8（二进制/半个多字节字符）→ 有损转换，别让整条命令失败。
            out.stdout = String::from_utf8_lossy(&so).into_owned();
            out.stderr = String::from_utf8_lossy(&se).into_owned();
            out
        };
        let res = tokio::time::timeout(Duration::from_secs(timeout_secs), collect).await;
        // ★ 收尾这几行**必须也走 sink**：sink 才是 echo 开关（echo=false → None）。
        //   之前这里用的是 self.echo(...)——它自己去表里找通道，把开关整个绕过去了，
        //   于是每 5 秒一次的后台负载探测都会往终端里吐一行「▸ 完成」。
        let say = |text: String| {
            if let Some(s) = &sink2 {
                let _ = s.send(SshEvent::Data {
                    b64: base64::engine::general_purpose::STANDARD.encode(text),
                });
            }
        };
        match res {
            Ok(out) => {
                say(if out.code == 0 {
                    format!(
                        "\x1b[38;5;108m▸ 完成\x1b[0m{}\r\n",
                        if out.truncated {
                            "\x1b[90m（输出过长已截断）\x1b[0m"
                        } else {
                            ""
                        }
                    )
                } else {
                    format!("\x1b[31m▸ 退出码 {}\x1b[0m\r\n", out.code)
                });
                Ok(out)
            }
            Err(_) => {
                say(format!(
                    "\r\n\x1b[31m▸ AI 命令超时（{timeout_secs}s）\x1b[0m\r\n"
                ));
                Err(format!(
                    "命令超时（{timeout_secs} 秒未结束）。远端可能仍在跑：长任务请用 nohup/systemd 放后台再轮询查看。"
                ))
            }
        }
    }

    /// 在已建立的连接上开一路 SFTP。每次调用开一个新通道（通道很便宜，不用重新握手认证）；
    /// 不做缓存是刻意的：SftpSession 不能克隆，缓存起来要么跨 await 持锁、要么得再包一层 Arc
    /// 加生命周期管理，收益不抵复杂度。
    async fn sftp(&self, conn_id: &str) -> Result<SftpSession, String> {
        let handle = {
            let g = self.map.lock().await;
            g.get(conn_id)
                .ok_or("会话未打开：请先连接远程终端")?
                .handle
                .clone()
        }; // 出锁再开通道：开通道要等服务端确认，持锁 await 会连坐整张表
        let ch = handle
            .channel_open_session()
            .await
            .map_err(|e| format!("打开 SFTP 通道失败: {e}"))?;
        ch.request_subsystem(true, "sftp")
            .await
            .map_err(|e| format!("请求 sftp 子系统失败（远端可能没开 sftp）: {e}"))?;
        let sftp = SftpSession::new(ch.into_stream())
            .await
            .map_err(|e| format!("建立 SFTP 会话失败: {e}"))?;
        // 文件操作是直接交互，不能无限卡住按钮。超时只作用于这一条 SFTP 通道，
        // 不影响同一 SSH 连接上的终端 PTY。
        sftp.set_timeout(30);
        Ok(sftp)
    }

    /// 列目录（目录在前、再按名排序）。
    pub async fn list_dir(&self, conn_id: &str, path: &str) -> Result<Vec<SftpEntry>, String> {
        let sftp = self.sftp(conn_id).await?;
        let p = if path.trim().is_empty() { "." } else { path };
        let mut out: Vec<SftpEntry> = sftp
            .read_dir(p)
            .await
            .map_err(|e| format!("读取目录 {p} 失败: {e}"))?
            .map(|e| {
                let m = e.metadata();
                SftpEntry {
                    name: e.file_name(),
                    dir: m.is_dir(),
                    link: m.is_symlink(),
                    perms: m.permissions.map(perm_str).unwrap_or_default(),
                    size: m.size.unwrap_or(0),
                    mtime: m.mtime.unwrap_or(0) as u64,
                }
            })
            .collect();
        out.sort_by(|a, b| b.dir.cmp(&a.dir).then_with(|| a.name.cmp(&b.name)));
        Ok(out)
    }

    /// 解析为绝对路径（用于把 "." 变成真实 home）。
    pub async fn real_path(&self, conn_id: &str, path: &str) -> Result<String, String> {
        let sftp = self.sftp(conn_id).await?;
        sftp.canonicalize(if path.trim().is_empty() { "." } else { path })
            .await
            .map_err(|e| format!("解析路径失败: {e}"))
    }

    /// 读整个文件（在线编辑 / 下载用）。带大小闸门，别把几个 G 的东西吸进内存。
    pub async fn read_file(&self, conn_id: &str, path: &str, max: u64) -> Result<Vec<u8>, String> {
        let sftp = self.sftp(conn_id).await?;
        if let Ok(m) = sftp.metadata(path).await {
            if let Some(sz) = m.size {
                if sz > max {
                    return Err(format!("文件太大（{sz} 字节，上限 {max}）"));
                }
            }
        }
        sftp.read(path)
            .await
            .map_err(|e| format!("读取 {path} 失败: {e}"))
    }

    /// 写整个文件（在线编辑保存 / 上传用）。
    pub async fn write_file(&self, conn_id: &str, path: &str, data: &[u8]) -> Result<(), String> {
        use tokio::io::AsyncWriteExt as _;
        let sftp = self.sftp(conn_id).await?;
        // Session::write 只用 WRITE 打开：不会截断旧内容，而且 write_all 返回后也没有等待
        // 远端 ACK/close。编辑后内容变短会残留旧尾部，网络稍慢时还可能看起来「保存没生效」。
        let mut dst = sftp
            .create(path)
            .await
            .map_err(|e| format!("打开 {path} 写入失败: {e}"))?;
        dst.write_all(data)
            .await
            .map_err(|e| format!("写入 {path} 失败: {e}"))?;
        dst.shutdown()
            .await
            .map_err(|e| format!("保存 {path} 收尾失败: {e}"))
    }

    pub async fn rename(&self, conn_id: &str, from: &str, to: &str) -> Result<(), String> {
        let sftp = self.sftp(conn_id).await?;
        sftp.rename(from, to)
            .await
            .map_err(|e| format!("重命名失败: {e}"))
    }

    pub async fn remove(&self, conn_id: &str, path: &str, dir: bool) -> Result<(), String> {
        let sftp = self.sftp(conn_id).await?;
        if dir {
            validate_remove_dir_path(path)?;
            // 先用 lstat 确认调用方没有把符号链接伪装成目录；canonicalize 会跟随
            // 最后一段符号链接，所以顺序不能反。
            let metadata = sftp
                .symlink_metadata(path)
                .await
                .map_err(|e| format!("读取待删除文件夹失败: {e}"))?;
            if !metadata.is_dir() {
                return Err("目标不是文件夹，请刷新目录后重试".to_string());
            }
            let target = sftp
                .canonicalize(path)
                .await
                .map_err(|e| format!("解析待删除文件夹失败: {e}"))?;
            if target.trim_matches('/').is_empty() {
                return Err("拒绝删除远端根目录".to_string());
            }
            // SFTP 的 rmdir 只能删空目录；逐项删除会为每个文件产生一次网络往返。
            // 校验完成后交给远端 rm 在服务器本地遍历，速度接近用户直接敲命令。
            drop(sftp);
            let command = format!("rm -rf -- {}", posix_shell_quote(&target));
            let output = self.exec(conn_id, &command, 300, false).await?;
            if output.code != 0 {
                let detail = if !output.stderr.trim().is_empty() {
                    output.stderr.trim()
                } else if !output.stdout.trim().is_empty() {
                    output.stdout.trim()
                } else {
                    "远端未返回详细原因"
                };
                return Err(format!(
                    "删除文件夹失败（退出码 {}）：{detail}",
                    output.code
                ));
            }
            Ok(())
        } else {
            sftp.remove_file(path)
                .await
                .map_err(|e| format!("删除文件失败: {e}"))
        }
    }

    pub async fn mkdir(&self, conn_id: &str, path: &str) -> Result<(), String> {
        let sftp = self.sftp(conn_id).await?;
        sftp.create_dir(path)
            .await
            .map_err(|e| format!("新建目录失败: {e}"))
    }

    /// 下载：远程 → 本地，流式拷贝（大文件不整块进内存，不走 read_file 的闸门）。
    pub async fn download(&self, conn_id: &str, remote: &str, local: &str) -> Result<u64, String> {
        let sftp = self.sftp(conn_id).await?;
        let mut src = sftp
            .open(remote)
            .await
            .map_err(|e| format!("打开远程文件 {remote} 失败: {e}"))?;
        let mut dst = tokio::fs::File::create(local)
            .await
            .map_err(|e| format!("创建本地文件 {local} 失败: {e}"))?;
        tokio::io::copy(&mut src, &mut dst)
            .await
            .map_err(|e| format!("下载失败: {e}"))
    }

    /// 上传：本地 → 远程，流式拷贝。目标已存在则截断覆盖。
    pub async fn upload(&self, conn_id: &str, local: &str, remote: &str) -> Result<u64, String> {
        use tokio::io::AsyncWriteExt as _;
        let sftp = self.sftp(conn_id).await?;
        let mut src = tokio::fs::File::open(local)
            .await
            .map_err(|e| format!("打开本地文件 {local} 失败: {e}"))?;
        let mut dst = sftp
            .create(remote)
            .await
            .map_err(|e| format!("创建远程文件 {remote} 失败: {e}"))?;
        let n = tokio::io::copy(&mut src, &mut dst)
            .await
            .map_err(|e| format!("上传失败: {e}"))?;
        // copy 只 flush 不关句柄；SFTP 的 close 才让远端落定文件。
        dst.shutdown()
            .await
            .map_err(|e| format!("上传收尾失败: {e}"))?;
        Ok(n)
    }

    /// 在两条已认证 SSH 连接之间流式转发文件。数据只经过 Pilot 进程内存中的固定大小
    /// 缓冲区，不写入本机磁盘，也不会把任一连接的 SSH 凭据交给另一台服务器。
    pub async fn relay_file<F>(
        &self,
        source_conn_id: &str,
        source_remote: &str,
        target_conn_id: &str,
        target_remote: &str,
        mut on_progress: F,
    ) -> Result<u64, String>
    where
        F: FnMut(u64),
    {
        use tokio::io::{AsyncReadExt as _, AsyncWriteExt as _};

        let source_sftp = self.sftp(source_conn_id).await?;
        let target_sftp = self.sftp(target_conn_id).await?;
        let mut source = source_sftp
            .open(source_remote)
            .await
            .map_err(|e| format!("打开源服务器文件 {source_remote} 失败: {e}"))?;
        let mut target = target_sftp
            .create(target_remote)
            .await
            .map_err(|e| format!("创建目标服务器文件 {target_remote} 失败: {e}"))?;
        let mut buffer = vec![0_u8; 128 * 1024];
        let mut transferred = 0_u64;
        on_progress(0);
        loop {
            let read = source
                .read(&mut buffer)
                .await
                .map_err(|e| format!("读取源服务器快照失败: {e}"))?;
            if read == 0 {
                break;
            }
            target
                .write_all(&buffer[..read])
                .await
                .map_err(|e| format!("写入目标服务器快照失败: {e}"))?;
            transferred = transferred.saturating_add(read as u64);
            on_progress(transferred);
        }
        // SFTP close 才代表服务端已经接收完整文件；不能只依赖 write_all 的成功。
        target
            .shutdown()
            .await
            .map_err(|e| format!("目标服务器文件收尾失败: {e}"))?;
        Ok(transferred)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn remove_dir_rejects_dangerous_directory_paths() {
        for path in ["", " ", "/", "///", ".", "..", "/tmp/../", "/home/./site"] {
            assert!(
                validate_remove_dir_path(path).is_err(),
                "危险路径应该被拒绝: {path:?}"
            );
        }
        assert!(validate_remove_dir_path("/home/deploy/cms.ccvar.com").is_ok());
        assert!(validate_remove_dir_path("cms.ccvar.com").is_ok());
    }

    #[test]
    fn remove_dir_shell_quote_cannot_escape_the_argument() {
        assert_eq!(posix_shell_quote("/srv/site"), "'/srv/site'");
        assert_eq!(
            posix_shell_quote("/srv/a'b; rm -rf /"),
            "'/srv/a'\"'\"'b; rm -rf /'"
        );
    }

    /// ★ 回归：**回显必须只经 sink 这一个出口**。
    /// 出过的 bug：收尾那行「▸ 完成」用的是「按 conn_id 现查通道」的辅助函数，绕过了 echo 开关，
    /// 于是每 5 秒一次的后台负载探测都会往用户终端里吐一行「▸ 完成」（用户截图里刷了一屏）。
    /// 那个辅助函数已删；这里守住「源码里不该再出现这种绕过写法」。
    #[test]
    fn no_echo_helper_that_bypasses_the_sink() {
        // 只看 #[cfg(test)] 之前的**代码行**（跳过注释）—— 否则会命中讲这个坑的注释本身。
        let src = include_str!("ssh.rs");
        let code = src.split("#[cfg(test)]").next().unwrap();
        let bypass = format!("self.{}(", "echo");
        let hit: Vec<&str> = code
            .lines()
            .filter(|l| !l.trim_start().starts_with("//"))
            .filter(|l| l.contains(&bypass))
            .collect();
        assert!(
            hit.is_empty(),
            "回显又出现了绕过 sink 的写法 —— echo=false 的后台探测会污染终端: {hit:?}"
        );
        // exec 的两个调用点：桥要回显、系统探测不要（改签名时别把这俩弄反）
        assert!(
            src.contains("self.exec(conn_id, STATS_CMD, 15, false)"),
            "负载探测必须 echo=false"
        );
    }

    /// 负载解析：夹具照 Ubuntu 22.04 真机的输出格式写（用户那台就是）。
    #[test]
    fn parse_stats_reads_cpu_mem_disk() {
        let out = "cpu  1234567 8901 234567 45678901 12345 0 6789 0 0 0\n\
MemTotal:        4009884 kB\n\
MemFree:          645956 kB\n\
MemAvailable:    3455572 kB\n\
/dev/vda1       20511312 5350956  14096816  28% /\n";
        let s = parse_stats(out).unwrap();
        assert_eq!(
            s.cpu_total,
            1234567 + 8901 + 234567 + 45678901 + 12345 + 0 + 6789
        );
        assert_eq!(
            s.cpu_idle,
            45678901 + 12345,
            "idle 要含 iowait（等 IO 也不是在干活）"
        );
        assert_eq!(s.mem_total_kb, 4009884);
        assert_eq!(
            s.mem_avail_kb, 3455572,
            "有 MemAvailable 就别用 MemFree —— 缓存是可回收的"
        );
        assert_eq!(s.disk_total_kb, 20511312);
        assert_eq!(s.disk_used_kb, 5350956);
    }

    #[test]
    fn parse_stats_edge_cases() {
        // 老内核没有 MemAvailable → 退回 MemFree
        let s = parse_stats(
            "cpu 1 2 3 4 5\nMemTotal: 100 kB\nMemFree: 40 kB\n/dev/sda1 10 4 6 40% /\n",
        )
        .unwrap();
        assert_eq!(s.mem_avail_kb, 40);
        // 只认挂载点是 / 的那行（df 可能带别的行/表头）
        let s = parse_stats(
            "cpu 1 2 3 4 5\nMemTotal: 100 kB\nMemAvailable: 50 kB\n\
Filesystem 1024-blocks Used Available Capacity Mounted on\n\
/dev/sda2 999 999 0 100% /boot\n/dev/sda1 20 8 12 40% /\n",
        )
        .unwrap();
        assert_eq!(s.disk_total_kb, 20, "别把 /boot 那行当成根分区");
        assert_eq!(s.disk_used_kb, 8);
        // cpu 那行还有 per-core 的 cpu0/cpu1，只取汇总那行（我们只 head -1，但防一手）
        let s = parse_stats("cpu 10 0 10 80 0\ncpu0 5 0 5 40 0\nMemTotal: 100 kB\nMemAvailable: 50 kB\n/ 10 4 6 40% /\n").unwrap();
        assert_eq!(s.cpu_total, 100);
        // 不是 Linux / 缺行 → 报错，UI 就不显示（别编数）
        assert!(parse_stats("").is_err());
        assert!(
            parse_stats("cpu 1 2 3 4 5\n").is_err(),
            "缺内存和磁盘不能算成功"
        );
        assert!(
            parse_stats("MemTotal: 100 kB\nMemAvailable: 50 kB\n/dev/sda1 20 8 12 40% /\n")
                .is_err(),
            "缺 cpu 行"
        );
    }

    /// exec 没有 PTY → 输出是裸 \n，直接写进终端会「阶梯状」（每行从上一行结尾处开始）。
    #[test]
    fn crlf_fixes_bare_newlines() {
        let mut cr = false;
        assert_eq!(to_crlf(b"a\nb\n", &mut cr), b"a\r\nb\r\n");

        // 已经是 \r\n 的别再补（否则变成 \r\r\n，多空一行）
        let mut cr = false;
        assert_eq!(to_crlf(b"a\r\nb\r\n", &mut cr), b"a\r\nb\r\n");

        // ★ 跨块：\r 在这块末尾、\n 在下块开头 —— 只看本块会误判成裸 \n
        let mut cr = false;
        assert_eq!(to_crlf(b"a\r", &mut cr), b"a\r");
        assert!(cr, "块尾是 \\r，状态要带到下一块");
        assert_eq!(
            to_crlf(b"\nb", &mut cr),
            b"\nb",
            "下块开头的 \\n 属于上块的 \\r，不该补"
        );

        // 跨块的裸 \n（上块不是以 \r 结尾）照样要补
        let mut cr = false;
        assert_eq!(to_crlf(b"a", &mut cr), b"a");
        assert_eq!(to_crlf(b"\nb", &mut cr), b"\r\nb");

        // 没有换行就原样；空块不炸
        let mut cr = false;
        assert_eq!(to_crlf(b"plain", &mut cr), b"plain");
        assert_eq!(to_crlf(b"", &mut cr), b"");
        // 二进制/ANSI 转义原样穿过（终端要靠它上色、清屏）
        let mut cr = false;
        assert_eq!(
            to_crlf(b"\x1b[31mred\x1b[0m\n", &mut cr),
            b"\x1b[31mred\x1b[0m\r\n"
        );
    }

    #[test]
    fn perm_str_matches_ls() {
        assert_eq!(perm_str(0o755), "rwxr-xr-x");
        assert_eq!(perm_str(0o644), "rw-r--r--");
        assert_eq!(perm_str(0o700), "rwx------");
        assert_eq!(perm_str(0o777), "rwxrwxrwx");
        assert_eq!(perm_str(0o000), "---------");
        // 文件类型位在高处（0o100644 = 普通文件 644），不能漏进来
        assert_eq!(perm_str(0o100_644), "rw-r--r--");
        assert_eq!(perm_str(0o040_755), "rwxr-xr-x");
        // /tmp 的 sticky（1777）、setuid（4755）——按 ls 的写法叠在执行位上
        assert_eq!(perm_str(0o1777), "rwxrwxrwt");
        assert_eq!(perm_str(0o4755), "rwsr-xr-x");
        assert_eq!(perm_str(0o2755), "rwxr-sr-x");
        // 有 setuid 但没有执行位 → 大写
        assert_eq!(perm_str(0o4644), "rwSr--r--");
        assert_eq!(perm_str(0o1666), "rw-rw-rwT");
    }
}
