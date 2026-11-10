// 仅开发 feature 编译本文件，发布构建不会包含示例数据或演示入口。
use agent_pulse_tray::{
    model::{Event, Session, State},
    sync::View,
};
pub fn view() -> View {
    let mut state = State::default();
    for (i, (title, source, agent, status)) in [
        ("API 鉴权检查", "开发机 / Windows", "Codex", "waiting"),
        ("登录异常修复", "构建机 / Linux", "OpenCode", "failed"),
        (
            "项目文档索引构建与搜索优化",
            "开发机 / Debian",
            "Qoder",
            "running",
        ),
        ("安装包回归", "测试机 / Windows", "Codex", "idle"),
    ]
    .into_iter()
    .enumerate()
    {
        state.snapshot.sessions.push(Session {
            source_id: source.into(),
            agent: agent.into(),
            session_id: format!("demo-{i}"),
            title: title.into(),
            state: status.into(),
            last_event_id: format!("event-{i}"),
            last_kind: if i == 0 {
                "approval.required"
            } else {
                "run.failed"
            }
            .into(),
            updated_at: "2026-11-10T10:24:00Z".into(),
            ..Default::default()
        });
        if i < 2 {
            state.snapshot.unread.push(Event{event_id:format!("event-{i}"),position:i as i64+1,source_id:source.into(),agent:agent.into(),session_id:format!("demo-{i}"),title:if i==0{"等待确认请求权限"}else{"测试执行失败"}.into(),body:"这是开发演示数据。请在原始 Agent 窗口中查看上下文；本窗口不批准请求或执行远程命令。".into(),valid:true,occurred_at:"2026-11-10T10:24:00Z".into(),..Default::default()});
        }
    }
    View {
        state,
        status: "开发演示 · 未连接服务".into(),
        alert: None,
        can_notify: true,
    }
}

pub fn long_event() -> Event {
    let mut event = view().state.snapshot.unread[0].clone();
    event.event_id = "demo-long-event".into();
    event.title = "长消息滚动检查：标题和底部翻页区域应保持清晰".into();
    event.body = (1..=36).map(|i| format!("第 {i} 段：这是用于检查长消息的合成内容。缩小或放大详情窗口后，正文应自动换行；可以向下滚动阅读，底部翻页操作不应被挤出窗口。\n")).collect();
    event
}
