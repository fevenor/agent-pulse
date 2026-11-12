use serde::{Deserialize, Serialize};
use std::{
    fs::{self, OpenOptions},
    io::Write,
    path::{Path, PathBuf},
};

#[derive(Clone, Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Settings {
    #[serde(skip)]
    pub preferences: std::sync::Arc<std::sync::RwLock<crate::preferences::Preferences>>,
    #[serde(skip)]
    pub discovered_relay: std::sync::Arc<std::sync::RwLock<crate::discovery::Relay>>,
    pub hub_url: String,
    pub host_name: String,
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
        settings.host_name = settings.host_name.trim().to_owned();
        if settings.host_name.is_empty() {
            settings.host_name = Self::default_host_name();
        }
        if settings.host_name.len() > 256 || settings.host_name.contains(['\0', '\r', '\n']) {
            return Err("客户端显示名最长 256 字节，且不能包含换行或空字符".into());
        }
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
        {
            let value = &mut settings.bridge_config;
            if !value.as_os_str().is_empty() && value.is_relative() {
                *value = base.join(&*value);
            }
            if settings.bridge_enabled && value.as_os_str().is_empty() {
                *value = settings.data_dir.join("bridge.yaml");
                Self::ensure_bridge_config(&*value, &settings.hub_url, &settings.token)?;
            } else if settings.bridge_enabled && !value.exists() {
                return Err("本地接入配置不存在，请检查 bridge_config 配置文件".into());
            }
        }
        settings.config_path = config;
        *settings.preferences.write().unwrap() =
            crate::preferences::Preferences::load(&settings.data_dir.join("hosts.json"))?;
        *settings.discovered_relay.write().unwrap() = crate::discovery::cached(&settings);
        Ok(settings)
    }
    fn default_host_name() -> String {
        ["COMPUTERNAME", "HOSTNAME"]
            .into_iter()
            .filter_map(|key| std::env::var(key).ok())
            .map(|value| value.trim().to_owned())
            .find(|value| !value.is_empty())
            .unwrap_or_else(|| "Windows主机".into())
    }

    pub fn bridge_config_path(&self) -> PathBuf {
        if self.bridge_config.as_os_str().is_empty() {
            self.data_dir.join("bridge.yaml")
        } else {
            self.bridge_config.clone()
        }
    }

    /// 为内置 Bridge 创建便携配置；已有文件一律保留，不覆盖用户配置。
    pub fn ensure_bridge_config(path: &Path, hub_url: &str, token: &str) -> Result<(), String> {
        if path.exists() {
            return Ok(());
        }
        if hub_url.is_empty() {
            return Err("请先在 tray.yaml 中填写 Hub 地址，再配置本机 Agent 接入".into());
        }
        let parent = path.parent().ok_or("无法定位本地接入配置目录")?;
        fs::create_dir_all(parent).map_err(|_| "无法创建本地接入配置目录")?;
        let source_id = format!("windows-{}", uuid::Uuid::new_v4());
        let host_name = Self::default_host_name();
        let config = BridgeBootstrap {
            listen: "127.0.0.1:18702",
            source_id,
            host_name: host_name.trim().to_string(),
            hub_url,
            token,
        };
        let raw = serde_yaml::to_string(&config).map_err(|_| "无法生成本地接入配置")?;
        let mut file = match OpenOptions::new().write(true).create_new(true).open(path) {
            Ok(file) => file,
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => return Ok(()),
            Err(_) => return Err("无法创建本地接入配置，请检查便携目录权限".into()),
        };
        if file
            .write_all(raw.as_bytes())
            .and_then(|_| file.sync_all())
            .is_err()
        {
            drop(file);
            let _ = fs::remove_file(path);
            return Err("无法保存本地接入配置，请检查便携目录权限".into());
        }
        Ok(())
    }

    /// 只修改 tray.yaml 中的 bridge_enabled，保留其余设置和行尾注释。
    pub fn set_bridge_enabled(path: &Path, enabled: bool) -> Result<(), String> {
        let raw = fs::read_to_string(path).map_err(|_| "无法读取便携配置")?;
        let newline = if raw.contains("\r\n") { "\r\n" } else { "\n" };
        let mut lines: Vec<String> = raw
            .split_terminator('\n')
            .map(|line| line.strip_suffix('\r').unwrap_or(line).to_string())
            .collect();
        let mut found = None;
        for (index, line) in lines.iter().enumerate() {
            if line.starts_with("bridge_enabled:") && found.replace(index).is_some() {
                return Err("tray.yaml 中 bridge_enabled 出现重复配置".into());
            }
        }
        let value = if enabled { "true" } else { "false" };
        if let Some(index) = found {
            let line = &lines[index];
            let comment = line.find('#').map(|at| line[at..].trim_start());
            lines[index] = match comment {
                Some(comment) if !comment.is_empty() => {
                    format!("bridge_enabled: {value}  {comment}")
                }
                _ => format!("bridge_enabled: {value}"),
            };
        } else {
            lines.push(format!("bridge_enabled: {value}"));
        }
        let mut updated = lines.join(newline);
        if raw.ends_with('\n') || !raw.is_empty() {
            updated.push_str(newline);
        }
        // 临时文件写完再替换，避免向导成功后写配置失败导致原配置被截断。
        let temporary = path.with_extension(format!("tmp-{}", uuid::Uuid::new_v4()));
        let result = (|| -> std::io::Result<()> {
            let mut file = OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(&temporary)?;
            file.write_all(updated.as_bytes())?;
            file.sync_all()?;
            drop(file);
            fs::rename(&temporary, path)
        })();
        if result.is_err() {
            let _ = fs::remove_file(&temporary);
            return Err("无法保存便携配置，请检查程序目录权限".into());
        }
        Ok(())
    }
    pub fn relay(&self) -> crate::discovery::Relay {
        if self.relay_url.is_empty() {
            self.discovered_relay.read().unwrap().clone()
        } else {
            crate::discovery::Relay {
                relay_url: self.relay_url.clone(),
                relay_token: self.relay_token.clone(),
            }
        }
    }
}

#[derive(Serialize)]
struct BridgeBootstrap<'a> {
    listen: &'static str,
    source_id: String,
    host_name: String,
    hub_url: &'a str,
    token: &'a str,
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
        assert_eq!(initial.host_name, Settings::default_host_name());
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
    #[test]
    fn bridge_config_is_stable_and_existing_file_is_preserved() {
        let root = std::env::temp_dir().join(format!("pulse-bridge-{}", uuid::Uuid::new_v4()));
        fs::create_dir_all(&root).unwrap();
        let path = root.join("data/bridge.yaml");
        Settings::ensure_bridge_config(&path, "http://hub.example", "secret").unwrap();
        let first = fs::read_to_string(&path).unwrap();
        Settings::ensure_bridge_config(&path, "http://other.example", "changed").unwrap();
        assert_eq!(fs::read_to_string(&path).unwrap(), first);
        assert!(first.contains("source_id: windows-"));
        assert!(first.contains("hub_url: http://hub.example"));
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn enabling_bridge_without_custom_config_creates_portable_config() {
        let root = std::env::temp_dir().join(format!("pulse-enable-{}", uuid::Uuid::new_v4()));
        fs::create_dir_all(&root).unwrap();
        let exe = root.join("tray.exe");
        fs::write(
            root.join("tray.yaml"),
            "hub_url: http://127.0.0.1:18700\nhost_name: 客厅提醒端\nbridge_enabled: true\n",
        )
        .unwrap();
        let settings = Settings::load_at(None, &exe).unwrap();
        assert_eq!(settings.bridge_config, root.join("data/bridge.yaml"));
        let config = fs::read_to_string(&settings.bridge_config).unwrap();
        assert!(config.contains("source_id: windows-"));
        assert!(config.contains("host_name:"));
        assert_eq!(settings.host_name, "客厅提醒端");
        let bridge: serde_yaml::Value = serde_yaml::from_str(&config).unwrap();
        assert_eq!(
            bridge["host_name"].as_str(),
            Some(Settings::default_host_name().as_str())
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn bridge_enabled_edit_preserves_unrelated_settings_and_comment() {
        let root = std::env::temp_dir().join(format!("pulse-setting-{}", uuid::Uuid::new_v4()));
        fs::create_dir_all(&root).unwrap();
        let path = root.join("tray.yaml");
        fs::write(
            &path,
            "hub_url: http://127.0.0.1:18700\ntoken: secret\nbridge_enabled: false # 本机接入\n",
        )
        .unwrap();
        Settings::set_bridge_enabled(&path, true).unwrap();
        let updated = fs::read_to_string(&path).unwrap();
        assert!(updated.contains("hub_url: http://127.0.0.1:18700"));
        assert!(updated.contains("token: secret"));
        assert!(updated.contains("bridge_enabled: true  # 本机接入"));
        fs::remove_dir_all(root).unwrap();
    }
}
