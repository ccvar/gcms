//! Regression fixtures emulate Claude's prefix-bound thinking signatures.
//! They exercise the real model/tool loop and local files, never paid APIs.
use super::tests::{conn, event, profile};
use super::*;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

#[derive(Clone)]
enum Response {
    Write,
    Complete(&'static str),
    Capped,
}

fn signature(system: &Value, tools: &Value, messages: &[Value]) -> String {
    // Deliberately binds the entire prior transcript, not just system/tools.
    format!(
        "fixture:{:x}",
        Sha256::digest(json!([system, tools, messages]).to_string().as_bytes())
    )
}

fn check_bindings(body: &Value) -> Result<(), String> {
    let messages = body["messages"].as_array().ok_or("missing messages")?;
    for (index, message) in messages.iter().enumerate() {
        if message["role"] != "assistant" {
            continue;
        }
        let Some(blocks) = message["content"].as_array() else {
            continue;
        };
        for block in blocks {
            if block["type"] == "thinking" {
                let expected = signature(&body["system"], &body["tools"], &messages[..index]);
                if block["signature"] != expected {
                    return Err(format!(
                        "messages.{index}.content: Invalid `signature` in `thinking` block. The block is bound to a different conversation. The `system` prompt, tools or prior messages differ."
                    ));
                }
            }
        }
    }
    Ok(())
}

fn signed_response(body: &Value, response: Response) -> String {
    let thought = json!({"type":"thinking","thinking":"fixture reasoning — preserve exact bytes","signature":signature(&body["system"], &body["tools"], body["messages"].as_array().unwrap())});
    let write = json!({"type":"tool_use","id":"write-once","name":"write_file","input":{"path":"created.txt","content":"created exactly once","overwrite":false}});
    let (blocks, stop) = match response {
        Response::Write => (vec![thought, write], "tool_use"),
        Response::Complete(text) => (
            vec![thought, json!({"type":"text","text":text})],
            "end_turn",
        ),
        Response::Capped => (
            vec![
                thought,
                json!({"type":"text","text":"discard this incomplete answer"}),
                json!({"type":"tool_use","id":"discard-capped","name":"write_file","input":{"path":"must-not-exist.txt","content":"unsafe","overwrite":false}}),
            ],
            "max_tokens",
        ),
    };
    let mut stream = event(json!({"type":"message_start","message":{"usage":{"input_tokens":10}}}));
    for (index, block) in blocks.into_iter().enumerate() {
        stream += &event(json!({"type":"content_block_start","index":index,"content_block":block}));
        stream += &event(json!({"type":"content_block_stop","index":index}));
    }
    stream += &event(
        json!({"type":"message_delta","delta":{"stop_reason":stop},"usage":{"output_tokens":20}}),
    );
    stream += &event(json!({"type":"message_stop"}));
    stream
}

async fn strict_server(responses: Vec<Response>) -> (String, tokio::task::JoinHandle<Vec<Value>>) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let task = tokio::spawn(async move {
        let mut requests = Vec::new();
        for response in responses {
            let accepted = tokio::time::timeout(Duration::from_secs(5), listener.accept()).await;
            let Ok(Ok((mut socket, _))) = accepted else {
                break;
            };
            let mut bytes = Vec::new();
            let header_end = loop {
                let mut chunk = [0u8; 4096];
                let size = socket.read(&mut chunk).await.unwrap();
                assert!(size > 0);
                bytes.extend_from_slice(&chunk[..size]);
                if let Some(end) = bytes.windows(4).position(|b| b == b"\r\n\r\n") {
                    break end + 4;
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
                let size = socket.read(&mut chunk).await.unwrap();
                assert!(size > 0);
                bytes.extend_from_slice(&chunk[..size]);
            }
            let request: Value =
                serde_json::from_slice(&bytes[header_end..header_end + length]).unwrap();
            let validation = check_bindings(&request);
            let (status, content_type, body) = match &validation {
                Ok(()) => (
                    200,
                    "text/event-stream",
                    signed_response(&request, response),
                ),
                Err(error) => (
                    400,
                    "application/json",
                    json!({"error":{"type":"invalid_request_error","message":error}}).to_string(),
                ),
            };
            requests.push(request);
            let headers = format!("HTTP/1.1 {status} fixture\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len());
            socket.write_all(headers.as_bytes()).await.unwrap();
            for chunk in body.as_bytes().chunks(13) {
                socket.write_all(chunk).await.unwrap();
            }
            if validation.is_err() {
                break;
            }
        }
        requests
    });
    (format!("http://{address}"), task)
}

fn history() -> History {
    serde_json::from_value(json!({
        "scope":"binding-fixture", "protocol":"anthropic_messages", "system":"Keep instructions stable.",
        "items":[{"role":"user","content":"Write created.txt once, then explain."}], "interrupted":false
    })).unwrap()
}

fn reload(root: &Path) -> History {
    serde_json::from_slice(&std::fs::read(root.join("history.json")).unwrap()).unwrap()
}

async fn run_loop(
    root: &Path,
    connection: &Connection,
    p: &ApiProfile,
    h: &mut History,
) -> TurnResult {
    let work = std::fs::canonicalize(root).unwrap();
    let tools = tool_runtime::Tools::new(
        connection,
        &work,
        &work.join("data"),
        &[],
        PermMode::Full,
        "fixture",
        None,
        "",
    )
    .unwrap();
    let path = work.join("history.json");
    save_history(&path, h).unwrap();
    let channel = Channel::new(|_| Ok(()));
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
            p,
            "claude-opus-5-5",
            "api:high",
            "fake-fixture-key",
            &tools,
            h,
            &path,
            &channel,
            "",
            &mut result,
        ),
    )
    .await
    .expect("bounded binding regression loop");
    result.ok = outcome.is_ok();
    result.error = outcome.err().unwrap_or_default();
    result
}

async fn requests(task: tokio::task::JoinHandle<Vec<Value>>) -> Vec<Value> {
    tokio::time::timeout(Duration::from_secs(6), task)
        .await
        .unwrap()
        .unwrap()
}

fn thinking_count(messages: &[Value]) -> usize {
    messages
        .iter()
        .filter_map(|m| m["content"].as_array())
        .flatten()
        .filter(|b| matches!(b["type"].as_str(), Some("thinking" | "redacted_thinking")))
        .count()
}

#[test]
fn strict_fixture_rejects_any_changed_bound_prefix() {
    let first = json!({"system":"original", "tools":[{"name":"write_file"}], "messages":[{"role":"user","content":"hello"}]});
    let signed = json!({"role":"assistant","content":[{"type":"thinking","thinking":"opaque","signature":signature(&first["system"], &first["tools"], first["messages"].as_array().unwrap())},{"type":"text","text":"hello"}]});
    let mut valid = first;
    valid["messages"].as_array_mut().unwrap().push(signed);
    assert!(check_bindings(&valid).is_ok());
    for (field, replacement) in [
        ("system", json!("changed")),
        ("tools", json!([])),
        ("messages", json!([])),
    ] {
        let mut changed = valid.clone();
        if field == "messages" {
            changed["messages"][0]["content"] = json!("changed prior message");
        } else {
            changed[field] = replacement;
        }
        assert!(check_bindings(&changed)
            .unwrap_err()
            .contains("Invalid `signature`"));
    }
}

#[tokio::test]
async fn signed_tool_loop_and_disk_resume_preserve_thinking_bytes() {
    let dir = tempfile::tempdir().unwrap();
    let (url, server) = strict_server(vec![
        Response::Write,
        Response::Complete("first done"),
        Response::Complete("continued"),
    ])
    .await;
    let p = profile("anthropic_messages", &url);
    let c = conn();
    let mut h = history();
    let first = run_loop(dir.path(), &c, &p, &mut h).await;
    assert!(first.ok, "{}", first.error);
    assert_eq!(first.tools.len(), 1);
    let signed_items = h.items.clone();
    assert_eq!(thinking_count(&signed_items), 2);
    let prefix = h.thinking_prefix.clone();
    assert!(!prefix.is_empty());
    h = reload(dir.path());
    assert_eq!(h.items, signed_items);
    assert_eq!(h.thinking_prefix, prefix);
    h.items
        .push(json!({"role":"user","content":"Continue without writing again."}));
    let second = run_loop(dir.path(), &c, &p, &mut h).await;
    assert!(second.ok, "{}", second.error);
    assert!(second.tools.is_empty());
    assert_eq!(&h.items[..signed_items.len()], signed_items.as_slice());
    let requests = requests(server).await;
    assert_eq!(requests.len(), 3);
    assert_eq!(
        &requests[2]["messages"].as_array().unwrap()[..signed_items.len()],
        signed_items.as_slice()
    );
    assert_eq!(
        std::fs::read_to_string(dir.path().join("created.txt")).unwrap(),
        "created exactly once"
    );
}

#[tokio::test]
async fn signed_output_recovery_retains_prefix_and_never_reexecutes_completed_write() {
    let dir = tempfile::tempdir().unwrap();
    let (url, server) = strict_server(vec![
        Response::Write,
        Response::Capped,
        Response::Complete("recovered"),
        Response::Complete("continued"),
    ])
    .await;
    let p = profile("anthropic_messages", &url);
    let c = conn();
    let mut h = history();
    let result = run_loop(dir.path(), &c, &p, &mut h).await;
    assert!(result.ok, "{}", result.error);
    assert_eq!(result.tools.len(), 1);
    assert!(!dir.path().join("must-not-exist.txt").exists());
    assert!(!json!(&h.items).to_string().contains("discard-capped"));
    assert!(!json!(&h.items)
        .to_string()
        .contains("discard this incomplete answer"));
    assert_eq!(thinking_count(&h.items), 2);
    h = reload(dir.path());
    h.items.push(json!({"role":"user","content":"Continue."}));
    let resumed = run_loop(dir.path(), &c, &p, &mut h).await;
    assert!(resumed.ok, "{}", resumed.error);
    assert!(resumed.tools.is_empty());
    let requests = requests(server).await;
    assert_eq!(requests.len(), 4);
    for request in &requests[1..] {
        assert_eq!(request["system"], requests[0]["system"]);
        assert_eq!(request["tools"], requests[0]["tools"]);
        assert_eq!(request["max_tokens"], 512);
        assert_eq!(request["output_config"]["effort"], "high");
    }
    let checkpoint = requests[1]["messages"].as_array().unwrap();
    let retry = requests[2]["messages"].as_array().unwrap();
    assert_eq!(&retry[..checkpoint.len()], checkpoint.as_slice());
    assert_eq!(retry.len(), checkpoint.len() + 1);
    assert_eq!(retry.last().unwrap()["role"], "user");
    assert_eq!(
        &requests[3]["messages"].as_array().unwrap()[..retry.len()],
        retry.as_slice()
    );
    assert_eq!(
        std::fs::read_to_string(dir.path().join("created.txt")).unwrap(),
        "created exactly once"
    );
}

#[tokio::test]
async fn strict_server_returns_http_400_for_old_system_appended_recovery() {
    let (url, server) = strict_server(vec![
        Response::Complete("signed"),
        Response::Complete("must fail"),
    ])
    .await;
    let p = profile("anthropic_messages", &url);
    let client = api_profiles::http_client().unwrap();
    let mut items = vec![json!({"role":"user","content":"hello"})];
    let first_body = request_body(
        &p,
        "claude-opus-5-5",
        "api:high",
        "stable system",
        &items,
        &[],
    )
    .unwrap();
    let reply = stream_request(&client, &p, "fixture-key", first_body, |_| {})
        .await
        .unwrap();
    items.extend(reply.items);
    // This is precisely the pre-fix recovery behavior: append a hint to the
    // system while resending provider-signed reasoning from the prior prefix.
    let broken_body = request_body(
        &p,
        "claude-opus-5-5",
        "api:high",
        "stable system\nRecovery hint added to system.",
        &items,
        &[],
    )
    .unwrap();
    let failure = stream_request(&client, &p, "fixture-key", broken_body, |_| {}).await;
    let error = match failure {
        Ok(_) => panic!("changed signed prefix must be rejected"),
        Err(error) => error,
    };
    assert!(error.contains("HTTP 400"), "{error}");
    assert!(error.contains("Invalid `signature`"), "{error}");
    assert_eq!(requests(server).await.len(), 2);
}

#[tokio::test]
async fn legacy_history_migrates_once_without_discarding_tool_results() {
    let dir = tempfile::tempdir().unwrap();
    let (url, server) = strict_server(vec![
        Response::Write,
        Response::Complete("first"),
        Response::Complete("migrated"),
        Response::Complete("continued"),
    ])
    .await;
    let p = profile("anthropic_messages", &url);
    let c = conn();
    let mut h = history();
    assert!(run_loop(dir.path(), &c, &p, &mut h).await.ok);
    let mut legacy = serde_json::to_value(&h).unwrap();
    legacy.as_object_mut().unwrap().remove("thinking_prefix");
    h = serde_json::from_value(legacy).unwrap();
    h.items
        .push(json!({"role":"user","content":"Continue the old conversation."}));
    let migrated = run_loop(dir.path(), &c, &p, &mut h).await;
    assert!(migrated.ok, "{}", migrated.error);
    assert!(migrated.tools.is_empty());
    assert_eq!(thinking_count(&h.items), 1);
    let migrated_items = h.items.clone();
    h = reload(dir.path());
    h.items
        .push(json!({"role":"user","content":"Continue again."}));
    let resumed = run_loop(dir.path(), &c, &p, &mut h).await;
    assert!(resumed.ok, "{}", resumed.error);
    assert!(resumed.tools.is_empty());
    assert_eq!(&h.items[..migrated_items.len()], migrated_items.as_slice());
    let requests = requests(server).await;
    assert_eq!(requests.len(), 4);
    assert_eq!(
        thinking_count(requests[2]["messages"].as_array().unwrap()),
        0
    );
    assert_eq!(
        thinking_count(requests[3]["messages"].as_array().unwrap()),
        1
    );
    assert!(requests[2]["messages"].to_string().contains("tool_result"));
    assert!(requests[2]["messages"].to_string().contains("write-once"));
}

#[tokio::test]
async fn changed_system_or_tool_schema_invalidates_all_old_reasoning_only() {
    for change_tools in [false, true] {
        let dir = tempfile::tempdir().unwrap();
        let (url, server) = strict_server(vec![
            Response::Write,
            Response::Complete("first"),
            Response::Complete("changed prefix"),
        ])
        .await;
        let p = profile("anthropic_messages", &url);
        let mut c = conn();
        let mut h = history();
        assert!(run_loop(dir.path(), &c, &p, &mut h).await.ok);
        if change_tools {
            c.kind = "gcms".into();
        } else {
            h.system
                .push_str(" Runtime instructions changed by an app upgrade.");
        }
        h.items
            .push(json!({"role":"user","content":"Continue with the updated runtime."}));
        let result = run_loop(dir.path(), &c, &p, &mut h).await;
        assert!(result.ok, "{}", result.error);
        assert!(result.tools.is_empty());
        let requests = requests(server).await;
        assert_eq!(requests.len(), 3);
        assert_eq!(
            thinking_count(requests[2]["messages"].as_array().unwrap()),
            0
        );
        assert!(requests[2]["messages"].to_string().contains("tool_result"));
        assert!(requests[2]["messages"].to_string().contains("write-once"));
        if change_tools {
            assert_ne!(requests[0]["tools"], requests[2]["tools"]);
            assert_eq!(requests[0]["system"], requests[2]["system"]);
        } else {
            assert_ne!(requests[0]["system"], requests[2]["system"]);
            assert_eq!(requests[0]["tools"], requests[2]["tools"]);
        }
        assert_eq!(
            std::fs::read_to_string(dir.path().join("created.txt")).unwrap(),
            "created exactly once"
        );
    }
}

#[test]
fn migration_removes_empty_thought_only_turn_and_every_later_signature() {
    let mut h = history();
    h.items.extend([
        json!({"role":"assistant","content":[{"type":"thinking","thinking":"old","signature":"first"},{"type":"redacted_thinking","data":"opaque"}]}),
        json!({"role":"user","content":"next"}),
        json!({"role":"assistant","content":[{"type":"thinking","thinking":"later","signature":"second"},{"type":"tool_use","id":"saved","name":"write_file","input":{"path":"saved.txt","content":"saved","overwrite":false}}]}),
        json!({"role":"user","content":[{"type":"tool_result","tool_use_id":"saved","content":"already completed"}]}),
    ]);
    let user = h.items[2].clone();
    let tool = h.items[3]["content"][1].clone();
    let result = h.items[4].clone();
    assert_eq!(h.prepare_thinking_prefix("new runtime", &[]), 3);
    assert_eq!(h.items.len(), 4);
    assert_eq!(h.items[1], user);
    assert_eq!(h.items[2]["content"], json!([tool]));
    assert_eq!(h.items[3], result);
    assert_eq!(thinking_count(&h.items), 0);
    let unchanged = serde_json::to_vec(&h.items).unwrap();
    assert_eq!(h.prepare_thinking_prefix("new runtime", &[]), 0);
    assert_eq!(serde_json::to_vec(&h.items).unwrap(), unchanged);
}
