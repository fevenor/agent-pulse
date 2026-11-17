use markdown::{ParseOptions, mdast::Node};

// 外部图片不自动加载；原始HTML只显示为文本；链接仅允许HTTP(S)。正文原文另行保留。
pub fn safe_markdown(raw: &str) -> Option<String> {
    let tree = markdown::to_mdast(raw, &ParseOptions::gfm()).ok()?;
    let mut edits = Vec::new();
    visit(&tree, &mut edits);
    let mut result = raw.to_owned();
    edits.sort_by_key(|edit| edit.0);
    for (start, end, value) in edits.into_iter().rev() {
        if !result.is_char_boundary(start) || !result.is_char_boundary(end) {
            return None;
        }
        result.replace_range(start..end, &value);
    }
    Some(result)
}
fn literal(value: &str) -> String {
    value
        .chars()
        .flat_map(|c| {
            if "\\`*_{}[]<>()#+-.!|".contains(c) {
                vec!['\\', c]
            } else {
                vec![c]
            }
        })
        .collect()
}
fn visit(node: &Node, edits: &mut Vec<(usize, usize, String)>) {
    let replacement = match node {
        Node::Image(image) => Some(literal(&format!("图片：{}", image.alt))),
        Node::ImageReference(image) => Some(literal(&format!("图片：{}", image.alt))),
        Node::Html(html) => Some(literal(&html.value)),
        Node::Link(link) if !safe_url(&link.url) => Some(literal(&node.to_string())),
        Node::Definition(definition) if !safe_url(&definition.url) => Some(String::new()),
        _ => None,
    };
    if let Some(value) = replacement {
        if let Some(pos) = node.position() {
            edits.push((pos.start.offset, pos.end.offset, value));
        }
        return;
    }
    if let Some(children) = node.children() {
        for child in children {
            visit(child, edits);
        }
    }
}
fn safe_url(value: &str) -> bool {
    url::Url::parse(value).is_ok_and(|url| matches!(url.scheme(), "http" | "https"))
}

pub fn custom_minutes(value: &str) -> Result<u64, &'static str> {
    let value = value.trim();
    if value.is_empty() || !value.bytes().all(|c| c.is_ascii_digit()) {
        return Err("请输入1至1440之间的整数分钟");
    }
    match value.parse::<u64>() {
        Ok(n @ 1..=1440) => Ok(n),
        _ => Err("请输入1至1440之间的整数分钟"),
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn markdown_keeps_code_and_blocks_automatic_content() {
        let raw = "# 标题\n**内容** ![图](https://example.invalid/a.png) [危险](file:///private)\n\n`![代码](x)` <img src='x'>";
        let safe = safe_markdown(raw).unwrap();
        assert!(safe.contains("**内容**"));
        assert!(safe.contains("`![代码](x)`"));
        assert!(!safe.contains("https://example.invalid/a.png"));
        assert!(!safe.contains("file:///private"));
    }
    #[test]
    fn custom_minutes_validate_only_on_commit() {
        for text in ["", "-1", "1.5", "1441", "abc"] {
            assert!(custom_minutes(text).is_err());
        }
        assert_eq!(custom_minutes(" 25 "), Ok(25));
    }
}
