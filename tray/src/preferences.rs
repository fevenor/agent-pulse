// 每个 Windows 客户端独立选择来源；保存成功后才替换运行中的策略。
use crate::model::Snapshot;
use serde::{Deserialize, Serialize};
use std::{collections::BTreeSet, fs, path::Path};

#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Preferences {
    pub muted_sources: BTreeSet<String>,
    pub awake_sources: BTreeSet<String>,
}
impl Preferences {
    pub fn load(path: &Path) -> Result<Self, String> {
        match fs::read(path) {
            Ok(raw) => {
                serde_json::from_slice(&raw).map_err(|_| "主机偏好文件损坏，请恢复备份".into())
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Self::default()),
            Err(_) => Err("无法读取主机偏好".into()),
        }
    }
    pub fn save(&self, path: &Path) -> Result<(), String> {
        let tmp = path.with_extension("tmp");
        let raw = serde_json::to_vec(self).map_err(|_| "无法编码主机偏好")?;
        fs::write(&tmp, raw)
            .and_then(|_| fs::rename(&tmp, path))
            .map_err(|_| "无法保存主机偏好".into())
    }
    pub fn running(&self, snapshot: &Snapshot, online: bool) -> usize {
        if !online {
            return 0;
        }
        snapshot
            .sessions
            .iter()
            .filter(|s| {
                !snapshot.source_archived(&s.source_id)
                    && self.awake_sources.contains(&s.source_id)
                    && s.effective_state() == "running"
                    && snapshot
                        .devices
                        .iter()
                        .any(|d| d.role == "bridge" && d.id == s.source_id)
            })
            .count()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{Device, Session};
    #[test]
    fn mute_preserves_unread_and_requires_delivery_confirmation() {
        use crate::model::{Event, State};
        let mut state = State::default();
        state.snapshot.unread = vec![
            Event {
                event_id: "native-event".into(),
                source_id: "native".into(),
                session_id: "a".into(),
                valid: true,
                ..Default::default()
            },
            Event {
                event_id: "wsl-event".into(),
                source_id: "wsl".into(),
                session_id: "a".into(),
                valid: true,
                ..Default::default()
            },
        ];
        let muted = BTreeSet::from(["native".into()]);
        assert_eq!(state.notifications_for(&muted).len(), 1);
        assert_eq!(state.unread().len(), 2);
        assert!(state.notified.is_empty());
        state.notified.insert("wsl-event".into());
        assert!(state.notifications_for(&muted).is_empty());
        assert_eq!(state.unread().len(), 2);
    }
    #[test]
    fn awake_only_for_bound_online_running_sources() {
        let prefs = Preferences {
            awake_sources: BTreeSet::from(["wsl".into(), "remote".into()]),
            ..Default::default()
        };
        let mut snapshot = Snapshot {
            sessions: vec![Session {
                source_id: "wsl".into(),
                state: "running".into(),
                ..Default::default()
            }],
            devices: vec![Device {
                id: "wsl".into(),
                role: "bridge".into(),
                ..Default::default()
            }],
            ..Default::default()
        };
        assert_eq!(prefs.running(&snapshot, true), 1);
        assert_eq!(prefs.running(&snapshot, false), 0);
        for state in [
            "waiting",
            "idle",
            "failed",
            "interrupted",
            "closed",
            "unknown",
        ] {
            snapshot.sessions[0].state = state.into();
            assert_eq!(prefs.running(&snapshot, true), 0);
        }
        snapshot.sessions[0].state = "running".into();
        snapshot.sessions[0].override_state = "closed".into();
        assert_eq!(prefs.running(&snapshot, true), 0);
        snapshot.sessions[0].override_state.clear();
        snapshot.devices.clear();
        assert_eq!(prefs.running(&snapshot, true), 0);
    }
    #[test]
    fn multiple_hosts_hold_until_all_bound_tasks_finish() {
        let prefs = Preferences {
            awake_sources: BTreeSet::from(["wsl".into(), "remote".into()]),
            ..Default::default()
        };
        let mut snapshot = Snapshot::default();
        for source in ["wsl", "remote", "unbound"] {
            snapshot.devices.push(Device {
                id: source.into(),
                role: "bridge".into(),
                ..Default::default()
            });
            snapshot.sessions.push(Session {
                source_id: source.into(),
                state: "running".into(),
                ..Default::default()
            });
        }
        assert_eq!(prefs.running(&snapshot, true), 2);
        snapshot.sources.insert(
            "wsl".into(),
            crate::model::SourceLabel {
                name: "WSL".into(),
                environment: String::new(),
                archived: true,
            },
        );
        assert_eq!(prefs.running(&snapshot, true), 1);
        snapshot.sessions[0].state = "idle".into();
        assert_eq!(prefs.running(&snapshot, true), 1);
        snapshot.sessions[1].state = "idle".into();
        assert_eq!(prefs.running(&snapshot, true), 0);
    }
}
