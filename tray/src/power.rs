// 请求只属于当前 UI 线程，不更改电源计划；退出自动释放，不强制屏幕常亮。
use windows::Win32::System::Power::{ES_CONTINUOUS, ES_SYSTEM_REQUIRED, SetThreadExecutionState};
#[derive(Default)]
pub struct WakeGuard {
    active: bool,
}
impl WakeGuard {
    pub fn update(&mut self, required: bool) -> Result<(), String> {
        if self.active == required {
            return Ok(());
        }
        let flags = if required {
            ES_CONTINUOUS | ES_SYSTEM_REQUIRED
        } else {
            ES_CONTINUOUS
        };
        if unsafe { SetThreadExecutionState(flags) }.0 == 0 {
            return Err("保持唤醒请求失败".into());
        }
        self.active = required;
        Ok(())
    }
}
impl Drop for WakeGuard {
    fn drop(&mut self) {
        let _ = self.update(false);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn native_request_can_be_released_on_same_thread() {
        let mut guard = WakeGuard::default();
        guard.update(true).unwrap();
        let previous = unsafe { SetThreadExecutionState(ES_CONTINUOUS | ES_SYSTEM_REQUIRED) };
        assert_ne!(previous.0 & ES_SYSTEM_REQUIRED.0, 0);
        drop(guard);
        let previous = unsafe { SetThreadExecutionState(ES_CONTINUOUS) };
        assert_eq!(previous.0 & ES_SYSTEM_REQUIRED.0, 0);
    }
}
