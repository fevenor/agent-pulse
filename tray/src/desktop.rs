use windows::Win32::{
    Foundation::RECT, Graphics::Gdi::*, System::RemoteDesktop::*, UI::WindowsAndMessaging::*,
};

// 只读取状态，不记录用户名、窗口标题或屏幕内容。读取失败保持未知。
pub fn status() -> (Option<bool>, Option<bool>) {
    unsafe {
        let mut buffer = windows::core::PWSTR::null();
        let mut bytes = 0;
        let locked = if WTSQuerySessionInformationW(
            None,
            WTS_CURRENT_SESSION,
            WTSSessionInfoEx,
            &mut buffer,
            &mut bytes,
        )
        .is_ok()
        {
            let result = if bytes as usize >= std::mem::size_of::<WTSINFOEXW>() {
                let info = &*(buffer.0 as *const WTSINFOEXW);
                if info.Level == 1 {
                    let session = info.Data.WTSInfoExLevel1;
                    if session.SessionState != WTSActive {
                        Some(true)
                    } else {
                        match session.SessionFlags as u32 {
                            WTS_SESSIONSTATE_LOCK => Some(true),
                            WTS_SESSIONSTATE_UNLOCK => Some(false),
                            _ => None,
                        }
                    }
                } else {
                    None
                }
            } else {
                None
            };
            WTSFreeMemory(buffer.0.cast());
            result
        } else {
            None
        };
        let foreground = GetForegroundWindow();
        let fullscreen = if foreground.0.is_null() {
            None
        } else if foreground == GetDesktopWindow() || foreground == GetShellWindow() {
            Some(false)
        } else {
            let mut rect = RECT::default();
            let mut monitor = MONITORINFO {
                cbSize: std::mem::size_of::<MONITORINFO>() as u32,
                ..Default::default()
            };
            if GetWindowRect(foreground, &mut rect).is_ok()
                && GetMonitorInfoW(
                    MonitorFromWindow(foreground, MONITOR_DEFAULTTONEAREST),
                    &mut monitor,
                )
                .as_bool()
            {
                Some(
                    rect.left <= monitor.rcMonitor.left
                        && rect.top <= monitor.rcMonitor.top
                        && rect.right >= monitor.rcMonitor.right
                        && rect.bottom >= monitor.rcMonitor.bottom,
                )
            } else {
                None
            }
        };
        let locked = if locked == Some(false) {
            away_status()
        } else {
            locked
        };
        (locked, fullscreen)
    }
}

// 仅保存可信输入的时间，不读取按键、坐标或窗口内容；软件注入不延长在场时间。
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
static AWAY_SECONDS: AtomicU64 = AtomicU64::new(0);
static LAST_HUMAN_INPUT: AtomicU64 = AtomicU64::new(0);
static MONITOR_READY: AtomicBool = AtomicBool::new(false);
static MONITOR_STARTED: AtomicBool = AtomicBool::new(false);

pub fn configure(minutes: u64) {
    AWAY_SECONDS.store(minutes.saturating_mul(60), Ordering::Relaxed);
    if minutes == 0 || MONITOR_STARTED.swap(true, Ordering::SeqCst) {
        return;
    }
    std::thread::spawn(|| unsafe {
        use windows::Win32::System::LibraryLoader::GetModuleHandleW;
        let module = GetModuleHandleW(None).ok().map(Into::into);
        let keyboard = SetWindowsHookExW(WH_KEYBOARD_LL, Some(keyboard_input), module, 0);
        let mouse = SetWindowsHookExW(WH_MOUSE_LL, Some(mouse_input), module, 0);
        match (keyboard, mouse) {
            (Ok(keyboard), Ok(mouse)) => {
                MONITOR_READY.store(true, Ordering::Release);
                let mut message = MSG::default();
                while GetMessageW(&mut message, None, 0, 0).0 > 0 {
                    let _ = TranslateMessage(&message);
                    DispatchMessageW(&message);
                }
                MONITOR_READY.store(false, Ordering::Release);
                let _ = UnhookWindowsHookEx(keyboard);
                let _ = UnhookWindowsHookEx(mouse);
            }
            (keyboard, mouse) => {
                MONITOR_STARTED.store(false, Ordering::Release);
                if let Ok(h) = keyboard {
                    let _ = UnhookWindowsHookEx(h);
                }
                if let Ok(h) = mouse {
                    let _ = UnhookWindowsHookEx(h);
                }
            }
        }
    });
}
fn register_human_input(injected: bool) {
    if !injected && AWAY_SECONDS.load(Ordering::Relaxed) > 0 {
        LAST_HUMAN_INPUT.store(
            unsafe { windows::Win32::System::SystemInformation::GetTickCount64() },
            Ordering::Relaxed,
        );
    }
}
unsafe extern "system" fn keyboard_input(
    code: i32,
    w: windows::Win32::Foundation::WPARAM,
    l: windows::Win32::Foundation::LPARAM,
) -> windows::Win32::Foundation::LRESULT {
    if code >= 0 {
        let input = unsafe { &*(l.0 as *const KBDLLHOOKSTRUCT) };
        register_human_input(input.flags.0 & LLKHF_INJECTED.0 != 0);
    }
    unsafe { CallNextHookEx(None, code, w, l) }
}
unsafe extern "system" fn mouse_input(
    code: i32,
    w: windows::Win32::Foundation::WPARAM,
    l: windows::Win32::Foundation::LPARAM,
) -> windows::Win32::Foundation::LRESULT {
    if code >= 0 {
        let input = unsafe { &*(l.0 as *const MSLLHOOKSTRUCT) };
        register_human_input(input.flags & LLMHF_INJECTED != 0);
    }
    unsafe { CallNextHookEx(None, code, w, l) }
}
fn away_status() -> Option<bool> {
    let threshold = AWAY_SECONDS.load(Ordering::Relaxed);
    if threshold == 0 {
        return Some(false);
    }
    if !MONITOR_READY.load(Ordering::Acquire) {
        return None;
    }
    idle_elapsed(
        unsafe { windows::Win32::System::SystemInformation::GetTickCount64() },
        LAST_HUMAN_INPUT.load(Ordering::Relaxed),
        threshold,
    )
}
fn idle_elapsed(now: u64, last: u64, threshold: u64) -> Option<bool> {
    if threshold == 0 {
        return Some(false);
    }
    if last == 0 {
        return None;
    }
    Some(now.saturating_sub(last) >= threshold.saturating_mul(1000))
}
// 设置页仅显示监测结论，不展示任何输入内容。
pub fn presence_label() -> String {
    if AWAY_SECONDS.load(Ordering::Relaxed) == 0 {
        return "离开检测已关闭".into();
    }
    if !MONITOR_READY.load(Ordering::Acquire) {
        return "真人输入监测不可用，当前按未知状态处理；可重新应用检测设置。".into();
    }
    match away_status() {
        Some(true) => "当前已离开".into(),
        Some(false) => "当前观察到用户在场".into(),
        None => "尚未观察到可信真人输入，当前在场状态未知".into(),
    }
}

#[cfg(test)]
mod presence_tests {
    use super::*;
    #[test]
    fn idle_requires_real_input_and_respects_threshold() {
        assert_eq!(idle_elapsed(900000, 0, 600), None);
        assert_eq!(idle_elapsed(600999, 1000, 600), Some(false));
        assert_eq!(idle_elapsed(601000, 1000, 600), Some(true));
        assert_eq!(idle_elapsed(900000, 0, 0), Some(false));
        assert_eq!(idle_elapsed(1000, 2000, 600), Some(false));
    }
    #[test]
    fn injected_input_never_records_a_human_timestamp() {
        let before = LAST_HUMAN_INPUT.load(Ordering::Relaxed);
        register_human_input(true);
        assert_eq!(LAST_HUMAN_INPUT.load(Ordering::Relaxed), before);
    }
}
