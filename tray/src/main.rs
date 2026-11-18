#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]
#[cfg(all(feature = "dev-demo", not(debug_assertions)))]
compile_error!("发布构建禁止包含演示功能；请使用 preview profile");
mod artwork;
mod assets;
#[cfg(feature = "dev-demo")]
mod demo;
mod floating;
mod icon;
mod platform;
mod ui;
use agent_pulse_tray::{
    autostart::{Registration, Status as StartupStatus},
    instance::Instance,
    model::{Session, SessionKey, Store},
    sessions::{self, Reply, Request},
    settings::Settings,
    sync::{self, Input, View},
};
use gpui::{
    App, AppContext, Application, Bounds, WindowBounds, WindowHandle, WindowKind, WindowOptions,
    px, size,
};
#[cfg(feature = "dev-demo")]
use std::sync::atomic::{AtomicBool, Ordering};
use std::{
    cell::RefCell,
    path::PathBuf,
    rc::Rc,
    time::{Duration, Instant},
};
use ui::{Kind, Shared, Surface};

#[derive(Clone, Debug)]
enum Command {
    Show(bool),
    Active,
    Attention,
    Detail(SessionKey),
    MenuSession(String),
    ToggleFloat,
    HostPolicy(String, bool),
    AwayMinutes(u64),
    ToggleEvent(String),
    ReportVisible(String),
    ResetFloat,
    Drag(i32, i32),
    Settings,
    Autostart(bool),
    StartupSettings,
    BridgeSetup,
    BridgeSetupFinished(Result<bool, String>),
    Read(Vec<String>),
    ReadAll(SessionKey),
    Manage(Box<Session>, &'static str, serde_json::Value),
    Older,
    Latest,
    Quit,
    #[cfg(feature = "dev-demo")]
    DemoAlert,
    #[cfg(feature = "dev-demo")]
    DemoMenu,
    #[cfg(feature = "dev-demo")]
    DemoTheme,
    #[cfg(feature = "dev-demo")]
    DemoLong,
}
#[cfg(feature = "dev-demo")]
static STOP: AtomicBool = AtomicBool::new(false);
#[cfg(feature = "dev-demo")]
unsafe extern "system" fn console_control(_: u32) -> windows::core::BOOL {
    STOP.store(true, Ordering::Relaxed);
    true.into()
}
fn main() {
    let args: Vec<_> = std::env::args_os().collect();
    if args
        .get(1)
        .is_some_and(|arg| arg == "--bridge-setup-console")
    {
        let code = if args.len() == 3 {
            platform::Bridge::setup_console(std::path::Path::new(&args[2]))
        } else {
            1
        };
        std::process::exit(code);
    }
    if let Err(e) = run() {
        eprintln!("AgentPulse：{e}");
        if !cfg!(debug_assertions) {
            if e.starts_with("不支持的参数") {
                unsafe {
                    let _ = windows::Win32::System::Console::AttachConsole(
                        windows::Win32::System::Console::ATTACH_PARENT_PROCESS,
                    );
                }
                eprintln!("AgentPulse：{e}");
            } else {
                platform::error(&e);
            }
        }
        std::process::exit(1);
    }
}
fn window(
    kind: Kind,
    shared: Rc<RefCell<Shared>>,
    tx: async_channel::Sender<Command>,
    cx: &mut App,
) -> WindowHandle<Surface> {
    let (width, height, title) = match kind {
        Kind::List => (360., 400., "AgentPulse — 会话列表"),
        Kind::Detail => (760., 560., "AgentPulse — 会话详情"),
        Kind::Float => (44., 44., "AgentPulse — 悬浮图标"),
        Kind::Card => (320., 180., "AgentPulse — 提醒"),
        Kind::Settings => (460., 420., "AgentPulse — 设置"),
    };
    let popup = matches!(kind, Kind::List | Kind::Float | Kind::Card);
    let options = WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(Bounds::centered(
            None,
            size(px(width), px(height)),
            cx,
        ))),
        show: false,
        focus: false,
        kind: if popup {
            WindowKind::PopUp
        } else {
            WindowKind::Normal
        },
        is_resizable: !popup,
        is_minimizable: !popup,
        window_min_size: match kind {
            Kind::Settings => Some(size(px(400.), px(320.))),
            Kind::Detail => Some(size(px(520.), px(360.))),
            _ => None,
        },
        ..Default::default()
    };
    cx.open_window(options, move |w, cx| {
        w.set_window_title(title);
        platform::set_window_icon(w);
        platform::preserve_display_visibility(w);
        if popup {
            platform::borderless(w, kind == Kind::Float || kind == Kind::Card);
        } else if kind == Kind::Settings {
            platform::disable_maximize(w);
        }
        w.on_window_should_close(cx, |w, _| {
            platform::hide(w);
            false
        });
        cx.new(|cx| Surface::new(kind, shared, tx, w, cx))
    })
    .expect("无法创建桌面窗口")
}
// 输入组件要求专用根视图；仅设置窗口使用它，其他窗口保持原有轻量Surface。
fn settings_window(
    shared: Rc<RefCell<Shared>>,
    tx: async_channel::Sender<Command>,
    cx: &mut App,
) -> WindowHandle<gpui_component::Root> {
    let options = WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(Bounds::centered(
            None,
            size(px(560.), px(620.)),
            cx,
        ))),
        show: false,
        focus: false,
        kind: WindowKind::Normal,
        is_resizable: true,
        is_minimizable: true,
        window_min_size: Some(size(px(400.), px(320.))),
        ..Default::default()
    };
    cx.open_window(options, move |w, cx| {
        w.set_window_title("AgentPulse — 设置");
        platform::set_window_icon(w);
        platform::preserve_display_visibility(w);
        platform::disable_maximize(w);
        w.on_window_should_close(cx, |w, cx| {
            w.blur();
            platform::hide(w);
            cx.refresh_windows();
            false
        });
        let content = cx.new(|cx| Surface::new(Kind::Settings, shared, tx, w, cx));
        cx.new(|cx| gpui_component::Root::new(content, w, cx))
    })
    .expect("无法创建设置窗口")
}
fn notify(handle: impl Into<gpui::AnyWindowHandle>, cx: &mut gpui::AsyncApp) {
    let _ = handle.into().update(cx, |_, w, _| w.refresh());
}
fn run() -> Result<(), String> {
    let args: Vec<_> = std::env::args().skip(1).collect();
    let mut config = None;
    let mut show = false;
    let mut show_settings = false;
    let mut autostart_launch = false;
    #[cfg(feature = "dev-demo")]
    let demo_mode = args.iter().any(|a| a == "--demo");
    #[cfg(not(feature = "dev-demo"))]
    let demo_mode = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            #[cfg(feature = "dev-demo")]
            "--demo" => {}
            "--show" => show = true,
            "--settings" => show_settings = true,
            "--autostart" => autostart_launch = true,
            "--config" => {
                i += 1;
                config = Some(PathBuf::from(args.get(i).ok_or("--config 缺少配置文件")?));
            }
            _ => {
                return Err(
                    "不支持的参数；支持 --show、--settings、--config <配置文件>，开发演示需专用构建".into(),
                );
            }
        }
        i += 1;
    }
    let Some(instance) = Instance::acquire(!autostart_launch)? else {
        eprintln!("AgentPulse 已运行，已通知现有实例显示列表");
        return Ok(());
    };
    #[cfg(feature = "dev-demo")]
    if demo_mode {
        eprintln!("开发演示前台运行；Ctrl+C 退出；不读取生产配置或连接服务");
        unsafe {
            let _ =
                windows::Win32::System::Console::SetConsoleCtrlHandler(Some(console_control), true);
        }
    }
    let settings = if demo_mode {
        Settings {
            away_after_minutes: 10,
            ..Settings::default()
        }
    } else {
        Settings::load(config.as_deref())?
    };
    let floating_path = if demo_mode {
        None
    } else {
        Some(settings.data_dir.join("desktop.json"))
    };
    let autostart = if demo_mode {
        None
    } else {
        Some(Registration::new(
            &std::env::current_exe().map_err(|_| "无法定位程序")?,
            &settings.config_path,
        )?)
    };
    let mut setup_settings = settings.clone();
    let startup_status = autostart
        .as_ref()
        .map(Registration::status)
        .unwrap_or_else(|| {
            #[cfg(feature = "dev-demo")]
            {
                StartupStatus::Demo
            }
            #[cfg(not(feature = "dev-demo"))]
            {
                StartupStatus::Off
            }
        });
    let away_minutes = settings.away_after_minutes;
    agent_pulse_tray::desktop::configure(if demo_mode { 0 } else { away_minutes });
    let (updates_tx, updates_rx) = async_channel::unbounded();
    let (view, input, worker) = if demo_mode {
        #[cfg(feature = "dev-demo")]
        {
            (demo::view(), None, None)
        }
        #[cfg(not(feature = "dev-demo"))]
        {
            unreachable!()
        }
    } else {
        let store = Store::open(&settings.state_file)?;
        let view = View {
            snapshot_confirmed_at: None,
            state: store.state.clone(),
            status: "正在连接".into(),
            alert: None,
            can_notify: false,
        };
        let worker = sync::start(settings.clone(), store, updates_tx);
        (view, Some(worker.input.clone()), Some(worker))
    };
    let mut bridge = if demo_mode {
        None
    } else {
        platform::Bridge::start(&settings)?
    };
    let bridge_status = bridge
        .as_mut()
        .map(|b| b.status())
        .unwrap_or_else(|| "本机接入未启用".into());
    let (reply_tx, reply_rx) = async_channel::unbounded();
    let host_preferences = settings.preferences.clone();
    let host_path = if demo_mode {
        None
    } else {
        Some(settings.data_dir.join("hosts.json"))
    };
    let requests = if demo_mode {
        None
    } else {
        Some(sessions::start(settings, reply_tx))
    };
    let shared = Rc::new(RefCell::new(Shared {
        view,
        card_alert: None,
        selected: None,
        history: vec![],
        expanded_events: Default::default(),
        visible_events: Default::default(),
        visible_elapsed: Default::default(),
        read_requested: Default::default(),
        history_more: false,
        loading: false,
        latest_loading: false,
        new_events: 0,
        generation: 0,
        message: String::new(),
        management_busy: false,
        filter: 0,
        floating: false,
        bridge: bridge_status,
        demo: demo_mode,
        startup_status,
        startup_message: String::new(),
        preferences: host_preferences.read().unwrap().clone(),
        power_status: "未请求保持唤醒".into(),
        emphasis: false,
        bridge_setup_running: false,
        away_minutes,
        #[cfg(feature = "dev-demo")]
        demo_dark: false,
    }));
    let stop = input.clone();
    Application::new()
        .with_assets(assets::Assets)
        .run(move |cx: &mut App| {
            gpui_component::init(cx);
            let (tx, rx) = async_channel::unbounded();
            cx.set_global(icon::Tray::create(tx.clone()));
            let list = window(Kind::List, shared.clone(), tx.clone(), cx);
            let detail = window(Kind::Detail, shared.clone(), tx.clone(), cx);
            let float = window(Kind::Float, shared.clone(), tx.clone(), cx);
            let card = window(Kind::Card, shared.clone(), tx.clone(), cx);
            let settings_window = settings_window(shared.clone(), tx.clone(), cx);
            let mut floating = float
                .update(cx, |_, w, _| {
                    floating::Floating::new(platform::handle(w), w.scale_factor(), floating_path)
                })
                .unwrap();
            let float_hwnd = float.update(cx, |_, w, _| platform::handle(w)).unwrap();
            let list_hwnd = list.update(cx, |_, w, _| platform::handle(w)).unwrap();
            shared.borrow_mut().floating = floating.prefs.visible;
            if !autostart_launch && (show || show_settings) {
                let _ = tx.try_send(if show_settings {
                    Command::Settings
                } else {
                    Command::Show(false)
                });
            }
            let _ = detail.update(cx, |_, w, _| platform::place_detail(w));
            let _ = settings_window.update(cx, |_, w, _| platform::place_main(w));
            cx.spawn(async move |cx| {
                #[cfg(feature = "dev-demo")]
                if demo_mode {
                    eprintln!("开发演示已就绪：默认驻留托盘，设置可通过托盘菜单打开");
                }
                let mut card_until = None;
                let mut awake = agent_pulse_tray::power::WakeGuard::default();
                let mut latest_retry = Instant::now();
                let mut emphasis_until: Option<Instant> = None;
                let mut bridge_check = Instant::now();
                let mut menu_dirty = true;
                loop {
                    let tick_ms = floating.interval_ms();
                    cx.background_executor()
                        .timer(Duration::from_millis(tick_ms))
                        .await;
                    #[cfg(feature = "dev-demo")]
                    if STOP.load(Ordering::Relaxed) {
                        let _ = tx.try_send(Command::Quit);
                    }
                    if instance.activation_requested() {
                        let _ = tx.try_send(Command::Show(false));
                    }
                    if floating.tick(list_hwnd) {
                        let _ = tx.try_send(Command::Show(true));
                    }
                    let detail_visible = detail
                        .update(cx, |_, w, _| platform::readable(platform::handle(w)))
                        .unwrap_or(false);
                    let visible_reads =
                        if detail_visible && agent_pulse_tray::desktop::status().0 == Some(false) {
                            shared
                                .borrow_mut()
                                .advance_read_visibility(Duration::from_millis(tick_ms))
                        } else {
                            shared.borrow_mut().visible_events.clear();
                            Vec::new()
                        };
                    if !visible_reads.is_empty() {
                        let _ = tx.try_send(Command::Read(visible_reads));
                    }
                    let needs_read_poll = {
                        let data = shared.borrow();
                        detail_visible
                            && data.history.iter().any(|event| {
                                data.expanded_events.contains(&event.event_id)
                                    && data
                                        .view
                                        .state
                                        .unread()
                                        .iter()
                                        .any(|e| e.event_id == event.event_id)
                            })
                    };
                    if needs_read_poll {
                        notify(detail, cx);
                    }
                    while let Ok(update) = updates_rx.try_recv() {
                        menu_dirty = true;
                        let alert = update.alert.clone();
                        let can_notify = update.can_notify;
                        {
                            let mut data = shared.borrow_mut();
                            if data.view.state.snapshot.run_id != update.state.snapshot.run_id {
                                data.generation += 1;
                                data.history.clear();
                                data.initialize_history_expansion();
                                data.latest_loading = false;
                                data.loading = false;
                                data.new_events = 0;
                            }
                            let current_unread: std::collections::BTreeSet<_> = update
                                .state
                                .unread()
                                .into_iter()
                                .map(|e| e.event_id)
                                .collect();
                            let newly_read: std::collections::BTreeSet<_> = data
                                .view
                                .state
                                .unread()
                                .into_iter()
                                .filter(|old| !current_unread.contains(&old.event_id))
                                .map(|e| e.event_id)
                                .collect();
                            // 已读只更新标记，不改变本次浏览的展开状态，避免正文跳动。
                            for event in &mut data.history {
                                if newly_read.contains(&event.event_id) {
                                    event.read = true;
                                }
                            }
                            data.view = update;
                            let dismiss = data.card_alert.as_ref().is_some_and(|alert| {
                                if alert.session_id.is_empty() {
                                    !data.view.state.unread().iter().any(|e| e.valid)
                                } else {
                                    !data
                                        .view
                                        .state
                                        .unread()
                                        .iter()
                                        .any(|e| e.event_id == alert.event_id && e.valid)
                                }
                            });
                            if dismiss {
                                let _ = card.update(cx, |_, w, _| platform::hide(w));
                                card_until = None;
                            }
                        }
                        if !can_notify {
                            let _ = card.update(cx, |_, w, _| platform::hide(w));
                            card_until = None;
                        }
                        if let Some(alert) = alert.filter(|_| can_notify) {
                            let muted = host_preferences.read().unwrap().muted_sources.clone();
                            let ids: Vec<_> = shared
                                .borrow()
                                .view
                                .state
                                .notifications()
                                .iter()
                                .filter(|e| !muted.contains(&e.source_id))
                                .filter(|e| {
                                    alert.event_id.is_empty() || alert.event_id == e.event_id
                                })
                                .map(|e| e.event_id.clone())
                                .collect();
                            if !ids.is_empty() {
                                let delivered = if floating.prefs.visible {
                                    floating.alert();
                                    emphasis_until = Some(Instant::now() + Duration::from_secs(7));
                                    shared.borrow_mut().emphasis = true;
                                    true
                                } else {
                                    cx.update(|cx| {
                                        cx.global::<icon::Tray>().notify(&alert.title, &alert.body)
                                    })
                                    .unwrap_or(false)
                                };
                                if delivered {
                                    if let Some(input) = &input {
                                        let _ = input.send(Input::Notified(ids));
                                    }
                                } else {
                                    shared.borrow_mut().card_alert = Some(alert);
                                    let shown = card
                                        .update(cx, |_, w, cx| {
                                            platform::show_card(w);
                                            cx.notify();
                                            platform::visible(platform::handle(w))
                                        })
                                        .unwrap_or(false);
                                    if shown {
                                        if let Some(input) = &input {
                                            let _ = input.send(Input::Notified(ids));
                                        }
                                        card_until = Some(Instant::now() + Duration::from_secs(7));
                                    }
                                }
                            }
                        }
                        for handle in [
                            list.into(),
                            detail.into(),
                            float.into(),
                            gpui::AnyWindowHandle::from(settings_window),
                        ] {
                            notify(handle, cx);
                        }
                    }
                    while let Ok(reply) = reply_rx.try_recv() {
                        match reply {
                            Reply::History {
                                key,
                                before,
                                generation,
                                result,
                            } => {
                                let mut data = shared.borrow_mut();
                                if data.selected.as_ref() != Some(&key)
                                    || data.generation != generation
                                {
                                    continue;
                                }
                                data.loading = false;
                                match result {
                                    Ok(events) => {
                                        data.history_more = events.len() == 30;
                                        if before == 0 {
                                            data.history = events;
                                            data.initialize_history_expansion();
                                        } else {
                                            data.merge_latest(events);
                                        }
                                        data.message.clear();
                                    }
                                    Err(e) => data.message = e,
                                }
                                drop(data);
                                notify(detail, cx);
                            }
                            Reply::Latest {
                                key,
                                generation,
                                run_id,
                                result,
                            } => {
                                let valid = {
                                    let data = shared.borrow();
                                    data.selected.as_ref() == Some(&key)
                                        && data.generation == generation
                                        && data.view.state.snapshot.run_id == run_id
                                };
                                if !valid {
                                    continue;
                                }
                                let follows =
                                    if result.as_ref().is_ok_and(|events| !events.is_empty()) {
                                        detail
                                            .update(cx, |s, _, _| s.prepare_latest_insertion())
                                            .unwrap_or(false)
                                    } else {
                                        false
                                    };
                                let mut data = shared.borrow_mut();
                                data.latest_loading = false;
                                match result {
                                    Ok(events) if !events.is_empty() => {
                                        let count = data.merge_latest(events);
                                        if follows {
                                            data.new_events = 0;
                                        } else {
                                            data.new_events += count;
                                        }
                                        data.message.clear();
                                        latest_retry = Instant::now();
                                    }
                                    Ok(_) => {
                                        latest_retry = Instant::now() + Duration::from_secs(2);
                                    }
                                    Err(error) => {
                                        data.message = error;
                                        latest_retry = Instant::now() + Duration::from_secs(2);
                                    }
                                }
                                drop(data);
                                notify(detail, cx);
                            }
                            Reply::Managed { key, result } => {
                                let mut data = shared.borrow_mut();
                                data.management_busy = false;
                                if data.selected.as_ref() == Some(&key) {
                                    data.message = match result {
                                        Ok(()) => "管理操作已保存，正在同步最新状态".into(),
                                        Err(e) => e,
                                    };
                                }
                                drop(data);
                                if let Some(input) = &input {
                                    let _ = input.send(Input::Refresh);
                                }
                                notify(detail, cx);
                            }
                        }
                    }
                    if !demo_mode && Instant::now() >= latest_retry {
                        let mut data = shared.borrow_mut();
                        if !data.loading
                            && !data.latest_loading
                            && data.view.status.starts_with("已")
                            && let (Some(session), Some(requests)) = (data.selected(), &requests)
                            && data
                                .history
                                .first()
                                .is_none_or(|e| e.event_id != session.last_event_id)
                        {
                            if data.history.is_empty() {
                                data.loading = true;
                                let _ = requests.send(Request::History {
                                    key: session.key(),
                                    before: 0,
                                    generation: data.generation,
                                });
                            } else {
                                let after = data.history.first().map_or(0, |e| e.position);
                                data.latest_loading = true;
                                let _ = requests.send(Request::Latest {
                                    key: session.key(),
                                    after,
                                    generation: data.generation,
                                    run_id: data.view.state.snapshot.run_id.clone(),
                                });
                            }
                        }
                    }
                    while let Ok(command) = rx.try_recv() {
                        menu_dirty = true;
                        #[cfg(feature = "dev-demo")]
                        if demo_mode {
                            eprintln!("开发交互：{command:?}");
                        }
                        match command {
                            Command::Quit => {
                                let _ = cx.update(|cx| cx.quit());
                                return;
                            }
                            Command::MenuSession(id) => {
                                let key = cx
                                    .update(|cx| cx.global::<icon::Tray>().resolve(&id))
                                    .ok()
                                    .flatten();
                                if let Some(key) = key {
                                    let _ = tx.try_send(Command::Detail(key));
                                }
                            }
                            Command::Attention => {
                                shared.borrow_mut().filter = 1;
                                let _ = tx.try_send(Command::Show(false));
                            }
                            Command::Active => {
                                shared.borrow_mut().filter = 0;
                                let _ = tx.try_send(Command::Show(false));
                            }
                            Command::Show(from_float) => {
                                let rows = shared.borrow().sessions().len();
                                let height = (rows.min(6) as f32 * 64. + 136.).clamp(180., 480.);
                                let _ = list.update(cx, |s, w, cx| {
                                    platform::place_list(
                                        w,
                                        if from_float { Some(float_hwnd) } else { None },
                                        height,
                                    );
                                    w.focus(&s.focus);
                                    cx.notify();
                                });
                            }
                            Command::HostPolicy(source, wake) => {
                                let mut next = host_preferences.read().unwrap().clone();
                                let selected = if wake {
                                    &mut next.awake_sources
                                } else {
                                    &mut next.muted_sources
                                };
                                if !selected.remove(&source) {
                                    selected.insert(source);
                                }
                                let result = host_path
                                    .as_ref()
                                    .map(|path| next.save(path))
                                    .unwrap_or(Ok(()));
                                let mut data = shared.borrow_mut();
                                match result {
                                    Ok(()) => {
                                        *host_preferences.write().unwrap() = next.clone();
                                        data.preferences = next;
                                        data.startup_message.clear();
                                    }
                                    Err(e) => data.startup_message = e,
                                }
                                drop(data);
                                // 快捷偏好同时显示在详情和设置，不能等待下一次心跳才重绘。
                                notify(detail, cx);
                                notify(settings_window, cx);
                            }
                            Command::ToggleFloat => {
                                floating.toggle();
                                shared.borrow_mut().floating = floating.prefs.visible;
                                notify(settings_window, cx);
                            }
                            Command::ResetFloat => {
                                floating.reset();
                                if !floating.prefs.visible {
                                    floating.toggle();
                                }
                                shared.borrow_mut().floating = true;
                                notify(settings_window, cx);
                            }
                            Command::Drag(x, y) => {
                                floating.begin_drag(windows::Win32::Foundation::POINT { x, y })
                            }
                            Command::AwayMinutes(minutes) => {
                                let result = if demo_mode {
                                    Ok(())
                                } else {
                                    Settings::set_away_minutes(&setup_settings.config_path, minutes)
                                };
                                let mut data = shared.borrow_mut();
                                match result {
                                    Ok(()) => {
                                        setup_settings.away_after_minutes = minutes;
                                        if !demo_mode {
                                            agent_pulse_tray::desktop::configure(minutes);
                                        }
                                        data.away_minutes = minutes;
                                        data.startup_message = "离开检测设置已保存并生效。".into();
                                    }
                                    Err(error) => data.startup_message = error,
                                }
                                drop(data);
                                notify(settings_window, cx);
                            }
                            Command::Settings => {
                                if let Some(registration) = &autostart {
                                    shared.borrow_mut().startup_status = registration.status();
                                }
                                let _ = settings_window.update(cx, |_, w, cx| {
                                    platform::show(w, true);
                                    cx.notify();
                                });
                            }
                            Command::Autostart(enabled) => {
                                if let Some(registration) = &autostart {
                                    let result = registration.set_enabled(enabled);
                                    let mut data = shared.borrow_mut();
                                    data.startup_status = registration.status();
                                    data.startup_message = match result {
                                        Ok(()) => {
                                            "已更新启动快捷方式，当前状态已从系统重新读取".into()
                                        }
                                        Err(e) => e,
                                    };
                                    drop(data);
                                    notify(settings_window, cx);
                                }
                            }
                            Command::BridgeSetup => {
                                if demo_mode || shared.borrow().bridge_setup_running {
                                    continue;
                                }
                                shared.borrow_mut().bridge_setup_running = true;
                                shared.borrow_mut().startup_message =
                                    "正在打开 Agent 配置向导…".into();
                                notify(settings_window, cx);
                                let setup_settings = setup_settings.clone();
                                let setup_tx = tx.clone();
                                if std::thread::Builder::new()
                                    .name("agent-pulse-bridge-setup".into())
                                    .spawn(move || {
                                        let result = platform::Bridge::run_setup(&setup_settings);
                                        let _ =
                                            setup_tx.try_send(Command::BridgeSetupFinished(result));
                                    })
                                    .is_err()
                                {
                                    let mut data = shared.borrow_mut();
                                    data.bridge_setup_running = false;
                                    data.startup_message = "无法启动本机 Agent 配置线程".into();
                                    drop(data);
                                    notify(settings_window, cx);
                                }
                            }
                            Command::BridgeSetupFinished(result) => {
                                // 只刷新本机接入，不重建网络工作线程、托盘或窗口，也不清理离线已读。
                                let partial = matches!(result, Ok(true));
                                let result = result.and_then(|_| {
                                    platform::Bridge::apply_config(&mut bridge, &mut setup_settings)
                                });
                                let mut data = shared.borrow_mut();
                                data.bridge_setup_running = false;
                                data.bridge = bridge
                                    .as_mut()
                                    .map(|b| b.status())
                                    .unwrap_or_else(|| "本机接入未启用".into());
                                data.startup_message = match result {
                                    Ok(()) if partial => {
                                        "Bridge已加载，但部分Agent接入未完成，请查看向导报告。"
                                            .into()
                                    }
                                    Ok(()) => {
                                        "Agent 配置已生效，本机接入已重新加载，无需重启 Tray。"
                                            .into()
                                    }
                                    Err(error) => {
                                        format!("本机 Agent 配置未完成或接入未生效：{error}")
                                    }
                                };
                                drop(data);
                                notify(settings_window, cx);
                            }
                            Command::StartupSettings => {
                                if let Err(e) = agent_pulse_tray::autostart::open_system_settings()
                                {
                                    shared.borrow_mut().startup_message = e;
                                    notify(settings_window, cx);
                                }
                            }
                            Command::Detail(key) => {
                                let _ = list.update(cx, |_, w, _| platform::hide(w));
                                let mut data = shared.borrow_mut();
                                data.selected = Some(key.clone());
                                data.history.clear();
                                data.expanded_events.clear();
                                data.visible_events.clear();
                                data.visible_elapsed.clear();
                                data.read_requested.clear();
                                data.message.clear();
                                data.generation += 1;
                                data.loading = !demo_mode;
                                data.history_more = false;
                                data.latest_loading = false;
                                data.new_events = 0;
                                if demo_mode {
                                    data.history = data
                                        .view
                                        .state
                                        .snapshot
                                        .unread
                                        .iter()
                                        .filter(|e| e.key() == key)
                                        .cloned()
                                        .collect();
                                    data.initialize_history_expansion();
                                } else if let Some(requests) = &requests {
                                    let _ = requests.send(Request::History {
                                        key,
                                        before: 0,
                                        generation: data.generation,
                                    });
                                }
                                drop(data);
                                let _ = detail.update(cx, |s, w, cx| {
                                    platform::show(w, true);
                                    w.focus(&s.focus);
                                    s.manage = false;
                                    s.reset_timeline_scroll();
                                    cx.notify();
                                });
                            }
                            Command::Read(ids) => {
                                {
                                    let mut data = shared.borrow_mut();
                                    for id in &ids {
                                        data.visible_elapsed.remove(id);
                                    }
                                }
                                if let Some(input) = &input {
                                    let _ = input.send(Input::Read(ids));
                                } else {
                                    shared.borrow_mut().view.state.pending_reads.extend(ids);
                                    notify(list, cx);
                                    notify(float, cx);
                                }
                                notify(detail, cx);
                            }
                            Command::ReadAll(key) => {
                                let ids = shared
                                    .borrow()
                                    .view
                                    .state
                                    .unread()
                                    .into_iter()
                                    .filter(|e| e.key() == key)
                                    .map(|e| e.event_id)
                                    .collect();
                                let _ = tx.try_send(Command::Read(ids));
                            }
                            Command::ToggleEvent(id) => {
                                let mut data = shared.borrow_mut();
                                if !data.expanded_events.remove(&id) {
                                    data.expanded_events.insert(id);
                                }
                                drop(data);
                                notify(detail, cx);
                            }
                            Command::ReportVisible(id) => {
                                shared.borrow_mut().visible_events.insert(id);
                            }
                            Command::Latest => {
                                let _ = detail.update(cx, |s, _, cx| {
                                    s.back_to_latest();
                                    cx.notify();
                                });
                            }
                            Command::Older => {
                                let mut data = shared.borrow_mut();
                                if !data.loading
                                    && let (Some(key), Some(event), Some(requests)) =
                                        (data.selected.clone(), data.history.last(), &requests)
                                {
                                    let _ = requests.send(Request::History {
                                        key,
                                        before: event.position,
                                        generation: data.generation,
                                    });
                                    data.loading = true;
                                }
                                drop(data);
                                notify(detail, cx);
                            }
                            Command::Manage(session, field, value) => {
                                let mut data = shared.borrow_mut();
                                if data.management_busy {
                                    continue;
                                }
                                if demo_mode {
                                    #[cfg(feature = "dev-demo")]
                                    {
                                        if let Some(s) = data
                                            .view
                                            .state
                                            .snapshot
                                            .sessions
                                            .iter_mut()
                                            .find(|s| s.key() == session.key())
                                        {
                                            match field {
                                                "archived" => {
                                                    s.archived = value.as_bool().unwrap_or(false)
                                                }
                                                "override_state" => {
                                                    s.override_state =
                                                        value.as_str().unwrap_or_default().into();
                                                    s.override_at = if s.override_state.is_empty() {
                                                        String::new()
                                                    } else {
                                                        "开发演示时间".into()
                                                    };
                                                }
                                                _ => {}
                                            }
                                            s.management_version += 1;
                                        }
                                        data.message = "演示：管理操作已应用".into();
                                    }
                                } else if let Some(requests) = &requests {
                                    data.management_busy = true;
                                    data.message = "正在提交管理操作…".into();
                                    let _ = requests.send(Request::Manage {
                                        session,
                                        field,
                                        value,
                                    });
                                }
                                drop(data);
                                notify(detail, cx);
                                notify(list, cx);
                            }
                            #[cfg(feature = "dev-demo")]
                            Command::DemoLong => {
                                let event = demo::long_event();
                                let key = event.key();
                                let mut data = shared.borrow_mut();
                                data.view
                                    .state
                                    .snapshot
                                    .unread
                                    .retain(|e| e.event_id != event.event_id);
                                data.view.state.pending_reads.remove(&event.event_id);
                                data.expanded_events.insert(event.event_id.clone());
                                data.visible_elapsed.remove(&event.event_id);
                                data.read_requested.remove(&event.event_id);
                                data.view.state.snapshot.unread.insert(0, event);
                                drop(data);
                                let _ = tx.try_send(Command::Detail(key));
                            }
                            #[cfg(feature = "dev-demo")]
                            Command::DemoTheme => {
                                let dark = shared.borrow().demo_dark;
                                shared.borrow_mut().demo_dark = !dark;
                                for handle in [
                                    list.into(),
                                    detail.into(),
                                    float.into(),
                                    card.into(),
                                    gpui::AnyWindowHandle::from(settings_window),
                                ] {
                                    notify(handle, cx);
                                }
                            }
                            #[cfg(feature = "dev-demo")]
                            Command::DemoMenu => {
                                let menu = cx
                                    .update(|cx| cx.global::<icon::Tray>().menu_for_demo())
                                    .unwrap();
                                let hwnd = settings_window
                                    .update(cx, |_, w, _| platform::handle(w))
                                    .unwrap();
                                use tray_icon::menu::ContextMenu;
                                // 复用真正的托盘菜单，仅增加开发入口，便于检查外观与键盘交互。
                                unsafe {
                                    menu.show_context_menu_for_hwnd(hwnd.0 as isize, None);
                                }
                            }
                            #[cfg(feature = "dev-demo")]
                            Command::DemoAlert => {
                                let alert =
                                    shared.borrow().view.state.snapshot.unread.first().cloned();
                                if let Some(alert) = alert {
                                    if floating.prefs.visible {
                                        floating.alert();
                                        emphasis_until =
                                            Some(Instant::now() + Duration::from_secs(7));
                                        shared.borrow_mut().emphasis = true;
                                        notify(float, cx);
                                    } else if !cx
                                        .update(|cx| {
                                            cx.global::<icon::Tray>()
                                                .notify(&alert.title, &alert.body)
                                        })
                                        .unwrap_or(false)
                                    {
                                        shared.borrow_mut().card_alert = Some(alert);
                                        let _ = card.update(cx, |_, w, cx| {
                                            platform::show_card(w);
                                            cx.notify();
                                        });
                                        card_until = Some(Instant::now() + Duration::from_secs(7));
                                    }
                                }
                            }
                        }
                    }
                    if emphasis_until.is_some_and(|until| Instant::now() >= until) {
                        emphasis_until = None;
                        shared.borrow_mut().emphasis = false;
                        notify(float, cx);
                    }
                    let running = {
                        let data = shared.borrow();
                        data.preferences.running(
                            &data.view.state.snapshot,
                            !demo_mode && data.view.status.starts_with("已") && data.view.current(),
                        )
                    };
                    let power_status = match awake.update(running > 0) {
                        Ok(()) if running > 0 => {
                            format!("保持唤醒：所选主机有 {running} 个任务运行中")
                        }
                        Ok(()) => "未请求保持唤醒：没有所选主机的在线运行任务".into(),
                        Err(e) => e,
                    };
                    if shared.borrow().power_status != power_status {
                        shared.borrow_mut().power_status = power_status;
                        notify(settings_window, cx);
                    }
                    if card_until.is_some_and(|at| Instant::now() >= at) {
                        let _ = card.update(cx, |_, w, _| platform::hide(w));
                        card_until = None;
                    }
                    if menu_dirty && !platform::menu_open() {
                        let data = shared.borrow();
                        let _ = cx.update(|cx| {
                            cx.global_mut::<icon::Tray>().update(
                                &data.sessions(),
                                data.view.state.unread().len(),
                                floating.prefs.visible,
                                data.demo || data.view.status.starts_with("已"),
                            );
                        });
                        drop(data);
                        menu_dirty = false;
                    }
                    if bridge_check.elapsed() >= Duration::from_secs(3) {
                        bridge_check = Instant::now();
                        if let Some(registration) = &autostart {
                            let visible = settings_window
                                .update(cx, |_, w, _| platform::visible(platform::handle(w)))
                                .unwrap_or(false);
                            if visible {
                                let status = registration.status();
                                if shared.borrow().startup_status != status {
                                    shared.borrow_mut().startup_status = status;
                                    notify(settings_window, cx);
                                }
                            }
                        }
                        if let Some(b) = bridge.as_mut() {
                            let status = b.status();
                            if shared.borrow().bridge != status {
                                shared.borrow_mut().bridge = status;
                                notify(settings_window, cx);
                            }
                        }
                    }
                }
            })
            .detach();
        });
    if let Some(stop) = stop {
        let _ = stop.send(Input::Stop);
    }
    drop(worker);
    Ok(())
}
