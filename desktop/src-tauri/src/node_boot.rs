//! 托管 Node 自举：机器没有系统 Node(≥18) 时，把便携版 Node 下到 <data_dir>/node/ 复用，
//! npm 全局包装进 <data_dir>/node/npm-global——claude/codex 的 npm 通道从此不依赖用户装 Node。
//! 下载走官源 nodejs.org/dist、失败自动切 npmmirror 镜像；代理由进程环境变量继承
//!（启动时 apply_system_proxy_to_process 已注入，reqwest 默认读取）。
//! PATH 两层：① 进程内前置（探测与所有 CLI 子进程立即可用，见 prepend_process_path）；
//! ② 用户级持久 PATH（Windows 注册表 User Path 读-查重-追加、绝不 setx；macOS ~/.zprofile 守卫块，均幂等）。

use std::path::{Path, PathBuf};

/// 钉死的托管 Node 版本：v22 LTS（nodejs.org/dist/v22.14.0 实测在档，win/darwin/linux 全平台有包）。
pub const NODE_VERSION: &str = "v22.14.0";

/// macOS ~/.zprofile 守卫块标记（存在即视为已写入，跳过）。
pub const ZPROFILE_MARK: &str = "# gcms-pilot managed node";

/// 平台/架构 → 发行包文件名；不支持的组合返回 None（调用方回退官方脚本渠道）。
pub fn dist_filename(os: &str, arch: &str) -> Option<String> {
    let stem = match (os, arch) {
        ("windows", "aarch64") => format!("node-{NODE_VERSION}-win-arm64.zip"),
        ("windows", _) => format!("node-{NODE_VERSION}-win-x64.zip"),
        ("macos", "aarch64") => format!("node-{NODE_VERSION}-darwin-arm64.tar.gz"),
        ("macos", _) => format!("node-{NODE_VERSION}-darwin-x64.tar.gz"),
        ("linux", "aarch64") => format!("node-{NODE_VERSION}-linux-arm64.tar.gz"),
        ("linux", "x86_64") => format!("node-{NODE_VERSION}-linux-x64.tar.gz"),
        _ => return None,
    };
    Some(stem)
}

/// 下载地址：官源优先，失败切 npmmirror 镜像（国内直连可达）。
pub fn dist_urls(filename: &str) -> [String; 2] {
    [
        format!("https://nodejs.org/dist/{NODE_VERSION}/{filename}"),
        format!("https://registry.npmmirror.com/-/binary/node/{NODE_VERSION}/{filename}"),
    ]
}

/// 发行包文件名去扩展名＝解压后的根目录名。
pub fn dist_root(filename: &str) -> String {
    filename
        .trim_end_matches(".zip")
        .trim_end_matches(".tar.gz")
        .to_string()
}

/// "v22.14.0" / "22.14.0" → 22；解析不了 → 0。
pub fn parse_node_major(v: &str) -> u32 {
    v.trim()
        .trim_start_matches('v')
        .split('.')
        .next()
        .and_then(|s| s.parse().ok())
        .unwrap_or(0)
}

fn current_os() -> &'static str {
    if cfg!(windows) {
        "windows"
    } else if cfg!(target_os = "macos") {
        "macos"
    } else {
        "linux"
    }
}

/// 托管 node 的 bin 目录（win 发行包根目录即 bin；unix 是 <根>/bin）。平台不支持返回 None。
pub fn managed_bin_dir(data_dir: &Path) -> Option<PathBuf> {
    let f = dist_filename(current_os(), std::env::consts::ARCH)?;
    let root = data_dir.join("node").join(dist_root(&f));
    Some(if cfg!(windows) {
        root
    } else {
        root.join("bin")
    })
}

/// npm 全局 prefix（托管安装的 claude/codex 落这里，不碰系统目录）。
pub fn npm_prefix_dir(data_dir: &Path) -> PathBuf {
    data_dir.join("node").join("npm-global")
}

/// npm 全局 bin：win 的 .cmd shim 就在 prefix 根；unix 在 prefix/bin。
pub fn npm_global_bin(data_dir: &Path) -> PathBuf {
    let p = npm_prefix_dir(data_dir);
    if cfg!(windows) {
        p
    } else {
        p.join("bin")
    }
}

/// 托管 node 可执行路径。
pub fn managed_node_exe(bin_dir: &Path) -> PathBuf {
    bin_dir.join(if cfg!(windows) { "node.exe" } else { "node" })
}

/// 托管 npm 可执行路径（win 是 npm.cmd）。
pub fn managed_npm_exe(bin_dir: &Path) -> PathBuf {
    bin_dir.join(if cfg!(windows) { "npm.cmd" } else { "npm" })
}

/// 跑 node --version 校验托管 node 可用且 ≥18。
pub fn verify_managed(bin_dir: &Path) -> bool {
    let mut c = std::process::Command::new(managed_node_exe(bin_dir));
    c.arg("--version");
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000);
    }
    match c.output() {
        Ok(o) if o.status.success() => parse_node_major(&String::from_utf8_lossy(&o.stdout)) >= 18,
        _ => false,
    }
}

/// 任意目录前置进本进程 PATH（幂等）——探测（which 读 PATH）与之后 spawn 的子进程自动继承。
pub fn prepend_path_dirs(dirs: &[PathBuf]) {
    let sep = if cfg!(windows) { ';' } else { ':' };
    let cur = std::env::var("PATH").unwrap_or_default();
    let mut add: Vec<String> = dirs
        .iter()
        .map(|p| p.to_string_lossy().to_string())
        .collect();
    add.retain(|p| !cur.split(sep).any(|x| x == p));
    if add.is_empty() {
        return;
    }
    std::env::set_var("PATH", format!("{}{sep}{cur}", add.join(&sep.to_string())));
}

/// 把托管 node bin + npm 全局 bin 前置进本进程 PATH；幂等。启动时与自举成功后各调一次。
pub fn prepend_process_path(data_dir: &Path) {
    let Some(bin) = managed_bin_dir(data_dir) else {
        return;
    };
    if !bin.exists() {
        return;
    }
    prepend_path_dirs(&[bin, npm_global_bin(data_dir)]);
}

/// macOS ~/.zprofile 追加块（带守卫标记；导出为 PATH 前置）。
pub fn zprofile_block(bin_dirs: &[&str]) -> String {
    format!(
        "\n{ZPROFILE_MARK} (auto-added; safe to remove)\nexport PATH=\"{}:$PATH\"\n",
        bin_dirs.join(":")
    )
}

/// zprofile 是否还需要追加（守卫标记不存在才写，幂等）。
pub fn zprofile_needs_append(existing: &str) -> bool {
    !existing.contains(ZPROFILE_MARK)
}

/// Windows 用户级 Path 追加脚本（PowerShell）：读现值 → 按目录逐个查重 → 追加 → 全量写回。
/// 用 [Environment]::SetEnvironmentVariable('Path',…,'User')，**绝不用 setx**（1024 字符截断）。
#[cfg(any(windows, test))]
pub fn ps_path_append_script(dirs: &[&str]) -> String {
    let mut s = String::from(
        "$p=[Environment]::GetEnvironmentVariable('Path','User'); if($null -eq $p){$p=''};",
    );
    for d in dirs {
        s.push_str(&format!(
            " if(-not (($p -split ';') -contains '{d}')){{ $p = (($p.TrimEnd(';')) + ';{d}').TrimStart(';') }};"
        ));
    }
    s.push_str(" [Environment]::SetEnvironmentVariable('Path',$p,'User')");
    s
}

/// 用户级持久 PATH 写入（幂等；失败不致命——调用方吐司提示手动配置）。
/// Windows 写注册表 User Path；macOS 追加 ~/.zprofile 守卫块；Linux 不写（进程内前置已够应用内使用）。
pub fn register_user_path(bin_dirs: &[PathBuf]) -> Result<(), String> {
    let strs: Vec<String> = bin_dirs
        .iter()
        .map(|p| p.to_string_lossy().to_string())
        .collect();
    let refs: Vec<&str> = strs.iter().map(|s| s.as_str()).collect();
    #[cfg(windows)]
    {
        let script = ps_path_append_script(&refs);
        let mut c = std::process::Command::new("powershell");
        c.args(["-NoProfile", "-Command", &script]);
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000);
        let out = c.output().map_err(|e| e.to_string())?;
        if !out.status.success() {
            return Err(String::from_utf8_lossy(&out.stderr)
                .chars()
                .take(200)
                .collect());
        }
        return Ok(());
    }
    #[cfg(target_os = "macos")]
    {
        let home = std::env::var("HOME").map_err(|e| e.to_string())?;
        let zp = Path::new(&home).join(".zprofile");
        let existing = std::fs::read_to_string(&zp).unwrap_or_default();
        if zprofile_needs_append(&existing) {
            use std::io::Write;
            let mut f = std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(&zp)
                .map_err(|e| e.to_string())?;
            f.write_all(zprofile_block(&refs).as_bytes())
                .map_err(|e| e.to_string())?;
        }
        return Ok(());
    }
    #[cfg(not(any(windows, target_os = "macos")))]
    {
        let _ = refs;
        Ok(())
    }
}

/// 确保托管 Node 就绪：托管目录已有且 node -v 通过 → 直接复用；否则下载（官源→镜像）→
/// 解压（zip/tar.gz）→ 校验。progress(phase, pct)：download(带百分比)/extract/verify。
/// 系统 Node 的优先使用由调用方判断，这里只管托管副本。
pub async fn ensure(data_dir: &Path, progress: impl Fn(&str, u32)) -> Result<PathBuf, String> {
    let bin = managed_bin_dir(data_dir).ok_or("当前平台没有对应的 Node 便携包")?;
    if verify_managed(&bin) {
        return Ok(bin);
    }
    let filename =
        dist_filename(current_os(), std::env::consts::ARCH).expect("managed_bin_dir 已校验平台");
    let mut data: Option<Vec<u8>> = None;
    let mut last_err = String::new();
    for url in dist_urls(&filename) {
        match download(&url, &progress).await {
            Ok(b) => {
                data = Some(b);
                break;
            }
            Err(e) => last_err = format!("{url}: {e}"),
        }
    }
    let data = data.ok_or_else(|| format!("下载 Node 失败：{last_err}"))?;
    progress("extract", 0);
    let node_dir = data_dir.join("node");
    let fname = filename.clone();
    tauri::async_runtime::spawn_blocking(move || extract(&node_dir, &fname, &data))
        .await
        .map_err(|e| e.to_string())??;
    progress("verify", 0);
    if !verify_managed(&bin) {
        return Err("托管 Node 校验失败（node --version 未通过）".into());
    }
    Ok(bin)
}

// ---- Grok CLI（Windows 直下官方原生 exe；URL 口径抄官方 install.sh）----
// 侦查结论（2026-07 实测）：官方没有 npm 包（@xai/grok-cli 404）；install.sh 对
// MINGW/MSYS/CYGWIN 输出 windows 平台并直下单文件 exe——Pilot 在 Windows 上跳过
// bash 层直接用同一套 URL：版本指针 {base}/stable → 产物 {base}/grok-{ver}-windows-{arch}.exe，
// 主源 x.ai/cli、回退 GCS 公共桶；官方安装布局 ~/.grok/bin/{grok.exe, agent.exe}。

/// grok stable 版本指针地址（主源 → 回退）。
pub fn grok_version_urls() -> [String; 2] {
    [
        "https://x.ai/cli/stable".into(),
        "https://storage.googleapis.com/grok-build-public-artifacts/cli/stable".into(),
    ]
}

/// grok Windows 单文件 exe 产物地址（主源 → 回退）。arch 传 std::env::consts::ARCH。
pub fn grok_win_exe_urls(version: &str, arch: &str) -> [String; 2] {
    let plat = if arch == "aarch64" {
        "windows-aarch64"
    } else {
        "windows-x86_64"
    };
    [
        format!("https://x.ai/cli/grok-{version}-{plat}.exe"),
        format!("https://storage.googleapis.com/grok-build-public-artifacts/cli/grok-{version}-{plat}.exe"),
    ]
}

/// 版本指针响应 → 版本号：X.Y.Z(-suffix) 形态才算（防把错误页/HTML 当版本）。
pub fn parse_grok_version(body: &str) -> Option<String> {
    let v = body.trim();
    let mut parts = v.splitn(3, '.');
    let (a, b, c) = (parts.next()?, parts.next()?, parts.next()?);
    let num = |s: &str| !s.is_empty() && s.chars().all(|ch| ch.is_ascii_digit());
    let tail_ok = c.split('-').next().map(num).unwrap_or(false)
        && c.chars()
            .all(|ch| ch.is_ascii_alphanumeric() || ch == '.' || ch == '-');
    (num(a) && num(b) && tail_ok && !v.contains(char::is_whitespace)).then(|| v.to_string())
}

/// grok 官方安装目录（Windows：%USERPROFILE%\.grok\bin）。
pub fn grok_win_bin_dir(home: &Path) -> PathBuf {
    home.join(".grok").join("bin")
}

/// 拉一小段文本（版本指针等）。代理走进程环境变量。
pub(crate) async fn fetch_text(url: &str) -> Result<String, String> {
    let resp = reqwest::Client::new()
        .get(url)
        .timeout(std::time::Duration::from_secs(30))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if !resp.status().is_success() {
        return Err(format!("HTTP {}", resp.status()));
    }
    resp.text().await.map_err(|e| e.to_string())
}

/// 分块下载（每 ≥2% 报一次进度；拿不到总长不报百分比）。代理走进程环境变量（reqwest 默认读取）。
pub(crate) async fn download(url: &str, progress: &impl Fn(&str, u32)) -> Result<Vec<u8>, String> {
    let mut resp = reqwest::Client::new()
        .get(url)
        .timeout(std::time::Duration::from_secs(600))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if !resp.status().is_success() {
        return Err(format!("HTTP {}", resp.status()));
    }
    let total = resp.content_length().unwrap_or(0);
    let mut buf: Vec<u8> = Vec::with_capacity(total as usize);
    let mut last_pct = 0u32;
    progress("download", 0);
    while let Some(chunk) = resp.chunk().await.map_err(|e| e.to_string())? {
        buf.extend_from_slice(&chunk);
        if total > 0 {
            let pct = ((buf.len() as u64 * 100) / total) as u32;
            if pct >= last_pct + 2 {
                last_pct = pct;
                progress("download", pct.min(100));
            }
        }
    }
    Ok(buf)
}

/// 解压 zip（win）/ tar.gz（unix，保留可执行位）到 <data_dir>/node/。
fn extract(node_dir: &Path, filename: &str, data: &[u8]) -> Result<(), String> {
    std::fs::create_dir_all(node_dir).map_err(|e| format!("创建 node 目录: {e}"))?;
    if filename.ends_with(".zip") {
        let mut ar = zip::ZipArchive::new(std::io::Cursor::new(data))
            .map_err(|e| format!("读取 zip: {e}"))?;
        ar.extract(node_dir).map_err(|e| format!("解压 zip: {e}"))?;
    } else {
        let gz = flate2::read::GzDecoder::new(data);
        let mut ar = tar::Archive::new(gz);
        ar.unpack(node_dir)
            .map_err(|e| format!("解压 tar.gz: {e}"))?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 各平台/架构的发行包文件名与双源 URL 拼装。
    #[test]
    fn dist_filename_and_urls() {
        assert_eq!(
            dist_filename("windows", "x86_64").unwrap(),
            format!("node-{NODE_VERSION}-win-x64.zip")
        );
        assert_eq!(
            dist_filename("windows", "aarch64").unwrap(),
            format!("node-{NODE_VERSION}-win-arm64.zip")
        );
        assert_eq!(
            dist_filename("macos", "aarch64").unwrap(),
            format!("node-{NODE_VERSION}-darwin-arm64.tar.gz")
        );
        assert_eq!(
            dist_filename("macos", "x86_64").unwrap(),
            format!("node-{NODE_VERSION}-darwin-x64.tar.gz")
        );
        assert_eq!(
            dist_filename("linux", "x86_64").unwrap(),
            format!("node-{NODE_VERSION}-linux-x64.tar.gz")
        );
        assert_eq!(
            dist_filename("linux", "aarch64").unwrap(),
            format!("node-{NODE_VERSION}-linux-arm64.tar.gz")
        );
        assert!(
            dist_filename("freebsd", "x86_64").is_none(),
            "不支持的平台回 None（调用方回退脚本渠道）"
        );
        let urls = dist_urls("node-v22.14.0-darwin-arm64.tar.gz");
        assert_eq!(
            urls[0],
            "https://nodejs.org/dist/v22.14.0/node-v22.14.0-darwin-arm64.tar.gz"
        );
        assert_eq!(urls[1], "https://registry.npmmirror.com/-/binary/node/v22.14.0/node-v22.14.0-darwin-arm64.tar.gz");
        assert_eq!(
            dist_root("node-v22.14.0-win-x64.zip"),
            "node-v22.14.0-win-x64"
        );
        assert_eq!(
            dist_root("node-v22.14.0-darwin-arm64.tar.gz"),
            "node-v22.14.0-darwin-arm64"
        );
    }

    /// 版本解析：v 前缀/裸版本/垃圾输入。
    #[test]
    fn node_version_parse() {
        assert_eq!(parse_node_major("v22.14.0"), 22);
        assert_eq!(parse_node_major("22.14.0"), 22);
        assert_eq!(parse_node_major(" v18.19.1\n"), 18);
        assert_eq!(parse_node_major("nope"), 0);
        assert_eq!(parse_node_major(""), 0);
    }

    /// PATH 持久化的字符串装配：zprofile 守卫幂等；PS 脚本读-查重-追加、无 setx。
    #[test]
    fn user_path_assembly_idempotent() {
        // zprofile：无标记要写、有标记跳过
        assert!(zprofile_needs_append(""));
        assert!(zprofile_needs_append("export PATH=/usr/local/bin:$PATH"));
        let block = zprofile_block(&["/a/bin", "/b"]);
        assert!(block.contains(ZPROFILE_MARK));
        assert!(block.contains("export PATH=\"/a/bin:/b:$PATH\""));
        assert!(!zprofile_needs_append(&block), "写过一次后幂等跳过");
        // PowerShell：SetEnvironmentVariable 全量写、逐目录 -contains 查重、绝不 setx
        let ps = ps_path_append_script(&["C:\\d\\node", "C:\\d\\npm-global"]);
        assert!(ps.contains("[Environment]::GetEnvironmentVariable('Path','User')"));
        assert!(ps.contains("[Environment]::SetEnvironmentVariable('Path',$p,'User')"));
        assert!(ps.matches("-contains").count() == 2, "两个目录各自查重");
        assert!(ps.contains("C:\\d\\node") && ps.contains("C:\\d\\npm-global"));
        assert!(
            !ps.to_lowercase().contains("setx"),
            "setx 会截断 1024 字符，绝不使用"
        );
    }

    /// 托管目录布局（当前平台口径）。
    #[test]
    fn managed_layout_paths() {
        let dd = std::path::Path::new("/tmp/appdata");
        let bin = managed_bin_dir(dd).expect("桌面平台都支持");
        let s = bin.to_string_lossy();
        assert!(s.contains("/tmp/appdata/node/node-v22.14.0-"));
        if cfg!(windows) {
            assert!(!s.ends_with("bin"));
        } else {
            assert!(s.ends_with("/bin"));
        }
        assert_eq!(npm_prefix_dir(dd), dd.join("node").join("npm-global"));
        let nb = npm_global_bin(dd);
        if cfg!(windows) {
            assert_eq!(nb, npm_prefix_dir(dd));
        } else {
            assert_eq!(nb, npm_prefix_dir(dd).join("bin"));
        }
    }

    /// grok Windows 直下通道的纯函数：URL 拼装（双源/双架构）、版本指针解析、安装目录。
    #[test]
    fn grok_win_urls_and_version() {
        let [p, f] = grok_version_urls();
        assert_eq!(p, "https://x.ai/cli/stable");
        assert!(f.starts_with("https://storage.googleapis.com/grok-build-public-artifacts/cli"));
        let [a, b] = grok_win_exe_urls("0.2.101", "x86_64");
        assert_eq!(a, "https://x.ai/cli/grok-0.2.101-windows-x86_64.exe");
        assert_eq!(b, "https://storage.googleapis.com/grok-build-public-artifacts/cli/grok-0.2.101-windows-x86_64.exe");
        assert!(grok_win_exe_urls("0.2.101", "aarch64")[0].contains("windows-aarch64.exe"));
        // 版本指针解析：正常/带后缀/换行 trim；HTML/空/多词拒绝
        assert_eq!(parse_grok_version("0.2.101\n").as_deref(), Some("0.2.101"));
        assert_eq!(
            parse_grok_version("1.2.3-beta.1").as_deref(),
            Some("1.2.3-beta.1")
        );
        assert!(parse_grok_version("<html>404</html>").is_none());
        assert!(parse_grok_version("").is_none());
        assert!(parse_grok_version("error: not found").is_none());
        assert!(parse_grok_version("0.2").is_none(), "两段不算版本");
        // 安装目录（官方布局）
        assert_eq!(
            grok_win_bin_dir(Path::new("C:/Users/u")),
            Path::new("C:/Users/u").join(".grok").join("bin")
        );
    }

    /// mac 真机自举 live 测试（下载 ~40MB，默认忽略）：cargo test --lib node_boot -- --ignored
    #[test]
    #[ignore]
    fn managed_node_bootstrap_live() {
        let dir = std::env::temp_dir().join(format!("node-boot-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        let bin = rt
            .block_on(ensure(&dir, |phase, pct| println!("{phase} {pct}%")))
            .expect("自举成功");
        assert!(verify_managed(&bin), "node -v 通过");
        std::fs::remove_dir_all(&dir).ok();
    }
}
