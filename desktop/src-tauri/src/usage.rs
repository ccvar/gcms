//! 本地用量日志：每轮 token 追加一行 JSONL（usage_log.jsonl），供「近 5 小时 / 今日」
//! 参考统计。只做本地体感计量——订阅额度官方无查询接口，换算不了真实剩余百分比。

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::io::Write;
use std::path::Path;

#[derive(Serialize, Deserialize)]
pub struct UsageEntry {
    pub ts: i64, // unix 秒
    pub brain: String,
    pub model: String,
    pub input: u64,
    pub output: u64,
    pub cache_read: u64,
    pub cache_create: u64,
}

fn log_path(data_dir: &Path) -> std::path::PathBuf {
    data_dir.join("usage_log.jsonl")
}

/// 追加一轮用量。文件超 1MB 时顺手裁剪到最近 8 天（重写）。追加失败静默——
/// 参考统计不值得让回合报错。
pub fn append(data_dir: &Path, e: &UsageEntry) {
    let p = log_path(data_dir);
    let Ok(line) = serde_json::to_string(e) else { return };
    if let Ok(mut f) = std::fs::OpenOptions::new().create(true).append(true).open(&p) {
        let _ = writeln!(f, "{line}");
    }
    if std::fs::metadata(&p).map(|m| m.len() > 1_000_000).unwrap_or(false) {
        prune(&p, e.ts - 8 * 24 * 3600);
    }
}

fn prune(p: &Path, keep_since: i64) {
    let Ok(raw) = std::fs::read_to_string(p) else { return };
    let kept: Vec<&str> = raw
        .lines()
        .filter(|l| {
            serde_json::from_str::<UsageEntry>(l)
                .map(|e| e.ts >= keep_since)
                .unwrap_or(false)
        })
        .collect();
    let tmp = p.with_extension("jsonl.tmp");
    if std::fs::write(&tmp, kept.join("\n") + "\n").is_ok() {
        let _ = std::fs::rename(&tmp, p);
    }
}

/// 两个起点各算一份「按 brain 汇总的处理量」（input+cache_read+cache_create+output，
/// 与会话累计 total_tokens 同口径）。起点由前端按本地时区算好传来（近 5 小时 / 今日零点）。
#[derive(Serialize, Default)]
pub struct UsageStats {
    pub window_a: HashMap<String, u64>,
    pub window_b: HashMap<String, u64>,
}

pub fn stats(data_dir: &Path, since_a: i64, since_b: i64) -> UsageStats {
    let mut out = UsageStats::default();
    let Ok(raw) = std::fs::read_to_string(log_path(data_dir)) else { return out };
    for l in raw.lines() {
        let Ok(e) = serde_json::from_str::<UsageEntry>(l) else { continue };
        let total = e.input + e.cache_read + e.cache_create + e.output;
        if e.ts >= since_a {
            *out.window_a.entry(e.brain.clone()).or_insert(0) += total;
        }
        if e.ts >= since_b {
            *out.window_b.entry(e.brain).or_insert(0) += total;
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn append_and_stats_windows() {
        let dir = std::env::temp_dir().join(format!("gcms-pilot-usage-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let mk = |ts: i64, brain: &str, output: u64| UsageEntry {
            ts,
            brain: brain.into(),
            model: "m".into(),
            input: 100,
            output,
            cache_read: 50,
            cache_create: 0,
        };
        append(&dir, &mk(1000, "claude", 10)); // 只落在窗口 B
        append(&dir, &mk(5000, "claude", 20)); // 两个窗口都在
        append(&dir, &mk(6000, "codex", 30));
        let s = stats(&dir, 4000, 500);
        assert_eq!(s.window_a.get("claude"), Some(&170u64)); // 100+50+20
        assert_eq!(s.window_a.get("codex"), Some(&180u64));
        assert_eq!(s.window_b.get("claude"), Some(&(160u64 + 170)));
        std::fs::remove_dir_all(&dir).ok();
    }
}
