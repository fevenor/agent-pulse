use crate::{
    model::{Event, Snapshot, State, Store},
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
    pub state: State,
    pub status: String,
    pub alert: Option<Event>,
    pub can_notify: bool,
}

fn view(store: &Store, status: &str) -> View {
    View {
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
        match input.recv_timeout(Duration::from_secs(2)) {
            Ok(Input::Stop) | Err(mpsc::RecvTimeoutError::Disconnected) => return,
            Ok(command) => {
                apply(command, &mut store, &updates, status, &mut desktop);
            }
            _ => {}
        }
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
    url.set_path("/api/v1/connect");
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
        let response = client
            .post(format!("{base}/api/v1/read"))
            .header("Authorization", format!("Bearer {token}"))
            .send_json(json!({"event_ids":chunk}))
            .map_err(|_| "已读同步失败")?;
        // 202 只说明中继已落盘；必须保留本地操作直到收到 Hub 的 200。
        if response.status().as_u16() == 200 {
            store.change(|s| {
                for id in chunk {
                    s.pending_reads.remove(id);
                }
            })?;
        } else if response.status().as_u16() != 202 {
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
    let mut socket = connect(base, token)?;
    socket.send(Message::Text(json!({"type":"hello","payload":{"id":store.state.device_id,"role":"tray","host_name":settings.host_name,"locked":desktop.0,"fullscreen":desktop.1}}).to_string().into())).map_err(|_|"登记设备失败")?;
    let start = Instant::now();
    let mut response_at = Instant::now();
    let mut heartbeat = Instant::now();
    let mut hello = false;
    let mut refresh = false;
    let mut synced = false;
    let mut required: Option<(String, u64)> = None;
    loop {
        let old_desktop = *desktop;
        *desktop = crate::desktop::status();
        while let Ok(command) = input.try_recv() {
            if !apply(command, store, updates, label, desktop) {
                let _ = socket.close(None);
                return Ok(false);
            }
            if hello {
                refresh = true;
                synced = false;
            }
        }
        if old_desktop != *desktop {
            refresh = hello;
            synced = false;
            let _ = updates.send_blocking(view(store, label));
            socket
                .send(Message::Text(
                    json!({"type":"state","payload":{"locked":desktop.0,"fullscreen":desktop.1}})
                        .to_string()
                        .into(),
                ))
                .map_err(|_| "设备状态更新失败")?;
        }
        if heartbeat.elapsed() >= Duration::from_secs(10) {
            socket
                .send(Message::Text(
                    json!({"type":"heartbeat"}).to_string().into(),
                ))
                .map_err(|_| "心跳失败")?;
            heartbeat = Instant::now();
            if !store.state.pending_reads.is_empty() {
                refresh = true;
            }
        }
        let mut quiet = false;
        match socket.read() {
            Ok(Message::Text(text)) => {
                response_at = Instant::now();
                if synced {
                    let _ = updates.send_blocking(view(store, label));
                }
                let message: Value = serde_json::from_str(&text).map_err(|_| "订阅消息无效")?;
                match message["type"].as_str() {
                    Some("hello") => {
                        hello = true;
                        refresh = true;
                    }
                    Some("changed") => {
                        let version = message["payload"]["version"].as_u64().unwrap_or(0);
                        let run = message["payload"]["run_id"].as_str().unwrap_or("");
                        if run != store.state.snapshot.run_id
                            || version > store.state.snapshot.version
                        {
                            refresh = true;
                            required = Some((run.into(), version));
                        }
                    }
                    _ => {}
                }
            }
            Ok(Message::Close(_)) => return Err("订阅关闭".into()),
            Ok(_) => {}
            Err(tungstenite::Error::Io(e))
                if matches!(e.kind(), ErrorKind::WouldBlock | ErrorKind::TimedOut) =>
            {
                quiet = true;
            }
            Err(_) => return Err("订阅断开".into()),
        }
        if hello && refresh {
            flush_reads(client, base, token, store)?;
            let snapshot: Snapshot = client
                .get(format!("{base}/api/v1/snapshot"))
                .header("Authorization", format!("Bearer {token}"))
                .call()
                .map_err(|_| "快照请求失败")?
                .body_mut()
                .with_config()
                .limit(8 << 20)
                .read_json()
                .map_err(|_| "快照无效或超过大小限制")?;
            if required.as_ref().is_some_and(|(run, version)| {
                run == &snapshot.run_id && *version > snapshot.version
            }) {
                continue;
            }
            store.change(|s| {
                s.notified
                    .retain(|id| snapshot.unread.iter().any(|e| &e.event_id == id));
                s.snapshot = snapshot;
            })?;
            refresh = false;
            synced = true;
            quiet = false;
            let _ = updates.send_blocking(view(store, label));
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
        if response_at.elapsed() > Duration::from_secs(35)
            || (!hello && start.elapsed() > Duration::from_secs(10))
        {
            return Err("订阅超时".into());
        }
        if label == "已通过中继连接" && start.elapsed() > Duration::from_secs(30) {
            return Ok(true);
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
