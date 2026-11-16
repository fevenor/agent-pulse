use super::*;
use windows::Win32::{
    Foundation::{LPARAM, LRESULT, WPARAM},
    UI::Shell::*,
};

// 模拟库的显示变化自动还原路径；这只证明消息保护，不冒充真实RDP验收。
unsafe extern "system" fn auto_restore(
    hwnd: HWND,
    msg: u32,
    w: WPARAM,
    l: LPARAM,
    _id: usize,
    _data: usize,
) -> LRESULT {
    if msg == WM_DISPLAYCHANGE {
        unsafe {
            let _ = ShowWindow(hwnd, SW_SHOWNORMAL);
        }
    }
    unsafe { DefSubclassProc(hwnd, msg, w, l) }
}
#[test]
fn display_change_cannot_expose_a_hidden_window() {
    unsafe {
        let hwnd = CreateWindowExW(
            WS_EX_TOOLWINDOW,
            windows::core::w!("STATIC"),
            windows::core::w!("隔离显示变化探针"),
            WS_OVERLAPPEDWINDOW,
            -10000,
            -10000,
            10,
            10,
            None,
            None,
            None,
            None,
        )
        .unwrap();
        assert!(SetWindowSubclass(hwnd, Some(auto_restore), 0x415051, 0).as_bool());
        assert!(SetWindowSubclass(hwnd, Some(display_visibility_proc), 0x415050, 0).as_bool());
        let _ = SendMessageW(hwnd, WM_DISPLAYCHANGE, Some(WPARAM(0)), Some(LPARAM(0)));
        let visible = IsWindowVisible(hwnd).as_bool();
        let _ = DestroyWindow(hwnd);
        assert!(!visible, "显示变化使隐藏窗口自动出现");
    }
}
