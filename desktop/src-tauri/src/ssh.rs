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
    for (i, group) in [(mode >> 6) & 7, (mode >> 3) & 7, mode & 7].iter().enumerate() {
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
        let fp = server_public_key.fingerprint(Default::default()).to_string();
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
    let handler = Client { expect: expect.clone(), seen: seen.clone() };
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
            Ok(SshProbe { fingerprint: fp, auth_ok: true, error: String::new() })
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
    ch: Option<russh::ChannelWriteHalf<client::Msg>>,
    /// 前端终端的事件通道。留一份克隆，AI 桥就能把「AI 正在跑什么」推到同一个终端里给人看见
    /// （复用 PTY 那条现成的通道，不必另起一套事件系统）。None = 没开终端，AI 干活就不显示。
    on_event: Option<Channel<SshEvent>>,
    /// 当前 PTY 的世代号。PTY 读任务退出时只有「自己仍是当前世代」才动表 ——
    /// 否则重连时旧任务的收尾会把刚建好的新会话删掉（旧代码有这个竞态，靠 connect 的网络延时侥幸不中）。
    pty_gen: u64,
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
                g.insert(conn_id.to_string(), Live { handle: Arc::new(session), ch: None, on_event: None, pty_gen: 0 });
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
                live.ch = Some(writer);
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
                        let _ = on_event.send(SshEvent::Data { b64: b64.encode(&data[..]) });
                    }
                    // stderr 也直接进终端（交互式 shell 本来就混在一起显示）。
                    Some(ChannelMsg::ExtendedData { data, .. }) => {
                        let _ = on_event.send(SshEvent::Data { b64: b64.encode(&data[..]) });
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
            let _ = on_event.send(SshEvent::Closed { error: String::new() });
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
        let g = self.map.lock().await;
        let live = g.get(conn_id).ok_or("会话未打开")?;
        live.ch
            .as_ref()
            .ok_or("终端未打开")?
            .data(data)
            .await
            .map_err(|e| format!("写入失败: {e}"))
    }

    /// 终端尺寸变化 → 通知远端重排（vim/top 才不会花屏）。
    pub async fn resize(&self, conn_id: &str, cols: u32, rows: u32) -> Result<(), String> {
        let g = self.map.lock().await;
        let live = g.get(conn_id).ok_or("会话未打开")?;
        live.ch
            .as_ref()
            .ok_or("终端未打开")?
            .window_change(cols, rows, 0, 0)
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
    pub async fn is_open(&self, conn_id: &str) -> bool {
        self.map.lock().await.contains_key(conn_id)
    }

    /// 前端终端的事件通道（克隆一份出来用，别攥着表锁）。没开终端就是 None。
    async fn sink(&self, conn_id: &str) -> Option<Channel<SshEvent>> {
        self.map.lock().await.get(conn_id).and_then(|l| l.on_event.clone())
    }

    /// 往终端里写一段**只给人看**的文字（AI 桥的回显）。
    ///
    /// 注意这不是发给远端 shell 的：远端并不知道屏幕上多了这些字。所以 AI 干活时你要是
    /// 正好在敲命令，提示符可能错位——敲个回车就恢复。回显只在开着终端时发生。
    async fn echo(&self, conn_id: &str, text: &str) {
        if let Some(s) = self.sink(conn_id).await {
            let _ = s.send(SshEvent::Data { b64: base64::engine::general_purpose::STANDARD.encode(text) });
        }
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
        let sink = if echo_to_term { self.sink(conn_id).await } else { None };
        if let Some(s) = &sink {
            // 醒目但克制：一行提示 + 命令原文，让人一眼看出这行不是自己敲的。
            let head = format!("\r\n\x1b[38;5;110m▸ AI\x1b[0m \x1b[1m{cmd}\x1b[0m\r\n");
            let _ = s.send(SshEvent::Data { b64: base64::engine::general_purpose::STANDARD.encode(&head) });
        }
        self.exec_inner(conn_id, cmd, timeout_secs, sink).await
    }

    async fn exec_inner(
        &self,
        conn_id: &str,
        cmd: &str,
        timeout_secs: u64,
        sink: Option<Channel<SshEvent>>,
    ) -> Result<ExecOut, String> {
        let handle = {
            let g = self.map.lock().await;
            g.get(conn_id).ok_or("会话未打开：请先连接远程终端")?.handle.clone()
        }; // 出锁再开通道：同上，别让一台失联的机器把整张会话表锁死
        let ch = handle
            .channel_open_session()
            .await
            .map_err(|e| format!("打开命令通道失败: {e}"))?;
        ch.exec(true, cmd)
            .await
            .map_err(|e| format!("下发命令失败: {e}"))?;

        let (mut reader, _writer) = ch.split();
        let collect = async {
            let mut out = ExecOut { stdout: String::new(), stderr: String::new(), code: -1, truncated: false };
            let (mut so, mut se): (Vec<u8>, Vec<u8>) = (Vec::new(), Vec::new());
            let b64 = base64::engine::general_purpose::STANDARD;
            // 收到一块就往终端推一块 —— 这样 apt 装东西的进度是**滚出来**的，
            // 而不是憋到最后一次性倒出来（那就不叫「看执行过程」了）。
            let mut tee = |data: &[u8]| {
                if let Some(s) = &sink {
                    let _ = s.send(SshEvent::Data { b64: b64.encode(data) });
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
        match res {
            Ok(out) => {
                self.echo_result(conn_id, out.code, out.truncated).await;
                Ok(out)
            }
            Err(_) => {
                self.echo(conn_id, &format!("\r\n\x1b[31m▸ AI 命令超时（{timeout_secs}s）\x1b[0m\r\n")).await;
                Err(format!(
                    "命令超时（{timeout_secs} 秒未结束）。远端可能仍在跑：长任务请用 nohup/systemd 放后台再轮询查看。"
                ))
            }
        }
    }

    /// 命令跑完在终端里补一行结果（只在开着终端、且这次是回显模式时才有意义）。
    async fn echo_result(&self, conn_id: &str, code: i32, truncated: bool) {
        let tail = if code == 0 {
            format!("\x1b[38;5;108m▸ 完成\x1b[0m{}\r\n", if truncated { "\x1b[90m（输出过长已截断）\x1b[0m" } else { "" })
        } else {
            format!("\x1b[31m▸ 退出码 {code}\x1b[0m\r\n")
        };
        self.echo(conn_id, &tail).await;
    }

    /// 在已建立的连接上开一路 SFTP。每次调用开一个新通道（通道很便宜，不用重新握手认证）；
    /// 不做缓存是刻意的：SftpSession 不能克隆，缓存起来要么跨 await 持锁、要么得再包一层 Arc
    /// 加生命周期管理，收益不抵复杂度。
    async fn sftp(&self, conn_id: &str) -> Result<SftpSession, String> {
        let handle = {
            let g = self.map.lock().await;
            g.get(conn_id).ok_or("会话未打开：请先连接远程终端")?.handle.clone()
        }; // 出锁再开通道：开通道要等服务端确认，持锁 await 会连坐整张表
        let ch = handle
            .channel_open_session()
            .await
            .map_err(|e| format!("打开 SFTP 通道失败: {e}"))?;
        ch.request_subsystem(true, "sftp")
            .await
            .map_err(|e| format!("请求 sftp 子系统失败（远端可能没开 sftp）: {e}"))?;
        SftpSession::new(ch.into_stream())
            .await
            .map_err(|e| format!("建立 SFTP 会话失败: {e}"))
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
        sftp.read(path).await.map_err(|e| format!("读取 {path} 失败: {e}"))
    }

    /// 写整个文件（在线编辑保存 / 上传用）。
    pub async fn write_file(&self, conn_id: &str, path: &str, data: &[u8]) -> Result<(), String> {
        let sftp = self.sftp(conn_id).await?;
        sftp.write(path, data)
            .await
            .map_err(|e| format!("写入 {path} 失败: {e}"))
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
            sftp.remove_dir(path).await.map_err(|e| format!("删除目录失败（非空？）: {e}"))
        } else {
            sftp.remove_file(path).await.map_err(|e| format!("删除文件失败: {e}"))
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
        dst.shutdown().await.map_err(|e| format!("上传收尾失败: {e}"))?;
        Ok(n)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

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
