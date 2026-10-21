//! Bounded managed continuation uses only localhost fixtures and temporary files.
//! These tests never load the credential vault, launch a model CLI, or call a paid API.
use super::tests::{conn, event, profile, server};
use super::*;
use crate::api_execution::ApiExecutionPolicy;
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

fn response(blocks: Vec<Value>, stop: &str) -> String {
    response_with_usage(blocks, stop, 10, 20)
}

fn response_with_usage(blocks: Vec<Value>, stop: &str, input: u64, output: u64) -> String {
    let mut body =
        event(json!({"type":"message_start","message":{"usage":{"input_tokens":input}}}));
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
        json!({"type":"message_delta","delta":{"stop_reason":stop},"usage":{"output_tokens":output}}),
    );
    body += &event(json!({"type":"message_stop"}));
    body
}

fn write(id: &str, path: &str, content: &str) -> Value {
    json!({"type":"tool_use","id":id,"name":"write_file","input":{"path":path,"content":content,"overwrite":true}})
}

fn read(id: &str, path: &str) -> Value {
    json!({"type":"tool_use","id":id,"name":"read_file","input":{"path":path}})
}

fn final_response() -> (u16, String) {
    (
        200,
        response(vec![json!({"type":"text","text":"全部完成。"})], "end_turn"),
    )
}

fn write_responses(count: usize) -> Vec<(u16, String)> {
    (0..count)
        .map(|n| {
            (
                200,
                response(
                    vec![write(
                        &format!("call-{n}"),
                        &format!("step-{n}.txt"),
                        &format!("completed {n}"),
                    )],
                    "tool_use",
                ),
            )
        })
        .collect()
}

fn initial_history() -> History {
    History {
        scope: "managed-fixture".into(),
        protocol: "anthropic_messages".into(),
        system: "Complete the requested work without repeating finished operations.".into(),
        thinking_prefix: String::new(),
        items: vec![json!({"role":"user","content":"complete a long task"})],
        interrupted: false,
    }
}

fn initial_result() -> TurnResult {
    TurnResult {
        ok: false,
        text: String::new(),
        tools: vec![],
        error: String::new(),
        session_ref: "api:11111111-1111-4111-8111-111111111111".into(),
        proposal: None,
        usage: None,
        limit_reset: None,
    }
}

struct Run {
    result: TurnResult,
    history: History,
    requests: Vec<(String, Value)>,
    events: Vec<Value>,
}

async fn run(
    responses: Vec<(u16, String)>,
    root: &Path,
    policy: &ApiExecutionPolicy,
    history: Option<History>,
) -> Run {
    let initial_requests = policy.requests_used();
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
        "managed-fixture",
        None,
        "",
    )
    .unwrap();
    let mut history = history.unwrap_or_else(initial_history);
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
    let mut result = initial_result();
    let outcome = tokio::time::timeout(
        Duration::from_secs(15),
        execute_model_loop_with_policy(
            &p,
            "test-model",
            "",
            "fake-managed-fixture-key",
            &tools,
            &mut history,
            &path,
            &channel,
            "",
            &mut result,
            policy,
        ),
    )
    .await
    .expect("bounded fixture execution");
    result.ok = outcome.is_ok();
    result.error = outcome.err().unwrap_or_default();
    let history = serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    let requests = tokio::time::timeout(Duration::from_secs(2), server)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(
        policy.requests_used().saturating_sub(initial_requests),
        requests.len(),
        "the shared attempt counter must match actual HTTP requests"
    );
    let events = events.lock().unwrap().clone();
    Run {
        result,
        history,
        requests,
        events,
    }
}

fn assert_complete_checkpoint(run: &Run, expected_tools: usize) {
    assert!(
        !run.history.interrupted,
        "a safe boundary must not look like an uncertain tool execution"
    );
    assert_eq!(run.result.tools.len(), expected_tools);
    let results = run
        .history
        .items
        .iter()
        .filter_map(|item| item["content"].as_array())
        .flatten()
        .filter(|block| block["type"] == "tool_result")
        .count();
    assert_eq!(
        results, expected_tools,
        "every attempted tool has one durable success or explicit skipped/error result"
    );
}

#[tokio::test]
async fn managed_does_not_retry_http_auth_or_server_errors_or_replay_completed_writes() {
    for status in [401, 500] {
        let dir = tempfile::tempdir().unwrap();
        let mut responses = write_responses(1);
        responses.push((
            status,
            json!({"error":{"message":format!("fixture HTTP {status}")}}).to_string(),
        ));
        let run = run(
            responses,
            dir.path(),
            &ApiExecutionPolicy::managed(None),
            None,
        )
        .await;
        assert!(!run.result.ok);
        assert!(
            run.result.error.contains(&status.to_string()),
            "the original HTTP failure must be preserved: {}",
            run.result.error
        );
        assert_eq!(run.requests.len(), 2);
        assert_complete_checkpoint(&run, 1);
        assert_eq!(
            std::fs::read_to_string(dir.path().join("step-0.txt")).unwrap(),
            "completed 0"
        );
        assert!(run.events.iter().all(|event| event["type"] != "text_reset"));
    }
}

#[tokio::test]
async fn managed_continues_beyond_64_with_one_history_and_stable_provider_prefix() {
    let dir = tempfile::tempdir().unwrap();
    let mut responses = write_responses(66);
    responses.push(final_response());
    let run = run(
        responses,
        dir.path(),
        &ApiExecutionPolicy::managed(None),
        None,
    )
    .await;
    assert!(run.result.ok, "{}", run.result.error);
    assert_eq!(run.requests.len(), 67);
    assert_complete_checkpoint(&run, 66);
    assert_eq!(run.result.text, "全部完成。");
    assert_eq!(run.result.usage.as_ref().unwrap().input, 670);
    assert_eq!(run.result.usage.as_ref().unwrap().output, 1340);
    for n in 0..66 {
        assert_eq!(
            std::fs::read_to_string(dir.path().join(format!("step-{n}.txt"))).unwrap(),
            format!("completed {n}")
        );
    }
    let original = &run.requests[0].1;
    for (index, (_, request)) in run.requests.iter().enumerate() {
        assert_eq!(
            request["system"], original["system"],
            "system prefix changed at request {index}"
        );
        assert_eq!(
            request["tools"], original["tools"],
            "tool schema changed at request {index}"
        );
        let messages = request["messages"].as_array().unwrap();
        assert_eq!(
            messages
                .iter()
                .filter(|m| m["content"] == "complete a long task")
                .count(),
            1
        );
        if index > 0 {
            let previous = run.requests[index - 1].1["messages"].as_array().unwrap();
            assert_eq!(
                &messages[..previous.len()],
                previous.as_slice(),
                "history must only append across stage boundaries"
            );
        }
    }
    for checkpoint in [32, 64] {
        assert!(
            run.events.iter().any(|event| {
                event["type"] == "activity"
                    && event["label"] == "托管继续执行"
                    && event["current"] == checkpoint
                    && event["total"] == 96
            }),
            "missing managed continuation checkpoint after {checkpoint} requests"
        );
    }
    // No intermediate terminal failure is emitted merely to start another segment.
    assert!(run.events.iter().all(|event| event["type"] != "done"));
}

#[tokio::test]
async fn ordinary_api_policy_still_stops_after_32_requests() {
    let dir = tempfile::tempdir().unwrap();
    let run = run(
        write_responses(32),
        dir.path(),
        &ApiExecutionPolicy::default(),
        None,
    )
    .await;
    assert!(!run.result.ok);
    assert!(run.result.error.contains("32"), "{}", run.result.error);
    assert_eq!(run.requests.len(), 32);
    assert_complete_checkpoint(&run, 32);
    assert!(!dir.path().join("step-32.txt").exists());
}

#[tokio::test]
async fn managed_final_request_cap_preserves_completed_writes_without_replaying_them() {
    let dir = tempfile::tempdir().unwrap();
    let mut policy = ApiExecutionPolicy::managed(None);
    policy.max_requests = 3;
    policy.stage_requests = 2;
    let stopped = run(write_responses(3), dir.path(), &policy, None).await;
    assert!(!stopped.result.ok);
    assert!(
        stopped.result.error.contains("托管请求次数上限"),
        "{}",
        stopped.result.error
    );
    assert_eq!(stopped.requests.len(), 3);
    assert_complete_checkpoint(&stopped, 3);
    let finished = run(
        vec![final_response()],
        dir.path(),
        &policy,
        Some(stopped.history),
    )
    .await;
    assert!(finished.result.ok, "{}", finished.result.error);
    assert!(
        finished.result.tools.is_empty(),
        "resuming must not rerun already recorded tools"
    );
    assert_eq!(finished.requests.len(), 1);
    for n in 0..3 {
        assert_eq!(
            std::fs::read_to_string(dir.path().join(format!("step-{n}.txt"))).unwrap(),
            format!("completed {n}")
        );
        assert!(finished.requests[0].1["messages"]
            .to_string()
            .contains(&format!("call-{n}")));
    }
}

#[tokio::test]
async fn managed_token_budget_stops_before_an_additional_request_and_keeps_usage() {
    let dir = tempfile::tempdir().unwrap();
    let mut policy = ApiExecutionPolicy::managed(None);
    policy.max_total_tokens = Some(60);
    let run = run(write_responses(2), dir.path(), &policy, None).await;
    assert!(!run.result.ok);
    assert!(
        run.result.error.contains("托管累计 token 预算"),
        "{}",
        run.result.error
    );
    assert_eq!(run.requests.len(), 2);
    assert_complete_checkpoint(&run, 2);
    assert_eq!(run.result.usage.as_ref().unwrap().input, 20);
    assert_eq!(run.result.usage.as_ref().unwrap().output, 40);
    assert!(!dir.path().join("step-2.txt").exists());
}

#[tokio::test]
async fn exhausted_remaining_week_budget_prevents_even_the_first_request() {
    let dir = tempfile::tempdir().unwrap();
    let run = run(
        vec![],
        dir.path(),
        &ApiExecutionPolicy::managed(Some(0)),
        None,
    )
    .await;
    assert!(!run.result.ok);
    assert!(
        run.result.error.contains("托管累计 token 预算"),
        "{}",
        run.result.error
    );
    assert!(run.requests.is_empty());
    assert_complete_checkpoint(&run, 0);
    assert!(run.result.usage.is_none());
}

#[tokio::test]
async fn managed_stops_repeated_reads_without_progress_even_with_new_call_ids() {
    let dir = tempfile::tempdir().unwrap();
    let mut responses = vec![(
        200,
        response(
            vec![write("created", "read-me.txt", "fixed contents")],
            "tool_use",
        ),
    )];
    responses.extend((0..4).map(|n| {
        (
            200,
            response(vec![read(&format!("read-{n}"), "read-me.txt")], "tool_use"),
        )
    }));
    let run = run(
        responses,
        dir.path(),
        &ApiExecutionPolicy::managed(None),
        None,
    )
    .await;
    assert!(!run.result.ok);
    assert!(
        run.result.error.contains("托管连续无进展"),
        "{}",
        run.result.error
    );
    assert_eq!(run.requests.len(), 5);
    assert_complete_checkpoint(&run, 5);
    assert_eq!(
        std::fs::read_to_string(dir.path().join("read-me.txt")).unwrap(),
        "fixed contents"
    );
}

#[tokio::test]
async fn managed_blocks_same_write_under_a_new_call_id_before_execution() {
    let dir = tempfile::tempdir().unwrap();
    let run = run(
        vec![
            (
                200,
                response(vec![write("first", "created.txt", "only once")], "tool_use"),
            ),
            (
                200,
                response(
                    vec![write("different-id", "created.txt", "only once")],
                    "tool_use",
                ),
            ),
        ],
        dir.path(),
        &ApiExecutionPolicy::managed(None),
        None,
    )
    .await;
    assert!(!run.result.ok);
    assert!(
        run.result.error.contains("托管已阻止重复操作"),
        "{}",
        run.result.error
    );
    assert_eq!(run.requests.len(), 2);
    assert_complete_checkpoint(&run, 2);
    assert_eq!(
        std::fs::read_to_string(dir.path().join("created.txt")).unwrap(),
        "only once"
    );
    let blocked = run
        .history
        .items
        .iter()
        .filter_map(|item| item["content"].as_array())
        .flatten()
        .find(|block| block["tool_use_id"] == "different-id")
        .expect("a skipped call must retain its paired non-execution result");
    assert_eq!(blocked["is_error"], true);
    assert!(blocked["content"]
        .as_str()
        .unwrap()
        .contains("未执行此工具"));
}

#[tokio::test]
async fn managed_stops_consecutive_tool_failures_without_hiding_the_results() {
    let dir = tempfile::tempdir().unwrap();
    let responses = (0..3)
        .map(|n| {
            (
                200,
                response(
                    vec![read(&format!("failed-{n}"), &format!("missing-{n}.txt"))],
                    "tool_use",
                ),
            )
        })
        .collect();
    let run = run(
        responses,
        dir.path(),
        &ApiExecutionPolicy::managed(None),
        None,
    )
    .await;
    assert!(!run.result.ok);
    assert!(
        run.result.error.contains("托管工具连续失败"),
        "{}",
        run.result.error
    );
    assert_eq!(run.requests.len(), 3);
    assert_complete_checkpoint(&run, 3);
}

#[tokio::test]
async fn managed_stops_missing_usage_after_saving_completed_tool_results() {
    let dir = tempfile::tempdir().unwrap();
    let run = run(
        vec![(
            200,
            response_with_usage(
                vec![write("usage-missing", "created.txt", "durable")],
                "tool_use",
                0,
                0,
            ),
        )],
        dir.path(),
        &ApiExecutionPolicy::managed(None),
        None,
    )
    .await;
    assert!(!run.result.ok);
    assert!(
        run.result.error.contains("服务商未返回用量"),
        "{}",
        run.result.error
    );
    assert_eq!(run.requests.len(), 1);
    assert_complete_checkpoint(&run, 1);
    assert_eq!(
        std::fs::read_to_string(dir.path().join("created.txt")).unwrap(),
        "durable"
    );
}

#[tokio::test]
async fn managed_does_not_continue_a_batch_after_a_command_partially_writes_then_fails() {
    let dir = tempfile::tempdir().unwrap();
    #[cfg(unix)]
    let command = "printf partial-change > changed.txt; exit 7";
    #[cfg(windows)]
    let command = "echo partial-change > changed.txt & exit /b 7";
    let blocks = vec![
        json!({"type":"tool_use","id":"uncertain-command","name":"run_command","input":{"command":command}}),
        write(
            "must-not-run",
            "forbidden.txt",
            "not allowed after uncertain failure",
        ),
    ];
    let run = run(
        vec![(200, response(blocks, "tool_use"))],
        dir.path(),
        &ApiExecutionPolicy::managed(None),
        None,
    )
    .await;
    assert!(!run.result.ok);
    assert!(
        run.result.error.contains("托管操作结果未确认"),
        "{}",
        run.result.error
    );
    assert_eq!(run.requests.len(), 1);
    assert_eq!(
        run.result.tools.len(),
        2,
        "the blocked call remains visible in the execution log"
    );
    assert_eq!(
        std::fs::read_to_string(dir.path().join("changed.txt"))
            .unwrap()
            .trim(),
        "partial-change"
    );
    assert!(!dir.path().join("forbidden.txt").exists());
    let tool_results: Vec<_> = run
        .history
        .items
        .iter()
        .filter_map(|item| item["content"].as_array())
        .flatten()
        .filter(|block| block["type"] == "tool_result")
        .collect();
    assert_eq!(
        tool_results.len(),
        2,
        "the skipped call also needs a durable explicit non-execution result"
    );
    assert!(tool_results.iter().all(|block| block["is_error"] == true));
    assert!(tool_results
        .iter()
        .any(|block| block["tool_use_id"] == "must-not-run"
            && block["content"].as_str().unwrap().contains("未执行此工具")));
}

#[cfg(unix)]
#[tokio::test]
async fn managed_deadline_during_a_command_blocks_uncertain_recovery_and_kills_children() {
    let dir = tempfile::tempdir().unwrap();
    let command =
        "printf before-deadline > partial.txt; sleep 3; printf after-deadline > forbidden.txt";
    let blocks = vec![
        json!({"type":"tool_use","id":"slow-command","name":"run_command","input":{"command":command}}),
    ];
    let mut policy = ApiExecutionPolicy::managed(None);
    policy.total_timeout = Some(Duration::from_secs(1));
    let run = run(
        vec![(200, response(blocks, "tool_use"))],
        dir.path(),
        &policy,
        None,
    )
    .await;
    assert!(!run.result.ok);
    assert!(
        run.result.error.contains("托管执行时长上限"),
        "{}",
        run.result.error
    );
    assert_eq!(run.requests.len(), 1);
    assert_eq!(run.result.tools.len(), 1);
    assert_eq!(
        std::fs::read_to_string(dir.path().join("partial.txt")).unwrap(),
        "before-deadline"
    );
    assert!(
        run.history.interrupted,
        "a stopped in-flight write must never look like a complete checkpoint"
    );
    assert!(!recovery::can_resume_legacy(
        &run.history.protocol,
        &run.history.items
    ));
    tokio::time::sleep(Duration::from_secs(3)).await;
    assert!(
        !dir.path().join("forbidden.txt").exists(),
        "deadline must stop the process tree, not just stop awaiting it"
    );
}

async fn read_request(socket: &mut tokio::net::TcpStream) -> (String, Value) {
    let mut bytes = Vec::new();
    let header_end = loop {
        let mut chunk = [0u8; 4096];
        let n = socket.read(&mut chunk).await.unwrap();
        assert!(n > 0);
        bytes.extend_from_slice(&chunk[..n]);
        if let Some(p) = bytes.windows(4).position(|x| x == b"\r\n\r\n") {
            break p + 4;
        }
    };
    let headers = String::from_utf8(bytes[..header_end].to_vec()).unwrap();
    let length = headers
        .lines()
        .find_map(|line| {
            let (key, value) = line.split_once(':')?;
            key.eq_ignore_ascii_case("content-length")
                .then(|| value.trim().parse::<usize>().unwrap())
        })
        .unwrap();
    while bytes.len() < header_end + length {
        let mut chunk = [0u8; 4096];
        let n = socket.read(&mut chunk).await.unwrap();
        assert!(n > 0);
        bytes.extend_from_slice(&chunk[..n]);
    }
    (
        headers,
        serde_json::from_slice(&bytes[header_end..header_end + length]).unwrap(),
    )
}

async fn interrupted_after_checkpoint(cancel: bool) {
    let dir = tempfile::tempdir().unwrap();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/v1", listener.local_addr().unwrap());
    let (arrived_tx, arrived_rx) = tokio::sync::oneshot::channel();
    let (release_tx, release_rx) = tokio::sync::oneshot::channel();
    let server = tokio::spawn(async move {
        let (mut first, _) = listener.accept().await.unwrap();
        let first_request = read_request(&mut first).await;
        let body = response(
            vec![write("durable-write", "created.txt", "must survive")],
            "tool_use",
        );
        let header = format!("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len());
        first.write_all(header.as_bytes()).await.unwrap();
        first.write_all(body.as_bytes()).await.unwrap();
        drop(first);
        let (mut second, _) = listener.accept().await.unwrap();
        let second_request = read_request(&mut second).await;
        let _ = arrived_tx.send(());
        let _ = release_rx.await;
        drop(second);
        vec![first_request, second_request]
    });
    let p = profile("anthropic_messages", &url);
    let c = conn();
    let work = std::fs::canonicalize(dir.path()).unwrap();
    let tools = tool_runtime::Tools::new(
        &c,
        &work,
        &work.join("data"),
        &[],
        PermMode::Full,
        "managed-fixture",
        None,
        "",
    )
    .unwrap();
    let mut history = initial_history();
    let path = work.join("history.json");
    let mut result = initial_result();
    let channel = Channel::new(|_| Ok(()));
    let mut policy = ApiExecutionPolicy::managed(None);
    policy.total_timeout = Some(Duration::from_secs(if cancel { 10 } else { 1 }));
    let outcome = {
        let operation = execute_model_loop_with_policy(
            &p,
            "test-model",
            "",
            "fake-managed-fixture-key",
            &tools,
            &mut history,
            &path,
            &channel,
            "",
            &mut result,
            &policy,
        );
        if cancel {
            // The production RunRegistry uses the same select/drop mechanism.
            tokio::select! {
                outcome = operation => panic!("fixture should await its second response: {outcome:?}"),
                arrived = arrived_rx => { arrived.unwrap(); Err("fixture cancelled".to_string()) },
            }
        } else {
            tokio::time::timeout(Duration::from_secs(5), operation)
                .await
                .expect("managed deadline must interrupt an in-flight request")
        }
    };
    assert!(outcome.is_err());
    if !cancel {
        assert!(outcome.unwrap_err().contains("托管执行时长上限"));
    }
    let _ = release_tx.send(());
    let requests = tokio::time::timeout(Duration::from_secs(2), server)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(requests.len(), 2);
    assert_eq!(result.tools.len(), 1);
    assert_eq!(
        std::fs::read_to_string(work.join("created.txt")).unwrap(),
        "must survive"
    );
    let saved: History = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
    assert!(!saved.interrupted);
    assert!(serde_json::to_string(&saved.items)
        .unwrap()
        .contains("durable-write"));
    let finished = run(
        vec![final_response()],
        dir.path(),
        &ApiExecutionPolicy::managed(None),
        Some(saved),
    )
    .await;
    assert!(finished.result.ok, "{}", finished.result.error);
    assert!(finished.result.tools.is_empty());
    assert_eq!(
        std::fs::read_to_string(work.join("created.txt")).unwrap(),
        "must survive"
    );
}

#[tokio::test]
async fn managed_deadline_preserves_checkpoint_and_does_not_retry_http() {
    interrupted_after_checkpoint(false).await;
}

#[tokio::test]
async fn cancellation_preserves_checkpoint_without_replaying_completed_write() {
    interrupted_after_checkpoint(true).await;
}
