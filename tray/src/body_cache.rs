use crate::{model::Event, network};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeMap,
    fs,
    io::Write,
    path::{Path, PathBuf},
};

pub fn hash(body: &str) -> String {
    format!("{:x}", Sha256::digest(body.as_bytes()))
}
fn valid(value: &str) -> bool {
    value.len() == 64
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}
fn directory(data: &Path) -> PathBuf {
    data.join("bodies")
}
fn cached(data: &Path, digest: &str) -> Option<String> {
    if !valid(digest) {
        return None;
    }
    let path = directory(data).join(digest);
    let info = fs::metadata(&path).ok()?;
    if info.len() > 32768 {
        return None;
    }
    let body = fs::read_to_string(path).ok()?;
    (hash(&body) == digest).then_some(body)
}
fn store(data: &Path, digest: &str, body: &str) -> Result<(), String> {
    if !valid(digest) || hash(body) != digest || body.len() > 32768 {
        return Err("正文摘要或大小不匹配".into());
    }
    let root = directory(data);
    fs::create_dir_all(&root).map_err(|_| "无法创建正文缓存")?;
    let dest = root.join(digest);
    if cached(data, digest).is_some() {
        return Ok(());
    }
    let temp = root.join(format!("{}.tmp", uuid::Uuid::new_v4()));
    let result = (|| -> std::io::Result<()> {
        let mut file = fs::File::create(&temp)?;
        file.write_all(body.as_bytes())?;
        file.sync_all()?;
        drop(file);
        fs::rename(&temp, &dest)
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temp);
        if cached(data, digest).is_none() {
            return Err("正文缓存写入失败".into());
        }
    }
    Ok(())
}
pub fn hydrate(
    client: &ureq::Agent,
    base: &str,
    token: &str,
    data: &Path,
    events: &mut [Event],
    known: &[Event],
) -> Result<(), String> {
    let known: BTreeMap<_, _> = known.iter().map(|e| (e.event_id.as_str(), e)).collect();
    let empty = hash("");
    let mut missing = Vec::new();
    for (i, e) in events.iter_mut().enumerate() {
        if e.body_hash.is_empty() {
            continue;
        }
        if !valid(&e.body_hash) {
            return Err("正文摘要无效".into());
        }
        if e.body_hash == empty {
            e.body.clear();
            continue;
        }
        if let Some(old) = known.get(e.event_id.as_str())
            && hash(&old.body) == e.body_hash
        {
            e.body = old.body.clone();
            let _ = store(data, &e.body_hash, &e.body);
            continue;
        }
        if let Some(body) = cached(data, &e.body_hash) {
            e.body = body;
            continue;
        }
        missing.push(i);
    }
    for group in missing.chunks(32) {
        let mut url =
            url::Url::parse(&format!("{base}/api/v1/events/bodies")).map_err(|_| "正文地址无效")?;
        for i in group {
            url.query_pairs_mut()
                .append_pair("id", &events[*i].event_id);
        }
        let bodies: Vec<network::Body> = network::get_json(client, url.as_str(), token, "bodies")?;
        if bodies.len() != group.len() {
            return Err("正文数量不匹配".into());
        }
        for (i, b) in group.iter().zip(bodies) {
            let e = &mut events[*i];
            if b.event_id != e.event_id || b.body_hash != e.body_hash {
                return Err("正文身份不匹配".into());
            };
            store(data, &b.body_hash, &b.body)?;
            e.body = b.body;
        }
    }
    Ok(())
}
// 正文缓存可重建；未读正文同时随现有状态保存，清理不会丢失离线未读。
pub fn prune(data: &Path) {
    let Ok(entries) = fs::read_dir(directory(data)) else {
        return;
    };
    let mut files = Vec::new();
    let mut size = 0;
    for entry in entries.flatten() {
        let name = entry.file_name();
        let Some(name) = name.to_str() else { continue };
        if !valid(name) {
            continue;
        };
        if let Ok(meta) = entry.metadata()
            && meta.is_file()
        {
            size += meta.len();
            files.push((meta.modified().ok(), meta.len(), entry.path()));
        }
    }
    files.sort_by_key(|x| x.0);
    for (_, bytes, path) in files {
        if size <= 32 << 20 {
            break;
        };
        if fs::remove_file(path).is_ok() {
            size = size.saturating_sub(bytes)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn cached_body_survives_status_only_change_and_corruption_refetches() {
        let root = std::env::temp_dir().join(format!("pulse-body-{}", uuid::Uuid::new_v4()));
        let body = "完整中文正文";
        let digest = hash(body);
        store(&root, &digest, body).unwrap();
        assert_eq!(cached(&root, &digest).as_deref(), Some(body));
        let mut events = vec![Event {
            event_id: "one".into(),
            body_hash: digest.clone(),
            ..Default::default()
        }];
        let client: ureq::Agent = ureq::Agent::new_with_defaults();
        hydrate(&client, "http://127.0.0.1:1", "", &root, &mut events, &[]).unwrap();
        assert_eq!(events[0].body, body);
        std::fs::write(directory(&root).join(&digest), "已损坏").unwrap();
        assert!(cached(&root, &digest).is_none());
        let _ = std::fs::remove_dir_all(root);
    }
}
