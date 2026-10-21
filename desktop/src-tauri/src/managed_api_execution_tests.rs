//! Store-only policy wiring and concurrency regressions. No API/CLI or production data.
use super::*;

struct Fixture {
    dir: tempfile::TempDir,
    tasks: tasks::TaskStore,
    managed: managed::ManagedStore,
    convos: convo::ConvStore,
}

impl Fixture {
    fn new(budget: u64) -> Self {
        let dir = tempfile::tempdir().unwrap();
        let tasks = tasks::TaskStore::new(dir.path());
        let managed = managed::ManagedStore::new(dir.path());
        let convos = convo::ConvStore::new(dir.path());
        for id in ["daily", "audit", "report"] {
            tasks.upsert(task(id)).unwrap();
        }
        let site = serde_json::from_value(serde_json::json!({
            "id":"managed-site", "conn_id":"conn", "site_slug":"site", "site_name":"站点",
            "mode":"plan", "level":"l0", "weekly_post_limit":3, "plan":"计划",
            "task_ids":["daily","audit","report"], "paused":false, "token_weekly_budget":budget,
            "created_at":1, "updated_at":1
        }))
        .unwrap();
        managed.upsert(site).unwrap();
        Self {
            dir,
            tasks,
            managed,
            convos,
        }
    }

    fn conversation(&self, id: &str, tokens: u64) {
        self.convos.upsert(serde_json::from_value(serde_json::json!({
            "id":id, "conn_id":"conn", "conn_name":"连接", "site_slug":"site", "site_name":"站点",
            "task_type":"article", "brain":"api:profile", "model":"model", "perm_mode":"full",
            "session_ref":format!("api:{}", uuid::Uuid::new_v4()), "title":"任务",
            "messages":[], "status":"idle", "created_at":1, "updated_at":1, "total_tokens":tokens
        })).unwrap()).unwrap();
    }

    fn history(&self, task_id: &str, ts: u64, final_id: &str, attempts: &[&str]) {
        self.tasks
            .mutate(task_id, |task| {
                task.history.push(tasks::TaskRun {
                    ts,
                    ok: false,
                    summary: "result".into(),
                    deferred: false,
                    sites: vec![tasks::TaskRunSite {
                        slug: "site".into(),
                        ok: false,
                        conv_id: final_id.into(),
                        attempt_conv_ids: attempts.iter().map(|id| (*id).to_string()).collect(),
                        error: "error".into(),
                        deferred: false,
                        executor: "api:profile / model".into(),
                        fallback_used: true,
                    }],
                })
            })
            .unwrap();
    }

    fn policy(&self, brain: &str, pending: &[String]) -> api_execution::ApiExecutionPolicy {
        managed_api_policy_for_attempt(
            &self.managed,
            &self.tasks,
            &self.convos,
            Some("managed-site"),
            &self.tasks.get("daily").unwrap(),
            brain,
            pending,
        )
        .unwrap()
    }
}

fn task(id: &str) -> ScheduledTask {
    serde_json::from_value(serde_json::json!({
        "id":id, "conn_id":"conn", "conn_name":"连接", "site_slug":"site", "site_name":"站点",
        "task_type":"article", "brain":"api:profile", "model":"model", "title":"托管 · 每日内容",
        "prompt":"original prompt", "interval_minutes":1440, "next_run":123456, "enabled":true,
        "last_run":0, "last_status":"", "last_summary":"", "last_conv_id":"", "runs":0,
        "created_at":1, "updated_at":1
    }))
    .unwrap()
}

#[test]
fn managed_api_budget_deduplicates_history_and_counts_failed_primary_and_pending_usage() {
    let f = Fixture::new(1_000);
    let week = managed::week_start_local().max(0) as u64;
    f.conversation("primary", 100);
    f.conversation("fallback", 200);
    f.conversation("previous-week", 90_000);
    f.conversation("pending-current", 250);
    f.history(
        "daily",
        week,
        "fallback",
        &["primary", "fallback", "primary"],
    );
    f.history("audit", week + 1, "fallback", &["primary"]);
    f.history("report", week.saturating_sub(1), "previous-week", &[]);
    let site = f.managed.get("managed-site").unwrap();
    assert_eq!(managed_week_tokens(&f.tasks, &f.convos, &site, week), 300);
    let policy = f.policy(
        "api:profile",
        &[
            "pending-current".into(),
            "pending-current".into(),
            "primary".into(),
        ],
    );
    assert!(policy.managed);
    assert_eq!(policy.max_requests, 96);
    assert_eq!(policy.total_timeout, Some(Duration::from_secs(45 * 60)));
    assert_eq!(policy.max_total_tokens, Some(450));
    // Once this run is in history, it consumes the same amount, not twice.
    f.history("daily", week + 2, "pending-current", &["pending-current"]);
    assert_eq!(f.policy("api:profile", &[]).max_total_tokens, Some(450));
    assert_eq!(f.tasks.get("daily").unwrap().history.len(), 2);
}

#[test]
fn managed_api_budget_exhaustion_is_zero_not_unlimited_and_uses_latest_site_budget() {
    let f = Fixture::new(300);
    let week = managed::week_start_local().max(0) as u64;
    f.conversation("last", 200);
    f.conversation("current", 150);
    f.history("daily", week, "last", &[]);
    let exhausted = f.policy("api:profile", &["current".into()]);
    assert_eq!(exhausted.max_total_tokens, Some(0));
    assert!(exhausted.check_boundary(0, 0, Duration::ZERO).is_err());
    f.managed
        .mutate("managed-site", now_secs(), |site| {
            site.token_weekly_budget = 700
        })
        .unwrap();
    assert_eq!(
        f.policy("api:profile", &["current".into()])
            .max_total_tokens,
        Some(350)
    );
    f.managed
        .mutate("managed-site", now_secs(), |site| {
            site.token_weekly_budget = 0
        })
        .unwrap();
    assert_eq!(
        f.policy("api:profile", &[]).max_total_tokens,
        Some(8_000_000)
    );
    assert_eq!(
        f.policy("api:profile", &["current".into()])
            .max_total_tokens,
        Some(7_999_850)
    );
    f.conversation("overspent-primary", 9_000_000);
    assert_eq!(
        f.policy("api:profile", &["overspent-primary".into()])
            .max_total_tokens,
        Some(0)
    );
}

#[test]
fn managed_api_policy_never_expands_cli_or_unmanaged_task_limits() {
    let f = Fixture::new(300);
    for brain in ["claude", "codex", "grok"] {
        let policy = f.policy(brain, &[]);
        assert!(!policy.managed);
        assert_eq!(policy.max_requests, 32);
        assert_eq!(policy.total_timeout, None);
        assert_eq!(policy.max_total_tokens, None);
        let mut cli = task("daily");
        cli.brain = brain.into();
        assert!(!task_has_api_executor(&cli));
        cli.fallback_brain = "api:fallback".into();
        cli.fallback_model = "model".into();
        assert!(task_has_api_executor(&cli));
    }
    let fake = task("not-a-managed-task");
    assert!(managed_owner_for_task(&f.managed, &fake).is_none());
    let regular = managed_api_policy_for_attempt(
        &f.managed,
        &f.tasks,
        &f.convos,
        None,
        &fake,
        "api:profile",
        &[],
    )
    .unwrap();
    assert!(!regular.managed);
    assert_eq!(regular.max_requests, 32);
    assert!(managed_api_policy_for_attempt(
        &f.managed,
        &f.tasks,
        &f.convos,
        Some("managed-site"),
        &fake,
        "api:profile",
        &[]
    )
    .is_err());
    for (conn, site) in [("other", "site"), ("conn", "other")] {
        let mut foreign = task("daily");
        foreign.conn_id = conn.into();
        foreign.site_slug = site.into();
        assert!(managed_owner_for_task(&f.managed, &foreign).is_none());
    }
    let mut multi = task("daily");
    multi.site_slugs = vec!["site".into(), "other".into()];
    assert!(managed_owner_for_task(&f.managed, &multi).is_none());
}

#[test]
fn managed_api_budget_does_not_charge_another_site_or_workspace() {
    let f = Fixture::new(300);
    f.conversation("foreign", 100_000);
    f.convos
        .mutate("foreign", now_secs(), |conv| {
            conv.conn_id = "another-connection".into()
        })
        .unwrap();
    f.history(
        "daily",
        managed::week_start_local().max(0) as u64,
        "foreign",
        &["foreign"],
    );
    assert_eq!(
        f.policy("api:profile", &["foreign".into()])
            .max_total_tokens,
        Some(300)
    );
}

#[tokio::test]
async fn managed_api_lock_is_site_scoped_and_rechecks_waited_task_and_budget() {
    let f = Fixture::new(1_000);
    let second_dir = tempfile::tempdir().unwrap();
    let lock = managed_api_run_lock(f.dir.path(), "managed-site");
    let same = managed_api_run_lock(f.dir.path(), "managed-site");
    assert!(Arc::ptr_eq(&lock, &same));
    let guard = lock.lock_owned().await;
    assert!(same.try_lock().is_err());
    assert!(managed_api_run_lock(f.dir.path(), "another-site")
        .try_lock()
        .is_ok());
    assert!(managed_api_run_lock(second_dir.path(), "managed-site")
        .try_lock()
        .is_ok());
    let ready = Arc::new(tokio::sync::Notify::new());
    let notified = ready.notified();
    let (tasks, managed, convos) = (f.tasks.clone(), f.managed.clone(), f.convos.clone());
    let ready2 = ready.clone();
    let waiting = tokio::spawn(async move {
        ready2.notify_one();
        let _guard = same.lock_owned().await;
        let current = refresh_managed_api_task(&tasks, &managed, "daily", "managed-site").unwrap();
        let policy = managed_api_policy_for_attempt(
            &managed,
            &tasks,
            &convos,
            Some("managed-site"),
            &current,
            &current.brain,
            &[],
        )
        .unwrap();
        (current, policy)
    });
    notified.await;
    f.tasks
        .mutate("daily", |task| {
            task.prompt = "updated while queued".into();
            task.model = "new-model".into();
        })
        .unwrap();
    f.managed
        .mutate("managed-site", now_secs(), |site| {
            site.mode = managed::ManagedMode::Growth
        })
        .unwrap();
    f.conversation("preceding-task", 600);
    f.history("audit", now_secs(), "preceding-task", &[]);
    drop(guard);
    let (current, policy) = waiting.await.unwrap();
    assert_eq!(current.prompt, "updated while queued");
    assert_eq!(current.model, "new-model");
    assert_eq!(policy.max_total_tokens, Some(400));
    assert_eq!(scheduled_task_perm_mode(&f.managed.list(), "audit"), "plan");
    assert!(managed_api_run_lock(f.dir.path(), "managed-site")
        .try_lock()
        .is_ok());
}

#[test]
fn managed_api_waited_task_rejects_pause_fuse_disable_and_removed_ownership() {
    let f = Fixture::new(0);
    f.managed
        .mutate("managed-site", 2, |site| site.paused = true)
        .unwrap();
    assert!(refresh_managed_api_task(&f.tasks, &f.managed, "daily", "managed-site").is_err());
    f.managed
        .mutate("managed-site", 3, |site| {
            site.paused = false;
            site.fused_at = 1;
        })
        .unwrap();
    assert!(refresh_managed_api_task(&f.tasks, &f.managed, "daily", "managed-site").is_err());
    f.managed
        .mutate("managed-site", 4, |site| site.fused_at = 0)
        .unwrap();
    f.tasks
        .mutate("daily", |task| task.enabled = false)
        .unwrap();
    assert!(refresh_managed_api_task(&f.tasks, &f.managed, "daily", "managed-site").is_err());
    f.tasks.mutate("daily", |task| task.enabled = true).unwrap();
    f.managed.remove("managed-site").unwrap();
    assert!(refresh_managed_api_task(&f.tasks, &f.managed, "daily", "managed-site").is_err());
}

#[test]
fn managed_api_safety_stops_never_trigger_fallback_or_rewrite_failed_runs() {
    use api_execution::StopReason;
    for reason in [
        StopReason::RequestBudget {
            limit: 96,
            managed: true,
        },
        StopReason::TokenBudget { limit: 0 },
        StopReason::Deadline,
        StopReason::NoProgress,
        StopReason::RepeatedFailure,
        StopReason::RepeatedEffect,
        StopReason::UncertainEffect,
        StopReason::MissingUsage,
    ] {
        let error = format!("{reason}\n此前输出中提到 model unavailable");
        assert!(!retryable_executor_error(&error), "{error}");
    }
    assert!(retryable_executor_error("model unavailable"));
    assert!(retryable_executor_error("HTTP 429 Too Many Requests"));
    let f = Fixture::new(0);
    f.history("daily", now_secs(), "failed-conv", &[]);
    let before = serde_json::to_value(f.tasks.list()).unwrap();
    let _ = f.policy("api:profile", &[]);
    assert_eq!(
        serde_json::to_value(f.tasks.list()).unwrap(),
        before,
        "constructing policy never retries saved failures"
    );
}

#[test]
fn managed_api_attempt_history_is_backward_compatible() {
    let old: tasks::TaskRunSite = serde_json::from_value(serde_json::json!({
        "slug":"site", "ok":true, "conv_id":"old-conv", "error":""
    }))
    .unwrap();
    assert!(old.attempt_conv_ids.is_empty());
    assert_eq!(old.conv_id, "old-conv");
}

#[test]
fn managed_api_fallback_shares_request_and_elapsed_limits_without_resetting_them() {
    let primary = api_execution::ApiExecutionPolicy::managed(None);
    let observer = primary.clone();
    for _ in 0..4 {
        primary.record_request();
    }
    let mut fallback = api_execution::ApiExecutionPolicy::managed(None);
    account_managed_fallback_budget(&mut fallback, &observer, Duration::from_secs(120));
    assert_eq!(fallback.max_requests, 92);
    assert_eq!(fallback.total_timeout, Some(Duration::from_secs(2_580)));
    assert_eq!(fallback.requests_used(), 0);
    let mut expired = api_execution::ApiExecutionPolicy::managed(None);
    account_managed_fallback_budget(&mut expired, &observer, Duration::from_secs(3_000));
    assert!(expired.check_boundary(0, 0, Duration::ZERO).is_err());
    let mut regular = api_execution::ApiExecutionPolicy::default();
    account_managed_fallback_budget(&mut regular, &observer, Duration::from_secs(3_000));
    assert_eq!(regular.max_requests, 32);
    assert_eq!(regular.total_timeout, None);
}

#[test]
fn managed_api_history_failure_pauses_only_managed_api_and_reports_pause_write_failure() {
    let f = Fixture::new(1_000);
    std::fs::create_dir(f.dir.path().join("tasks.json.tmp")).unwrap();
    let saved = f.tasks.mutate("daily", |task| task.runs += 1);
    assert!(saved.is_err());
    let error = managed_api_history_error(
        &f.managed,
        &f.tasks,
        &f.convos,
        Some("managed-site"),
        saved,
        &[],
    );
    assert!(error.contains("运行记录保存失败"));
    let paused = f.managed.get("managed-site").unwrap();
    assert!(paused.paused);
    assert!(paused.fused_at > 0);
    assert!(refresh_managed_api_task(&f.tasks, &f.managed, "daily", "managed-site").is_err());

    let f = Fixture::new(1_000);
    let before = serde_json::to_value(f.managed.list()).unwrap();
    assert!(managed_api_history_error(
        &f.managed,
        &f.tasks,
        &f.convos,
        None,
        Err("disk error".into()),
        &[]
    )
    .is_empty());
    assert_eq!(serde_json::to_value(f.managed.list()).unwrap(), before);
    std::fs::create_dir(f.dir.path().join("managed.json.tmp")).unwrap();
    let error = managed_api_history_error(
        &f.managed,
        &f.tasks,
        &f.convos,
        Some("managed-site"),
        Err("disk error".into()),
        &[],
    );
    assert!(error.contains("暂停状态保存也失败"));
    assert!(error.contains("请立即关闭 Pilot"));
}

#[test]
fn managed_api_paid_attempt_requires_saved_idle_conversation_before_releasing_queue() {
    for status in ["missing", "running"] {
        let f = Fixture::new(1_000);
        if status == "running" {
            f.conversation("paid-attempt", 0);
            f.convos
                .mutate("paid-attempt", now_secs(), |conv| {
                    conv.status = "running".into()
                })
                .unwrap();
        }
        let error = managed_api_history_error(
            &f.managed,
            &f.tasks,
            &f.convos,
            Some("managed-site"),
            Ok(f.tasks.get("daily")),
            &["paid-attempt".into()],
        );
        assert!(
            error.contains("付费 API 尝试的会话/用量未完整保存"),
            "{status}: {error}"
        );
        assert!(f.managed.get("managed-site").unwrap().paused);
        assert!(!f.tasks.get("daily").unwrap().enabled);
    }

    let f = Fixture::new(1_000);
    // Validation may fail before creating a conversation or dispatching any request.
    // Such an attempt has no paid ID and must not falsely fuse the site.
    assert!(managed_api_history_error(
        &f.managed,
        &f.tasks,
        &f.convos,
        Some("managed-site"),
        Ok(f.tasks.get("daily")),
        &[],
    )
    .is_empty());
    assert!(!f.managed.get("managed-site").unwrap().paused);
    f.conversation("paid-and-saved", 125);
    assert!(managed_api_history_error(
        &f.managed,
        &f.tasks,
        &f.convos,
        Some("managed-site"),
        Ok(f.tasks.get("daily")),
        &["paid-and-saved".into()],
    )
    .is_empty());
    assert!(!f.managed.get("managed-site").unwrap().paused);
}

#[test]
fn managed_api_wiring_keeps_manual_chat_and_cli_routes_unchanged() {
    let source = include_str!("lib.rs");
    for name in [
        "start_conversation",
        "send_message",
        "retry_turn",
        "run_fresh_turn",
    ] {
        let function = source.split(&format!("async fn {name}(")).nth(1).unwrap();
        let function = function.split("\n#[tauri::command]").next().unwrap();
        assert!(
            function.contains("Default::default(),"),
            "{name} must use the ordinary policy"
        );
    }
    let agent_source = include_str!("agent.rs");
    let cli_route = agent_source
        .split("if !matches!(brain.as_str(), \"claude\" | \"codex\" | \"grok\")")
        .nth(1)
        .unwrap();
    let cli_route = cli_route.split("\nfn ").next().unwrap();
    assert!(
        !cli_route.contains("api_execution_policy"),
        "native CLI arguments/lifecycle must not depend on API budgets"
    );
}
