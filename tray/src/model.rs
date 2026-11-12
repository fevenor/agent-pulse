use serde::{Deserialize, Serialize};
use std::{
    collections::BTreeSet,
    fs::{self, File, OpenOptions},
    io::Write,
    path::{Path, PathBuf},
};

#[derive(Clone, Default, Debug, Serialize, Deserialize, PartialEq)]
pub struct Event {
    #[serde(default)]
    pub position: i64,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub occurred_at: String,
    pub event_id: String,
    pub source_id: String,
    pub agent: String,
    pub session_id: String,
    pub title: String,
    #[serde(default)]
    pub body: String,
    #[serde(default)]
    pub project: String,
    #[serde(default)]
    pub valid: bool,
    #[serde(default)]
    pub read: bool,
}
#[derive(Clone, Default, Debug, Serialize, Deserialize, PartialEq)]
pub struct Session {
    #[serde(default)]
    pub alias: String,
    #[serde(default)]
    pub archived: bool,
    #[serde(default)]
    pub override_state: String,
    #[serde(default)]
    pub override_at: String,
    #[serde(default)]
    pub management_version: i64,
    #[serde(default)]
    pub last_event_id: String,
    #[serde(default)]
    pub last_kind: String,
    #[serde(default)]
    pub updated_at: String,
    pub source_id: String,
    pub agent: String,
    pub session_id: String,
    pub title: String,
    pub state: String,
    #[serde(default)]
    pub project: String,
}
#[derive(Clone, Default, Debug, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord)]
pub struct SessionKey {
    pub source_id: String,
    pub agent: String,
    pub session_id: String,
}
impl Session {
    pub fn key(&self) -> SessionKey {
        SessionKey {
            source_id: self.source_id.clone(),
            agent: self.agent.clone(),
            session_id: self.session_id.clone(),
        }
    }
    pub fn display_title(&self) -> &str {
        if self.alias.is_empty() {
            &self.title
        } else {
            &self.alias
        }
    }
    pub fn effective_state(&self) -> &str {
        if self.override_state.is_empty() {
            &self.state
        } else {
            &self.override_state
        }
    }
    pub fn active(&self) -> bool {
        !self.archived && self.effective_state() != "closed"
    }
    pub fn label(&self) -> &'static str {
        if self.effective_state() == "waiting" {
            return match self.last_kind.as_str() {
                "approval.required" => "等待批准",
                "input.required" => "等待输入",
                _ => "等待处理",
            };
        }
        state_label(self.effective_state())
    }
}
impl Event {
    pub fn key(&self) -> SessionKey {
        SessionKey {
            source_id: self.source_id.clone(),
            agent: self.agent.clone(),
            session_id: self.session_id.clone(),
        }
    }
}
pub fn event_label(kind: &str) -> &'static str {
    match kind {
        "session.started" => "会话开始",
        "session.resumed" => "继续执行",
        "session.closed" => "会话结束",
        "activity.observed" => "任务活动",
        "turn.completed" => "执行完成",
        "input.required" => "等待输入",
        "approval.required" => "等待批准",
        "run.failed" => "执行失败",
        "run.interrupted" => "执行中断",
        "observation.lost" => "状态待确认",
        _ => "状态记录",
    }
}
pub fn event_time(value: &str) -> String {
    use time::{OffsetDateTime, UtcOffset, format_description::well_known::Rfc3339};
    let Ok(date) = OffsetDateTime::parse(value, &Rfc3339) else {
        return value.into();
    };
    let offset = UtcOffset::local_offset_at(date);
    let date = date.to_offset(offset.unwrap_or(UtcOffset::UTC));
    format!(
        "{:04}-{:02}-{:02} {:02}:{:02}:{:02}{}",
        date.year(),
        date.month() as u8,
        date.day(),
        date.hour(),
        date.minute(),
        date.second(),
        if offset.is_err() { " UTC" } else { "" }
    )
}
pub fn state_label(state: &str) -> &'static str {
    match state {
        "running" => "进行中",
        "waiting" => "等待处理",
        "idle" => "待继续",
        "failed" => "执行失败",
        "interrupted" => "已中断",
        "closed" => "已结束",
        _ => "未知",
    }
}
#[derive(Clone, Default, Debug, Serialize, Deserialize, PartialEq)]
pub struct Device {
    pub id: String,
    pub role: String,
    #[serde(default)]
    pub host_name: String,
    #[serde(default)]
    pub last_seen: String,
}
#[derive(Clone, Default, Debug, Serialize, Deserialize, PartialEq)]
pub struct SourceLabel {
    pub name: String,
    pub environment: String,
    #[serde(default)]
    pub archived: bool,
}
#[derive(Clone, Default, Debug, Serialize, Deserialize, PartialEq)]
pub struct HostLabel {
    #[serde(default)]
    #[serde(alias = "name")]
    pub display_name: String,
    #[serde(default)]
    pub default_name: String,
    #[serde(default)]
    pub archived: bool,
}
#[derive(Clone, Default, Debug, Serialize, Deserialize, PartialEq)]
pub struct Snapshot {
    #[serde(default)]
    pub sources: std::collections::BTreeMap<String, SourceLabel>,
    #[serde(default, deserialize_with = "null_vec")]
    pub devices: Vec<Device>,
    pub run_id: String,
    pub version: u64,
    #[serde(default, deserialize_with = "null_vec")]
    pub sessions: Vec<Session>,
    #[serde(default, deserialize_with = "null_vec")]
    pub unread: Vec<Event>,
}
fn null_vec<'de, D: serde::Deserializer<'de>, T: Deserialize<'de>>(
    d: D,
) -> Result<Vec<T>, D::Error> {
    Ok(Option::<Vec<T>>::deserialize(d)?.unwrap_or_default())
}

#[derive(Clone, Default, Debug, Serialize, Deserialize, PartialEq)]
#[serde(default)]
pub struct State {
    pub device_id: String,
    pub snapshot: Snapshot,
    pub pending_reads: BTreeSet<String>,
    pub notified: BTreeSet<String>,
}
impl State {
    pub fn unread(&self) -> Vec<Event> {
        self.snapshot
            .unread
            .iter()
            .filter(|e| {
                !e.read
                    && !self.pending_reads.contains(&e.event_id)
                    && !self.snapshot.source_archived(&e.source_id)
            })
            .cloned()
            .collect()
    }
    pub fn notifications(&self) -> Vec<Event> {
        // 同一会话只弹最新的一项，列表保留全部未读。
        let mut sessions = BTreeSet::new();
        self.unread()
            .into_iter()
            .rev()
            .filter(|e| e.valid && !self.notified.contains(&e.event_id))
            .filter(|e| {
                sessions.insert((e.source_id.clone(), e.agent.clone(), e.session_id.clone()))
            })
            .collect()
    }
    pub fn notifications_for(&self, muted: &BTreeSet<String>) -> Vec<Event> {
        self.notifications()
            .into_iter()
            .filter(|e| !muted.contains(&e.source_id))
            .collect()
    }
}

pub struct Store {
    pub state: State,
    pub error: Option<String>,
    path: PathBuf,
    _lock: File,
}
impl Store {
    pub fn open(path: &Path) -> Result<Self, String> {
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).map_err(|_| "无法创建本地状态目录")?;
        }
        let lock = OpenOptions::new()
            .create(true)
            .truncate(false)
            .read(true)
            .write(true)
            .open(path.with_extension("lock"))
            .map_err(|_| "无法打开状态锁")?;
        lock.try_lock()
            .map_err(|_| "该状态目录已有运行中的客户端")?;
        let mut state: State = match fs::read(path) {
            Ok(raw) => serde_json::from_slice(&raw)
                .map_err(|_| "状态文件损坏；保留原文件，请恢复备份后重试")?,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => State::default(),
            Err(_) => return Err("无法读取状态文件".into()),
        };
        if state.device_id.is_empty() {
            state.device_id = format!("tray-{}", uuid::Uuid::new_v4());
        }
        let mut store = Self {
            state,
            error: None,
            path: path.into(),
            _lock: lock,
        };
        store.change(|_| {})?;
        Ok(store)
    }
    // 只有落盘成功才更新内存；失败不会向界面报告已读成功。
    pub fn change(&mut self, apply: impl FnOnce(&mut State)) -> Result<(), String> {
        let mut next = self.state.clone();
        apply(&mut next);
        let raw = serde_json::to_vec(&next).map_err(|_| "状态编码失败")?;
        let temp = self.path.with_extension("tmp");
        let write = || -> std::io::Result<()> {
            let mut file = File::create(&temp)?;
            file.write_all(&raw)?;
            file.sync_all()?;
            drop(file);
            fs::rename(&temp, &self.path)?;
            Ok(())
        };
        if write().is_err() {
            let error = "本地状态保存失败，操作未确认；请检查可用空间及写入权限".to_string();
            self.error = Some(error.clone());
            return Err(error);
        }
        self.state = next;
        Ok(())
    }
}

impl Snapshot {
    pub fn source_archived(&self, id: &str) -> bool {
        self.sources.get(id).is_some_and(|source| source.archived)
    }

    pub fn source_label(&self, id: &str) -> String {
        self.sources
            .get(id)
            .map(|source| source.name.clone())
            .filter(|name| !name.is_empty())
            .unwrap_or_else(|| id.into())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn event(id: &str) -> Event {
        Event {
            event_id: id.into(),
            session_id: id.into(),
            valid: true,
            ..Default::default()
        }
    }
    #[test]
    fn offline_reads_survive_snapshot_and_restart() {
        let dir = std::env::temp_dir().join(uuid::Uuid::new_v4().to_string());
        let path = dir.join("中文 空格状态.json");
        {
            let mut store = Store::open(&path).unwrap();
            assert!(Store::open(&path).is_err());
            store
                .change(|s| {
                    s.pending_reads.insert("read".into());
                    s.notified.insert("shown".into());
                })
                .unwrap();
            store
                .change(|s| s.snapshot.unread = vec![event("read"), event("shown"), event("new")])
                .unwrap();
            assert_eq!(
                store
                    .state
                    .notifications()
                    .iter()
                    .map(|e| e.event_id.as_str())
                    .collect::<Vec<_>>(),
                vec!["new"]
            );
        }
        let store = Store::open(&path).unwrap();
        assert!(store.state.pending_reads.contains("read"));
        assert_eq!(store.state.unread().len(), 2);
        drop(store);
        fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn full_snapshot_removes_other_devices_reads_and_invalid_events_do_not_notify() {
        let mut s = State::default();
        s.snapshot.unread = vec![event("old")];
        s.snapshot = Snapshot {
            unread: vec![Event {
                valid: false,
                ..event("invalid")
            }],
            ..Default::default()
        };
        assert_eq!(s.unread().len(), 1);
        assert!(s.notifications().is_empty());
    }
    #[test]
    fn central_host_name_and_archive_flag_apply_to_events() {
        let mut state = State::default();
        state.snapshot.sources.insert(
            "host-b".into(),
            SourceLabel {
                name: "旧主机名".into(),
                environment: String::new(),
                archived: false,
            },
        );
        assert!(!state.snapshot.source_archived("host-b"));
        assert_eq!(state.snapshot.source_label("host-b"), "旧主机名");
        state.snapshot.sources.get_mut("host-b").unwrap().archived = true;
        state.snapshot.unread.push(Event {
            source_id: "host-b".into(),
            valid: true,
            ..event("host-event")
        });
        assert!(state.unread().is_empty());
        assert!(state.notifications().is_empty());
    }

    #[test]
    fn failed_save_keeps_memory_and_existing_file() {
        let dir = std::env::temp_dir().join(uuid::Uuid::new_v4().to_string());
        let path = dir.join("state.json");
        let mut s = Store::open(&path).unwrap();
        let before = fs::read(&path).unwrap();
        fs::create_dir(path.with_extension("tmp")).unwrap();
        assert!(
            s.change(|s| {
                s.pending_reads.insert("x".into());
            })
            .is_err()
        );
        assert!(s.state.pending_reads.is_empty());
        assert!(s.error.is_some());
        assert_eq!(fs::read(&path).unwrap(), before);
        drop(s);
        fs::remove_dir(path.with_extension("tmp")).unwrap();
        let s = Store::open(&path).unwrap();
        assert!(s.state.pending_reads.is_empty());
        drop(s);
        fs::remove_dir_all(dir).unwrap();
    }
}
