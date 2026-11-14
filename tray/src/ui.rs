use crate::{Command, platform};
use agent_pulse_tray::{
    autostart::Status as StartupStatus,
    model::{Event, Session, SessionKey},
    sync::View,
};
use gpui::{div, prelude::*, px, rgb, *};
use std::{cell::RefCell, collections::BTreeSet, rc::Rc};

#[derive(Clone, Copy, PartialEq)]
pub enum Kind {
    List,
    Detail,
    Float,
    Card,
    Settings,
}
pub struct Shared {
    pub view: View,
    pub card_alert: Option<Event>,
    pub selected: Option<SessionKey>,
    pub history: Vec<Event>,
    pub expanded_events: BTreeSet<String>,
    pub visible_events: BTreeSet<String>,
    pub visible_elapsed: std::collections::BTreeMap<String, std::time::Duration>,
    pub read_requested: BTreeSet<String>,
    pub history_more: bool,
    pub loading: bool,
    pub generation: u64,
    pub message: String,
    pub management_busy: bool,
    pub filter: u8,
    pub floating: bool,
    pub bridge: String,
    pub demo: bool,
    pub startup_status: StartupStatus,
    pub startup_message: String,
    pub preferences: agent_pulse_tray::preferences::Preferences,
    pub power_status: String,
    pub emphasis: bool,
    pub bridge_setup_running: bool,
    #[cfg(feature = "dev-demo")]
    pub demo_dark: bool,
}
impl Shared {
    pub fn sessions(&self) -> Vec<Session> {
        let mut rows = self.view.state.snapshot.sessions.clone();
        let unread = self.view.state.unread();
        rows.retain(|s| {
            !self.view.state.snapshot.source_archived(&s.source_id)
                && match self.filter {
                    0 => s.active(),
                    1 => unread.iter().any(|e| e.key() == s.key()),
                    _ => true,
                }
        });
        rows.sort_by(|a, b| {
            let priority = |s: &Session| {
                let unread = unread.iter().any(|e| e.key() == s.key());
                match (rank(s), unread) {
                    (0, _) => 0,
                    (1, true) => 1,
                    (_, true) => 2,
                    _ => 3,
                }
            };
            priority(a)
                .cmp(&priority(b))
                .then_with(|| b.updated_at.cmp(&a.updated_at))
                .then_with(|| a.key().cmp(&b.key()))
        });
        rows
    }
    pub fn initialize_history_expansion(&mut self) {
        self.expanded_events.clear();
        self.visible_events.clear();
        self.visible_elapsed.clear();
        self.read_requested.clear();
        let unread: BTreeSet<_> = self
            .view
            .state
            .unread()
            .into_iter()
            .map(|event| event.event_id)
            .collect();
        self.expanded_events = Self::timeline_expansion_defaults(&self.history, &unread);
    }

    pub fn advance_read_visibility(&mut self, elapsed: std::time::Duration) -> Vec<String> {
        let visible = std::mem::take(&mut self.visible_events);
        let unread: BTreeSet<_> = self
            .view
            .state
            .unread()
            .into_iter()
            .map(|event| event.event_id)
            .collect();
        advance_visible_reads(
            visible,
            &unread,
            &mut self.visible_elapsed,
            &mut self.read_requested,
            elapsed,
        )
    }

    pub fn timeline_expansion_defaults(
        history: &[Event],
        unread: &BTreeSet<String>,
    ) -> BTreeSet<String> {
        history
            .iter()
            .enumerate()
            .filter(|(index, event)| *index == 0 || unread.contains(&event.event_id))
            .map(|(_, event)| event.event_id.clone())
            .collect()
    }

    pub fn selected(&self) -> Option<Session> {
        self.view
            .state
            .snapshot
            .sessions
            .iter()
            .find(|s| {
                Some(s.key()) == self.selected
                    && !self.view.state.snapshot.source_archived(&s.source_id)
            })
            .cloned()
    }
}
fn advance_visible_reads(
    visible: BTreeSet<String>,
    unread: &BTreeSet<String>,
    elapsed_by_id: &mut std::collections::BTreeMap<String, std::time::Duration>,
    requested: &mut BTreeSet<String>,
    elapsed: std::time::Duration,
) -> Vec<String> {
    elapsed_by_id.retain(|id, _| unread.contains(id));
    let mut ready = Vec::new();
    for id in visible {
        if !unread.contains(&id) || requested.contains(&id) {
            continue;
        }
        let accumulated = elapsed_by_id.entry(id.clone()).or_default();
        *accumulated += elapsed;
        if *accumulated >= std::time::Duration::from_secs(3) {
            requested.insert(id.clone());
            ready.push(id);
        }
    }
    ready
}

fn rank(s: &Session) -> u8 {
    match s.effective_state() {
        "waiting" => 0,
        "failed" | "interrupted" | "unknown" => 1,
        "running" => 2,
        _ => 3,
    }
}
pub struct Surface {
    pub kind: Kind,
    pub shared: Rc<RefCell<Shared>>,
    pub commands: async_channel::Sender<Command>,
    pub focus: FocusHandle,
    pub cursor: usize,
    pub manage: bool,
    order: Vec<SessionKey>,
    order_filter: u8,
}
#[derive(Clone, Copy)]
struct Theme {
    bg: u32,
    ink: u32,
    muted: u32,
    line: u32,
    hover: u32,
    blue: u32,
    amber: u32,
    red: u32,
}
impl Theme {
    fn new(dark: bool) -> Self {
        if dark {
            Self {
                bg: 0x292f37,
                ink: 0xe7ebf2,
                muted: 0xa5afbd,
                line: 0x3b4450,
                hover: 0x303741,
                blue: 0x8ab4ff,
                amber: 0xedc274,
                red: 0xff9c94,
            }
        } else {
            Self {
                bg: 0xffffff,
                ink: 0x202733,
                muted: 0x637083,
                line: 0xe5e8ed,
                hover: 0xf5f6f8,
                blue: 0x225bcb,
                amber: 0x926011,
                red: 0xb83d3d,
            }
        }
    }
}
fn button(id: impl Into<ElementId>, label: impl Into<SharedString>, t: Theme) -> Stateful<Div> {
    div()
        .id(id)
        .px_2()
        .py_1()
        .rounded_md()
        .text_sm()
        .cursor_pointer()
        .hover(move |s| s.bg(rgb(t.hover)))
        .child(label.into())
}
impl Surface {
    // 小窗打开期间只更新内容；新会话追加，避免鼠标或键盘指向的行换成另一会话。
    fn list_rows(&mut self) -> Vec<Session> {
        let data = self.shared.borrow();
        let mut rows = data.sessions();
        let selected = self.order.get(self.cursor).cloned();
        if self.order_filter != data.filter {
            self.order.clear();
            self.cursor = 0;
            self.order_filter = data.filter;
        }
        self.order
            .retain(|key| rows.iter().any(|s| &s.key() == key));
        for s in &rows {
            if !self.order.contains(&s.key()) {
                self.order.push(s.key());
            }
        }
        rows.sort_by_key(|s| {
            self.order
                .iter()
                .position(|key| key == &s.key())
                .unwrap_or(usize::MAX)
        });
        if let Some(index) = self
            .order
            .iter()
            .position(|key| Some(key) == selected.as_ref())
        {
            self.cursor = index;
        }
        self.cursor = self.cursor.min(rows.len().saturating_sub(1));
        rows
    }
    pub fn new(
        kind: Kind,
        shared: Rc<RefCell<Shared>>,
        commands: async_channel::Sender<Command>,
        w: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        let focus = cx.focus_handle();
        w.focus(&focus);
        if kind == Kind::List {
            cx.observe_window_activation(w, |s, w, _| {
                if !w.is_window_active() {
                    s.order.clear();
                    s.cursor = 0;
                    platform::hide(w);
                }
            })
            .detach();
        }
        Self {
            kind,
            shared,
            commands,
            focus,
            cursor: 0,
            manage: false,
            order: Vec::new(),
            order_filter: 0,
        }
    }
}
impl Render for Surface {
    fn render(&mut self, w: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let dark = matches!(
            w.appearance(),
            WindowAppearance::Dark | WindowAppearance::VibrantDark
        );
        #[cfg(feature = "dev-demo")]
        let dark = if self.shared.borrow().demo {
            self.shared.borrow().demo_dark
        } else {
            dark
        };
        let t = Theme::new(dark);
        let sender = self.commands.clone();
        let shared = self.shared.clone();
        let mut root = div()
            .id("root")
            .track_focus(&self.focus)
            .size_full()
            .font_family("Microsoft YaHei UI")
            .text_size(px(13.))
            .text_color(rgb(t.ink))
            .bg(rgb(t.bg))
            .border_1()
            .border_color(rgb(t.line))
            .flex()
            .flex_col()
            .overflow_hidden()
            .on_key_down(cx.listener(|s, e: &KeyDownEvent, w, cx| {
                let key = e.keystroke.key.as_str();
                if key == "escape" {
                    platform::hide(w);
                    return;
                }
                if s.kind == Kind::List {
                    if key == "tab" {
                        let mut data = s.shared.borrow_mut();
                        data.filter = (data.filter + 1) % 3;
                        s.cursor = 0;
                        cx.notify();
                        return;
                    }
                    let rows = s.list_rows();
                    if rows.is_empty() {
                        return;
                    }
                    match key {
                        "down" => s.cursor = (s.cursor + 1) % rows.len(),
                        "up" => s.cursor = (s.cursor + rows.len() - 1) % rows.len(),
                        "enter" => {
                            let _ = s.commands.try_send(Command::Detail(
                                rows[s.cursor.min(rows.len() - 1)].key(),
                            ));
                        }
                        _ => {}
                    }
                    cx.notify();
                }
            }));
        if self.kind == Kind::Float {
            let unread = self.shared.borrow().view.state.unread().len();
            return root
                .rounded_lg()
                .when(self.shared.borrow().emphasis, |d| {
                    d.bg(rgb(t.hover)).border_2().border_color(rgb(t.amber))
                })
                .items_center()
                .justify_center()
                .cursor_pointer()
                .text_size(px(26.))
                .text_color(rgb(t.blue))
                .child(
                    svg()
                        .path("pulse.svg")
                        .size(px(24.))
                        .text_color(rgb(t.blue)),
                )
                .when(unread > 0, |d| {
                    d.child(
                        div()
                            .absolute()
                            .top_0()
                            .right_0()
                            .text_size(px(10.))
                            .text_color(rgb(t.amber))
                            .child(unread.min(99).to_string()),
                    )
                })
                .on_mouse_down(MouseButton::Left, move |event, w, _| {
                    let p = platform::capture_pointer(w, event.position);
                    let _ = sender.try_send(Command::Drag(p.x, p.y));
                })
                .into_any_element();
        }
        if self.kind == Kind::Card {
            let alert = self.shared.borrow().card_alert.clone().unwrap_or_default();
            let key = alert.key();
            let merged = alert.event_id.is_empty() || alert.session_id.is_empty();
            return root
                .p_3()
                .gap_2()
                .child(
                    div()
                        .flex()
                        .justify_between()
                        .child("有会话需要关注")
                        .child(button("dismiss", "×", t).on_click(|_, w, _| platform::hide(w))),
                )
                .child(div().font_weight(FontWeight::SEMIBOLD).child(alert.title))
                .child(
                    div()
                        .flex_1()
                        .overflow_hidden()
                        .text_color(rgb(t.muted))
                        .child(alert.body),
                )
                .child(
                    button("open-alert", "查看提醒", t)
                        .text_color(rgb(t.blue))
                        .on_click(move |_, w, _| {
                            platform::hide(w);
                            let _ = sender.try_send(if merged {
                                Command::Attention
                            } else {
                                Command::Detail(key.clone())
                            });
                        }),
                )
                .into_any_element();
        }
        if self.kind == Kind::List {
            root = root.child(
                div()
                    .px_4()
                    .py_3()
                    .flex()
                    .items_center()
                    .justify_between()
                    .border_b_1()
                    .border_color(rgb(t.line))
                    .child(div().font_weight(FontWeight::SEMIBOLD).child("AgentPulse"))
                    .child(button("close", "×", t).on_click(|_, w, _| platform::hide(w))),
            );
        }
        if self.kind == Kind::List {
            let mut tabs = div().px_3().py_1().flex().gap_2();
            for (i, label) in ["活跃", "需关注", "全部"].into_iter().enumerate() {
                let selected = shared.borrow().filter == i as u8;
                let data = shared.clone();
                tabs = tabs.child(
                    button(("filter", i), label, t)
                        .when(selected, |b| b.bg(rgb(t.hover)).text_color(rgb(t.blue)))
                        .on_click(cx.listener(move |s, _, _, cx| {
                            data.borrow_mut().filter = i as u8;
                            s.cursor = 0;
                            cx.notify();
                        })),
                );
            }
            let rows = self.list_rows();
            let data = shared.borrow();
            let unread = data.view.state.unread();
            let mut list = div()
                .id("sessions")
                .flex_1()
                .min_h_0()
                .overflow_y_scroll()
                .px_1();
            if rows.is_empty() {
                list = list.child(div().p_5().text_color(rgb(t.muted)).child("暂无此类会话"));
            }
            for (i, session) in rows.iter().enumerate() {
                let key = session.key();
                let tx = sender.clone();
                let count = unread.iter().filter(|e| e.key() == key).count();
                let color = match session.effective_state() {
                    "waiting" => t.amber,
                    "failed" | "interrupted" => t.red,
                    "running" => t.blue,
                    _ => t.muted,
                };
                list = list.child(
                    div()
                        .id(("session", i))
                        .px_3()
                        .py_2()
                        .h(px(64.))
                        .rounded_md()
                        .cursor_pointer()
                        .hover(move |d| d.bg(rgb(t.hover)))
                        .when(self.cursor == i && w.is_window_active(), |d| {
                            d.border_l_2().border_color(rgb(t.blue))
                        })
                        .child(
                            div()
                                .flex()
                                .justify_between()
                                .gap_2()
                                .child(
                                    div()
                                        .flex_1()
                                        .min_w_0()
                                        .text_ellipsis()
                                        .child(session.display_title().to_string()),
                                )
                                .child(
                                    div()
                                        .text_size(px(11.))
                                        .text_color(rgb(color))
                                        .child(session.label()),
                                ),
                        )
                        .child(
                            div()
                                .mt_1()
                                .flex()
                                .justify_between()
                                .gap_1()
                                .text_size(px(11.))
                                .text_color(rgb(t.muted))
                                .child(div().flex_1().text_ellipsis().child(format!(
                                    "{} · {}",
                                    data.view.state.snapshot.source_label(&session.source_id),
                                    session.agent
                                )))
                                .when(count > 0, |d| {
                                    d.child(
                                        div()
                                            .text_color(rgb(t.amber))
                                            .child(format!("{count} 未读")),
                                    )
                                }),
                        )
                        .on_click(move |_, _, _| {
                            let _ = tx.try_send(Command::Detail(key.clone()));
                        }),
                );
            }
            return root
                .child(tabs)
                .child(list)
                .child(
                    div()
                        .px_4()
                        .py_2()
                        .text_size(px(11.))
                        .text_color(rgb(t.muted))
                        .border_t_1()
                        .border_color(rgb(t.line))
                        .child(data.view.status.clone()),
                )
                .into_any_element();
        }
        if self.kind == Kind::Settings {
            let data = shared.borrow();
            let show_list = sender.clone();
            let reset = sender.clone();
            let toggle = sender.clone();
            let startup = sender.clone();
            let remove_startup = sender.clone();
            let bridge_setup = sender.clone();
            let bridge_setup_running = data.bridge_setup_running;
            let body = div()
                .p_5()
                .flex()
                .flex_col()
                .gap_2()
                .child("桌面显示")
                .child(
                    button("show-list", "打开会话列表", t).on_click(move |_, _, _| {
                        let _ = show_list.try_send(Command::Show(false));
                    }),
                )
                .child(
                    button(
                        "float-toggle",
                        if data.floating {
                            "隐藏悬浮图标"
                        } else {
                            "显示悬浮图标"
                        },
                        t,
                    )
                    .on_click(move |_, _, _| {
                        let _ = toggle.try_send(Command::ToggleFloat);
                    }),
                )
                .child(
                    button("reset-float", "重置悬浮位置", t).on_click(move |_, _, _| {
                        let _ = reset.try_send(Command::ResetFloat);
                    }),
                )
                .child(div().mt_3().child("开机自动启动"))
                .child(div().text_color(rgb(t.muted)).child(data.startup_status.label()))
                .when(!data.demo, |body| {
                    let enabled = data.startup_status != StartupStatus::Enabled;
                    let disabled = data.startup_status == StartupStatus::Disabled;
                    body.child(button("startup-toggle", if disabled { "打开 Windows 启动应用设置" } else if enabled { "启用当前位置的自动启动" } else { "关闭自动启动" }, t)
                        .on_click(move |_, _, _| { let _ = startup.try_send(if disabled { Command::StartupSettings } else { Command::Autostart(enabled) }); }))
                        .when(matches!(data.startup_status, StartupStatus::OtherLocation | StartupStatus::Disabled | StartupStatus::Unknown(_)), |body| {
                            body.child(button("startup-remove", "移除已有启动快捷方式", t).on_click(move |_, _, _| { let _ = remove_startup.try_send(Command::Autostart(false)); }))
                        })
                })
                .when(!data.startup_message.is_empty(), |body| body.child(div().text_color(rgb(t.amber)).child(data.startup_message.clone())))
                .when(!data.demo, |body| {
                    body.child(div().mt_3().child("本机 Agent 接入"))
                        .child(button(
                            "bridge-setup",
                            if bridge_setup_running { "等待 Agent 配置向导完成…" } else { "配置并启用本机 Agent" },
                            t,
                        ).on_click(move |_, _, _| {
                            let _ = bridge_setup.try_send(Command::BridgeSetup);
                        }))
                        .child(div().text_color(rgb(t.muted)).child("向导会在独立控制台中扫描并逐项询问支持的 Agent；最后报告保留至按 Enter 关闭，完成后需重启 Tray。配置保存在 data/，Tray 只需设置 Hub 地址。"))
                })
                .child(div().mt_3().child("服务连接"))
                .child(
                    div()
                        .text_color(rgb(t.muted))
                        .child(data.view.status.clone()),
                )
                .child(div().text_color(rgb(t.muted)).child(data.bridge.clone()))
                .child("连接地址和凭据保存在程序旁的 tray.yaml，数据位于 data/。省略中继配置时，首次直连 Hub 后自动获取并缓存；手动中继配置优先。修改连接配置后重启生效。");
            let mut body = body.child(div().mt_3().child("主机提醒与唤醒"))
                .child(div().text_color(rgb(t.muted)).child("静默仅停止主动提醒，仍保留记录与未读。保持唤醒只在所选主机在线且有运行中任务时生效，允许屏幕熄灭。"))
                .child(div().text_color(rgb(t.muted)).child(data.power_status.clone()));
            let mut sources: BTreeSet<String> =
                data.view.state.snapshot.sources.keys().cloned().collect();
            sources.extend(
                data.view
                    .state
                    .snapshot
                    .sessions
                    .iter()
                    .map(|s| s.source_id.clone()),
            );
            sources.extend(
                data.view
                    .state
                    .snapshot
                    .devices
                    .iter()
                    .filter(|d| d.role == "bridge")
                    .map(|d| d.id.clone()),
            );
            sources.retain(|source| !data.view.state.snapshot.source_archived(source));
            if sources.is_empty() {
                body = body.child("连接 Hub 后可选择主机");
            }
            for (i, source) in sources.into_iter().enumerate() {
                let mute = data.preferences.muted_sources.contains(&source);
                let wake = data.preferences.awake_sources.contains(&source);
                let mute_id = source.clone();
                let wake_id = source.clone();
                let mute_tx = sender.clone();
                let wake_tx = sender.clone();
                body = body.child(
                    div()
                        .mt_2()
                        .child(data.view.state.snapshot.source_label(&source))
                        .child(
                            div()
                                .flex()
                                .gap_2()
                                .child(
                                    button(
                                        ("mute", i),
                                        if mute {
                                            "静默：开启"
                                        } else {
                                            "静默：关闭"
                                        },
                                        t,
                                    )
                                    .on_click(
                                        move |_, _, _| {
                                            let _ = mute_tx.try_send(Command::HostPolicy(
                                                mute_id.clone(),
                                                false,
                                            ));
                                        },
                                    ),
                                )
                                .child(
                                    button(
                                        ("wake", i),
                                        if wake {
                                            "任务唤醒：开启"
                                        } else {
                                            "任务唤醒：关闭"
                                        },
                                        t,
                                    )
                                    .on_click(
                                        move |_, _, _| {
                                            let _ = wake_tx.try_send(Command::HostPolicy(
                                                wake_id.clone(),
                                                true,
                                            ));
                                        },
                                    ),
                                ),
                        ),
                );
            }
            #[cfg(feature = "dev-demo")]
            let body = if data.demo {
                let menu = sender.clone();
                let theme = sender.clone();
                let detail = sender.clone();
                let demo_key = data.view.state.snapshot.sessions.first().map(|s| s.key());
                let long = sender.clone();
                body.child(div().mt_3().child("开发演示"))
                    .child(
                        button("demo-long", "检查长消息滚动", t).on_click(move |_, _, _| {
                            let _ = long.try_send(Command::DemoLong);
                        }),
                    )
                    .child(
                        button("demo-menu", "检查原生托盘菜单", t).on_click(move |_, _, _| {
                            let _ = menu.try_send(Command::DemoMenu);
                        }),
                    )
                    .child(
                        button("demo-detail", "检查示例会话详情", t).on_click(move |_, _, _| {
                            if let Some(key) = &demo_key {
                                let _ = detail.try_send(Command::Detail(key.clone()));
                            }
                        }),
                    )
                    .child(button("demo-theme", "切换演示浅色 / 深色", t).on_click(
                        move |_, _, _| {
                            let _ = theme.try_send(Command::DemoTheme);
                        },
                    ))
                    .child(
                        button("demo-notice", "模拟新提醒", t).on_click(move |_, _, _| {
                            let _ = sender.try_send(Command::DemoAlert);
                        }),
                    )
            } else {
                body
            };
            return root
                .child(
                    div()
                        .id("settings-content")
                        .flex_1()
                        .min_h_0()
                        .overflow_y_scroll()
                        .child(body),
                )
                .into_any_element();
        }
        let data = shared.borrow();
        let Some(session) = data.selected() else {
            return root
                .child(div().p_5().child("会话已不可用，请返回列表刷新"))
                .into_any_element();
        };
        let key = session.key();
        let mut body = div().p_5().flex().flex_col().gap_3().h_auto();
        body = body
            .child(
                div()
                    .text_size(px(20.))
                    .font_weight(FontWeight::SEMIBOLD)
                    .child(session.display_title().to_string()),
            )
            .child(div().text_color(rgb(t.muted)).child(format!(
                "{} · {} · {}{}",
                data.view.state.snapshot.source_label(&session.source_id),
                session.agent,
                session.label(),
                if session.archived {
                    " · 已归档"
                } else {
                    ""
                }
            )))
            .child(
                div()
                    .text_color(rgb(t.muted))
                    .text_size(px(11.))
                    .child(format!(
                        "最近活动：{}",
                        agent_pulse_tray::model::event_time(&session.updated_at)
                    )),
            )
            .child(
                div().text_color(rgb(t.muted)).text_size(px(11.)).child(
                    if !data.view.status.starts_with("已") {
                        "来源连接：暂时无法确认"
                    } else if data
                        .view
                        .state
                        .snapshot
                        .devices
                        .iter()
                        .any(|d| d.role == "bridge" && d.id == session.source_id)
                    {
                        "来源连接：在线；连接状态不代表任务仍在运行"
                    } else {
                        "来源连接：未连接；任务状态等待来源确认"
                    },
                ),
            );
        if !session.alias.is_empty() {
            body = body.child(
                div()
                    .text_color(rgb(t.muted))
                    .child(format!("备注：{}", session.alias)),
            );
        }
        let host = session.source_id.clone();
        let mute_enabled = data.preferences.muted_sources.contains(&host);
        let wake_enabled = data.preferences.awake_sources.contains(&host);
        let mute_tx = sender.clone();
        let wake_tx = sender.clone();
        let wake_host = host.clone();
        body = body.child(
            div()
                .flex()
                .gap_2()
                .child(
                    button(
                        "detail-host-mute",
                        if mute_enabled {
                            "主机静默：开启"
                        } else {
                            "主机静默：关闭"
                        },
                        t,
                    )
                    .on_click(move |_, _, _| {
                        let _ = mute_tx.try_send(Command::HostPolicy(host.clone(), false));
                    }),
                )
                .child(
                    button(
                        "detail-host-awake",
                        if wake_enabled {
                            "任务期间保持唤醒：开启"
                        } else {
                            "任务期间保持唤醒：关闭"
                        },
                        t,
                    )
                    .on_click(move |_, _, _| {
                        let _ = wake_tx.try_send(Command::HostPolicy(wake_host.clone(), true));
                    }),
                ),
        );
        if !session.override_state.is_empty() {
            body = body.child(div().p_2().bg(rgb(t.hover)).child(format!(
                "手动修正：{}；来源状态：{}；修正时间：{}。新的有效活动会恢复自动跟随。",
                session.label(),
                agent_pulse_tray::model::state_label(&session.state),
                session.override_at
            )));
        }
        let read_sender = sender.clone();
        let refresh_sender = sender.clone();
        let read_key = key.clone();
        let refresh_key = key.clone();
        body = body.child(
            div()
                .flex()
                .gap_2()
                .child(
                    button("read-all", "本会话全部已读", t).on_click(move |_, _, _| {
                        let _ = read_sender.try_send(Command::ReadAll(read_key.clone()));
                    }),
                )
                .child(button("refresh", "刷新", t).on_click(move |_, _, _| {
                    let _ = refresh_sender.try_send(Command::Detail(refresh_key.clone()));
                }))
                .child(
                    button("manage", "会话管理", t).on_click(cx.listener(|s, _, _, cx| {
                        s.manage = !s.manage;
                        cx.notify();
                    })),
                ),
        );
        if self.manage && !data.management_busy {
            let mut actions = div().flex().flex_wrap().gap_1();
            for (i, (label, field, value)) in [
                (
                    if session.archived {
                        "恢复会话"
                    } else {
                        "归档会话"
                    },
                    "archived",
                    serde_json::json!(!session.archived),
                ),
                ("修正为待继续", "override_state", serde_json::json!("idle")),
                (
                    "修正为已结束",
                    "override_state",
                    serde_json::json!("closed"),
                ),
                (
                    "修正为已中断",
                    "override_state",
                    serde_json::json!("interrupted"),
                ),
                ("修正为未知", "override_state", serde_json::json!("unknown")),
                ("恢复自动跟随", "override_state", serde_json::json!("")),
            ]
            .into_iter()
            .enumerate()
            {
                let tx = sender.clone();
                let session = session.clone();
                actions = actions.child(button(("management", i), label, t).on_click(
                    move |_, _, _| {
                        let _ = tx.try_send(Command::Manage(
                            Box::new(session.clone()),
                            field,
                            value.clone(),
                        ));
                    },
                ));
            }
            body = body.child(actions);
        }
        if !data.message.is_empty() {
            body = body.child(div().text_color(rgb(t.amber)).child(data.message.clone()));
        }
        body = body.child(
            div()
                .border_t_1()
                .border_color(rgb(t.line))
                .pt_3()
                .child("事件时间线 · 本地时间"),
        );
        if data.loading {
            body = body.child(div().text_color(rgb(t.muted)).child("正在加载…"));
        }
        for (index, event) in data.history.iter().cloned().enumerate() {
            let expanded = data.expanded_events.contains(&event.event_id);
            let unread = !event.read
                && data
                    .view
                    .state
                    .unread()
                    .iter()
                    .any(|e| e.event_id == event.event_id);
            let tx_select = sender.clone();
            let toggle_id = event.event_id.clone();
            let status = if unread {
                "未读"
            } else if event.read || data.view.state.pending_reads.contains(&event.event_id) {
                "已读"
            } else {
                "记录"
            };
            let label = agent_pulse_tray::model::event_label(&event.kind);
            let summary = format!(
                "{} · {} · {}{}",
                agent_pulse_tray::model::event_time(&event.occurred_at),
                label,
                status,
                if unread && !event.valid {
                    " · 已失效"
                } else {
                    ""
                }
            );
            let mut entry = div()
                .flex()
                .flex_col()
                .flex_shrink_0()
                .h_auto()
                .border_l_2()
                .border_color(rgb(if unread { t.blue } else { t.line }))
                .pl_3()
                .py_2()
                .child(
                    button(
                        ("timeline", index),
                        format!("{}  {}", if expanded { "−" } else { "+" }, summary),
                        t,
                    )
                    .on_click(move |_, _, _| {
                        let _ = tx_select.try_send(Command::ToggleEvent(toggle_id.clone()));
                    }),
                );
            if expanded {
                let tx = sender.clone();
                let id = event.event_id.clone();
                // 每次绘制均报告当前可见事件，由主循环累计前台可见时间。
                let visible_title = canvas(
                    |_, _, _| (),
                    move |bounds, _, w, _| {
                        let mask = w.content_mask().bounds;
                        if !unread
                            || !w.is_window_active()
                            || bounds.is_empty()
                            || bounds.intersect(&mask).is_empty()
                        {
                            return;
                        }
                        let _ = tx.try_send(Command::ReportVisible(id.clone()));
                    },
                )
                .absolute()
                .top_0()
                .left_0()
                .size_full();
                // 可见性探针不参与内容尺寸计算；空正文不创建容器或额外间距。
                let mut content = div().flex().flex_col().flex_shrink_0().h_auto().child(
                    div()
                        .relative()
                        .child(div().font_weight(FontWeight::SEMIBOLD).child(event.title))
                        .child(visible_title),
                );
                if !event.body.is_empty() {
                    content = content.child(div().mt_2().child(event.body));
                }
                entry = entry.child(content);
            }
            body = body.child(entry);
        }
        if data.history.is_empty() && !data.loading {
            body = body.child("暂无已采集事件");
        }
        let older = sender.clone();
        let nav = div()
            .flex_shrink_0()
            .px_4()
            .py_2()
            .border_t_1()
            .border_color(rgb(t.line))
            .flex()
            .justify_between()
            .child(format!("已加载 {} 条 · 最新在上", data.history.len()))
            .when(data.history_more && !data.loading, |d| {
                d.child(button("older", "加载更早", t).on_click(move |_, _, _| {
                    let _ = older.try_send(Command::Older);
                }))
            });
        root.child(
            div()
                .id("detail-content")
                .flex_1()
                .min_h_0()
                .overflow_y_scroll()
                .child(body),
        )
        .child(nav)
        .into_any_element()
    }
}

#[cfg(test)]
mod timeline_tests {
    use super::{Shared, advance_visible_reads};
    use agent_pulse_tray::model::Event;
    use std::collections::BTreeSet;

    fn event(id: &str) -> Event {
        Event {
            event_id: id.into(),
            ..Default::default()
        }
    }

    #[test]
    fn unread_default_expansion_and_latest_first_event() {
        let history = vec![event("latest"), event("read"), event("unread")];
        let unread = BTreeSet::from(["unread".to_string()]);
        assert_eq!(
            Shared::timeline_expansion_defaults(&history, &unread),
            BTreeSet::from(["latest".to_string(), "unread".to_string()]),
        );
    }

    #[test]
    fn visible_unread_time_accumulates_only_for_reported_events() {
        let unread = BTreeSet::from(["u".to_string()]);
        let mut elapsed = std::collections::BTreeMap::new();
        let mut requested = BTreeSet::new();
        let tick = std::time::Duration::from_millis(1500);
        assert!(
            advance_visible_reads(
                BTreeSet::from(["u".to_string()]),
                &unread,
                &mut elapsed,
                &mut requested,
                tick,
            )
            .is_empty()
        );
        assert!(
            advance_visible_reads(BTreeSet::new(), &unread, &mut elapsed, &mut requested, tick,)
                .is_empty()
        );
        assert_eq!(
            advance_visible_reads(
                BTreeSet::from(["u".to_string()]),
                &unread,
                &mut elapsed,
                &mut requested,
                tick,
            ),
            vec!["u".to_string()],
        );
        assert!(
            advance_visible_reads(
                BTreeSet::from(["u".to_string()]),
                &unread,
                &mut elapsed,
                &mut requested,
                std::time::Duration::from_secs(1),
            )
            .is_empty()
        );
    }
}
