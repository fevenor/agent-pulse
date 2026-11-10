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
            .expect("请先运行 scripts/build.ps1 -DevelopmentTools");
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
        relay_url: relay.clone(),
        relay_token: "isolated-test-token".into(),
        state_file: state.clone(),
        ..Default::default()
    };
    let worker = sync::start(settings.clone(), Store::open(&state).unwrap(), tx);
    wait_view(&rx, |v| v.status == "已直连服务");
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
        relay_url: relay,
        relay_token: "isolated-test-token".into(),
        state_file: state.clone(),
        ..Default::default()
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
