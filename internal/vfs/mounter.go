package vfs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/winfsp/cgofuse/fuse"

	"fileload/internal/mount"
)

const (
	initTimeout  = 20 * time.Second
	readyTimeout = 10 * time.Second
)

// Mounter mounts through cgofuse.
type Mounter struct{}

var _ mount.Mounter = Mounter{}

type mounted struct {
	host   *fuse.FileSystemHost
	fs     *FS
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	err    error
}

// Mount starts the driver and waits until the file system is initialised
// (cgofuse Init callback) and, if req.Ready is set, visible. No fixed sleep:
// a failing mount returns as soon as the driver gives up.
func (Mounter) Mount(ctx context.Context, req mount.Request) (mount.Handle, error) {
	log := req.Log
	if log == nil {
		log = slog.Default()
	}
	fctx, cancel := context.WithCancel(context.Background())
	fsys := newFS(fctx, req.Backend, req.Cache, req.Root, log)
	host := fuse.NewFileSystemHost(fsys)
	m := &mounted{host: host, fs: fsys, cancel: cancel, done: make(chan struct{})}

	go func() {
		defer close(m.done)
		if !host.Mount(req.Target, req.Options) {
			m.err = errors.New("文件系统驱动拒绝挂载")
		}
		fsys.closeAll()
		cancel()
	}()

	fail := func(err error) (mount.Handle, error) {
		_ = m.Unmount()
		return nil, err
	}
	select {
	case <-fsys.ready:
	case <-m.done:
		return nil, fmt.Errorf("挂载失败: %w", cmpErr(m.err))
	case <-time.After(initTimeout):
		return fail(errors.New("挂载超时：文件系统驱动未响应"))
	case <-ctx.Done():
		return fail(ctx.Err())
	}
	if req.Ready == nil {
		return m, nil
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(readyTimeout)
	for !req.Ready() {
		select {
		case <-tick.C:
		case <-m.done:
			return nil, fmt.Errorf("挂载失败: %w", cmpErr(m.err))
		case <-deadline:
			return fail(fmt.Errorf("挂载失败：%s 未出现", req.Target))
		case <-ctx.Done():
			return fail(ctx.Err())
		}
	}
	return m, nil
}

func cmpErr(err error) error {
	if err == nil {
		return errors.New("文件系统已退出")
	}
	return err
}

func (m *mounted) Unmount() error {
	m.once.Do(func() {
		select {
		case <-m.done:
			return // already gone
		default:
		}
		m.host.Unmount()
		select {
		case <-m.done:
		case <-time.After(30 * time.Second):
			m.err = errors.New("卸载超时")
		}
	})
	return nil
}

func (m *mounted) Done() <-chan struct{} { return m.done }
