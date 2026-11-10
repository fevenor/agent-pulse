//go:build !windows

package adapters

import (
	"context"
	"os/exec"
	"strings"
)

func hostCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, path, args...)
}

func quote(s string) string                       { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func hookCommand(prefix, event string) string     { return prefix + quote(event) }
func ownsHookCommand(command, prefix string) bool { return strings.HasPrefix(command, prefix) }
