//! Explicit per-run API execution budgets. Managed continuation is not a retry:
//! every request consumes the same budget and continues the same transcript.
use std::fmt;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

#[derive(Clone, Debug)]
pub(crate) struct ApiExecutionPolicy {
    pub(crate) managed: bool,
    pub(crate) max_requests: usize,
    pub(crate) total_timeout: Option<Duration>,
    pub(crate) max_total_tokens: Option<u64>,
    pub(crate) stage_requests: usize,
    pub(crate) max_no_progress_rounds: usize,
    pub(crate) max_failure_rounds: usize,
    // Clones observe this attempt's actual HTTP requests. A fallback receives
    // a fresh policy/counter with limits reduced by the caller, not this Arc.
    request_count: Arc<AtomicUsize>,
}

impl Default for ApiExecutionPolicy {
    fn default() -> Self {
        Self {
            managed: false,
            max_requests: 32,
            total_timeout: None,
            max_total_tokens: None,
            stage_requests: 32,
            max_no_progress_rounds: 3,
            max_failure_rounds: 3,
            request_count: Arc::new(AtomicUsize::new(0)),
        }
    }
}

impl ApiExecutionPolicy {
    pub(crate) fn requests_used(&self) -> usize {
        self.request_count.load(Ordering::Relaxed)
    }

    pub(crate) fn record_request(&self) {
        self.request_count.fetch_add(1, Ordering::Relaxed);
    }

    pub fn managed(remaining_week_tokens: Option<u64>) -> Self {
        const MANAGED_TOKEN_LIMIT: u64 = 8_000_000;
        Self {
            managed: true,
            max_requests: 96,
            total_timeout: Some(Duration::from_secs(45 * 60)),
            max_total_tokens: Some(
                remaining_week_tokens.map_or(MANAGED_TOKEN_LIMIT, |remaining| {
                    remaining.min(MANAGED_TOKEN_LIMIT)
                }),
            ),
            ..Self::default()
        }
    }

    pub(crate) fn check_boundary(
        &self,
        requests: usize,
        total_tokens: u64,
        elapsed: Duration,
    ) -> Result<(), StopReason> {
        if self.total_timeout.is_some_and(|timeout| elapsed >= timeout) {
            return Err(StopReason::Deadline);
        }
        if self
            .max_total_tokens
            .is_some_and(|limit| total_tokens >= limit)
        {
            return Err(StopReason::TokenBudget {
                limit: self.max_total_tokens.unwrap_or(0),
            });
        }
        if requests >= self.max_requests {
            return Err(StopReason::RequestBudget {
                limit: self.max_requests,
                managed: self.managed,
            });
        }
        Ok(())
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum StopReason {
    RequestBudget { limit: usize, managed: bool },
    TokenBudget { limit: u64 },
    Deadline,
    NoProgress,
    RepeatedFailure,
    RepeatedEffect,
    UncertainEffect,
    MissingUsage,
}

impl fmt::Display for StopReason {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::RequestBudget { limit, managed: true } => write!(f, "托管请求次数上限：已完成 {limit} 次模型请求，当前完整结果已保存，未自动重新开始或切换模型"),
            Self::RequestBudget { limit, managed: false } => write!(f, "本轮已达到 {limit} 次模型调用上限，已停止；请核对执行记录，避免重复操作"),
            Self::TokenBudget { limit } => write!(f, "托管累计 token 预算已达到或耗尽（本次上限 {limit}），已停止下一次请求；预算按请求边界检查，单次已发请求可能跨过上限"),
            Self::Deadline => write!(f, "托管执行时长上限已到，已停止当前请求和本地工具；请核对执行记录，未确认的操作不会自动重放"),
            Self::NoProgress => write!(f, "托管连续无进展：相同工具、参数和结果重复出现，已保存结果并停止自动续跑"),
            Self::RepeatedFailure => write!(f, "托管工具连续失败：只读操作多轮未能取得进展，已保存结果并停止自动续跑"),
            Self::RepeatedEffect => write!(f, "托管已阻止重复操作：没有中间不同写操作的相同操作再次被请求，未重复执行，请核对已保存的结果"),
            Self::UncertainEffect => write!(f, "托管操作结果未确认：写入或命令返回失败，可能已有部分副作用；已停止后续操作和模型请求，请核对执行记录"),
            Self::MissingUsage => write!(f, "服务商未返回用量，已保存本轮结果并停止自动续跑，无法可靠核算剩余 token 预算"),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn request_counter_is_shared_only_by_clones_of_the_same_attempt() {
        let primary = ApiExecutionPolicy::managed(None);
        let observer = primary.clone();
        primary.record_request();
        primary.record_request();
        assert_eq!(observer.requests_used(), 2);
        let mut fallback = ApiExecutionPolicy::managed(None);
        fallback.max_requests -= observer.requests_used();
        fallback.record_request();
        assert_eq!(fallback.max_requests, 94);
        assert_eq!(fallback.requests_used(), 1);
        assert_eq!(primary.requests_used(), 2);
    }
    #[test]
    fn ordinary_policy_preserves_original_request_ceiling_only() {
        let policy = ApiExecutionPolicy::default();
        assert!(!policy.managed);
        assert_eq!(policy.max_requests, 32);
        assert_eq!(policy.max_total_tokens, None);
        assert_eq!(policy.total_timeout, None);
        assert!(policy
            .check_boundary(31, u64::MAX, Duration::from_secs(90_000))
            .is_ok());
    }
    #[test]
    fn managed_limits_include_remaining_weekly_allowance_and_zero() {
        let full = ApiExecutionPolicy::managed(None);
        assert_eq!(full.max_requests, 96);
        assert_eq!(full.total_timeout, Some(Duration::from_secs(2700)));
        assert_eq!(full.max_total_tokens, Some(8_000_000));
        assert_eq!(
            ApiExecutionPolicy::managed(Some(9_000_000)).max_total_tokens,
            Some(8_000_000)
        );
        assert_eq!(
            ApiExecutionPolicy::managed(Some(400)).max_total_tokens,
            Some(400)
        );
        assert!(matches!(
            ApiExecutionPolicy::managed(Some(0)).check_boundary(0, 0, Duration::ZERO),
            Err(StopReason::TokenBudget { limit: 0 })
        ));
    }
}
