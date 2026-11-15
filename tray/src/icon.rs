use crate::{Command, platform};
use agent_pulse_tray::model::{Session, SessionKey};
use std::{cell::RefCell, collections::BTreeMap, rc::Rc};
use tray_icon::{
    Icon, MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent,
    menu::{CheckMenuItem, Menu, MenuEvent, MenuItem, PredefinedMenuItem},
};
use unicode_segmentation::UnicodeSegmentation;

// Windows 以 GUID 保存通知区域偏好；同一产品重启和升级不得生成新图标身份。
// 不写系统钉住设置，不依赖配置或程序版本；同一登录会话由单实例锁保证唯一。
const TRAY_GUID: u128 = 0x5c676aa6_6b5e_4f58_b6f1_373063caa271;

pub struct Tray {
    guid: u128,
    pub icon: TrayIcon,
    menu: Menu,
    sessions: Vec<MenuItem>,
    actions: Rc<RefCell<BTreeMap<String, SessionKey>>>,
    caption: MenuItem,
    open: MenuItem,
    float: CheckMenuItem,
    fingerprint: String,
}
impl gpui::Global for Tray {}
pub fn short_title(title: &str) -> String {
    let chars: Vec<_> = title.graphemes(true).collect();
    if chars.len() > 12 {
        format!("{}…", chars[..11].concat())
    } else {
        title.into()
    }
}
impl Tray {
    #[cfg(feature = "dev-demo")]
    pub fn menu_for_demo(&self) -> Menu {
        self.menu.clone()
    }
    pub fn create(sender: async_channel::Sender<Command>) -> Self {
        NOTIFICATION_SENDER.with(|s| *s.borrow_mut() = Some(sender.clone()));
        let guid = TRAY_GUID;
        let menu = Menu::new();
        let caption = MenuItem::new("活跃会话", false, None);
        let open = MenuItem::new("打开会话列表", true, None);
        let float = CheckMenuItem::new("显示悬浮图标", true, false, None);
        let settings = MenuItem::new("设置…", true, None);
        let quit = MenuItem::new("退出 AgentPulse", true, None);
        menu.append_items(&[
            &caption,
            &PredefinedMenuItem::separator(),
            &open,
            &float,
            &settings,
            &PredefinedMenuItem::separator(),
            &quit,
        ])
        .expect("无法创建托盘菜单");
        let ids = (
            open.id().clone(),
            float.id().clone(),
            settings.id().clone(),
            quit.id().clone(),
        );
        let actions = Rc::new(RefCell::new(BTreeMap::<String, SessionKey>::new()));
        // 菜单事件回调需要跨线程安全；映射由主事件循环解析，身份不依赖标题。
        let menu_sender = sender.clone();
        MenuEvent::set_event_handler(Some(move |e: MenuEvent| {
            let command = if e.id == ids.0 {
                Command::Active
            } else if e.id == ids.1 {
                Command::ToggleFloat
            } else if e.id == ids.2 {
                Command::Settings
            } else if e.id == ids.3 {
                Command::Quit
            } else {
                Command::MenuSession(e.id.0)
            };
            let _ = menu_sender.try_send(command);
        }));
        TrayIconEvent::set_event_handler(Some(move |e| {
            if matches!(
                e,
                TrayIconEvent::Click {
                    button: MouseButton::Left,
                    button_state: MouseButtonState::Up,
                    ..
                }
            ) {
                let _ = sender.try_send(Command::Show(false));
            }
        }));
        let icon = TrayIconBuilder::new()
            .with_guid(guid)
            .with_tooltip("AgentPulse")
            .with_menu(Box::new(menu.clone()))
            .with_menu_on_left_click(false)
            .with_icon(pulse_icon(false))
            .build()
            .expect("无法创建托盘图标");
        unsafe {
            let _ = windows::Win32::UI::Shell::SetWindowSubclass(
                windows::Win32::Foundation::HWND(icon.window_handle()),
                Some(notification_callback),
                1,
                0,
            );
        }
        Self {
            guid,
            icon,
            menu,
            sessions: vec![],
            actions,
            caption,
            open,
            float,
            fingerprint: String::new(),
        }
    }
    pub fn notify(&self, title: &str, body: &str) -> bool {
        use windows::{
            Win32::{Foundation::HWND, UI::Shell::*},
            core::GUID,
        };
        let mut data = NOTIFYICONDATAW {
            cbSize: std::mem::size_of::<NOTIFYICONDATAW>() as u32,
            hWnd: HWND(self.icon.window_handle()),
            uFlags: NIF_GUID | NIF_INFO,
            guidItem: GUID::from_u128(self.guid),
            dwInfoFlags: NIIF_INFO | NIIF_RESPECT_QUIET_TIME,
            ..Default::default()
        };
        wide_copy(&mut data.szInfoTitle, title);
        // Windows 将空正文解释为关闭气泡；命令钩子的正文通常为空。
        wide_copy(
            &mut data.szInfo,
            if body.is_empty() {
                "点击查看需要关注的会话。"
            } else {
                body
            },
        );
        unsafe { Shell_NotifyIconW(NIM_MODIFY, &data).as_bool() }
    }
    pub fn resolve(&self, id: &str) -> Option<SessionKey> {
        self.actions.borrow().get(id).cloned()
    }
    pub fn update(&mut self, sessions: &[Session], unread: usize, floating: bool, online: bool) {
        if platform::menu_open() {
            return;
        }
        let fingerprint = format!("{sessions:?}/{unread}/{floating}/{online}");
        if fingerprint == self.fingerprint {
            return;
        }
        self.fingerprint = fingerprint;
        for item in self.sessions.drain(..) {
            let _ = self.menu.remove(&item);
        }
        self.actions.borrow_mut().clear();
        let mut active: Vec<_> = sessions.iter().filter(|s| s.active()).collect();
        active.sort_by(|a, b| {
            rank(a)
                .cmp(&rank(b))
                .then_with(|| b.updated_at.cmp(&a.updated_at))
                .then_with(|| a.key().cmp(&b.key()))
        });
        self.caption.set_text(if online {
            "活跃会话"
        } else {
            "活跃会话 · 离线"
        });
        self.open.set_text(if active.len() > 5 {
            format!("查看全部活跃会话（{}）", active.len())
        } else {
            "打开会话列表".into()
        });
        for (i, s) in active.iter().take(5).enumerate() {
            // 原生菜单使用制表符对齐状态；转义标题里的快捷键标记。
            let title = short_title(s.display_title())
                .replace('&', "&&")
                .replace(['\r', '\n', '\t'], " ");
            let item = MenuItem::new(format!("{title}\t{}", s.label()), true, None);
            self.actions
                .borrow_mut()
                .insert(item.id().0.clone(), s.key());
            let _ = self.menu.insert(&item, i + 1);
            self.sessions.push(item);
        }
        if active.is_empty() {
            let item = MenuItem::new("暂无活跃会话", false, None);
            let _ = self.menu.insert(&item, 1);
            self.sessions.push(item);
        }
        self.float.set_checked(floating);
        let _ = self.icon.set_icon(Some(pulse_icon(unread > 0)));
        let _ = self
            .icon
            .set_tooltip(Some(format!("AgentPulse · {unread} 条未读")));
    }
}
fn rank(s: &Session) -> u8 {
    match s.effective_state() {
        "waiting" => 0,
        "failed" | "interrupted" | "unknown" => 1,
        "running" => 2,
        _ => 3,
    }
}
fn pulse_icon(unread: bool) -> Icon {
    Icon::from_rgba(crate::artwork::pixels(32, unread), 32, 32).expect("图标生成失败")
}
thread_local! {
    static NOTIFICATION_SENDER: RefCell<Option<async_channel::Sender<Command>>> = const { RefCell::new(None) };
}
fn wide_copy<const N: usize>(target: &mut [u16; N], text: &str) {
    let mut index = 0;
    for c in text.chars().filter(|c| *c != '\0') {
        let mut units = [0u16; 2];
        let encoded = c.encode_utf16(&mut units);
        if index + encoded.len() >= N {
            break;
        }
        target[index..index + encoded.len()].copy_from_slice(encoded);
        index += encoded.len();
    }
}
unsafe extern "system" fn notification_callback(
    hwnd: windows::Win32::Foundation::HWND,
    message: u32,
    wparam: windows::Win32::Foundation::WPARAM,
    lparam: windows::Win32::Foundation::LPARAM,
    _: usize,
    _: usize,
) -> windows::Win32::Foundation::LRESULT {
    use windows::Win32::UI::Shell::*;
    #[cfg(feature = "dev-demo")]
    if message == 6002 && (lparam.0 as u32 & 0xffff) == NIN_BALLOONSHOW {
        eprintln!("开发验收：Windows 已显示系统通知");
    }
    // 锁定 tray-icon 0.26.1 的托盘回调消息；其自身未转发气泡点击。
    if message == 6002 && (lparam.0 as u32 & 0xffff) == NIN_BALLOONUSERCLICK {
        NOTIFICATION_SENDER.with(|s| {
            if let Some(tx) = s.borrow().as_ref() {
                let _ = tx.try_send(Command::Attention);
            }
        });
    }
    unsafe { DefSubclassProc(hwnd, message, wparam, lparam) }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn notification_area_identity_is_product_stable() {
        assert_eq!(TRAY_GUID, 0x5c676aa6_6b5e_4f58_b6f1_373063caa271);
        assert_ne!(TRAY_GUID, 0);
    }
    #[test]
    fn stable_guid_can_register_again_after_native_icon_exit() {
        use windows::{Win32::UI::Shell::*, core::GUID};
        let identity = NOTIFYICONIDENTIFIER {
            cbSize: std::mem::size_of::<NOTIFYICONIDENTIFIER>() as u32,
            guidItem: GUID::from_u128(TRAY_GUID),
            ..Default::default()
        };
        if unsafe { Shell_NotifyIconGetRect(&identity) }.is_ok() {
            // 不覆盖已运行的新版本图标；有日常实例时原生重复登记测试另行执行。
            eprintln!("已有相同身份的托盘实例，保留用户图标，未执行原生登记测试");
            return;
        }
        for _ in 0..2 {
            let icon = TrayIconBuilder::new()
                .with_guid(TRAY_GUID)
                .with_tooltip("AgentPulse 原生图标回归")
                .with_icon(pulse_icon(false))
                .build()
                .unwrap();
            unsafe { Shell_NotifyIconGetRect(&identity) }.unwrap();
            drop(icon);
        }
    }

    #[test]
    fn menu_title_keeps_graphemes() {
        assert_eq!(
            short_title("项目文档索引构建与搜索优化"),
            "项目文档索引构建与搜索…"
        );
        assert_eq!(short_title("á👨‍👩‍👧‍👦短标题"), "á👨‍👩‍👧‍👦短标题");
    }
}
