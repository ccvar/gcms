//! Local mock HTTP + real temporary workspace: no credential vault or paid calls.
use super::tests::{conn, event, profile, server};
use super::*;
use std::sync::Arc;

fn response(blocks: Vec<Value>, stop: &str) -> String {
    let mut body = event(json!({"type":"message_start","message":{"usage":{"input_tokens":10}}}));
    for (index, block) in blocks.into_iter().enumerate() {
        if block["type"] == "text" {
            body += &event(
                json!({"type":"content_block_start","index":index,"content_block":{"type":"text","text":""}}),
            );
            body += &event(
                json!({"type":"content_block_delta","index":index,"delta":{"type":"text_delta","text":block["text"]}}),
            );
        } else {
            body +=
                &event(json!({"type":"content_block_start","index":index,"content_block":block}));
        }
        body += &event(json!({"type":"content_block_stop","index":index}));
    }
    body += &event(
        json!({"type":"message_delta","delta":{"stop_reason":stop},"usage":{"output_tokens":20}}),
    );
    body += &event(json!({"type":"message_stop"}));
    body
}

fn write(id: &str, content: &str) -> Value {
    json!({"type":"tool_use","id":id,"name":"write_file","input":{"path":"created.txt","content":content,"overwrite":false}})
}
fn text_block(text: &str) -> Value {
    json!({"type":"text","text":text})
}

struct Run {
    result: TurnResult,
    history: History,
    events: Vec<Value>,
    requests: Vec<(String, Value)>,
}

async fn run(responses: Vec<(u16, String)>, root: &Path) -> Run {
    let (url, server) = server(responses).await;
    let p = profile("anthropic_messages", &url);
    let c = conn();
    let work = std::fs::canonicalize(root).unwrap();
    let tools = tool_runtime::Tools::new(
        &c,
        &work,
        &work.join("data"),
        &[],
        PermMode::Full,
        "fixture",
        None,
        "",
    )
    .unwrap();
    let mut history = History {
        scope: "test".into(),
        protocol: p.protocol.clone(),
        system: "test instructions".into(),
        items: vec![json!({"role":"user","content":"write a file"})],
        interrupted: false,
    };
    let path = work.join("history.json");
    save_history(&path, &history).unwrap();
    let events = Arc::new(Mutex::new(Vec::<Value>::new()));
    let captured = events.clone();
    let channel = Channel::new(move |body| {
        if let tauri::ipc::InvokeResponseBody::Json(raw) = body {
            captured
                .lock()
                .unwrap()
                .push(serde_json::from_str(&raw).unwrap());
        }
        Ok(())
    });
    let mut result = TurnResult {
        ok: false,
        text: String::new(),
        tools: vec![],
        error: String::new(),
        session_ref: String::new(),
        proposal: None,
        usage: None,
        limit_reset: None,
    };
    let outcome = tokio::time::timeout(
        Duration::from_secs(10),
        execute_model_loop(
            &p,
            "test-model",
            "",
            "fake-fixture-key",
            &tools,
            &mut history,
            &path,
            &channel,
            "",
            &mut result,
        ),
    )
    .await
    .expect("bounded model loop");
    result.ok = outcome.is_ok();
    result.error = outcome.err().unwrap_or_default();
    let history = serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    let requests = tokio::time::timeout(Duration::from_secs(2), server)
        .await
        .unwrap()
        .unwrap();
    let events = events.lock().unwrap().clone();
    Run {
        result,
        history,
        requests,
        events,
    }
}

#[tokio::test]
async fn output_recovery_retains_checkpoint_without_replaying_completed_write() {
    let dir = tempfile::tempdir().unwrap();
    let run = run(
        vec![
            (
                200,
                response(
                    vec![text_block("开始。"), write("completed", "created once")],
                    "tool_use",
                ),
            ),
            (
                200,
                response(
                    vec![text_block("半截输出"), write("discarded", "must not write")],
                    "max_tokens",
                ),
            ),
            (200, response(vec![text_block("完成。")], "end_turn")),
        ],
        dir.path(),
    )
    .await;
    assert!(run.result.ok, "{}", run.result.error);
    assert_eq!(
        std::fs::read_to_string(dir.path().join("created.txt")).unwrap(),
        "created once"
    );
    assert_eq!(run.result.tools.len(), 1);
    assert_eq!(run.result.text, "开始。完成。");
    assert_eq!(run.result.usage.as_ref().unwrap().input, 30);
    assert_eq!(run.result.usage.as_ref().unwrap().output, 60);
    assert!(!run.history.interrupted);
    assert!(!serde_json::to_string(&run.history.items)
        .unwrap()
        .contains("discarded"));
    assert_eq!(run.requests.len(), 3);
    assert_eq!(run.requests[1].1["messages"], run.requests[2].1["messages"]);
    assert!(run.requests[2].1["messages"]
        .to_string()
        .contains("tool_result"));
    for (_, request) in &run.requests {
        assert_eq!(request["max_tokens"], 512);
    }
    let mut visible = String::new();
    for e in &run.events {
        match e["type"].as_str() {
            Some("delta") => visible.push_str(e["text"].as_str().unwrap()),
            Some("text_reset") => visible = e["text"].as_str().unwrap().into(),
            _ => {}
        }
    }
    assert_eq!(visible, run.result.text);
    assert_eq!(
        run.events
            .iter()
            .filter(|e| e["type"] == "text_reset")
            .count(),
        1
    );
}

#[tokio::test]
async fn output_recovery_is_bounded_and_never_executes_capped_calls() {
    let dir = tempfile::tempdir().unwrap();
    let capped = response(
        vec![write("not-executed", "unsafe"), text_block("truncated")],
        "max_tokens",
    );
    let run = run(vec![(200, capped.clone()), (200, capped)], dir.path()).await;
    assert!(!run.result.ok);
    assert!(run.result.error.contains("自动恢复一次"));
    assert!(run.result.error.contains("无需重建会话"));
    assert!(!dir.path().join("created.txt").exists());
    assert!(run.result.tools.is_empty());
    assert_eq!(run.history.items.len(), 1);
    assert!(!run.history.interrupted);
    assert_eq!(run.requests.len(), 2);
    assert_eq!(run.result.usage.unwrap().output, 40);
}

#[tokio::test]
async fn output_recovery_budget_is_per_turn_not_per_tool_round() {
    let dir = tempfile::tempdir().unwrap();
    let capped = response(vec![text_block("partial")], "max_tokens");
    let run = run(
        vec![
            (200, capped.clone()),
            (200, response(vec![write("completed", "once")], "tool_use")),
            (200, capped),
        ],
        dir.path(),
    )
    .await;
    assert!(!run.result.ok);
    assert_eq!(run.requests.len(), 3);
    assert_eq!(run.result.tools.len(), 1);
    assert!(!run.history.interrupted);
    assert!(recovery::can_resume_legacy(
        &run.history.protocol,
        &run.history.items
    ));
}

#[tokio::test]
async fn output_recovery_does_not_retry_transport_auth_or_refusal_errors() {
    for response_body in [
        (401, json!({"error":{"message":"invalid key"}}).to_string()),
        (
            200,
            event(
                json!({"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}),
            ),
        ),
        (200, response(vec![text_block("refused")], "refusal")),
    ] {
        let dir = tempfile::tempdir().unwrap();
        let run = run(vec![response_body], dir.path()).await;
        assert!(!run.result.ok);
        assert_eq!(run.requests.len(), 1);
        assert!(run.result.tools.is_empty());
        assert!(run.events.iter().all(|e| e["type"] != "text_reset"));
    }
}

#[tokio::test]
async fn output_recovery_rejects_an_already_recorded_tool_id() {
    let dir = tempfile::tempdir().unwrap();
    let run = run(
        vec![
            (200, response(vec![write("completed", "once")], "tool_use")),
            (200, response(vec![text_block("partial")], "max_tokens")),
            (
                200,
                response(vec![write("completed", "replayed")], "tool_use"),
            ),
        ],
        dir.path(),
    )
    .await;
    assert!(!run.result.ok);
    assert!(run.result.error.contains("阻止重复执行"));
    assert_eq!(run.result.tools.len(), 1);
    assert_eq!(
        std::fs::read_to_string(dir.path().join("created.txt")).unwrap(),
        "once"
    );
    assert!(!run.history.interrupted);
    assert_eq!(run.requests.len(), 3);
}

#[tokio::test]
async fn output_recovery_rejects_inconsistent_stop_or_unknown_provider_tools() {
    for (blocks, stop) in [
        (vec![write("unexpected", "unsafe")], "end_turn"),
        (vec![write("unexpected", "unsafe")], "stop_sequence"),
        (vec![text_block("no tools")], "tool_use"),
        (
            vec![json!({"type":"server_tool_use","id":"server","name":"unknown","input":{}})],
            "max_tokens",
        ),
    ] {
        let dir = tempfile::tempdir().unwrap();
        let run = run(vec![(200, response(blocks, stop))], dir.path()).await;
        assert!(!run.result.ok);
        assert!(run.result.tools.is_empty());
        assert!(!dir.path().join("created.txt").exists());
        assert_eq!(run.requests.len(), 1);
    }
}

#[tokio::test]
async fn output_recovery_keeps_completed_checkpoint_on_later_http_error() {
    let dir = tempfile::tempdir().unwrap();
    let run = run(
        vec![
            (200, response(vec![write("completed", "once")], "tool_use")),
            (
                400,
                json!({"error":{"message":"provider error"}}).to_string(),
            ),
        ],
        dir.path(),
    )
    .await;
    assert!(!run.result.ok);
    assert_eq!(run.result.tools.len(), 1);
    assert!(!run.history.interrupted);
    assert!(recovery::can_resume_legacy(
        &run.history.protocol,
        &run.history.items
    ));
}
