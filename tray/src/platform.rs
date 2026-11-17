// Windows 窗口差异集中在这里，业务状态不依赖窗口句柄。
use gpui::Window;
use raw_window_handle::{HasWindowHandle, RawWindowHandle};
use windows::Win32::{
    Foundation::{HWND, POINT},
    Graphics::Gdi::{GetMonitorInfoW, MONITOR_DEFAULTTONEAREST, MONITORINFO, MonitorFromPoint},
    UI::WindowsAndMessaging::*,
};

pub fn handle(window: &Window) -> HWND {
    match HasWindowHandle::window_handle(window)
        .expect("窗口句柄不可用")
        .as_raw()
    {
        RawWindowHandle::Win32(h) => HWND(h.hwnd.get() as *mut _),
        _ => unreachable!("首版仅支持 Windows"),
    }
}

// MAKEINTRESOURCEW(1) 是资源编号，不是供 Rust 解引用的内存指针。
#[allow(clippy::manual_dangling_ptr)]
pub fn set_window_icon(window: &Window) {
    use windows::{
        Win32::{
            Foundation::{LPARAM, WPARAM},
            System::LibraryLoader::GetModuleHandleW,
        },
        core::PCWSTR,
    };
    unsafe {
        if let Ok(module) = GetModuleHandleW(None) {
            for (kind, size) in [(ICON_SMALL, 16), (ICON_BIG, 32)] {
                if let Ok(icon) = LoadImageW(
                    Some(module.into()),
                    PCWSTR(1usize as *const u16),
                    IMAGE_ICON,
                    size,
                    size,
                    LR_SHARED,
                ) {
                    SendMessageW(
                        handle(window),
                        WM_SETICON,
                        Some(WPARAM(kind as usize)),
                        Some(LPARAM(icon.0 as isize)),
                    );
                }
            }
        }
    }
}

// 原生尺寸消息会同步回调 GPUI，更新窗口期间回调无法再次借用窗口。
// 显示后排队重发实际客户区尺寸，使首帧无需获得焦点也能完成布局。
pub fn queue_layout(hwnd: HWND) {
    unsafe {
        let mut rect = windows::Win32::Foundation::RECT::default();
        if GetClientRect(hwnd, &mut rect).is_ok() {
            let packed = ((rect.right - rect.left) as u32 & 0xffff)
                | (((rect.bottom - rect.top) as u32 & 0xffff) << 16);
            let _ = PostMessageW(
                Some(hwnd),
                WM_SIZE,
                windows::Win32::Foundation::WPARAM(SIZE_RESTORED as usize),
                windows::Win32::Foundation::LPARAM(packed as isize),
            );
            let _ = PostMessageW(
                Some(hwnd),
                WM_PAINT,
                windows::Win32::Foundation::WPARAM(0),
                windows::Win32::Foundation::LPARAM(0),
            );
        }
    }
}

pub fn hide(window: &Window) {
    unsafe {
        let _ = ShowWindow(handle(window), SW_HIDE);
    }
}

pub fn show(window: &Window, activate: bool) {
    unsafe {
        let hwnd = handle(window);
        let _ = ShowWindow(
            hwnd,
            if activate {
                if IsIconic(hwnd).as_bool() {
                    SW_RESTORE
                } else {
                    SW_SHOW
                }
            } else {
                SW_SHOWNOACTIVATE
            },
        );
        if activate {
            let _ = SetForegroundWindow(hwnd);
        }
        queue_layout(hwnd);
    }
}

pub fn disable_maximize(window: &Window) {
    unsafe {
        let hwnd = handle(window);
        SetWindowLongPtrW(
            hwnd,
            GWL_STYLE,
            GetWindowLongPtrW(hwnd, GWL_STYLE) & !(WS_MAXIMIZEBOX.0 as isize),
        );
        let _ = SetWindowPos(
            hwnd,
            None,
            0,
            0,
            0,
            0,
            SWP_FRAMECHANGED | SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE,
        );
    }
}

// GPUI 的隐藏窗口尚未应用初始 placement；原生显示前显式设置尺寸。
pub fn place_main(window: &Window) {
    unsafe {
        let scale = window.scale_factor();
        let mut point = POINT::default();
        let _ = GetCursorPos(&mut point);
        let mut info = MONITORINFO {
            cbSize: std::mem::size_of::<MONITORINFO>() as u32,
            ..Default::default()
        };
        let _ = GetMonitorInfoW(MonitorFromPoint(point, MONITOR_DEFAULTTONEAREST), &mut info);
        let width = (460.0 * scale) as i32;
        let height = (420.0 * scale) as i32;
        let _ = SetWindowPos(
            handle(window),
            None,
            info.rcWork.left + (info.rcWork.right - info.rcWork.left - width) / 2,
            info.rcWork.top + (info.rcWork.bottom - info.rcWork.top - height) / 2,
            width,
            height,
            SWP_NOACTIVATE | SWP_NOZORDER,
        );
    }
}

pub fn error(message: &str) {
    unsafe {
        let _ = MessageBoxW(
            None,
            &windows::core::HSTRING::from(message),
            windows::core::w!("AgentPulse"),
            MB_OK | MB_ICONERROR,
        );
    }
}

pub struct Bridge {
    child: std::process::Child,
    job: windows::Win32::Foundation::HANDLE,
    address: String,
    executable: std::path::PathBuf,
}
impl Bridge {
    pub fn start(settings: &agent_pulse_tray::settings::Settings) -> Result<Option<Self>, String> {
        use std::os::windows::process::CommandExt;
        if !settings.bridge_enabled {
            return Ok(None);
        }
        let executable = extract_bridge(&settings.data_dir)?;
        let address = String::from_utf8(bridge_query(
            &executable,
            "check-config",
            "--config",
            &settings.bridge_config_path().to_string_lossy(),
        )?)
        .map_err(|_| "本机IPC地址编码无效")?
        .trim()
        .to_string();
        let config =
            std::fs::canonicalize(&settings.bridge_config).map_err(|_| "本地接入配置不存在")?;
        let mut child = std::process::Command::new(&executable)
            .arg("serve")
            .arg("--config")
            .arg(config)
            .creation_flags(0x08000000)
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn()
            .map_err(|_| "本地接入进程启动失败")?;
        use std::os::windows::io::AsRawHandle;
        use windows::Win32::Foundation::{CloseHandle, HANDLE};
        use windows::Win32::System::JobObjects::*;
        let job =
            unsafe { CreateJobObjectW(None, windows::core::PCWSTR::null()) }.map_err(|_| {
                let _ = child.kill();
                "无法创建本地接入进程管理组"
            })?;
        let mut info = JOBOBJECT_EXTENDED_LIMIT_INFORMATION::default();
        info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
        let result = unsafe {
            SetInformationJobObject(
                job,
                JobObjectExtendedLimitInformation,
                (&info as *const JOBOBJECT_EXTENDED_LIMIT_INFORMATION).cast(),
                std::mem::size_of_val(&info) as u32,
            )
            .and_then(|_| AssignProcessToJobObject(job, HANDLE(child.as_raw_handle())))
        };
        if result.is_err() {
            let _ = child.kill();
            let _ = child.wait();
            unsafe {
                let _ = CloseHandle(job);
            }
            return Err("无法绑定本地接入进程生命周期".into());
        }
        let mut bridge = Self {
            child,
            job,
            address,
            executable,
        };
        // 只有自有子进程存活且本机接收端可用，才报告接入启动成功。
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(3);
        while std::time::Instant::now() < deadline {
            std::thread::sleep(std::time::Duration::from_millis(50));
            if bridge
                .child
                .try_wait()
                .map_err(|_| "无法读取接入进程状态")?
                .is_some()
            {
                return Err("本机接入启动失败，请检查配置与本机IPC端点".into());
            }
            if bridge.pending_events().is_ok() {
                return Ok(Some(bridge));
            }
        }
        Err("本机接入尚未就绪，请检查配置与本机IPC端点".into())
    }
    /// 配置向导返回后重新读取同一便携配置；该路径也由原生回归直接验证。
    pub fn apply_config(
        current: &mut Option<Self>,
        active: &mut agent_pulse_tray::settings::Settings,
    ) -> Result<(), String> {
        let refreshed = agent_pulse_tray::settings::Settings::load(Some(&active.config_path))?;
        Self::reload(current, &refreshed)?;
        *active = refreshed;
        Ok(())
    }

    /// 向导成功后仅重建自有接入进程；解析配置失败时保留原进程。
    pub fn reload(
        current: &mut Option<Self>,
        settings: &agent_pulse_tray::settings::Settings,
    ) -> Result<(), String> {
        // 先核对文件存在和内置产物可释放，再停旧进程，避免无效路径破坏已有接入。
        if settings.bridge_enabled {
            let _ = std::fs::canonicalize(settings.bridge_config_path())
                .map_err(|_| "本地接入配置不存在")?;
            let executable = extract_bridge(&settings.data_dir)?;
            bridge_query(
                &executable,
                "check-config",
                "--config",
                &settings.bridge_config_path().to_string_lossy(),
            )?;
        }
        if let Some(active) = current.as_mut()
            && active
                .child
                .try_wait()
                .map_err(|_| "无法读取接入进程状态")?
                .is_none()
            && active.pending_events()? > 0
        {
            return Err(
                "原接入还有事件等待交付，已保留原进程；连接恢复后可重新应用 Agent 配置".into(),
            );
        }
        // Drop 会等待自建进程退出并关闭 Job Object，旧监听释放后再启动。
        drop(current.take());
        *current = Self::start(settings)?;
        Ok(())
    }
    fn pending_events(&self) -> Result<u64, String> {
        // 查询启动时的固定端点，不能因用户已改配置而误查新进程或丢弃旧队列。
        let bytes = bridge_query(&self.executable, "health", "--address", &self.address)
            .map_err(|_| "无法核对原接入的待发队列，已保留原进程")?;
        let health: serde_json::Value =
            serde_json::from_slice(&bytes).map_err(|_| "本机接入健康响应无效")?;
        health["pending"]
            .as_u64()
            .ok_or_else(|| "本机接入健康响应缺少待发数量".into())
    }
    pub fn run_setup(settings: &agent_pulse_tray::settings::Settings) -> Result<bool, String> {
        use std::os::windows::process::CommandExt;
        let executable = std::env::current_exe().map_err(|_| "无法定位客户端程序")?;
        let status = std::process::Command::new(executable)
            .arg("--bridge-setup-console")
            .arg(&settings.config_path)
            .creation_flags(0x00000010) // CREATE_NEW_CONSOLE：独立窗口保留最终报告，等待用户确认。
            .status()
            .map_err(|_| "无法打开本机 Agent 配置向导")?;
        if !status.success() && status.code() != Some(3) {
            return Err(format!("配置向导退出，状态码 {:?}", status.code()));
        }
        if !settings.bridge_enabled {
            agent_pulse_tray::settings::Settings::set_bridge_enabled(&settings.config_path, true)?;
        }
        Ok(status.code() == Some(3))
    }

    /// 独立控制台宿主；不取得单实例锁，也不启动桌面窗口和后台接入服务。
    pub fn setup_console(config_path: &std::path::Path) -> i32 {
        unsafe {
            // 发布版是 GUI 子系统，需要主动创建控制台；调试版已由父进程创建。
            let _ = windows::Win32::System::Console::AllocConsole();
        }
        let result = (|| -> Result<i32, String> {
            let settings = agent_pulse_tray::settings::Settings::load(Some(config_path))?;
            let executable = extract_bridge(&settings.data_dir)?;
            let config = settings.bridge_config_path();
            if settings.bridge_config.as_os_str().is_empty() {
                agent_pulse_tray::settings::Settings::ensure_bridge_config(
                    &config,
                    &settings.hub_url,
                    &settings.token,
                )?;
            } else if !config.is_file() {
                return Err("指定的本地接入配置不存在".into());
            }
            let status = std::process::Command::new(executable)
                .arg("setup")
                .arg("--config")
                .arg(config)
                .status()
                .map_err(|_| "无法运行本机 Agent 配置向导")?;
            if !status.success() && status.code() != Some(3) {
                return Err(format!("配置向导退出，状态码 {:?}", status.code()));
            }
            println!("\n配置已保存，关闭此向导后 Tray 会自动应用本机接入配置。");
            if status.code() == Some(3) {
                println!("部分Agent接入未完成；Bridge仍将应用有效配置，请按上方报告排查。");
            }
            Ok(status.code().unwrap_or(1))
        })();
        if let Err(error) = &result {
            eprintln!("\n本机 Agent 配置未完成：{error}");
        }
        println!("\n请查看上方配置报告，按 Enter 关闭此窗口并返回 Tray。");
        let mut confirmation = String::new();
        let _ = std::io::stdin().read_line(&mut confirmation);
        result.unwrap_or(1)
    }

    pub fn stop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
    pub fn status(&mut self) -> String {
        match self.child.try_wait() {
            Ok(None) => "本地接入运行中".into(),
            Ok(Some(_)) => "本地接入已停止，请检查配置后重启客户端".into(),
            Err(_) => "无法读取本地接入状态".into(),
        }
    }
}
// 本机查询复用内置Go程序的配置解析和IPC客户端，避免在Rust中另建一套平台传输。
fn bridge_query(
    executable: &std::path::Path,
    operation: &str,
    flag: &str,
    value: &str,
) -> Result<Vec<u8>, String> {
    use std::{
        io::Read,
        os::windows::process::CommandExt,
        process::{Command, Stdio},
        time::{Duration, Instant},
    };
    let mut child = Command::new(executable)
        .args([operation, flag, value])
        .creation_flags(0x08000000)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|_| "无法运行本机接入查询")?;
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                if !status.success() {
                    let mut diagnostic = Vec::new();
                    if let Some(stderr) = child.stderr.take() { let _ = stderr.take(4096).read_to_end(&mut diagnostic); }
                    let detail = String::from_utf8_lossy(&diagnostic);
                    return Err(format!("本机接入查询失败：{}", detail.trim()));
                }
                let mut bytes = Vec::new();
                child
                    .stdout
                    .take()
                    .ok_or("本机查询没有输出")?
                    .take(65536)
                    .read_to_end(&mut bytes)
                    .map_err(|_| "本机查询响应读取失败")?;
                if bytes.len() >= 65536 {
                    return Err("本机查询响应超过上限".into());
                }
                return Ok(bytes);
            }
            Ok(None) if Instant::now() < deadline => std::thread::sleep(Duration::from_millis(20)),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                return Err("本机接入查询超时或进程状态不可读".into());
            }
        }
    }
}

impl Drop for Bridge {
    fn drop(&mut self) {
        self.stop();
        unsafe {
            let _ = windows::Win32::Foundation::CloseHandle(self.job);
        }
    }
}

// 卡片使用工具窗口和禁止激活样式，避开任务栏并保留当前键盘焦点。
pub fn show_card(window: &Window) {
    unsafe {
        let before = GetForegroundWindow();
        let hwnd = handle(window);
        let style = GetWindowLongPtrW(hwnd, GWL_EXSTYLE);
        SetWindowLongPtrW(
            hwnd,
            GWL_EXSTYLE,
            (style | WS_EX_NOACTIVATE.0 as isize | WS_EX_TOOLWINDOW.0 as isize)
                & !(WS_EX_APPWINDOW.0 as isize),
        );
        let mut point = POINT::default();
        let _ = GetCursorPos(&mut point);
        let monitor = MonitorFromPoint(point, MONITOR_DEFAULTTONEAREST);
        let mut info = MONITORINFO {
            cbSize: std::mem::size_of::<MONITORINFO>() as u32,
            ..Default::default()
        };
        if GetMonitorInfoW(monitor, &mut info).as_bool() {
            let scale = window.scale_factor();
            let width = (320.0 * scale) as i32;
            let height = (180.0 * scale) as i32;
            let margin = (20.0 * scale) as i32;
            let _ = SetWindowPos(
                hwnd,
                Some(HWND_TOPMOST),
                info.rcWork.right - width - margin,
                info.rcWork.bottom - height - margin,
                width,
                height,
                SWP_NOACTIVATE,
            );
        }
        let _ = ShowWindow(hwnd, SW_SHOWNOACTIVATE);
        queue_layout(hwnd);
        let mut rect = windows::Win32::Foundation::RECT::default();
        let _ = GetWindowRect(hwnd, &mut rect);
        eprintln!(
            "提醒窗口验证：visible={} width={} height={} focus_preserved={}",
            IsWindowVisible(hwnd).as_bool(),
            rect.right - rect.left,
            rect.bottom - rect.top,
            before == GetForegroundWindow()
        );
    }
}

// 仅在启用本机接入时释放资源；比较内置字节，不运行旧版本或被替换的文件。
fn extract_bridge(data_dir: &std::path::Path) -> Result<std::path::PathBuf, String> {
    const BINARY: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/bridge.bin"));
    if BINARY.is_empty() {
        return Err("当前开发构建未内置接入程序，请使用 Windows 构建脚本".into());
    }
    let root = data_dir.join("internal").join(env!("CARGO_PKG_VERSION"));
    std::fs::create_dir_all(&root).map_err(|_| "无法创建内部程序目录")?;
    let executable = root.join("bridge.exe");
    if std::fs::read(&executable).ok().as_deref() != Some(BINARY) {
        use std::io::Write;
        let temp = root.join("bridge.tmp");
        let mut file = std::fs::File::create(&temp).map_err(|_| "无法释放本地接入程序")?;
        file.write_all(BINARY)
            .and_then(|_| file.sync_all())
            .map_err(|_| "本地接入程序写入失败")?;
        drop(file);
        std::fs::rename(temp, &executable).map_err(|_| "本地接入程序更新失败，请关闭旧版本")?;
    }
    Ok(executable)
}

pub fn visible(hwnd: HWND) -> bool {
    unsafe { IsWindowVisible(hwnd).as_bool() }
}
/// 只在可供用户阅读的前台详情窗口中累计时间，最小化和后台窗口均不计时。
pub fn readable(hwnd: HWND) -> bool {
    unsafe {
        IsWindowVisible(hwnd).as_bool()
            && !IsIconic(hwnd).as_bool()
            && GetForegroundWindow() == hwnd
    }
}

pub fn menu_open() -> bool {
    unsafe {
        let mut info = GUITHREADINFO {
            cbSize: std::mem::size_of::<GUITHREADINFO>() as u32,
            ..Default::default()
        };
        GetGUIThreadInfo(0, &mut info).is_ok() && info.flags.0 & 0x1c != 0
    }
}
pub fn rect(hwnd: HWND) -> windows::Win32::Foundation::RECT {
    let mut r = windows::Win32::Foundation::RECT::default();
    unsafe {
        let _ = GetWindowRect(hwnd, &mut r);
    }
    r
}
pub fn cursor() -> POINT {
    let mut p = POINT::default();
    unsafe {
        let _ = GetCursorPos(&mut p);
    }
    p
}
pub fn capture_pointer(w: &Window, position: gpui::Point<gpui::Pixels>) -> POINT {
    // 使用鼠标按下事件自己的坐标，不能读取已被后续移动事件更新的全局位置。
    let bounds = rect(handle(w));
    let point = POINT {
        x: bounds.left + (f32::from(position.x) * w.scale_factor()) as i32,
        y: bounds.top + (f32::from(position.y) * w.scale_factor()) as i32,
    };
    unsafe {
        windows::Win32::UI::Input::KeyboardAndMouse::SetCapture(handle(w));
    }
    point
}
pub fn work_area(p: POINT) -> windows::Win32::Foundation::RECT {
    unsafe {
        let mut info = MONITORINFO {
            cbSize: std::mem::size_of::<MONITORINFO>() as u32,
            ..Default::default()
        };
        let _ = GetMonitorInfoW(MonitorFromPoint(p, MONITOR_DEFAULTTONEAREST), &mut info);
        info.rcWork
    }
}
pub fn borderless(w: &Window, no_activate: bool) {
    unsafe {
        let h = handle(w);
        SetWindowLongPtrW(h, GWL_STYLE, WS_POPUP.0 as isize);
        SetWindowLongPtrW(
            h,
            GWL_EXSTYLE,
            (WS_EX_TOOLWINDOW.0 | if no_activate { WS_EX_NOACTIVATE.0 } else { 0 }) as isize,
        );
        let _ = SetWindowPos(
            h,
            None,
            0,
            0,
            0,
            0,
            SWP_FRAMECHANGED | SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE,
        );
    }
}
pub fn place_list(w: &Window, anchor: Option<HWND>, height: f32) {
    let scale = w.scale_factor();
    let p = cursor();
    let work = work_area(p);
    let width = (360. * scale) as i32;
    let height = (height * scale) as i32;
    let margin = (10. * scale) as i32;
    let (x, y) = if let Some(a) = anchor {
        let r = rect(a);
        let x = if r.left - width - margin >= work.left {
            r.left - width - margin
        } else {
            r.right + margin
        };
        (x, r.bottom - height)
    } else {
        (p.x - width, p.y - height - margin)
    };
    unsafe {
        let _ = SetWindowPos(
            handle(w),
            Some(HWND_TOPMOST),
            x.clamp(
                work.left + margin,
                (work.right - width - margin).max(work.left + margin),
            ),
            y.clamp(
                work.top + margin,
                (work.bottom - height - margin).max(work.top + margin),
            ),
            width,
            height,
            SWP_NOACTIVATE,
        );
    }
    show(w, true);
}
pub fn place_detail(w: &Window) {
    let work = work_area(cursor());
    let scale = w.scale_factor();
    let width = (760. * scale) as i32;
    let height = (560. * scale) as i32;
    unsafe {
        let _ = SetWindowPos(
            handle(w),
            Some(HWND_NOTOPMOST),
            work.left + (work.right - work.left - width) / 2,
            work.top + (work.bottom - work.top - height) / 2,
            width,
            height,
            SWP_NOACTIVATE,
        );
    }
}
// GPUI在显示器断开时会自动显示/还原窗口；DPI、移动和尺寸消息已完成布局更新。
// 阻止这一自动还原入口，保留用户原有隐藏/最小化状态，不改变其他窗口消息。
pub fn preserve_display_visibility(window: &Window) {
    unsafe {
        let _ = windows::Win32::UI::Shell::SetWindowSubclass(
            handle(window),
            Some(display_visibility_proc),
            0x415050,
            0,
        );
    }
}
unsafe extern "system" fn display_visibility_proc(
    hwnd: HWND,
    msg: u32,
    w: windows::Win32::Foundation::WPARAM,
    l: windows::Win32::Foundation::LPARAM,
    _id: usize,
    _data: usize,
) -> windows::Win32::Foundation::LRESULT {
    if msg == WM_DISPLAYCHANGE {
        return windows::Win32::Foundation::LRESULT(0);
    }
    if msg == WM_NCDESTROY {
        unsafe {
            let _ = windows::Win32::UI::Shell::RemoveWindowSubclass(
                hwnd,
                Some(display_visibility_proc),
                0x415050,
            );
        }
    }
    unsafe { windows::Win32::UI::Shell::DefSubclassProc(hwnd, msg, w, l) }
}

#[cfg(test)]
#[path = "display_guard_tests.rs"]
mod display_guard_tests;

#[cfg(test)]
mod bridge_tests {
    use super::{Bridge, bridge_query};
    use agent_pulse_tray::settings::Settings;
    use std::{
        fs,
        io::Write,
        os::windows::process::CommandExt,
        process::{Command, Stdio},
    };

    #[test]
    #[ignore = "需先构建内置Bridge，显式执行Windows本机IPC集成检查"]
    fn bridge_reload_native_enables_reconfigures_and_keeps_pending_events() {
        let root = std::env::temp_dir().join(format!("pulse-reload-{}", uuid::Uuid::new_v4()));
        fs::create_dir_all(&root).unwrap();
        let config = root.join("bridge.json");
        let endpoint = || format!(r"pipe:\\.\pipe\pulse-reload-{}", uuid::Uuid::new_v4());
        let write_config = |listen: &str| {
            fs::write(&config,serde_json::to_vec(&serde_json::json!({"listen":listen,"source_id":"reload-test","hub_url":"http://127.0.0.1:1"})).unwrap()).unwrap()
        };
        let first = endpoint();
        write_config(&first);
        let tray_config = root.join("tray.yaml");
        fs::write(
            &tray_config,
            format!(
                "hub_url: http://127.0.0.1:1\nbridge_config: '{}'\nbridge_enabled: false\n",
                config.display()
            ),
        )
        .unwrap();
        let mut settings = Settings::load(Some(&tray_config)).unwrap();
        let mut current = None;
        Settings::set_bridge_enabled(&tray_config, true).unwrap();
        Bridge::apply_config(&mut current, &mut settings).unwrap();
        assert!(settings.bridge_enabled);
        let first_pid = current.as_ref().unwrap().child.id();
        assert_eq!(current.as_ref().unwrap().pending_events().unwrap(), 0);
        let second = endpoint();
        write_config(&second);
        Bridge::apply_config(&mut current, &mut settings).unwrap();
        assert_ne!(current.as_ref().unwrap().child.id(), first_pid);
        assert_eq!(current.as_ref().unwrap().address, second);
        let active = current.as_ref().unwrap();
        let pid = active.child.id();
        let mut emit = Command::new(&active.executable)
            .args(["emit", "--address", &second])
            .creation_flags(0x08000000)
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        emit.stdin.take().unwrap().write_all(serde_json::to_string(&serde_json::json!({"event_id":"reload-pending","source_id":"reload-test","session_id":"test","agent":"zcode","kind":"turn.completed","occurred_at":"2026-11-15T00:00:00Z","scope":"main","title":"仅测试"})).unwrap().as_bytes()).unwrap();
        assert!(emit.wait().unwrap().success());
        assert_eq!(active.pending_events().unwrap(), 1);
        // 已编辑的配置不能改变旧队列查询端点；待发和不可读都必须保留原进程。
        let third = endpoint();
        write_config(&third);
        assert!(
            Bridge::reload(&mut current, &settings)
                .unwrap_err()
                .contains("等待交付")
        );
        assert_eq!(current.as_ref().unwrap().child.id(), pid);
        assert_eq!(current.as_ref().unwrap().pending_events().unwrap(), 1);
        // 接收端不可核对时也不能替换仍存活的旧进程。
        current.as_mut().unwrap().address = endpoint();
        assert!(
            Bridge::reload(&mut current, &settings)
                .unwrap_err()
                .contains("无法核对")
        );
        assert_eq!(current.as_ref().unwrap().child.id(), pid);
        current.as_mut().unwrap().address = second.clone();
        fs::write(
            &config,
            r#"{"listen":"127.0.0.1:18702","source_id":"reload-test"}"#,
        )
        .unwrap();
        assert!(Bridge::reload(&mut current, &settings).unwrap_err().contains("删除Bridge配置"));
        assert_eq!(current.as_ref().unwrap().child.id(), pid);
        let bytes = bridge_query(
            &current.as_ref().unwrap().executable,
            "health",
            "--address",
            &second,
        )
        .unwrap();
        assert_eq!(
            serde_json::from_slice::<serde_json::Value>(&bytes).unwrap()["pending"],
            1
        );
        drop(current.take());
        let off = Settings {
            bridge_enabled: false,
            ..settings
        };
        Bridge::reload(&mut current, &off).unwrap();
        assert!(current.is_none());
        let resolved = root.canonicalize().unwrap();
        assert!(resolved.starts_with(std::env::temp_dir().canonicalize().unwrap()));
        fs::remove_dir_all(resolved).unwrap();
    }
}
