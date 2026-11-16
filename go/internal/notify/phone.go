package notify

import (
	"agent-pulse/protocol"
	"strings"
)

// 手机只格式化一次；返回临时副本，不改写Hub持久化事件及其他客户端正文。
func PhoneEvent(e protocol.Event, sessionTitle, hostName, environment string) protocol.Event {
	sessionTitle = strings.TrimSpace(sessionTitle)
	if sessionTitle == "" {
		sessionTitle = strings.TrimSpace(e.SessionTitle)
	}
	if sessionTitle == "" {
		sessionTitle = "未命名会话"
	}
	hostName = strings.TrimSpace(hostName)
	if hostName == "" {
		hostName = "未知主机"
	}
	if environment = strings.TrimSpace(environment); environment != "" {
		hostName += " / " + environment
	}
	kind := map[string]string{"turn.completed": "本轮完成", "task.completed": "任务完成", "input.required": "等待输入", "approval.required": "等待批准", "run.failed": "任务失败", "run.interrupted": "任务中断"}[e.Kind]
	if kind == "" {
		kind = "会话状态更新"
	}
	if e.Agent != "" {
		kind += "（" + e.Agent + "）"
	}
	body := strings.TrimSpace(e.Body)
	if body == "" {
		body = "原生正文未取得，请在Agent中查看。"
	}
	e.Title = sessionTitle
	e.Body = "主机：" + hostName + "\n事件：" + kind + "\n\n" + body
	return e
}
