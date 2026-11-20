use agent_pulse_tray::{
    model::Store,
    settings::Settings,
    sync::{self, Input, View},
};
use serde_json::{Value, json};
use std::{
    fs,
    net::TcpListener,
    os::windows::process::CommandExt,
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    thread,
    time::{Duration, Instant},
};

struct Service {
    child: Child,
    exe: PathBuf,
    config: PathBuf,
}
impl Service {
    fn start(exe: PathBuf, config: PathBuf) -> Self {
        let child = Command::new(&exe)
            .arg("--config")
            .arg(&config)
            .creation_flags(0x08000000)
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("请先运行 scripts/build/build.ps1 -DevelopmentTools");
        Self { child, exe, config }
    }
    fn stop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
    fn restart(&mut self) {
        self.stop();
        *self = Self::start(self.exe.clone(), self.config.clone());
    }
}
impl Drop for Service {
    fn drop(&mut self) {
        self.stop();
    }
}
struct Directory(PathBuf);
impl Drop for Directory {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}
fn port() -> u16 {
    TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}
fn wait(mut check: impl FnMut() -> bool) {
    let start = Instant::now();
    while start.elapsed() < Duration::from_secs(18) {
        if check() {
            return;
        }
        thread::sleep(Duration::from_millis(50));
    }
    panic!("联调条件未在限定时间内满足");
}
fn json_file(path: &Path, value: Value) {
    fs::write(path, serde_json::to_vec(&value).unwrap()).unwrap();
}
fn wait_view(rx: &async_channel::Receiver<View>, check: impl Fn(&View) -> bool) -> View {
    let mut result = None;
    wait(|| {
        while let Ok(view) = rx.try_recv() {
            if check(&view) {
                result = Some(view);
                return true;
            }
        }
        false
    });
    result.unwrap()
}
fn post(base: &str, id: &str) {
    ureq::post(format!("{base}/api/v1/events")).send_json(json!({"event_id":id,"source_id":"isolated-windows","agent":"test","session_id":id,"kind":"turn.completed","occurred_at":"2099-01-01T00:00:00Z","title":"隔离测试完成","body":"中文正文","scope":"main"})).unwrap();
}

#[test]
#[ignore = "需要先构建 Windows Go 服务；显式运行真实本地联调"]
fn real_services_offline_reads_restart_and_relay() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).parent().unwrap();
    let dir = Directory(std::env::temp_dir().join(format!("agent-pulse-{}", uuid::Uuid::new_v4())));
    fs::create_dir(&dir.0).unwrap();
    let hp = port();
    let rp = port();
    let hub = format!("http://127.0.0.1:{hp}");
    let relay = format!("http://127.0.0.1:{rp}");
    let hc = dir.0.join("hub.json");
    let rc = dir.0.join("relay.json");
    let state = dir.0.join("state.json");
    json_file(
        &hc,
        json!({"listen":format!("127.0.0.1:{hp}"),"database":dir.0.join("hub.db"),"relay_url":relay,"relay_token":"isolated-test-token","push":[]}),
    );
    json_file(
        &rc,
        json!({"listen":format!("127.0.0.1:{rp}"),"database":dir.0.join("relay.db"),"token":"isolated-test-token"}),
    );
    let _relay = Service::start(root.join("bin/dev/agent-pulse-relay.exe"), rc);
    let mut server = Service::start(root.join("bin/dev/agent-pulse-hub.exe"), hc);
    wait(|| ureq::get(format!("{hub}/healthz")).call().is_ok());
    let (tx, rx) = async_channel::unbounded();
    let settings = Settings {
        hub_url: hub.clone(),
        state_file: state.clone(),
        data_dir: dir.0.clone(),
        ..Default::default()
    };
    let worker = sync::start(settings.clone(), Store::open(&state).unwrap(), tx);
    wait_view(&rx, |v| v.status == "已直连服务");
    assert_eq!(settings.relay().relay_url, relay);
    assert_eq!(
        agent_pulse_tray::discovery::cached(&settings).relay_token,
        "isolated-test-token"
    );
    post(&hub, "one");
    let first = wait_view(&rx, |v| {
        v.state.unread().iter().any(|e| e.event_id == "one")
    });
    server.stop();
    worker.input.send(Input::Read(vec!["one".into()])).unwrap();
    wait_view(&rx, |v| {
        v.state.pending_reads.contains("one") && v.state.unread().is_empty()
    });
    drop(worker);
    let store = Store::open(&state).unwrap();
    assert!(store.state.pending_reads.contains("one"));
    server.restart();
    wait(|| ureq::get(format!("{hub}/healthz")).call().is_ok());
    let (tx, rx) = async_channel::unbounded();
    let worker = sync::start(settings, store, tx);
    wait_view(&rx, |v| {
        v.status == "已直连服务"
            && v.state.pending_reads.is_empty()
            && v.state.unread().is_empty()
            && v.state.snapshot.run_id != first.state.snapshot.run_id
    });
    post(&hub, "two");
    wait_view(&rx, |v| {
        v.state.unread().iter().any(|e| e.event_id == "two")
    });
    ureq::post(format!("{hub}/api/v1/read"))
        .send_json(json!({"event_ids":["two"]}))
        .unwrap();
    wait_view(&rx, |v| v.state.unread().is_empty());
    drop(worker);
    // 直连不可用时通过真实 Relay 取快照，再验证 Hub 确认的已读清理。
    let (tx, rx) = async_channel::unbounded();
    let settings = Settings {
        hub_url: format!("http://127.0.0.1:{}", port()),
        state_file: state.clone(),
        data_dir: dir.0.clone(),
        ..Default::default()
    };
    // 模拟离开内网后只能连接已发现的中继；不在配置中重复填写凭据。
    *settings.discovered_relay.write().unwrap() = agent_pulse_tray::discovery::Relay {
        relay_url: relay,
        relay_token: "isolated-test-token".into(),
    };
    let worker = sync::start(settings, Store::open(&state).unwrap(), tx);
    wait_view(&rx, |v| v.status == "已通过中继连接");
    post(&hub, "relay-event");
    wait_view(&rx, |v| {
        v.state.unread().iter().any(|e| e.event_id == "relay-event")
    });
    worker
        .input
        .send(Input::Read(vec!["relay-event".into()]))
        .unwrap();
    wait_view(&rx, |v| {
        v.status == "已通过中继连接"
            && v.state.pending_reads.is_empty()
            && v.state.unread().is_empty()
    });
    drop(worker);
}

#[test]
#[ignore = "需要先构建Windows Hub，显式检查真实时间线请求和扫描位置"]
fn real_timeline_filter_anchor_and_incremental_cursor() {
    use agent_pulse_tray::{
        model::SessionKey,
        sessions::{self, Reply, Request},
    };
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).parent().unwrap();
    let dir = Directory(
        std::env::temp_dir().join(format!("agent-pulse-timeline-{}", uuid::Uuid::new_v4())),
    );
    fs::create_dir(&dir.0).unwrap();
    let hp = port();
    let base = format!("http://127.0.0.1:{hp}");
    let config = dir.0.join("hub.json");
    json_file(
        &config,
        json!({"listen":format!("127.0.0.1:{hp}"),"database":dir.0.join("hub.db"),"push":[]}),
    );
    let _server = Service::start(root.join("bin/dev/agent-pulse-hub.exe"), config);
    wait(|| ureq::get(format!("{base}/healthz")).call().is_ok());
    let emit = |id: &str, kind: &str| {
        ureq::post(format!("{base}/api/v1/events")).send_json(json!({"event_id":id,"source_id":"source","agent":"test","session_id":"main","kind":kind,"title":if kind=="activity.observed"{"test · 正在运行"}else{"完成"},"body":"","occurred_at":"2026-11-19T00:00:00Z","scope":"main"})).unwrap();
    };
    emit("old", "turn.completed");
    for i in 0..90 {
        emit(&format!("running-{i}"), "activity.observed");
    }
    emit("new", "turn.completed");
    let (tx, rx) = async_channel::unbounded();
    let worker = sessions::start(
        Settings {
            hub_url: base.clone(),
            ..Default::default()
        },
        tx,
    );
    let key = SessionKey {
        source_id: "source".into(),
        agent: "test".into(),
        session_id: "main".into(),
    };
    worker
        .send(Request::History {
            key: key.clone(),
            before: 0,
            anchor: Some("old".into()),
            all: false,
            generation: 1,
            checked_event: "new".into(),
        })
        .unwrap();
    let mut answer = None;
    wait(|| {
        answer = rx.try_recv().ok();
        answer.is_some()
    });
    let Some(Reply::History {
        result: Ok(first),
        generation: 1,
        checked_event,
        ..
    }) = answer
    else {
        panic!("实际历史接口未正确解码")
    };
    assert_eq!(first.events.len(), 1);
    assert_eq!(first.events[0].event_id, "old");
    assert!(first.newer);
    assert_eq!(checked_event, "new");
    assert!(first.cursor > first.events[0].position);
    emit("running-only", "activity.observed");
    worker
        .send(Request::Latest {
            key: key.clone(),
            after: first.cursor,
            all: false,
            generation: 1,
            checked_event: "running-only".into(),
            run_id: first.run_id.clone(),
        })
        .unwrap();
    let mut answer = None;
    wait(|| {
        answer = rx.try_recv().ok();
        answer.is_some()
    });
    let Some(Reply::Latest {
        result: Ok(next),
        run_id,
        ..
    }) = answer
    else {
        panic!("实际增量接口未正确解码")
    };
    assert_eq!(run_id, first.run_id);
    assert!(next.events.is_empty());
    assert!(next.cursor > first.cursor);
    assert!(!next.more);
    worker
        .send(Request::History {
            key,
            before: 0,
            anchor: None,
            all: true,
            generation: 2,
            checked_event: "running-only".into(),
        })
        .unwrap();
    let mut answer = None;
    wait(|| {
        answer = rx.try_recv().ok();
        answer.is_some()
    });
    let Some(Reply::History {
        result: Ok(full), ..
    }) = answer
    else {
        panic!("完整活动接口未正确解码")
    };
    assert_eq!(full.events.len(), 30);
    assert!(full.more);
    assert!(full.events.iter().any(|e| e.kind == "activity.observed"));
    drop(worker);
}

#[test]
#[ignore = "需要真实Hub进程，验证增量正文只取一次与空闲无快照"]
fn real_incremental_body_cache_and_idle_traffic() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).parent().unwrap();
    let dir =
        Directory(std::env::temp_dir().join(format!("pulse-traffic-{}", uuid::Uuid::new_v4())));
    fs::create_dir(&dir.0).unwrap();
    let hp = port();
    let hub = format!("http://127.0.0.1:{hp}");
    let hc = dir.0.join("hub.json");
    json_file(
        &hc,
        json!({"listen":format!("127.0.0.1:{hp}"),"database":dir.0.join("hub.db"),"push":[]}),
    );
    let _server = Service::start(root.join("bin/dev/agent-pulse-hub.exe"), hc);
    wait(|| ureq::get(format!("{hub}/healthz")).call().is_ok());
    for i in 0..64 {
        ureq::post(format!("{hub}/api/v1/events")).send_json(json!({"event_id":format!("history-{i}"),"source_id":"source","agent":"test","session_id":format!("session-{i}"),"kind":"activity.observed","occurred_at":"2099-01-01T00:00:00Z","title":"test · 正在运行","body":"","scope":"main"})).unwrap();
    }
    let state = dir.0.join("state.json");
    let (tx, rx) = async_channel::unbounded();
    let settings = Settings {
        hub_url: hub.clone(),
        state_file: state.clone(),
        data_dir: dir.0.clone(),
        ..Default::default()
    };
    let worker = sync::start(settings, Store::open(&state).unwrap(), tx);
    wait_view(&rx, |v| {
        v.status == "已直连服务" && v.state.snapshot.sessions.len() == 64
    });
    let body = "完整正文缓存验证。".repeat(1200);
    ureq::post(format!("{hub}/api/v1/events")).send_json(json!({"event_id":"body-once","source_id":"source","agent":"test","session_id":"active","kind":"turn.completed","occurred_at":"2099-01-01T00:00:00Z","title":"完成","body":body,"scope":"main"})).unwrap();
    wait_view(&rx, |v| {
        v.state
            .unread()
            .iter()
            .any(|e| e.event_id == "body-once" && e.body == body)
    });
    for i in 0..30 {
        ureq::post(format!("{hub}/api/v1/events")).send_json(json!({"event_id":format!("activity-{i}"),"source_id":"source","agent":"test","session_id":"active","kind":"activity.observed","occurred_at":"2099-01-01T00:00:00Z","title":"test · 正在运行","body":"","scope":"main"})).unwrap();
    }
    wait_view(&rx, |v| {
        v.state
            .snapshot
            .sessions
            .iter()
            .any(|s| s.last_event_id == "activity-29")
    });
    let traffic = || -> Value {
        ureq::get(format!("{hub}/api/v1/traffic"))
            .call()
            .unwrap()
            .body_mut()
            .read_json()
            .unwrap()
    };
    let before = traffic();
    assert_eq!(
        before["categories"]["http_server/bodies"]["requests"].as_u64(),
        Some(1)
    );
    let wait_start = Instant::now();
    while wait_start.elapsed() < Duration::from_secs(40) {
        while rx.try_recv().is_ok() {}
        thread::sleep(Duration::from_millis(100));
    }
    let after = traffic();
    assert_eq!(
        after["categories"]["http_server/bodies"]["requests"].as_u64(),
        Some(1)
    );
    assert!(after["categories"].get("http_server/snapshot").is_none());
    let idle_sent = after["categories"]["sync"]["sent"].as_u64().unwrap()
        - before["categories"]["sync"]["sent"].as_u64().unwrap();
    assert!(idle_sent < 4096, "40秒空闲不应重传快照或正文：{idle_sent}");
    eprintln!(
        "真实Hub/Tray：64条历史会话，约{}字节正文只请求一次，40秒空闲服务端同步发送{}字节，无HTTP快照请求",
        body.len(),
        idle_sent
    );
    worker
        .input
        .send(Input::Read(vec!["body-once".into()]))
        .unwrap();
    wait_view(&rx, |v| {
        v.state.pending_reads.is_empty() && v.state.unread().is_empty()
    });
    drop(worker);
}
