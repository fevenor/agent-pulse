package notify

import (
	"bytes"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"strings"
)

// PlainText只改变单个渠道的临时正文，不改写Hub中的事件或其他渠道。
func PlainText(source string) string {
	raw := []byte(source)
	root := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(raw))
	var out bytes.Buffer
	newline := func() {
		if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
			out.WriteByte('\n')
		}
	}
	_ = ast.Walk(root, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			switch x := n.(type) {
			case *ast.Link:
				if string(x.Destination) != string(x.Text(raw)) {
					out.WriteString("（" + string(x.Destination) + "）")
				}
			case *ast.Paragraph, *ast.Heading, *ast.ListItem, *ast.Blockquote, *ast.CodeBlock, *ast.FencedCodeBlock, *extast.TableRow, *extast.TableHeader:
				newline()
			case *extast.TableCell:
				if n.NextSibling() != nil {
					out.WriteString(" | ")
				}
			}
			return ast.WalkContinue, nil
		}
		switch x := n.(type) {
		case *ast.Text:
			out.Write(x.Segment.Value(raw))
			if x.SoftLineBreak() || x.HardLineBreak() {
				out.WriteByte('\n')
			}
		case *ast.String:
			out.Write(x.Value)
		case *ast.AutoLink:
			out.Write(x.URL(raw))
			return ast.WalkSkipChildren, nil
		case *ast.Image:
			out.WriteString("[图片：" + string(x.Text(raw)) + "]")
			return ast.WalkSkipChildren, nil
		case *ast.ListItem:
			newline()
			out.WriteString("- ")
		case *extast.TaskCheckBox:
			if x.IsChecked {
				out.WriteString("[已完成] ")
			} else {
				out.WriteString("[未完成] ")
			}
		case *ast.CodeBlock:
			newline()
			for i := 0; i < x.Lines().Len(); i++ {
				line := x.Lines().At(i)
				out.Write(line.Value(raw))
			}
			return ast.WalkSkipChildren, nil
		case *ast.FencedCodeBlock:
			newline()
			for i := 0; i < x.Lines().Len(); i++ {
				line := x.Lines().At(i)
				out.Write(line.Value(raw))
			}
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			for i := 0; i < x.Segments.Len(); i++ {
				segment := x.Segments.At(i)
				out.Write(segment.Value(raw))
			}
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock:
			for i := 0; i < x.Lines().Len(); i++ {
				line := x.Lines().At(i)
				out.Write(line.Value(raw))
			}
			if x.HasClosure() {
				out.Write(x.ClosureLine.Value(raw))
			}
			return ast.WalkSkipChildren, nil
		case *ast.ThematicBreak:
			newline()
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(out.String())
}
