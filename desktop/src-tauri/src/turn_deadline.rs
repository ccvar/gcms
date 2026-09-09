//! Optional deadline for bounded foreground jobs. Cancellation is repeated until
//! the owner returns, including when a CLI registers late during Windows startup.
use crate::agent::RunRegistry;
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc,
};
use std::time::Duration;

pub struct TurnDeadline {
    expired: Arc<AtomicBool>,
    task: tokio::task::JoinHandle<()>,
}

impl TurnDeadline {
    pub fn start(runs: RunRegistry, id: String, delay: Duration) -> Self {
        let expired = Arc::new(AtomicBool::new(false));
        let flag = expired.clone();
        let task = tokio::spawn(async move {
            tokio::time::sleep(delay).await;
            flag.store(true, Ordering::SeqCst);
            loop {
                runs.cancel(&id);
                tokio::time::sleep(Duration::from_millis(250)).await;
            }
        });
        Self { expired, task }
    }
    pub fn expired(&self) -> bool {
        self.expired.load(Ordering::SeqCst)
    }
}

impl Drop for TurnDeadline {
    fn drop(&mut self) {
        self.task.abort();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn cancels_a_late_registration_without_canceling_other_turns() {
        let runs = RunRegistry::default();
        let (_, mut other) = runs.register("other");
        let guard = TurnDeadline::start(runs.clone(), "plan".into(), Duration::ZERO);
        tokio::time::sleep(Duration::from_millis(20)).await;
        assert!(guard.expired());
        let (_, rx) = runs.register("plan");
        tokio::time::timeout(Duration::from_secs(2), rx)
            .await
            .unwrap()
            .unwrap();
        assert!(runs.is_canceled("plan"));
        assert!(other.try_recv().is_err());
        runs.unregister("plan");
        let (_, retry) = runs.register("plan");
        tokio::time::timeout(Duration::from_secs(2), retry)
            .await
            .unwrap()
            .unwrap();
    }
    #[tokio::test]
    async fn finishing_before_deadline_disarms_it() {
        let runs = RunRegistry::default();
        let (_, mut rx) = runs.register("plan");
        let guard = TurnDeadline::start(runs.clone(), "plan".into(), Duration::from_millis(10));
        drop(guard);
        tokio::time::sleep(Duration::from_millis(30)).await;
        assert!(!runs.is_canceled("plan"));
        assert!(rx.try_recv().is_err());
    }
}
