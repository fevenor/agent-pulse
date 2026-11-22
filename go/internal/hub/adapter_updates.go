package hub

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"agent-pulse/internal/adapters"
	"agent-pulse/internal/config"
	"agent-pulse/internal/transport"
)

// UpdateAdapters 不打开业务数据库，管理员可与服务并行运行手工更新。
func UpdateAdapters(ctx context.Context, c config.Hub, offline string) error {
	if err := seedAdapterRepository(c); err != nil {
		return err
	}
	if c.AdapterDirectory == "" {
		return errors.New("必须配置 adapter_directory")
	}
	key, err := resolvePublisherKey(c.AdapterDirectory, c.AdapterPublicKey)
	if err != nil {
		return err
	}
	if key == "" {
		return errors.New("必须先配置可信发布公钥，离线包不能替换公钥")
	}
	unlock, err := adapters.LockRepository(c.AdapterDirectory)
	if err != nil {
		return errors.New("适配仓库更新正在进行")
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var source adapters.RepositorySource
	if offline != "" {
		var closeSource func()
		source, closeSource, err = adapters.OfflineRepository(offline)
		if err != nil {
			return err
		}
		defer closeSource()
	} else {
		updates := c.AdapterRepositoryUpdates.Defaults()
		source, err = adapters.GitHubRepository(ctx, transport.Client(60*time.Second), "https://api.github.com", updates.GithubRepository)
		if err != nil {
			return err
		}
	}
	return adapters.PromoteRepository(ctx, c.AdapterDirectory, key, source)
}

func nextAdapterCheck(now time.Time, c config.AdapterRepositoryUpdates) (time.Time, error) {
	c = c.Defaults()
	if err := c.Validate(); err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	clock, _ := time.Parse("15:04", c.DailyAt)
	local := now.In(location)
	next := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
	if !next.After(now) {
		tomorrow := local.AddDate(0, 0, 1)
		next = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
	}
	return next, nil
}

// RunAdapterUpdates 仅在每日配置时刻检查；失败继续服务上次完整仓库，不频繁重试。
func (h *Hub) RunAdapterUpdates(ctx context.Context) {
	c := h.config.AdapterRepositoryUpdates.Defaults()
	if c.Disabled || h.config.AdapterDirectory == "" {
		return
	}
	for {
		next, err := nextAdapterCheck(time.Now(), c)
		if err != nil {
			slog.Warn("适配仓库日程无效", "原因", err)
			return
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := UpdateAdapters(ctx, h.config, ""); err != nil {
			slog.Warn("适配仓库检查失败，保留当前仓库", "原因", err)
		} else {
			slog.Info("适配仓库检查完成")
		}
	}
}
