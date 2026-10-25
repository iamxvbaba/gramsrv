// Package systemidentity 把管理面板 Server Settings → Server identity 里配置的
// 服务器身份应用到正在运行的主服务器：官方系统账号 (777000) 的展示名，以及
// 配置了图标时它的头像。
//
// 管理二进制与主服务器二进制是两个进程，只通过磁盘上的 identity.json 共享状态
// （见 internal/identity 包注释）。因此主服务器不能靠一次启动读取就完事：名称与
// 图标必须在运行期持续跟随面板改动，否则 operator 保存后要重启服务器才生效。
// Watcher 以固定间隔轮询 identity.Store，名称与图标分别比较，只对真正变化的那一项
// 重新应用——名称变化不重建头像，图标变化才重建。
package systemidentity

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

// AvatarSeeder 用 operator 的图标字节（空表示回退到内置默认头像）播种官方系统
// 账号的头像，并把它设为当前头像。通常绑定到 files 服务上的
// botavatars.SeedOfficialSystemAvatar。
type AvatarSeeder func(ctx context.Context, icon []byte, now int64) (bool, error)

// Apply 读取已存储的身份并一次性投影到运行中的服务器：777000 展示名（经 domain），
// 以及（提供了 seeder 时）官方系统账号头像。启动阶段调用一次；运行期增量更新交给
// Watcher。
//
// 名称即使 seeder 失败也已写入——名称投影是廉价且独立的一步，不应因为头像播种
// 出错而一并丢失。
func Apply(ctx context.Context, store *identity.Store, seedAvatar AvatarSeeder, now int64) (identity.Info, error) {
	info, err := store.Get()
	if err != nil {
		return identity.Info{}, err
	}
	domain.SetOfficialSystemUserDisplayName(info.Name)
	if seedAvatar != nil {
		if err := seedAvatarFromStore(ctx, store, seedAvatar, now); err != nil {
			return info, err
		}
	}
	return info, nil
}

func seedAvatarFromStore(ctx context.Context, store *identity.Store, seedAvatar AvatarSeeder, now int64) error {
	var icon []byte
	if data, _, ok := store.Icon(); ok {
		icon = data
	}
	if _, err := seedAvatar(ctx, icon, now); err != nil {
		return fmt.Errorf("seed official system avatar: %w", err)
	}
	return nil
}

// Watcher 轮询 identity.Store，名称或图标变化时重新应用。名称变化只更新展示名，
// 图标变化才重建 777000 头像（含移除图标后回退到内置默认头像）。
type Watcher struct {
	Store      *identity.Store
	SeedAvatar AvatarSeeder
	// Interval 是轮询间隔，<= 0 时默认 5 秒。
	Interval time.Duration
	Logger   *zap.Logger
}

// Run 轮询直到 ctx 被取消。启动时先记下当前名称与图标指纹再进入轮询：启动阶段
// 已经应用过一次身份（见 cmd/telesrv/main.go），这里只响应此后的变化。
func (w *Watcher) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	name, icon, err := w.snapshot()
	if err != nil {
		w.log().Warn("server identity snapshot failed", zap.Error(err))
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newName, newIcon, err := w.snapshot()
			if err != nil {
				w.log().Warn("server identity snapshot failed", zap.Error(err))
				continue
			}
			if newName != name {
				domain.SetOfficialSystemUserDisplayName(newName)
				name = newName
				w.log().Info("server identity name applied", zap.String("name", newName))
			}
			if newIcon == icon {
				continue
			}
			if err := seedAvatarFromStore(ctx, w.Store, w.SeedAvatar, time.Now().Unix()); err != nil {
				w.log().Warn("apply server identity avatar failed", zap.Error(err))
				continue
			}
			icon = newIcon
			w.log().Info("server identity avatar applied")
		}
	}
}

func (w *Watcher) snapshot() (string, string, error) {
	info, err := w.Store.Get()
	if err != nil {
		return "", "", err
	}
	icon, err := w.Store.IconFingerprint()
	if err != nil {
		return "", "", err
	}
	return info.Name, icon, nil
}

func (w *Watcher) log() *zap.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return zap.NewNop()
}
