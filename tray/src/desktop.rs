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
        (locked, fullscreen)
    }
}
