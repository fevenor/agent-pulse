use crate::model::{Device, Event, Session, SessionKey, Snapshot, SourceLabel};
use serde::Deserialize;
use std::collections::BTreeMap;

#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Delta {
    pub run_id: String,
    pub version: u64,
    pub from: u64,
    pub reset: Option<Snapshot>,
    pub sessions: Vec<Session>,
    pub sessions_removed: Vec<SessionKey>,
    pub unread: Vec<Event>,
    pub unread_removed: Vec<String>,
    pub sources: BTreeMap<String, SourceLabel>,
    pub sources_removed: Vec<String>,
    pub devices: Vec<Device>,
    pub devices_removed: Vec<String>,
}
impl Delta {
    pub fn apply(self, old: &Snapshot) -> Result<Snapshot, String> {
        if self.run_id.is_empty() || self.version == 0 {
            return Err("同步位置无效".into());
        }
        if let Some(reset) = self.reset {
            if reset.run_id != self.run_id || reset.version != self.version {
                return Err("同步基准不一致".into());
            }
            return Ok(reset);
        }
        if old.run_id != self.run_id || old.version != self.from || self.version != self.from + 1 {
            return Err("同步位置存在缺口，重新核对状态".into());
        }
        let mut s = old.clone();
        s.version = self.version;
        s.sessions
            .retain(|x| !self.sessions_removed.contains(&x.key()));
        for v in self.sessions {
            if let Some(old) = s.sessions.iter_mut().find(|s| s.key() == v.key()) {
                *old = v
            } else {
                s.sessions.push(v)
            }
        }
        s.unread
            .retain(|e| !self.unread_removed.contains(&e.event_id));
        for v in self.unread {
            if let Some(old) = s.unread.iter_mut().find(|e| e.event_id == v.event_id) {
                *old = v
            } else {
                s.unread.push(v)
            }
        }
        s.unread.sort_by_key(|e| e.position);
        for k in self.sources_removed {
            s.sources.remove(&k);
        }
        s.sources.extend(self.sources);
        s.devices.retain(|d| {
            !self
                .devices_removed
                .contains(&format!("{}:{}", d.role, d.id))
        });
        for v in self.devices {
            if let Some(old) = s
                .devices
                .iter_mut()
                .find(|d| d.id == v.id && d.role == v.role)
            {
                *old = v
            } else {
                s.devices.push(v)
            }
        }
        Ok(s)
    }
}

#[cfg(test)]
mod delta_tests {
    use super::*;
    #[test]
    fn missing_delta_requires_baseline_and_removal_is_explicit() {
        let before = Snapshot {
            run_id: "run".into(),
            version: 1,
            unread: vec![Event {
                event_id: "one".into(),
                kind: "turn.completed".into(),
                ..Default::default()
            }],
            ..Default::default()
        };
        let d = Delta {
            run_id: "run".into(),
            version: 2,
            from: 1,
            unread_removed: vec!["one".into()],
            ..Default::default()
        };
        assert!(d.apply(&before).unwrap().unread.is_empty());
        assert_eq!(before.unread.len(), 1);
        for d in [
            Delta {
                run_id: "run".into(),
                version: 3,
                from: 2,
                ..Default::default()
            },
            Delta {
                run_id: "other".into(),
                version: 2,
                from: 1,
                ..Default::default()
            },
        ] {
            assert!(d.apply(&before).is_err());
        }
    }
}
