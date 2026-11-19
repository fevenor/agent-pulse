// 详情请求不占用订阅线程；每次操作显式返回结果，不在断线时缓存人工修改。
use crate::{
    model::{Event, Session, SessionKey},
    settings::Settings,
};
use serde::Deserialize;
use serde_json::{Value, json};

#[derive(Clone, Debug, Default, Deserialize)]
pub struct TimelinePage {
    pub events: Vec<Event>,
    pub more: bool,
    pub newer: bool,
    pub cursor: i64,
    pub run_id: String,
}

use std::{sync::mpsc, time::Duration};

pub enum Request {
    History {
        key: SessionKey,
        before: i64,
        anchor: Option<String>,
        all: bool,
        generation: u64,
        checked_event: String,
    },
    Latest {
        key: SessionKey,
        after: i64,
        all: bool,
        generation: u64,
        checked_event: String,
        run_id: String,
    },
    Manage {
        session: Box<Session>,
        field: &'static str,
        value: Value,
    },
}
#[derive(Clone)]
pub enum Reply {
    History {
        key: SessionKey,
        before: i64,
        generation: u64,
        checked_event: String,
        result: Result<TimelinePage, String>,
    },
    Latest {
        key: SessionKey,
        generation: u64,
        checked_event: String,
        run_id: String,
        result: Result<TimelinePage, String>,
    },
    Managed {
        key: SessionKey,
        result: Result<(), String>,
    },
}
pub fn start(settings: Settings, output: async_channel::Sender<Reply>) -> mpsc::Sender<Request> {
    let (tx, rx) = mpsc::channel();
    std::thread::spawn(move || {
        let client: ureq::Agent = ureq::Agent::config_builder()
            .timeout_global(Some(Duration::from_secs(8)))
            .max_redirects(0)
            .build()
            .into();
        for request in rx {
            let relay = settings.relay();
            let endpoints = [
                (&settings.hub_url, &settings.token),
                (&relay.relay_url, &relay.relay_token),
            ];
            match request {
                Request::History {
                    key,
                    before,
                    anchor,
                    all,
                    generation,
                    checked_event,
                } => {
                    let mut result = Err("服务未连接，无法加载历史".into());
                    for (base, token) in endpoints.iter().filter(|(b, _)| !b.is_empty()) {
                        let mut url = url::Url::parse(&format!("{base}/api/v1/events")).unwrap();
                        url.query_pairs_mut()
                            .append_pair("source_id", &key.source_id)
                            .append_pair("agent", &key.agent)
                            .append_pair("session_id", &key.session_id)
                            .append_pair("view", if all { "all" } else { "important" })
                            .append_pair("limit", "30")
                            .append_pair("before", &before.to_string());
                        if let Some(id) = &anchor {
                            url.query_pairs_mut().append_pair("anchor", id);
                        }
                        result = client
                            .get(url.as_str())
                            .header("Authorization", format!("Bearer {token}"))
                            .call()
                            .map_err(|_| "历史暂不可达，请重试".into())
                            .and_then(|mut r| {
                                r.body_mut()
                                    .with_config()
                                    .limit(8 << 20)
                                    .read_json::<TimelinePage>()
                                    .map_err(|_| "历史格式无效".into())
                            });
                        if result.is_ok() {
                            break;
                        }
                    }
                    let _ = output.send_blocking(Reply::History {
                        key,
                        before,
                        generation,
                        checked_event,
                        result,
                    });
                }
                Request::Latest {
                    key,
                    after,
                    all,
                    generation,
                    checked_event,
                    run_id,
                } => {
                    let mut result = Err("新事件暂不可达，正在自动重试".into());
                    for (base, token) in endpoints.iter().filter(|(b, _)| !b.is_empty()) {
                        let mut url = url::Url::parse(&format!("{base}/api/v1/events")).unwrap();
                        url.query_pairs_mut()
                            .append_pair("source_id", &key.source_id)
                            .append_pair("agent", &key.agent)
                            .append_pair("session_id", &key.session_id)
                            .append_pair("view", if all { "all" } else { "important" })
                            .append_pair("limit", "30")
                            .append_pair("after", &after.to_string());
                        result = client
                            .get(url.as_str())
                            .header("Authorization", format!("Bearer {token}"))
                            .call()
                            .map_err(|_| "新事件暂不可达，正在自动重试".into())
                            .and_then(|mut r| {
                                r.body_mut()
                                    .with_config()
                                    .limit(8 << 20)
                                    .read_json::<TimelinePage>()
                                    .map_err(|_| "新事件格式无效".into())
                            });
                        if result.is_ok() {
                            break;
                        }
                    }
                    let _ = output.send_blocking(Reply::Latest {
                        key,
                        generation,
                        checked_event,
                        run_id,
                        result,
                    });
                }
                Request::Manage {
                    session,
                    field,
                    value,
                } => {
                    let key = session.key();
                    let mut body = json!({"source_id":key.source_id,"agent":key.agent,"session_id":key.session_id,"last_event_id":session.last_event_id,"management_version":session.management_version});
                    body[field] = value;
                    // 先选择可达服务，再只提交一次；超时后由用户刷新确认，避免重复人工修改。
                    let endpoint =
                        endpoints
                            .iter()
                            .filter(|(b, _)| !b.is_empty())
                            .find(|(base, token)| {
                                client
                                    .get(format!("{base}/api/v1/snapshot"))
                                    .header("Authorization", format!("Bearer {token}"))
                                    .call()
                                    .is_ok()
                            });
                    let result = if let Some((base, token)) = endpoint {
                        match client
                            .post(format!("{base}/api/v1/sessions/manage"))
                            .header("Authorization", format!("Bearer {token}"))
                            .send_json(body)
                        {
                            Ok(r) if r.status().as_u16() == 200 => Ok(()),
                            Err(ureq::Error::StatusCode(409)) => {
                                Err("会话已更新，请刷新后重试".into())
                            }
                            _ => Err("操作未确认，请刷新确认后再重试".into()),
                        }
                    } else {
                        Err("离线时不能修改会话，请连接服务后重试".into())
                    };
                    let _ = output.send_blocking(Reply::Managed { key, result });
                }
            }
        }
    });
    tx
}
