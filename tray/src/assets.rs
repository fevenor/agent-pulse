use std::borrow::Cow;
pub struct Assets;
impl gpui::AssetSource for Assets {
    fn load(&self, path: &str) -> gpui::Result<Option<Cow<'static, [u8]>>> {
        Ok(match path {
            "pulse.svg" => Some(Cow::Borrowed(
                include_bytes!("../assets/pulse.svg").as_slice(),
            )),
            _ => None,
        })
    }
    fn list(&self, _: &str) -> gpui::Result<Vec<gpui::SharedString>> {
        Ok(vec!["pulse.svg".into()])
    }
}
