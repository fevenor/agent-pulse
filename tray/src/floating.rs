use crate::platform;
use serde::{Deserialize, Serialize};
use std::{
    path::PathBuf,
    time::{Duration, Instant},
};
use windows::Win32::{
    Foundation::{HWND, POINT, RECT},
    Graphics::Gdi::*,
    UI::{Input::KeyboardAndMouse::*, WindowsAndMessaging::*},
};

#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Preferences {
    pub visible: bool,
    pub x: Option<i32>,
    pub y: Option<i32>,
    pub edge: u8,
}
pub struct Floating {
    pub prefs: Preferences,
    path: Option<PathBuf>,
    hwnd: HWND,
    scale: f32,
    drag: Option<(POINT, i32, i32, bool)>,
    hovered: Option<Instant>,
    left_at: Option<Instant>,
    fraction: f32,
    last_tick: Instant,
    last_rect: Option<(i32, i32, i32, i32, [i32; 4])>,
}
impl Floating {
    pub fn new(hwnd: HWND, scale: f32, path: Option<PathBuf>) -> Self {
        let prefs = path
            .as_ref()
            .and_then(|p| std::fs::read(p).ok())
            .and_then(|raw| serde_json::from_slice(&raw).ok())
            .unwrap_or_default();
        let mut s = Self {
            prefs,
            path,
            hwnd,
            scale,
            drag: None,
            hovered: None,
            left_at: None,
            fraction: 0.,
            last_tick: Instant::now(),
            last_rect: None,
        };
        if s.prefs.x.is_none() {
            s.reset();
        }
        s.place();
        s.show();
        s
    }
    fn save(&self) {
        if let Some(p) = &self.path {
            if let Some(dir) = p.parent() {
                let _ = std::fs::create_dir_all(dir);
            }
            if let Ok(raw) = serde_json::to_vec(&self.prefs) {
                let tmp = p.with_extension("tmp");
                if std::fs::write(&tmp, raw).is_ok() {
                    let _ = std::fs::rename(tmp, p);
                }
            }
        }
    }
    fn show(&self) {
        unsafe {
            let _ = ShowWindow(
                self.hwnd,
                if self.prefs.visible {
                    SW_SHOWNOACTIVATE
                } else {
                    SW_HIDE
                },
            );
        }
        if self.prefs.visible {
            platform::queue_layout(self.hwnd);
        }
    }
    pub fn toggle(&mut self) {
        self.prefs.visible = !self.prefs.visible;
        self.fraction = 0.;
        self.place();
        self.show();
        self.save();
    }
    pub fn reset(&mut self) {
        let r = platform::work_area(POINT { x: 0, y: 0 });
        self.prefs.x = Some(r.right - (64. * self.scale) as i32);
        self.prefs.y = Some(r.bottom - (64. * self.scale) as i32);
        self.prefs.edge = 0;
        self.fraction = 0.;
        self.place();
        self.save();
    }
    pub fn begin_drag(&mut self, p: POINT) {
        self.fraction = 0.;
        self.place();
        self.drag = Some((
            p,
            self.prefs.x.unwrap_or(0),
            self.prefs.y.unwrap_or(0),
            false,
        ));
    }
    fn place(&mut self) {
        let size = (44. * self.scale) as i32;
        let mut x = self.prefs.x.unwrap_or(0);
        let mut y = self.prefs.y.unwrap_or(0);
        let work = platform::work_area(POINT {
            x: x + size / 2,
            y: y + size / 2,
        });
        x = x.clamp(work.left, (work.right - size).max(work.left));
        y = y.clamp(work.top, (work.bottom - size).max(work.top));
        match self.prefs.edge {
            1 => x = work.left,
            2 => x = work.right - size,
            3 => y = work.top,
            4 => y = work.bottom - size,
            _ => {}
        }
        self.prefs.x = Some(x);
        self.prefs.y = Some(y);
        let shift = (36. * self.scale * self.fraction) as i32;
        match self.prefs.edge {
            1 => x -= shift,
            2 => x += shift,
            3 => y -= shift,
            4 => y += shift,
            _ => {}
        }
        let placement = (
            x,
            y,
            size,
            shift,
            [work.left, work.top, work.right, work.bottom],
        );
        if self.last_rect == Some(placement) {
            return;
        }
        unsafe {
            let _ = SetWindowPos(
                self.hwnd,
                Some(HWND_TOPMOST),
                x,
                y,
                size,
                size,
                SWP_NOACTIVATE,
            );
            // 仅屏幕工作区内的部分可见且可命中，避免在多屏接缝露到另一屏。
            let region = CreateRectRgn(
                (work.left - x).max(0),
                (work.top - y).max(0),
                (work.right - x).min(size),
                (work.bottom - y).min(size),
            );
            if SetWindowRgn(self.hwnd, Some(region), true) == 0 {
                let _ = DeleteObject(region.into());
            }
        }
        if self.last_rect.is_none() {
            platform::queue_layout(self.hwnd);
        }
        self.last_rect = Some(placement);
    }
    pub fn interval_ms(&self) -> u64 {
        if self.drag.is_some() || (self.fraction > 0. && self.fraction < 1.) {
            16
        } else if self.prefs.visible {
            50
        } else {
            200
        }
    }
    pub fn tick(&mut self, list: HWND) -> bool {
        let now = Instant::now();
        let dt = now.duration_since(self.last_tick).as_secs_f32();
        self.last_tick = now;
        if !self.prefs.visible {
            return false;
        }
        let dpi = unsafe { windows::Win32::UI::HiDpi::GetDpiForWindow(self.hwnd) };
        if dpi > 0 {
            self.scale = dpi as f32 / 96.;
        }
        let p = platform::cursor();
        if let Some((start, x, y, mut moved)) = self.drag {
            moved |= (p.x - start.x).abs() + (p.y - start.y).abs() > (5. * self.scale) as i32;
            // 快速拖动可能在两次轮询间完成；释放时同样应用最终位移。
            if moved {
                self.prefs.edge = 0;
                self.prefs.x = Some(x + p.x - start.x);
                self.prefs.y = Some(y + p.y - start.y);
                self.place();
            }
            if unsafe { GetAsyncKeyState(VK_LBUTTON.0 as i32) } < 0 {
                self.drag = Some((start, x, y, moved));
                return false;
            }
            unsafe {
                let _ = ReleaseCapture();
            }
            self.drag = None;
            if moved {
                let r = platform::rect(self.hwnd);
                let work = platform::work_area(p);
                let distances = [
                    (1, (r.left - work.left).abs()),
                    (2, (work.right - r.right).abs()),
                    (3, (r.top - work.top).abs()),
                    (4, (work.bottom - r.bottom).abs()),
                ];
                if let Some((edge, distance)) = distances.into_iter().min_by_key(|(_, d)| *d)
                    && distance <= (16. * self.scale) as i32
                {
                    self.prefs.edge = edge;
                }
                self.left_at = Some(now);
                self.place();
                self.save();
                return false;
            }
            return true;
        }
        let area = platform::work_area(p);
        let r = platform::rect(self.hwnd);
        let inside = contains(r, p) && contains(area, p);
        let list_open = platform::visible(list);
        let target = if inside || list_open {
            self.left_at = None;
            let since = self.hovered.get_or_insert(now);
            if list_open || now.duration_since(*since) >= Duration::from_millis(150) {
                0.
            } else {
                self.fraction
            }
        } else {
            self.hovered = None;
            let since = self.left_at.get_or_insert(now);
            if self.prefs.edge != 0 && now.duration_since(*since) >= Duration::from_millis(650) {
                1.
            } else {
                self.fraction
            }
        };
        let mut animations = windows::core::BOOL(1);
        unsafe {
            let _ = SystemParametersInfoW(
                SPI_GETCLIENTAREAANIMATION,
                0,
                Some((&mut animations as *mut windows::core::BOOL).cast()),
                SYSTEM_PARAMETERS_INFO_UPDATE_FLAGS(0),
            );
        }
        let step = if animations.as_bool() { dt / 0.18 } else { 1. };
        self.fraction = if target > self.fraction {
            (self.fraction + step).min(target)
        } else {
            (self.fraction - step).max(target)
        };
        self.place();
        false
    }
}
fn contains(r: RECT, p: POINT) -> bool {
    p.x >= r.left && p.x < r.right && p.y >= r.top && p.y < r.bottom
}
