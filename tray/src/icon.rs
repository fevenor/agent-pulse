use crate::{Command, platform};
use agent_pulse_tray::model::{Session, SessionKey};
use std::{cell::RefCell, collections::BTreeMap, rc::Rc};
use tray_icon::{
    Icon, MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent,
    menu::{CheckMenuItem, Menu, MenuEvent, MenuItem, PredefinedMenuItem},
};
use unicode_segmentation::UnicodeSegmentation;

pub struct Tray {
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
            .with_tooltip("AgentPulse")
            .with_menu(Box::new(menu.clone()))
            .with_menu_on_left_click(false)
            .with_icon(pulse_icon(false))
            .build()
            .expect("无法创建托盘图标");
        Self {
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
    let mut pixels = vec![0; 32 * 32 * 4];
    for y in 0..32 {
        for x in 0..32 {
            let pulse = ((3..=10).contains(&x) && (y - 16i32).abs() <= 1)
                || ((10..=14).contains(&x) && (y - (16 - (x - 10) * 2)).abs() <= 1)
                || ((14..=20).contains(&x) && (y - (8 + (x - 14) * 3)).abs() <= 1)
                || ((20..=24).contains(&x) && (y - (26 - (x - 20) * 2)).abs() <= 1)
                || ((24..=29).contains(&x) && (y - 18).abs() <= 1);
            let dot = unread && (x - 26).pow(2) + (y - 5).pow(2) <= 16;
            let i = (y * 32 + x) as usize * 4;
            if pulse || dot {
                pixels[i..i + 4].copy_from_slice(if dot {
                    &[209, 137, 33, 255]
                } else {
                    &[52, 114, 209, 255]
                });
            }
        }
    }
    Icon::from_rgba(pixels, 32, 32).expect("图标生成失败")
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn menu_title_keeps_graphemes() {
        assert_eq!(
            short_title("项目文档索引构建与搜索优化"),
            "项目文档索引构建与搜索…"
        );
        assert_eq!(short_title("á👨‍👩‍👧‍👦短标题"), "á👨‍👩‍👧‍👦短标题");
    }
}
