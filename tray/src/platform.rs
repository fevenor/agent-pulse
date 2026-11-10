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
}
impl Bridge {
    pub fn start(settings: &agent_pulse_tray::settings::Settings) -> Result<Option<Self>, String> {
        use std::os::windows::process::CommandExt;
        if !settings.bridge_enabled {
            return Ok(None);
        }
        let executable = extract_bridge(&settings.data_dir)?;
        let config =
            std::fs::canonicalize(&settings.bridge_config).map_err(|_| "本地接入配置不存在")?;
        let mut child = std::process::Command::new(executable)
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
        Ok(Some(Self { child, job }))
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
