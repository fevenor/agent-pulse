use serde::{Deserialize, Serialize, de::DeserializeOwned};
use std::{
    collections::BTreeMap,
    io::Read,
    path::Path,
    sync::{Mutex, OnceLock},
};

#[derive(Clone, Default, Serialize)]
struct Count {
    sent: u64,
    received: u64,
    messages: u64,
}
static COUNTS: OnceLock<Mutex<BTreeMap<String, Count>>> = OnceLock::new();
pub fn record(category: &str, sent: usize, received: usize) {
    let mut counts = COUNTS.get_or_init(Default::default).lock().unwrap();
    let n = counts.entry(category.into()).or_default();
    n.sent += sent as u64;
    n.received += received as u64;
    n.messages += 1;
}
pub fn save(path: &Path) {
    if let Some(counts) = COUNTS.get()
        && let Ok(raw) = serde_json::to_vec(
            &serde_json::json!({"unit":"application_bytes","categories":*counts.lock().unwrap()}),
        )
    {
        let _ = std::fs::write(path, raw);
    }
}
pub fn decode(raw: &[u8], gzip: bool) -> Result<Vec<u8>, String> {
    if raw.len() > 8 << 20 {
        return Err("压缩消息超过大小限制".into());
    }
    if !gzip {
        return Ok(raw.to_vec());
    }
    let mut bytes = Vec::new();
    flate2::read::GzDecoder::new(raw)
        .take((8 << 20) + 1)
        .read_to_end(&mut bytes)
        .map_err(|_| "压缩消息无效")?;
    if bytes.len() > 8 << 20 {
        return Err("解压消息超过大小限制".into());
    }
    Ok(bytes)
}
pub fn get_json<T: DeserializeOwned>(
    client: &ureq::Agent,
    url: &str,
    token: &str,
    category: &str,
) -> Result<T, String> {
    let mut response = client
        .get(url)
        .header("Authorization", format!("Bearer {token}"))
        .header("Accept-Encoding", "gzip")
        .call()
        .map_err(|_| "读取暂不可达")?;
    let gzip = response
        .headers()
        .get("Content-Encoding")
        .is_some_and(|h| h == "gzip");
    let raw = response
        .body_mut()
        .with_config()
        .limit(8 << 20)
        .read_to_vec()
        .map_err(|_| "响应读取失败或超过限制")?;
    record(category, 0, raw.len());
    serde_json::from_slice(&decode(&raw, gzip)?).map_err(|_| "响应格式无效".into())
}

#[derive(Deserialize)]
pub struct Body {
    pub event_id: String,
    pub body_hash: String,
    pub body: String,
}
