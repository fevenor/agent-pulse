// 只向配置的 Hub 请求；中继地址和凭据绑定该 Hub 缓存在便携数据目录。
use crate::settings::Settings;
use serde::{Deserialize, Serialize};
#[derive(Clone, Default, Serialize, Deserialize)]
pub struct Relay {
    pub relay_url: String,
    pub relay_token: String,
}
#[derive(Serialize, Deserialize)]
struct Cache {
    hub_url: String,
    relay: Relay,
}
impl Relay {
    pub fn valid(&self) -> bool {
        if self.relay_url.is_empty() {
            return self.relay_token.is_empty();
        }
        let Ok(url) = url::Url::parse(&self.relay_url) else {
            return false;
        };
        let local = url
            .host_str()
            .and_then(|s| s.parse::<std::net::IpAddr>().ok())
            .is_some_and(|ip| ip.is_loopback());
        (url.scheme() == "https" || (url.scheme() == "http" && local))
            && url.host_str().is_some()
            && url.username().is_empty()
            && url.password().is_none()
            && url.query().is_none()
            && url.fragment().is_none()
            && url.path() == "/"
            && !self.relay_token.is_empty()
            && !self.relay_token.contains(['\r', '\n'])
    }
}
pub fn cached(settings: &Settings) -> Relay {
    std::fs::read(settings.data_dir.join("relay.json"))
        .ok()
        .and_then(|raw| serde_json::from_slice::<Cache>(&raw).ok())
        .filter(|c| c.hub_url == settings.hub_url && c.relay.valid())
        .map(|c| c.relay)
        .unwrap_or_default()
}
pub fn refresh(settings: &Settings) {
    if !settings.relay_url.is_empty() || settings.hub_url.is_empty() {
        return;
    }
    let client: ureq::Agent = ureq::Agent::config_builder()
        .timeout_global(Some(std::time::Duration::from_secs(3)))
        .max_redirects(0)
        .build()
        .into();
    let mut request = client.get(format!("{}/api/v1/client-config", settings.hub_url));
    if !settings.token.is_empty() {
        request = request.header("Authorization", format!("Bearer {}", settings.token));
    }
    let Ok(mut response) = request.call() else {
        return;
    };
    let Ok(relay) = response
        .body_mut()
        .with_config()
        .limit(16384)
        .read_json::<Relay>()
    else {
        return;
    };
    if !relay.valid() {
        return;
    }
    let cache = Cache {
        hub_url: settings.hub_url.clone(),
        relay: relay.clone(),
    };
    if let Ok(raw) = serde_json::to_vec(&cache) {
        let path = settings.data_dir.join("relay.json");
        let tmp = path.with_extension("tmp");
        if std::fs::write(&tmp, raw)
            .and_then(|_| std::fs::rename(tmp, path))
            .is_err()
        {
            return;
        }
        *settings.discovered_relay.write().unwrap() = relay;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    #[test]
    fn authenticated_discovery_cache_and_manual_precedence() {
        let dir = std::env::temp_dir().join(format!("pulse-discovery-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            stream
                .set_read_timeout(Some(std::time::Duration::from_secs(3)))
                .unwrap();
            let mut request = Vec::new();
            while !request.windows(4).any(|w| w == b"\r\n\r\n") {
                let mut buf = [0; 1024];
                let count = stream.read(&mut buf).unwrap();
                assert!(count > 0);
                request.extend(&buf[..count]);
            }
            let text = String::from_utf8(request).unwrap().to_lowercase();
            assert!(text.contains("get /api/v1/client-config "));
            assert!(text.contains("authorization: bearer isolated-hub-token"));
            let body = r#"{"relay_url":"https://relay.example.invalid","relay_token":"isolated-relay-token"}"#;
            write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            )
            .unwrap();
        });
        let mut settings = Settings {
            hub_url: format!("http://{address}"),
            token: "isolated-hub-token".into(),
            data_dir: dir.clone(),
            ..Default::default()
        };
        refresh(&settings);
        server.join().unwrap();
        assert_eq!(settings.relay().relay_url, "https://relay.example.invalid");
        assert_eq!(cached(&settings).relay_token, "isolated-relay-token");
        refresh(&settings); // Hub 离线仍保留已验证缓存。
        assert_eq!(settings.relay().relay_token, "isolated-relay-token");
        settings.relay_url = "https://manual.example.invalid".into();
        settings.relay_token = "manual-test-token".into();
        assert_eq!(settings.relay().relay_url, "https://manual.example.invalid");
        settings.hub_url = "https://another.example.invalid".into();
        assert!(cached(&settings).relay_url.is_empty());
        for address in [
            "http://public.example.invalid",
            "https://user:pass@relay.example.invalid",
            "https://relay.example.invalid/path",
            "https://relay.example.invalid?token=x",
        ] {
            assert!(
                !Relay {
                    relay_url: address.into(),
                    relay_token: "test".into()
                }
                .valid()
            );
        }
        std::fs::remove_dir_all(dir).unwrap();
    }
}
