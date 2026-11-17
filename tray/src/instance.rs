// Local 命名空间按 Windows 交互登录会话隔离，不依赖配置或安装路径。
use windows::{
    Win32::{Foundation::*, System::Threading::*},
    core::w,
};

pub struct Instance {
    mutex: HANDLE,
    activation: HANDLE,
}
impl Instance {
    pub fn acquire(activate_existing: bool) -> Result<Option<Self>, String> {
        unsafe {
            // 先持有激活事件，避免同时启动时次实例的信号随最后一个事件句柄关闭而丢失。
            let activation = CreateEventW(None, false, false, activation_name())
                .map_err(|_| "无法创建应用激活信号")?;
            let mutex = match CreateMutexW(None, false, instance_name()) {
                Ok(mutex) => mutex,
                Err(_) => {
                    let _ = CloseHandle(activation);
                    return Err("无法创建应用实例锁".into());
                }
            };
            let exists = GetLastError() == ERROR_ALREADY_EXISTS;
            if exists {
                if activate_existing {
                    let _ = SetEvent(activation);
                }
                let _ = CloseHandle(activation);
                let _ = CloseHandle(mutex);
                return Ok(None);
            }
            Ok(Some(Self { mutex, activation }))
        }
    }
    pub fn activation_requested(&self) -> bool {
        unsafe { WaitForSingleObject(self.activation, 0) == WAIT_OBJECT_0 }
    }
}
impl Drop for Instance {
    fn drop(&mut self) {
        unsafe {
            let _ = CloseHandle(self.activation);
            let _ = CloseHandle(self.mutex);
        }
    }
}

// 演示构建使用独立实例锁，不激活或阻断正在运行的正式客户端。
fn activation_name() -> windows::core::PCWSTR {
    #[cfg(feature = "dev-demo")]
    {
        w!("Local\\AgentPulse.Demo.Activate")
    }
    #[cfg(not(feature = "dev-demo"))]
    {
        w!("Local\\AgentPulse.Desktop.Activate")
    }
}
fn instance_name() -> windows::core::PCWSTR {
    #[cfg(feature = "dev-demo")]
    {
        w!("Local\\AgentPulse.Demo.Instance")
    }
    #[cfg(not(feature = "dev-demo"))]
    {
        w!("Local\\AgentPulse.Desktop.Instance")
    }
}
