package hub

import (
	"agent-pulse/internal/adapters"
	"agent-pulse/internal/config"
	"context"
	"errors"
	"os"
	"path/filepath"
)

// seedAdapterRepository 只在活动目录没有索引时复制镜像初始仓库；已有持久仓库绝不被镜像覆盖。
func seedAdapterRepository(c config.Hub) error {
	if c.AdapterDirectory == "" || c.AdapterSeedDirectory == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(c.AdapterDirectory, "index.json")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(filepath.Join(c.AdapterSeedDirectory, "index.json")); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	unlock, err := adapters.LockRepository(c.AdapterDirectory)
	if err != nil {
		return errors.New("初始适配仓库正在准备")
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(c.AdapterDirectory, "index.json")); err == nil {
		return nil
	}
	key := c.AdapterPublicKey
	if key == "" {
		existing, readErr := os.ReadFile(filepath.Join(c.AdapterDirectory, "publisher.pub"))
		if readErr == nil {
			key = string(existing)
		} else if !os.IsNotExist(readErr) {
			return readErr
		} else {
			raw, err := os.ReadFile(filepath.Join(c.AdapterSeedDirectory, "publisher.pub"))
			if err != nil {
				return err
			}
			key = string(raw)
		}
	}
	source, closeSource, err := adapters.OfflineRepository(c.AdapterSeedDirectory)
	if err != nil {
		return err
	}
	defer closeSource()
	if _, err := adapters.Verify(source.Index, key); err != nil {
		return err
	}
	// 在活动索引之前固定镜像初始公钥；任何后续来源均无法替换它。
	if c.AdapterPublicKey == "" {
		if err = adapters.AtomicFile(filepath.Join(c.AdapterDirectory, "publisher.pub"), []byte(key), 0644); err != nil {
			return err
		}
	}
	return adapters.PromoteRepository(context.Background(), c.AdapterDirectory, key, source)
}
