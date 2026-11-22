use unicode_segmentation::UnicodeSegmentation;

// GPUI的自动省略会在反复测量时复用已经截短的字体运行段。
// 列表先按完整组合字符选出可显示文本，再交给普通文本布局；原始数据不变。
pub fn fit_single_line(text: &str, width: f32, mut measure: impl FnMut(&str) -> f32) -> String {
    let text = text.replace(['\r', '\n'], " ");
    if width <= 0.0 || !width.is_finite() {
        return String::new();
    }
    if measure(&text) <= width {
        return text;
    }
    let suffix = "…";
    if measure(suffix) > width {
        return String::new();
    }
    let boundaries: Vec<_> = text
        .grapheme_indices(true)
        .map(|(index, _)| index)
        .chain(std::iter::once(text.len()))
        .collect();
    let mut low = 0;
    let mut high = boundaries.len() - 1;
    while low < high {
        let middle = (low + high).div_ceil(2);
        let candidate = format!("{}{suffix}", &text[..boundaries[middle]]);
        if measure(&candidate) <= width {
            low = middle;
        } else {
            high = middle - 1;
        }
    }
    format!("{}{suffix}", &text[..boundaries[low]])
}

#[cfg(test)]
mod tests {
    use super::*;

    fn width(text: &str) -> f32 {
        text.graphemes(true).count() as f32
    }

    #[test]
    fn repeated_narrow_and_wide_measurements_keep_original_text() {
        let text = "  远程会话中文标题";
        for limit in [1.0, 0.0, 3.0, 100.0, 2.0, 100.0] {
            let displayed = fit_single_line(text, limit, width);
            assert!(width(&displayed) <= limit);
            if limit == 100.0 {
                assert_eq!(displayed, text);
            }
        }
    }

    #[test]
    fn emoji_and_combining_characters_are_not_split() {
        let text = "中文👨‍👩‍👧‍👦e\u{301}结束";
        assert_eq!(fit_single_line(text, 4.0, width), "中文👨‍👩‍👧‍👦…");
        assert_eq!(fit_single_line(text, 5.0, width), "中文👨‍👩‍👧‍👦e\u{301}…");
        assert_eq!(fit_single_line(text, 0.5, width), "");
    }

    #[test]
    fn newlines_only_change_display_and_invalid_widths_are_empty() {
        let text = "中文\n继续\r结束";
        assert_eq!(fit_single_line(text, 100.0, width), "中文 继续 结束");
        assert!(text.contains('\n'));
        for limit in [-1.0, f32::NAN, f32::INFINITY] {
            assert_eq!(fit_single_line(text, limit, width), "");
        }
    }
}
