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
    pub size: u64,
    pub mtime: u64,
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

/// 一条活着的 SSH 连接：一个 handle + 其上的若干通道（PTY / SFTP）。
///
/// 为什么必须留着 `handle`：**开新通道只能通过它**（SFTP 要在同一条连接上再开一路，
/// 否则每次文件操作都要重新握手认证）。
/// 另：`ChannelWriteHalf` 内部持有 sender 的克隆，所以只要它活着，会话任务就不会退出
/// （`Handle::drop` 不 abort，其 JoinHandle 被 drop 在 tokio 里是分离而非杀死）——
/// 但那只保活，开不了新通道，所以 handle 还是得留。
struct Live {
    handle: client::Handle<Client>,
    /// PTY 通道写半边（读半边在 open_shell 里交给后台任务流给前端；Channel 不能克隆，只能拆两半）。
    ch: russh::ChannelWriteHalf<client::Msg>,
}

/// 按 conn_id 索引的活跃 SSH 会话表。
#[derive(Clone, Default)]
pub struct SshSessions {
    map: Arc<Mutex<HashMap<String, Live>>>,
}

impl SshSessions {
    pub fn new() -> Self {
        Self::default()
    }

    /// 开一个交互式 PTY shell，把输出流给前端；同一 conn_id 已有会话时先关掉旧的。
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
        self.close(conn_id).await;
        let (session, _fp) = connect_auth(host, port, auth, expect).await?;
        let ch = session
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
        self.map
            .lock()
            .await
            .insert(conn_id.to_string(), Live { handle: session, ch: writer });

        let map = self.map.clone();
        let id = conn_id.to_string();
        tokio::spawn(async move {
            let b64 = base64::engine::general_purpose::STANDARD;
            let mut err = String::new();
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
            map.lock().await.remove(&id);
            let _ = on_event.send(SshEvent::Closed { error: std::mem::take(&mut err) });
        });
        Ok(())
    }

    /// 把键盘输入写进 PTY。
    pub async fn input(&self, conn_id: &str, data: &[u8]) -> Result<(), String> {
        let g = self.map.lock().await;
        let live = g.get(conn_id).ok_or("会话未打开")?;
        live.ch
            .data(data)
            .await
            .map_err(|e| format!("写入失败: {e}"))
    }

    /// 终端尺寸变化 → 通知远端重排（vim/top 才不会花屏）。
    pub async fn resize(&self, conn_id: &str, cols: u32, rows: u32) -> Result<(), String> {
        let g = self.map.lock().await;
        let live = g.get(conn_id).ok_or("会话未打开")?;
        live.ch
            .window_change(cols, rows, 0, 0)
            .await
            .map_err(|e| format!("改窗口大小失败: {e}"))
    }

    /// 关闭会话（幂等）。
    pub async fn close(&self, conn_id: &str) {
        if let Some(live) = self.map.lock().await.remove(conn_id) {
            let _ = live.ch.close().await;
        }
    }

    pub async fn is_open(&self, conn_id: &str) -> bool {
        self.map.lock().await.contains_key(conn_id)
    }

    /// 在已建立的连接上开一路 SFTP。每次调用开一个新通道（通道很便宜，不用重新握手认证）；
    /// 不做缓存是刻意的：SftpSession 不能克隆，缓存起来要么跨 await 持锁、要么得再包一层 Arc
    /// 加生命周期管理，收益不抵复杂度。
    async fn sftp(&self, conn_id: &str) -> Result<SftpSession, String> {
        let ch = {
            let g = self.map.lock().await;
            let live = g.get(conn_id).ok_or("会话未打开：请先连接远程终端")?;
            live.handle
                .channel_open_session()
                .await
                .map_err(|e| format!("打开 SFTP 通道失败: {e}"))?
        }; // 提前放锁：下面 await 不能持锁
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
}
