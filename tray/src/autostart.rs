// 登录启动以启动文件夹中的快捷方式和 Windows 禁用状态为准，不写注册表。
use std::{
    fs,
    path::{Path, PathBuf},
};
use windows::{
    Win32::{
        Foundation::*,
        System::{Com::*, Registry::*},
        UI::{Shell::*, WindowsAndMessaging::SW_SHOWNORMAL},
    },
    core::{HSTRING, Interface, PCWSTR, w},
};

const SHORTCUT: &str = "AgentPulse.lnk";

#[derive(Clone, Debug, PartialEq)]
pub enum Status {
    Off,
    Enabled,
    Disabled,
    OtherLocation,
    Unknown(String),
    #[cfg(feature = "dev-demo")]
    Demo,
}
impl Status {
    pub fn label(&self) -> String {
        match self {
            Self::Off => "未启用".into(),
            Self::Enabled => "已启用：登录 Windows 后启动到托盘".into(),
            Self::Disabled => "快捷方式存在，但被 Windows 启动应用设置禁用".into(),
            Self::OtherLocation => "启动文件夹中留有其他位置或配置的快捷方式".into(),
            Self::Unknown(e) => format!("无法确认系统自启动状态：{e}"),
            #[cfg(feature = "dev-demo")]
            Self::Demo => "开发演示不登记或修改开机自动启动".into(),
        }
    }
}
pub struct Registration {
    executable: PathBuf,
    config: PathBuf,
    shortcut: PathBuf,
}
impl Registration {
    pub fn new(executable: &Path, config: &Path) -> Result<Self, String> {
        let _com = Com::initialize()?;
        let folder = unsafe { SHGetKnownFolderPath(&FOLDERID_Startup, KF_FLAG_DEFAULT, None) }
            .map_err(|_| "无法定位当前用户的启动文件夹")?;
        let path = unsafe { folder.to_string() };
        unsafe {
            CoTaskMemFree(Some(folder.0.cast()));
        }
        Ok(Self {
            executable: executable.into(),
            config: config.into(),
            shortcut: PathBuf::from(path.map_err(|_| "启动文件夹编码无效")?).join(SHORTCUT),
        })
    }
    pub fn status(&self) -> Status {
        self.read_status().unwrap_or_else(Status::Unknown)
    }
    fn read_status(&self) -> Result<Status, String> {
        match self.shortcut.try_exists() {
            Ok(false) => return Ok(Status::Off),
            Err(_) => return Err("无法读取启动文件夹".into()),
            _ => {}
        }
        let _com = Com::initialize()?;
        let link: IShellLinkW = unsafe { CoCreateInstance(&ShellLink, None, CLSCTX_INPROC_SERVER) }
            .map_err(|_| "无法读取快捷方式")?;
        let file: IPersistFile = link.cast().map_err(|_| "无法读取快捷方式")?;
        unsafe { file.Load(&path_string(&self.shortcut)?, STGM_READ) }
            .map_err(|_| "启动快捷方式已损坏或不可读取")?;
        let mut target = vec![0u16; 32768];
        let mut arguments = vec![0u16; 32768];
        unsafe {
            link.GetPath(&mut target, std::ptr::null_mut(), SLGP_RAWPATH.0 as u32)
                .and_then(|_| link.GetArguments(&mut arguments))
        }
        .map_err(|_| "无法读取快捷方式目标")?;
        if !wide_text(&target)?
            .eq_ignore_ascii_case(self.executable.to_str().ok_or("程序路径编码无效")?)
            || !wide_text(&arguments)?.eq_ignore_ascii_case(&self.arguments()?)
        {
            return Ok(Status::OtherLocation);
        }
        approval_status(read_approval()?.as_deref())
    }
    fn arguments(&self) -> Result<String, String> {
        if self
            .executable
            .parent()
            .map(|p| p.join("tray.yaml"))
            .as_deref()
            == Some(&self.config)
        {
            Ok("--autostart".into())
        } else {
            let value = self.config.to_str().ok_or("配置路径编码无效")?;
            if !self.config.is_absolute()
                || value.contains(['"', '\r', '\n', '\0'])
                || value.ends_with('\\')
            {
                return Err("自动启动需要有效的绝对配置路径".into());
            }
            Ok(format!("--autostart --config \"{value}\""))
        }
    }
    pub fn set_enabled(&self, enabled: bool) -> Result<(), String> {
        if !enabled {
            return match fs::remove_file(&self.shortcut) {
                Ok(()) => Ok(()),
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
                Err(_) => Err(
                    "无法移除启动快捷方式，可在 Windows 启动文件夹中手动删除 AgentPulse.lnk".into(),
                ),
            };
        }
        let _com = Com::initialize()?;
        let link: IShellLinkW = unsafe { CoCreateInstance(&ShellLink, None, CLSCTX_INPROC_SERVER) }
            .map_err(|_| "无法创建快捷方式")?;
        let directory = self.executable.parent().ok_or("无法定位程序目录")?;
        let executable = path_string(&self.executable)?;
        let arguments = HSTRING::from(self.arguments()?);
        let working = path_string(directory)?;
        unsafe {
            link.SetPath(&executable)
                .and_then(|_| link.SetArguments(&arguments))
                .and_then(|_| link.SetWorkingDirectory(&working))
                .and_then(|_| link.SetIconLocation(&executable, 0))
                .and_then(|_| link.SetDescription(w!("AgentPulse 登录后驻留托盘")))
        }
        .map_err(|_| "无法设置启动快捷方式")?;
        let file: IPersistFile = link.cast().map_err(|_| "无法保存快捷方式")?;
        let folder = self.shortcut.parent().ok_or("无法定位启动文件夹")?;
        fs::create_dir_all(folder).map_err(|_| "启动文件夹不可写")?;
        let temp = folder.join(format!("AgentPulse-{}.tmp", uuid::Uuid::new_v4()));
        let result = unsafe { file.Save(&path_string(&temp)?, true) }
            .map_err(|_| "无法保存启动快捷方式".to_string())
            .and_then(|_| {
                fs::rename(&temp, &self.shortcut).map_err(|_| "无法更新启动快捷方式".into())
            });
        let _ = fs::remove_file(&temp);
        result
    }
}
fn path_string(path: &Path) -> Result<HSTRING, String> {
    path.to_str()
        .map(HSTRING::from)
        .ok_or_else(|| "文件路径编码无效".into())
}
fn wide_text(value: &[u16]) -> Result<String, String> {
    String::from_utf16(&value[..value.iter().position(|v| *v == 0).unwrap_or(value.len())])
        .map_err(|_| "快捷方式编码无效".into())
}
struct Com(bool);
impl Com {
    fn initialize() -> Result<Self, String> {
        let result = unsafe { CoInitializeEx(None, COINIT_APARTMENTTHREADED) };
        if result == RPC_E_CHANGED_MODE {
            return Ok(Self(false));
        }
        result.ok().map_err(|_| "无法初始化 Windows 快捷方式服务")?;
        Ok(Self(true))
    }
}
impl Drop for Com {
    fn drop(&mut self) {
        if self.0 {
            unsafe {
                CoUninitialize();
            }
        }
    }
}
fn read_approval() -> Result<Option<Vec<u8>>, String> {
    let mut bytes = [0u8; 64];
    let mut length = bytes.len() as u32;
    let result = unsafe {
        RegGetValueW(
            HKEY_CURRENT_USER,
            w!(
                "Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\StartupApproved\\StartupFolder"
            ),
            w!("AgentPulse.lnk"),
            RRF_RT_REG_BINARY,
            None,
            Some(bytes.as_mut_ptr().cast()),
            Some(&mut length),
        )
    };
    if result == ERROR_FILE_NOT_FOUND || result == ERROR_PATH_NOT_FOUND {
        return Ok(None);
    }
    result
        .ok()
        .map_err(|_| "无法读取 Windows 对此快捷方式的禁用状态")?;
    Ok(Some(bytes[..length as usize].to_vec()))
}
fn approval_status(value: Option<&[u8]>) -> Result<Status, String> {
    // 未公开的 Windows 实现细节仅作保守识别；未知格式不能假报已启用。
    match value {
        None => Ok(Status::Enabled),
        Some(value) if value.len() == 12 => {
            match u32::from_le_bytes(value[..4].try_into().unwrap()) {
                0 | 2 if value[4..].iter().all(|b| *b == 0) => Ok(Status::Enabled),
                3 => Ok(Status::Disabled),
                _ => Err("Windows 返回尚未识别的启动批准状态".into()),
            }
        }
        _ => Err("Windows 启动批准数据格式无法识别".into()),
    }
}
pub fn open_system_settings() -> Result<(), String> {
    let result = unsafe {
        ShellExecuteW(
            None,
            w!("open"),
            w!("ms-settings:startupapps"),
            PCWSTR::null(),
            PCWSTR::null(),
            SW_SHOWNORMAL,
        )
    };
    if result.0 as isize <= 32 {
        return Err("无法打开 Windows 启动应用设置，请手动在系统设置中打开".into());
    }
    Ok(())
}
#[cfg(test)]
mod tests {
    use super::*;
    struct Directory(PathBuf);
    impl Drop for Directory {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }
    #[test]
    fn shortcut_detects_manual_removal_config_loss_and_moved_target() {
        // 真实生成 .lnk，但放在临时目录，不登记系统自启动。
        let root = Directory(
            std::env::temp_dir().join(format!("pulse-快捷方式 空格-{}", uuid::Uuid::new_v4())),
        );
        fs::create_dir_all(&root.0).unwrap();
        let exe = root.0.join("tray.exe");
        fs::write(&exe, b"test").unwrap();
        let config = root.0.join("custom.yaml");
        fs::write(&config, b"{}").unwrap();
        let mut registration = Registration {
            executable: exe,
            config: config.clone(),
            shortcut: root.0.join(SHORTCUT),
        };
        assert_eq!(registration.status(), Status::Off);
        registration.set_enabled(true).unwrap();
        let expected =
            approval_status(read_approval().unwrap().as_deref()).unwrap_or_else(Status::Unknown);
        assert_eq!(registration.status(), expected);
        fs::remove_file(config).unwrap();
        assert_eq!(registration.status(), expected);
        registration.executable = root.0.join("moved.exe");
        fs::write(&registration.executable, b"test").unwrap();
        assert_eq!(registration.status(), Status::OtherLocation);
        registration.set_enabled(true).unwrap();
        assert_eq!(registration.status(), expected);
        fs::remove_file(&registration.shortcut).unwrap();
        assert_eq!(registration.status(), Status::Off);
        registration.set_enabled(true).unwrap();
        registration.set_enabled(false).unwrap();
        assert_eq!(registration.status(), Status::Off);
        fs::write(&registration.shortcut, b"broken").unwrap();
        assert!(matches!(registration.status(), Status::Unknown(_)));
    }
    #[test]
    fn system_disabled_and_unknown_formats_are_not_enabled() {
        assert_eq!(approval_status(None).unwrap(), Status::Enabled);
        let mut value = [0u8; 12];
        value[0] = 2;
        assert_eq!(approval_status(Some(&value)).unwrap(), Status::Enabled);
        value[0] = 3;
        value[4] = 1;
        assert_eq!(approval_status(Some(&value)).unwrap(), Status::Disabled);
        value[0] = 99;
        assert!(approval_status(Some(&value)).is_err());
        assert!(approval_status(Some(&value[..2])).is_err());
    }
}
