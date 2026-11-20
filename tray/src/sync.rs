use crate::{
    model::{Event, State, Store},
    settings::Settings,
};
use serde_json::{Value, json};
use std::{
    io::ErrorKind,
    net::{TcpStream, ToSocketAddrs},
    sync::mpsc,
    time::{Duration, Instant},
};
use tungstenite::{Message, client::IntoClientRequest, stream::MaybeTlsStream};

pub enum Input {
    Refresh,
    Notified(Vec<String>),
    Read(Vec<String>),
    Stop,
    Desktop {
        locked: Option<bool>,
        fullscreen: Option<bool>,
    },
}
#[derive(Clone)]
pub struct View {
    pub snapshot_confirmed_at: Option<Instant>,
    pub state: State,
    pub status: String,
    pub alert: Option<Event>,
    pub can_notify: bool,
}

impl View {
    pub fn current(&self) -> bool {
        self.snapshot_confirmed_at
            .is_some_and(|at| at.elapsed() < Duration::from_secs(35))
    }
}

fn view(store: &Store, status: &str) -> View {
    View {
        snapshot_confirmed_at: store.snapshot_confirmed_at,
        state: store.state.clone(),
        status: store.error.clone().unwrap_or_else(|| status.into()),
        alert: None,
        can_notify: crate::desktop::status() == (Some(false), Some(false)),
    }
}

// 网络和落盘只有一个工作线程；GUI 只接收完成后的状态，不等待网络。
pub struct Worker {
    pub input: mpsc::Sender<Input>,
    handle: Option<std::thread::JoinHandle<()>>,
}
impl Drop for Worker {
    fn drop(&mut self) {
        let _ = self.input.send(Input::Stop);
        if let Some(handle) = self.handle.take() {
            let _ = handle.join();
        }
    }
}
pub fn start(settings: Settings, store: Store, updates: async_channel::Sender<View>) -> Worker {
    let (tx, rx) = mpsc::channel();
    let handle = std::thread::spawn(move || worker(settings, store, rx, updates));
    Worker {
        input: tx,
        handle: Some(handle),
    }
}

fn worker(
    settings: Settings,
    mut store: Store,
    input: mpsc::Receiver<Input>,
    updates: async_channel::Sender<View>,
) {
    let client: ureq::Agent = ureq::Agent::config_builder()
        .timeout_global(Some(Duration::from_secs(8)))
        .max_redirects(0)
        .build()
        .into();
    let mut desktop = crate::desktop::status();
    let mut retry_seconds = 2;
    let _ = updates.send_blocking(view(
        &store,
        if settings.hub_url.is_empty() && settings.relay().relay_url.is_empty() {
            "尚未配置连接"
        } else {
            "正在连接"
        },
    ));
    loop {
        crate::discovery::refresh(&settings);
        let relay = settings.relay();
        let endpoints = [
            (&settings.hub_url, &settings.token, "已直连服务"),
            (&relay.relay_url, &relay.relay_token, "已通过中继连接"),
        ];
        let connection_started = Instant::now();
        for (base, token, label) in endpoints.iter().filter(|e| !e.0.is_empty()) {
            let result = connected(
                (base, token, label),
                &client,
                &mut store,
                &input,
                &updates,
                &mut desktop,
                &settings,
            );
            store.snapshot_confirmed_at = None;
            let _ = updates.send_blocking(view(&store, "连接中断 · 显示本地缓存"));
            if matches!(result, Ok(false)) {
                return;
            }
        }
        let status = if settings.hub_url.is_empty() && settings.relay().relay_url.is_empty() {
            "尚未配置连接"
        } else {
            "连接中断 · 显示本地缓存"
        };
        let _ = updates.send_blocking(view(&store, status));
        if connection_started.elapsed() > Duration::from_secs(35) {
            retry_seconds = 2
        }
        match input.recv_timeout(Duration::from_secs(retry_seconds)) {
            Ok(Input::Stop) | Err(mpsc::RecvTimeoutError::Disconnected) => return,
            Ok(command) => {
                apply(command, &mut store, &updates, status, &mut desktop);
            }
            _ => {}
        }
        retry_seconds = (retry_seconds * 2).min(30);
    }
}

fn apply(
    command: Input,
    store: &mut Store,
    updates: &async_channel::Sender<View>,
    status: &str,
    desktop: &mut (Option<bool>, Option<bool>),
) -> bool {
    match command {
        Input::Stop => return false,
        Input::Refresh => {}
        Input::Notified(ids) => {
            let _ = store.change(|s| s.notified.extend(ids));
        }
        Input::Desktop { locked, fullscreen } => *desktop = (locked, fullscreen),
        Input::Read(ids) => {
            store.error = None;
            let result = store.change(|s| s.pending_reads.extend(ids));
            let _ = updates.send_blocking(view(
                store,
                result.as_ref().err().map(String::as_str).unwrap_or(status),
            ));
        }
    };
    true
}

fn connect(
    base: &str,
    token: &str,
) -> Result<tungstenite::WebSocket<MaybeTlsStream<TcpStream>>, String> {
    let mut url = url::Url::parse(base).map_err(|_| "服务地址无效")?;
    url.set_scheme(if base.starts_with("https:") {
        "wss"
    } else {
        "ws"
    })
    .map_err(|_| "服务地址无效")?;
    url.set_path("/api/v1/sync");
    let mut request = url
        .as_str()
        .into_client_request()
        .map_err(|_| "订阅请求无效")?;
    if !token.is_empty() {
        request.headers_mut().insert(
            "Authorization",
            format!("Bearer {token}").parse().map_err(|_| "令牌无效")?,
        );
    }
    let host = url.host_str().ok_or("地址缺少主机")?;
    let mut stream = None;
    for address in (host, url.port_or_known_default().ok_or("端口无效")?)
        .to_socket_addrs()
        .map_err(|_| "无法解析服务地址")?
    {
        if let Ok(s) = TcpStream::connect_timeout(&address, Duration::from_secs(4)) {
            stream = Some(s);
            break;
        }
    }
    let stream = stream.ok_or("无法连接服务")?;
    stream
        .set_read_timeout(Some(Duration::from_secs(8)))
        .map_err(|_| "连接超时设置失败")?;
    stream
        .set_write_timeout(Some(Duration::from_secs(8)))
        .map_err(|_| "连接超时设置失败")?;
    let (socket, _) = tungstenite::client_tls(request, stream).map_err(|_| "订阅握手失败")?;
    match socket.get_ref() {
        MaybeTlsStream::Plain(s) => s.set_read_timeout(Some(Duration::from_secs(1))),
        MaybeTlsStream::Rustls(s) => s.get_ref().set_read_timeout(Some(Duration::from_secs(1))),
        _ => return Err("不支持的连接类型".into()),
    }
    .map_err(|_| "连接超时设置失败")?;
    Ok(socket)
}

fn flush_reads(
    client: &ureq::Agent,
    base: &str,
    token: &str,
    store: &mut Store,
) -> Result<(), String> {
    let ids: Vec<_> = store.state.pending_reads.iter().cloned().collect();
    for chunk in ids.chunks(256) {
        let payload = json!({"event_ids":chunk});
        crate::network::record("reads", payload.to_string().len(), 0);
        let mut response = client
            .post(format!("{base}/api/v1/read"))
            .header("Authorization", format!("Bearer {token}"))
            .send_json(payload)
            .map_err(|_| "已读同步失败")?;
        // 202 只说明中继已落盘；必须保留本地操作直到收到 Hub 的 200。
        let status = response.status().as_u16();
        let raw = response
            .body_mut()
            .with_config()
            .limit(65536)
            .read_to_vec()
            .map_err(|_| "已读回执读取失败")?;
        crate::network::record("reads", 0, raw.len());
        if status == 200 {
            store.change(|s| {
                for id in chunk {
                    s.pending_reads.remove(id);
                }
            })?;
        } else if status != 202 {
            return Err("已读确认无效".into());
        }
    }
    Ok(())
}

fn connected(
    endpoint: (&str, &str, &str),
    client: &ureq::Agent,
    store: &mut Store,
    input: &mpsc::Receiver<Input>,
    updates: &async_channel::Sender<View>,
    desktop: &mut (Option<bool>, Option<bool>),
    settings: &Settings,
) -> Result<bool, String> {
    let (base, token, label) = endpoint;
    store.snapshot_confirmed_at = None;
    flush_reads(client, base, token, store)?;
    let mut socket = connect(base, token)?;
    crate::network::record("connections", 0, 0);
    let cursor = if store.force_baseline {
        json!({"run_id":"","version":0})
    } else {
        json!({"run_id":store.state.snapshot.run_id,"version":store.state.snapshot.version})
    };
    let probe = uuid::Uuid::new_v4().to_string();
    let mut probes = std::collections::BTreeMap::from([(probe.clone(), Instant::now())]);
    let hello=json!({"type":"sync_hello","request_id":probe,"payload":{"device":{"id":store.state.device_id,"role":"tray","host_name":settings.host_name,"locked":desktop.0,"fullscreen":desktop.1},"cursor":cursor}}).to_string();
    crate::network::record("sync", hello.len(), 0);
    socket
        .send(Message::Text(hello.into()))
        .map_err(|_| "增量登记失败")?;
    let mut response_at = Instant::now();
    let mut heartbeat = Instant::now();
    let mut synced = false;
    let mut next_probe = Instant::now() + Duration::from_secs(30);
    let mut probe_interval = 30;
    let (mut probe_tx, mut probe_rx): (Option<mpsc::Sender<bool>>, Option<mpsc::Receiver<bool>>) =
        (None, None);
    let mut stats_at = Instant::now();
    loop {
        let old_desktop = *desktop;
        *desktop = crate::desktop::status();
        let mut read_changed = false;
        while let Ok(command) = input.try_recv() {
            read_changed |= matches!(command, Input::Read(_) | Input::Refresh);
            if !apply(command, store, updates, label, desktop) {
                let _ = socket.close(None);
                return Ok(false);
            }
        }
        if read_changed {
            flush_reads(client, base, token, store)?;
        }
        if old_desktop != *desktop {
            let msg = json!({"type":"state","payload":{"locked":desktop.0,"fullscreen":desktop.1}})
                .to_string();
            crate::network::record("sync", msg.len(), 0);
            socket
                .send(Message::Text(msg.into()))
                .map_err(|_| "设备状态递交失败")?;
        }
        if heartbeat.elapsed() >= Duration::from_secs(10) {
            let probe = uuid::Uuid::new_v4().to_string();
            probes.retain(|_, at| at.elapsed() < Duration::from_secs(35));
            probes.insert(probe.clone(), Instant::now());
            let msg=json!({"type":"heartbeat","request_id":probe,"payload":{"run_id":store.state.snapshot.run_id,"version":store.state.snapshot.version}}).to_string();
            crate::network::record("sync", msg.len(), 0);
            socket
                .send(Message::Text(msg.into()))
                .map_err(|_| "同步心跳失败")?;
            heartbeat = Instant::now();
            if !store.state.pending_reads.is_empty() {
                flush_reads(client, base, token, store)?;
            }
        }
        let mut quiet = false;
        let bytes = match socket.read() {
            Ok(Message::Text(text)) => {
                crate::network::record("sync", 0, text.len());
                Some(text.as_bytes().to_vec())
            }
            Ok(Message::Binary(raw)) => {
                crate::network::record("sync", 0, raw.len());
                Some(crate::network::decode(&raw, true)?)
            }
            Ok(Message::Close(_)) => return Err("同步连接已关闭".into()),
            Ok(_) => None,
            Err(tungstenite::Error::Io(e))
                if matches!(e.kind(), ErrorKind::WouldBlock | ErrorKind::TimedOut) =>
            {
                quiet = true;
                None
            }
            Err(_) => return Err("同步连接中断".into()),
        };
        if let Some(bytes) = bytes {
            if bytes.len() > 8 << 20 {
                return Err("同步消息过大".into());
            }
            response_at = Instant::now();
            let message: Value = serde_json::from_slice(&bytes).map_err(|_| "同步消息无效")?;
            match message["type"].as_str() {
                Some("sync") => {
                    store.snapshot_confirmed_at = None;
                    synced = false;
                    let delta: crate::delta::Delta =
                        serde_json::from_value(message["payload"].clone())
                            .map_err(|_| "增量格式无效")?;
                    store.force_baseline = true;
                    let mut snapshot = delta.apply(&store.state.snapshot)?;
                    crate::body_cache::hydrate(
                        client,
                        base,
                        token,
                        &settings.data_dir,
                        &mut snapshot.unread,
                        &store.state.snapshot.unread,
                    )?;
                    store.change(|s| {
                        s.notified
                            .retain(|id| snapshot.unread.iter().any(|e| &e.event_id == id));
                        s.snapshot = snapshot;
                    })?;
                    store.force_baseline = false;
                    let ack=json!({"type":"ack","payload":{"run_id":store.state.snapshot.run_id,"version":store.state.snapshot.version}}).to_string();
                    crate::network::record("sync", ack.len(), 0);
                    socket
                        .send(Message::Text(ack.into()))
                        .map_err(|_| "同步确认失败")?;
                }
                Some("checkpoint") => {
                    if message["payload"]["run_id"].as_str() != Some(&store.state.snapshot.run_id)
                        || message["payload"]["version"].as_u64()
                            != Some(store.state.snapshot.version)
                    {
                        store.force_baseline = true;
                        return Err("权威状态位置不匹配".into());
                    }
                    let confirmed = message["payload"]["probe"]
                        .as_str()
                        .and_then(|id| probes.get(id))
                        .copied()
                        .filter(|at| at.elapsed() < Duration::from_secs(35))
                        .ok_or("权威状态确认已过期")?;
                    store.snapshot_confirmed_at = Some(confirmed);
                    synced = true;
                    let _ = updates.send_blocking(view(store, label));
                }
                _ => return Err("不支持的同步消息".into()),
            }
        }
        // 设备未知、锁屏和全屏均不弹出；解锁后先同步已读与快照。
        if synced && quiet && desktop.0 == Some(false) && desktop.1 == Some(false) {
            let candidates = store.state.notifications();
            let muted = settings.preferences.read().unwrap().muted_sources.clone();
            let suppressed: Vec<_> = candidates
                .iter()
                .filter(|e| muted.contains(&e.source_id))
                .map(|e| e.event_id.clone())
                .collect();
            if !suppressed.is_empty() {
                store.change(|s| s.notified.extend(suppressed))?;
            }
            let notifications = store.state.notifications_for(&muted);
            if !notifications.is_empty() {
                let mut update = view(store, label);
                let count = notifications.len();
                let mut alert = notifications.into_iter().next().unwrap();
                if count > 1 {
                    alert.source_id.clear();
                    alert.session_id.clear();
                    alert.event_id.clear();
                    alert.title = format!("{count} 个会话有新提醒");
                    alert.body = "打开列表查看各会话的最新结果。".into();
                }
                update.alert = Some(alert);
                let _ = updates.send_blocking(update);
            }
        }

        if response_at.elapsed() > Duration::from_secs(35) {
            return Err("同步确认超时".into());
        }
        if label == "已通过中继连接" {
            if let Some(rx) = &probe_rx
                && let Ok(healthy) = rx.try_recv()
            {
                probe_tx = None;
                probe_rx = None;
                if healthy {
                    return Ok(true);
                }
                probe_interval = (probe_interval * 2).min(300);
                next_probe = Instant::now() + Duration::from_secs(probe_interval);
            }
            if probe_tx.is_none() && Instant::now() >= next_probe {
                let (tx, rx) = mpsc::channel();
                probe_tx = Some(tx.clone());
                probe_rx = Some(rx);
                let hub = settings.hub_url.clone();
                let token = settings.token.clone();
                std::thread::spawn(move || {
                    let c: ureq::Agent = ureq::Agent::config_builder()
                        .timeout_global(Some(Duration::from_secs(3)))
                        .max_redirects(0)
                        .build()
                        .into();
                    let good = c
                        .get(format!("{hub}/healthz"))
                        .header("Authorization", format!("Bearer {token}"))
                        .call()
                        .is_ok();
                    let _ = tx.send(good);
                });
            }
        }
        if stats_at.elapsed() >= Duration::from_secs(60) {
            crate::network::save(&settings.data_dir.join("traffic.json"));
            crate::body_cache::prune(&settings.data_dir);
            stats_at = Instant::now();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    #[test]
    fn relay_acceptance_keeps_pending_until_hub_commit() {
        let dir = std::env::temp_dir().join(uuid::Uuid::new_v4().to_string());
        let mut store = Store::open(&dir.join("state.json")).unwrap();
        store
            .change(|s| {
                s.pending_reads.insert("offline-read".into());
            })
            .unwrap();
        let client: ureq::Agent = ureq::Agent::config_builder()
            .timeout_global(Some(Duration::from_secs(3)))
            .build()
            .into();
        for status in [202, 200] {
            let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            let address = listener.local_addr().unwrap();
            let server = std::thread::spawn(move || {
                let (mut stream, _) = listener.accept().unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(3)))
                    .unwrap();
                let mut request = Vec::new();
                loop {
                    let mut raw = [0u8; 1024];
                    let count = stream.read(&mut raw).unwrap();
                    assert!(count > 0, "请求提前关闭");
                    request.extend_from_slice(&raw[..count]);
                    if let Some(header_end) = request.windows(4).position(|w| w == b"\r\n\r\n") {
                        let headers = String::from_utf8_lossy(&request[..header_end]);
                        let length = headers
                            .lines()
                            .find_map(|line| {
                                line.to_ascii_lowercase()
                                    .strip_prefix("content-length:")
                                    .and_then(|v| v.trim().parse::<usize>().ok())
                            })
                            .unwrap();
                        if request.len() >= header_end + 4 + length {
                            break;
                        }
                    }
                }
                stream.write_all(format!("HTTP/1.1 {status} OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{{}}").as_bytes()).unwrap();
            });
            flush_reads(&client, &format!("http://{address}"), "", &mut store).unwrap();
            server.join().unwrap();
            assert_eq!(
                store.state.pending_reads.contains("offline-read"),
                status == 202
            );
        }
        drop(store);
        std::fs::remove_dir_all(dir).unwrap();
    }
}

#[cfg(test)]
mod freshness_tests {
    use super::*;
    #[test]
    fn only_recent_authoritative_snapshot_allows_wake() {
        let mut view = View {
            snapshot_confirmed_at: None,
            state: State::default(),
            status: "已直连服务".into(),
            alert: None,
            can_notify: false,
        };
        assert!(!view.current());
        view.snapshot_confirmed_at = Some(Instant::now());
        assert!(view.current());
        view.snapshot_confirmed_at = Instant::now().checked_sub(Duration::from_secs(36));
        assert!(!view.current());
        // 界面文字、已读和提醒更新不改变权威快照确认时间。
        view.status = "已通过中继连接".into();
        view.can_notify = true;
        assert!(!view.current());
    }
}
