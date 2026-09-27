//! A saved conversation is not necessarily a successful model turn.
use crate::{convo::Conversation, tasks::ScheduledTask};
use std::collections::HashMap;

pub fn error_summary(text: &str) -> String {
    let parsed = serde_json::from_str::<serde_json::Value>(text).ok();
    let message = parsed
        .as_ref()
        .and_then(|v| {
            v.pointer("/error/message")
                .or_else(|| v.get("message"))
                .and_then(serde_json::Value::as_str)
        })
        .unwrap_or(text)
        .trim();
    if message.is_empty() {
        return "模型执行失败，请查看对话详情".into();
    }
    if let Some(model) = message.strip_prefix("The '").and_then(|s| {
        s.strip_suffix("' model is not supported when using Codex with a ChatGPT account.")
    }) {
        return format!("当前 Codex/ChatGPT 登录通道不支持 {model}；请更新 Codex CLI 并检查模型可用性。原始错误见对话详情。");
    }
    message.chars().take(400).collect()
}

/// Historical checks stop at the run timestamp: later manual replies must not
/// change a past run's result. Only an explicit error is evidence of failure.
pub fn failure_at(conv: &Conversation, until: u64) -> Option<String> {
    conv.messages
        .iter()
        .rev()
        .find(|m| m.role == "assistant" && m.ts <= until)
        .filter(|m| m.error && m.limit_reset.is_none())
        .map(|m| error_summary(&m.text))
}

/// Read-only projection for old runs whose model errors were saved as success.
/// Does not rewrite tasks, models, schedules, or conversation contents.
pub fn correct_saved_outcomes(
    task: &mut ScheduledTask,
    conversations: &HashMap<String, Conversation>,
) {
    for run in &mut task.history {
        let mut changed = false;
        for site in &mut run.sites {
            if !site.ok || site.deferred {
                continue;
            }
            if let Some(error) = conversations
                .get(&site.conv_id)
                .and_then(|c| failure_at(c, run.ts))
            {
                site.ok = false;
                site.error = error;
                changed = true;
            }
        }
        if changed {
            run.ok = false;
            let failed = run.sites.iter().find(|s| !s.ok && !s.deferred).unwrap();
            run.summary = format!("{}: {}", failed.slug, failed.error);
        }
        if changed && run.ts == task.last_run {
            task.last_status = "error".into();
            task.last_summary = run.summary.clone();
        }
    }
    // Without a matching history entry, last_conv_id may belong to an older
    // run (for example a later budget-gate skip). Do not infer its outcome.
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    fn conv(messages: serde_json::Value) -> Conversation {
        serde_json::from_value(json!({"id":"c", "conn_id":"x", "conn_name":"", "site_slug":"s", "site_name":"", "task_type":"free", "brain":"codex", "model":"gpt-6-sol", "session_ref":"", "title":"", "messages":messages, "status":"idle", "created_at":0, "updated_at":0})).unwrap()
    }
    #[test]
    fn task_outcome_extracts_model_error_without_json_wrapper() {
        let error = r#"{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"The 'gpt-6-sol' model is not supported when using Codex with a ChatGPT account."}}"#;
        let c = conv(json!([{"role":"assistant","text":error,"error":true,"ts":10}]));
        let summary = failure_at(&c, 10).unwrap();
        assert!(summary.contains("不支持 gpt-6-sol"));
        assert!(summary.contains("更新 Codex CLI"));
        assert!(!summary.contains("invalid_request_error"));
        assert!(failure_at(&c, 9).is_none());
        assert!(!error_summary("").is_empty());
    }
    #[test]
    fn task_outcome_preserves_success_limits_and_past_results() {
        let c = conv(json!([
            {"role":"assistant","text":"failed","error":true,"ts":10},
            {"role":"assistant","text":"fixed manually","ts":20}
        ]));
        assert_eq!(failure_at(&c, 10).as_deref(), Some("failed"));
        assert!(failure_at(&c, 20).is_none());
        assert!(failure_at(
            &conv(json!([{"role":"assistant","text":"quota","error":true,"limit_reset":0,"ts":1}])),
            10
        )
        .is_none());
        assert!(failure_at(
            &conv(json!([{"role":"assistant","text":"Example: invalid_request_error","ts":1}])),
            10
        )
        .is_none());
    }
    #[test]
    fn task_outcome_corrects_history_without_changing_configuration() {
        let mut t:ScheduledTask=serde_json::from_value(json!({"id":"t","conn_id":"x","conn_name":"","site_slug":"s","site_name":"s","task_type":"free","brain":"codex","model":"gpt-6-sol","title":"test","prompt":"task","interval_minutes":1440,"next_run":99,"enabled":true,"last_run":10,"last_status":"ok","last_summary":"wrong success","last_conv_id":"c","runs":1,"created_at":0,"updated_at":0,"history":[{"ts":10,"ok":true,"summary":"wrong success","sites":[{"slug":"s","ok":true,"conv_id":"c"}]}]})).unwrap();
        let c = conv(
            json!([{"role":"assistant","text":"model unavailable","error":true,"ts":10},{"role":"assistant","text":"later reply","ts":11}]),
        );
        correct_saved_outcomes(&mut t, &HashMap::from([("c".into(), c)]));
        assert_eq!(t.last_status, "error");
        assert!(!t.history[0].ok);
        assert!(!t.history[0].sites[0].ok);
        assert_eq!(t.model, "gpt-6-sol");
        assert_eq!(t.next_run, 99);
        assert!(t.enabled);
        t.last_run = 30;
        t.last_status = "ok".into();
        t.last_summary = "预算上限，本轮跳过".into();
        let c = conv(json!([{"role":"assistant","text":"older failure","error":true,"ts":10}]));
        correct_saved_outcomes(&mut t, &HashMap::from([("c".into(), c)]));
        assert_eq!(t.last_status, "ok", "an older conversation must not override a later skipped run");
        assert_eq!(t.last_summary, "预算上限，本轮跳过");
    }
}
