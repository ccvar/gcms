//! 订阅限额登记（per-brain，全局）：任何一轮对话检测到限额（agent::detect_usage_limit 给出
//! 恢复时间戳）就登记 {brain → reset_ts, notified}，定时任务触发前查它决定是否顺延，
//! 免得到点白跑失败。持久化 limits.json（原子写）；进程级共享互斥（模块全局锁），
//! LimitStore::new 可随处便宜构造而不丢读改写原子性。

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

static LOCK: Mutex<()> = Mutex::new(());

/// 一个厂商的限额窗口。
#[derive(Clone, Serialize, Deserialize, Default)]
pub struct LimitEntry {
    /// 预计恢复时间（unix 秒）。
    pub reset_ts: u64,
    /// 本限额窗口是否已发过系统通知（每 brain 每窗口只发一次）。
    #[serde(default)]
    pub notified: bool,
}

/// 拿不到恢复时间（detect_usage_limit 返回 0）时的保守顺延：now + 30 分钟。
pub fn effective_reset(reset: i64, now: u64) -> u64 {
    if reset <= 0 {
        return now + 30 * 60;
    }
    let r = reset as u64;
    if r <= now { now + 30 * 60 } else { r }
}

/// 顺延后的 next_run：reset_ts 加 ~120s 抖动（按任务 id 稳定散列到 [60,180)），
/// 多个任务不在恢复点整齐扎堆重试。
pub fn defer_next_run(reset_ts: u64, task_id: &str) -> u64 {
    let hash: u64 = task_id.bytes().fold(1469598103934665603u64, |h, b| (h ^ b as u64).wrapping_mul(1099511628211));
    reset_ts + 60 + hash % 120
}

/// 是否处于限额期（now < reset_ts）。
pub fn is_limited(entry: Option<&LimitEntry>, now: u64) -> bool {
    entry.map(|e| now < e.reset_ts).unwrap_or(false)
}

#[derive(Clone)]
pub struct LimitStore {
    file: PathBuf,
}

impl LimitStore {
    pub fn new(data_dir: &Path) -> Self {
        Self { file: data_dir.join("limits.json") }
    }

    fn read(&self) -> HashMap<String, LimitEntry> {
        match fs::read(&self.file) {
            Ok(raw) => serde_json::from_slice(&raw).unwrap_or_default(),
            Err(_) => HashMap::new(),
        }
    }

    fn save(&self, map: &HashMap<String, LimitEntry>) {
        if let Ok(raw) = serde_json::to_vec_pretty(map) {
            let tmp = self.file.with_extension("json.tmp");
            if fs::write(&tmp, &raw).is_ok() {
                let _ = fs::rename(&tmp, &self.file);
            }
        }
    }

    /// 登记一次限额命中：reset<=0 或在过去 → now+30 分钟保守登记。
    /// 只延后不提前；reset 明显更晚（换了新窗口）时重置 notified，通知可再发一次。
    pub fn register(&self, brain: &str, reset: i64, now: u64) -> u64 {
        let _g = LOCK.lock().unwrap();
        let eff = effective_reset(reset, now);
        let mut map = self.read();
        let entry = map.entry(brain.to_string()).or_default();
        if eff > entry.reset_ts {
            let new_window = eff > entry.reset_ts + 60; // 只是微调恢复点不算新窗口
            entry.reset_ts = eff;
            if new_window {
                entry.notified = false;
            }
        }
        let out = entry.reset_ts;
        self.save(&map);
        out
    }

    /// 该 brain 若在限额期，返回登记条目。
    pub fn active(&self, brain: &str, now: u64) -> Option<LimitEntry> {
        let _g = LOCK.lock().unwrap();
        self.read().get(brain).filter(|e| now < e.reset_ts).cloned()
    }

    /// 通知节流：本窗口还没通知过 → 标记已通知并返回 true（该发）；否则 false。
    pub fn claim_notify(&self, brain: &str, now: u64) -> bool {
        let _g = LOCK.lock().unwrap();
        let mut map = self.read();
        let Some(entry) = map.get_mut(brain) else { return false };
        if now >= entry.reset_ts || entry.notified {
            return false;
        }
        entry.notified = true;
        self.save(&map);
        true
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 保守登记：拿不到时间/过去的时间 → now+30 分钟；正常时间原样。
    #[test]
    fn effective_reset_fallbacks() {
        assert_eq!(effective_reset(0, 1000), 1000 + 1800, "reset=0 保守 30 分钟");
        assert_eq!(effective_reset(-5, 1000), 1000 + 1800);
        assert_eq!(effective_reset(900, 1000), 1000 + 1800, "过去的时间同样保守兜底");
        assert_eq!(effective_reset(5000, 1000), 5000);
    }

    /// 顺延抖动：稳定（同 id 同值）、落在 [reset+60, reset+180)、不同任务大概率错开。
    #[test]
    fn defer_jitter_stable_and_bounded() {
        let a = defer_next_run(10_000, "task-a");
        assert_eq!(a, defer_next_run(10_000, "task-a"), "同任务确定性");
        assert!((10_060..10_180).contains(&a), "抖动区间 [60,180): {a}");
        let b = defer_next_run(10_000, "task-b");
        assert!((10_060..10_180).contains(&b));
        assert_ne!(a, b, "不同任务错开重试点");
    }

    /// 限额期判定。
    #[test]
    fn limited_window() {
        let e = LimitEntry { reset_ts: 2000, notified: false };
        assert!(is_limited(Some(&e), 1999));
        assert!(!is_limited(Some(&e), 2000), "到点即恢复");
        assert!(!is_limited(None, 0));
    }

    /// 登记/查询/通知节流全链路：同窗口只通知一次；新窗口重置可再通知；只延后不提前。
    #[test]
    fn store_register_active_and_notify_throttle() {
        let dir = std::env::temp_dir().join(format!("limits-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let st = LimitStore::new(&dir);
        assert!(st.active("claude", 100).is_none(), "未登记不拦");

        let ts = st.register("claude", 5000, 100);
        assert_eq!(ts, 5000);
        assert!(st.active("claude", 4999).is_some());
        assert!(st.active("claude", 5000).is_none(), "过点自动失效");
        assert!(st.active("codex", 200).is_none(), "各 brain 独立");

        // 通知节流：第一次 true、之后 false；持久化后依然 false
        assert!(st.claim_notify("claude", 200));
        assert!(!st.claim_notify("claude", 300), "同窗口只通知一次");
        // 只延后不提前
        assert_eq!(st.register("claude", 3000, 100), 5000, "更早的 reset 不回退");
        assert!(!st.claim_notify("claude", 300), "同窗口微调不重置通知");
        // 新窗口（明显更晚）：reset 更新且通知复位
        assert_eq!(st.register("claude", 9000, 6000), 9000);
        assert!(st.claim_notify("claude", 6100), "新窗口可再通知一次");
        assert!(!st.claim_notify("claude", 6200));
        // reset=0 兜底
        let t2 = st.register("grok", 0, 1_000_000);
        assert_eq!(t2, 1_000_000 + 1800);
        std::fs::remove_dir_all(&dir).ok();
    }
}
