package hub

import (
	"agent-pulse/internal/config"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAdapterScheduleDefaultsAndConfiguration(t *testing.T) {
	now := time.Date(2026, 11, 22, 1, 59, 0, 0, time.FixedZone("Shanghai", 8*3600))
	next, err := nextAdapterCheck(now, config.AdapterRepositoryUpdates{})
	if err != nil {
		t.Fatal(err)
	}
	if next.Hour() != 2 || next.Minute() != 0 || next.Day() != 22 || next.Location().String() != "Asia/Shanghai" {
		t.Fatal(next)
	}
	tomorrow, err := nextAdapterCheck(next, config.AdapterRepositoryUpdates{})
	if err != nil || tomorrow.Day() != 23 {
		t.Fatal(tomorrow, err)
	}
	next, err = nextAdapterCheck(now, config.AdapterRepositoryUpdates{DailyAt: "18:30", Timezone: "UTC"})
	if err != nil || next.Hour() != 18 || next.Minute() != 30 {
		t.Fatal(next, err)
	}
	for _, c := range []config.AdapterRepositoryUpdates{{DailyAt: "2:00"}, {DailyAt: "24:00"}, {Timezone: "invalid/zone"}, {GithubRepository: "bad"}} {
		if _, err := nextAdapterCheck(now, c); err == nil {
			t.Fatal("接受无效日程", c)
		}
	}
}
func TestDisabledAdapterUpdaterAndNoImplicitTrust(t *testing.T) {
	h := &Hub{config: config.Hub{AdapterDirectory: t.TempDir(), AdapterRepositoryUpdates: config.AdapterRepositoryUpdates{Disabled: true}}}
	done := make(chan struct{})
	go func() { h.RunAdapterUpdates(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("关闭的日程仍占用运行任务")
	}
	c := config.Hub{AdapterDirectory: filepath.Join(t.TempDir(), "repository")}
	if err := UpdateAdapters(context.Background(), c, t.TempDir()); err == nil {
		t.Fatal("离线包建立了隐式公钥信任")
	}
}
