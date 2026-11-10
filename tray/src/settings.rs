use serde::Deserialize;
use std::{
    fs::{self, OpenOptions},
    io::Write,
    path::{Path, PathBuf},
};

#[derive(Clone, Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Settings {
    pub hub_url: String,
    pub token: String,
    pub relay_url: String,
    pub relay_token: String,
    pub state_file: PathBuf,
    pub bridge_enabled: bool,
    pub bridge_config: PathBuf,
    #[serde(skip)]
    pub config_path: PathBuf,
    #[serde(skip)]
    pub data_dir: PathBuf,
}
impl Settings {
    pub fn load(path: Option<&Path>) -> Result<Self, String> {
        Self::load_at(
            path,
            &std::env::current_exe().map_err(|_| "无法定位程序目录")?,
        )
    }
    fn load_at(path: Option<&Path>, executable: &Path) -> Result<Self, String> {
        let config = match path {
            Some(path) => std::path::absolute(path).map_err(|_| "无法定位配置文件")?,
            None => executable
                .parent()
                .ok_or("无法定位程序目录")?
                .join("tray.yaml"),
        };
        if path.is_none() && !config.exists() {
            match OpenOptions::new().write(true).create_new(true).open(&config) {
                Ok(mut file) => file.write_all("# AgentPulse 便携配置，修改后重启生效。\nhub_url: \"\"\nbridge_enabled: false\n".as_bytes())
                    .and_then(|_| file.sync_all()).map_err(|_| "无法保存便携配置，请将程序放在可写目录")?,
                Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => {},
                Err(_) => return Err("无法创建便携配置，请将程序放在可写目录".into()),
            }
        }
        let mut settings: Self =
            serde_yaml::from_slice(&fs::read(&config).map_err(|_| "无法读取客户端配置")?)
                .map_err(|_| "客户端配置格式无效或包含未知配置项")?;
        let base = config.parent().ok_or("无法定位配置目录")?;
        settings.data_dir = base.join("data");
        fs::create_dir_all(&settings.data_dir)
            .map_err(|_| "无法创建便携数据目录，请使用可写目录")?;
        // 不静默回退到用户目录，保证整个目录可以一起移动和备份。
        let probe = settings
            .data_dir
            .join(format!(".write-check-{}", uuid::Uuid::new_v4()));
        let file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&probe)
            .map_err(|_| "便携数据目录不可写，请使用可写目录")?;
        drop(file);
        fs::remove_file(probe).map_err(|_| "便携数据目录不可写，请使用可写目录")?;
        for token in [&mut settings.token, &mut settings.relay_token] {
            if token.starts_with("${env:") && token.ends_with('}') {
                *token = std::env::var(&token[6..token.len() - 1])
                    .map_err(|_| "配置引用的令牌环境变量未设置")?;
            }
            if token.contains(['\r', '\n']) {
                return Err("令牌包含无效字符".into());
            }
        }
        for (value, relay) in [
            (&mut settings.hub_url, false),
            (&mut settings.relay_url, true),
        ] {
            if value.is_empty() {
                continue;
            }
            let parsed = url::Url::parse(value).map_err(|_| "服务地址格式无效")?;
            if !matches!(parsed.scheme(), "http" | "https")
                || parsed.host_str().is_none()
                || !parsed.username().is_empty()
                || parsed.password().is_some()
                || parsed.query().is_some()
                || parsed.fragment().is_some()
                || parsed.path() != "/"
            {
                return Err("服务地址只允许 HTTP(S) 根地址，不含账号、路径或查询参数".into());
            }
            let local = parsed
                .host_str()
                .and_then(|h| h.parse::<std::net::IpAddr>().ok())
                .is_some_and(|ip| ip.is_loopback());
            if relay && parsed.scheme() != "https" && !local {
                return Err("非回环中继必须使用 HTTPS".into());
            }
            *value = value.trim_end_matches('/').into();
        }
        if !settings.relay_url.is_empty() && settings.relay_token.is_empty() {
            return Err("中继令牌未设置".into());
        }
        if settings.state_file.as_os_str().is_empty() {
            settings.state_file = settings.data_dir.join("state.json");
        } else if settings.state_file.is_relative() {
            settings.state_file = base.join(&settings.state_file);
        }
        if settings.bridge_enabled && settings.bridge_config.as_os_str().is_empty() {
            return Err("启用本地接入需要 bridge_config 配置文件".into());
        }
        {
            let value = &mut settings.bridge_config;
            if !value.as_os_str().is_empty() && value.is_relative() {
                *value = base.join(&*value);
            }
        }
        settings.config_path = config;
        Ok(settings)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn portable_defaults_follow_executable_and_preserve_config() {
        let root = std::env::temp_dir().join(format!("pulse-便携 空格-{}", uuid::Uuid::new_v4()));
        fs::create_dir_all(&root).unwrap();
        let exe = root.join("tray.exe");
        let initial = Settings::load_at(None, &exe).unwrap();
        assert_eq!(initial.state_file, root.join("data/state.json"));
        assert_eq!(initial.config_path, root.join("tray.yaml"));
        fs::write(
            &initial.config_path,
            "hub_url: http://127.0.0.1:12345\nstate_file: data/custom.json\n",
        )
        .unwrap();
        let loaded = Settings::load_at(None, &exe).unwrap();
        assert_eq!(loaded.hub_url, "http://127.0.0.1:12345");
        assert_eq!(loaded.state_file, root.join("data/custom.json"));
        let moved = root.with_extension("moved");
        fs::rename(&root, &moved).unwrap();
        let loaded = Settings::load_at(None, &moved.join("tray.exe")).unwrap();
        assert_eq!(loaded.state_file, moved.join("data/custom.json"));
        assert!(Settings::load_at(Some(&moved.join("missing.yaml")), &exe).is_err());
        fs::remove_dir_all(moved).unwrap();
    }
}
