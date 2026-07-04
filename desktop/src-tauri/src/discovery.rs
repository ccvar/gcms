//! 冻结的发现契约：GET {api_base}/sites →
//! {"items":[{id,slug,name,capabilities,api_base}],"all_sites":bool}
//! 单站包（gcms_）没有 /sites，返回一个用 .env base 合成的单站列表。

use serde_json::{json, Value};

use crate::keychain;
use crate::pack::Connection;

pub async fn discover(conn: &Connection) -> Result<Value, String> {
    let key = keychain::get_key(&conn.id)?;
    if conn.key_kind == "gcms_" {
        // 单站连接：没有发现端点，合成一个条目保证 UI 统一。
        return Ok(json!({
            "items": [{
                "id": 0,
                "slug": "default",
                "name": conn.name,
                "capabilities": Value::Null,
                "api_base": conn.api_base,
            }],
            "all_sites": false,
            "synthetic": true,
        }));
    }
    let url = format!("{}/sites", conn.api_base);
    let resp = reqwest::Client::new()
        .get(&url)
        .header("Authorization", format!("Bearer {key}"))
        .timeout(std::time::Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| format!("请求发现接口失败: {e}"))?;
    let status = resp.status();
    let body: Value = resp
        .json()
        .await
        .map_err(|e| format!("发现接口响应不是 JSON: {e}"))?;
    if !status.is_success() {
        let msg = body
            .get("error")
            .and_then(|e| e.get("message"))
            .and_then(Value::as_str)
            .unwrap_or("未知错误");
        return Err(format!("发现接口 {status}: {msg}"));
    }
    Ok(body)
}
