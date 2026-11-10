// Package sftp implements backend.Backend over SSH/SFTP, with automatic
// reconnect, keepalive and a reference-counted connection pool.
package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"fileload/internal/backend"
	"fileload/internal/model"
)

const (
	dialTimeout       = 10 * time.Second
	keepaliveInterval = 15 * time.Second
	keepaliveTimeout  = 10 * time.Second
)

// session is one live SSH connection plus its SFTP subsystem.
type session struct {
	ssh  *ssh.Client
	sftp *sftp.Client
	once sync.Once
}

func (s *session) close() {
	s.once.Do(func() {
		_ = s.sftp.Close()
		_ = s.ssh.Close()
	})
}

// Client is a self-healing SFTP connection. The *sftp.Client is safe for
// concurrent use, so operations never hold a lock while talking to the
// server: the current session is read atomically and the mutex is only
// taken to replace a broken session (concurrent callers then share the one
// reconnect instead of each dialing).
type Client struct {
	cfg model.Server
	log *slog.Logger

	cur       atomic.Pointer[session]
	reconnect sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var _ backend.Backend = (*Client)(nil)

// Dial connects to cfg and starts the keepalive loop. The returned client
// lives until Close.
func Dial(ctx context.Context, cfg model.Server, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}
	s, err := connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithCancel(context.Background())
	c := &Client{cfg: cfg, log: log.With("server", cfg.Label()), ctx: cctx, cancel: cancel}
	c.cur.Store(s)
	c.wg.Go(c.keepalive)
	return c, nil
}

// Close stops the keepalive loop and closes the connection.
func (c *Client) Close() error {
	c.cancel()
	c.wg.Wait()
	if s := c.cur.Swap(nil); s != nil {
		s.close()
	}
	return nil
}

// Config returns the profile the client was dialed with.
func (c *Client) Config() model.Server { return c.cfg }

func connect(ctx context.Context, p model.Server) (*session, error) {
	cfg, err := clientConfig(p)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", p.Addr())
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", p.Addr(), errors.Join(err, backend.ErrUnavailable))
	}
	// bound the handshake too: a silent server must not hang the caller
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	sc, chans, reqs, err := ssh.NewClientConn(conn, p.Addr(), cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SSH 登录失败: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	sshc := ssh.NewClient(sc, chans, reqs)
	sftpc, err := sftp.NewClient(sshc, sftp.UseConcurrentReads(true), sftp.UseConcurrentWrites(true))
	if err != nil {
		_ = sshc.Close()
		return nil, fmt.Errorf("SFTP 初始化失败: %w", err)
	}
	return &session{ssh: sshc, sftp: sftpc}, nil
}

func clientConfig(p model.Server) (*ssh.ClientConfig, error) {
	var auth []ssh.AuthMethod
	switch {
	case p.KeyPath != "":
		key, err := os.ReadFile(p.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("读取私钥失败: %w", err)
		}
		var signer ssh.Signer
		if p.Password != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(p.Password))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	case p.Password != "":
		auth = append(auth, ssh.Password(p.Password),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range ans {
					ans[i] = p.Password
				}
				return ans, nil
			}))
	default:
		return nil, errors.New("请填写密码或私钥路径")
	}
	return &ssh.ClientConfig{
		User: p.User,
		Auth: auth,
		// TODO(M2): trust-on-first-use known_hosts instead of ignoring host keys.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         dialTimeout,
	}, nil
}

// session returns the live session, reconnecting if stale is the current
// one (or there is none).
func (c *Client) session(ctx context.Context, stale *session) (*session, error) {
	if s := c.cur.Load(); s != nil && s != stale {
		return s, nil
	}
	c.reconnect.Lock()
	defer c.reconnect.Unlock()
	if s := c.cur.Load(); s != nil && s != stale {
		return s, nil // somebody else reconnected meanwhile
	}
	if err := c.ctx.Err(); err != nil {
		return nil, fmt.Errorf("连接已关闭: %w", errors.Join(err, backend.ErrUnavailable))
	}
	if stale != nil {
		c.invalidate(stale)
	}
	c.log.Info("reconnecting")
	s, err := connect(ctx, c.cfg)
	if err != nil {
		c.log.Warn("reconnect failed", "err", err)
		return nil, errors.Join(err, backend.ErrUnavailable)
	}
	c.cur.Store(s)
	return s, nil
}

// invalidate drops s if it is still current.
func (c *Client) invalidate(s *session) {
	if c.cur.CompareAndSwap(s, nil) {
		s.close()
	}
}

// do runs fn on the current session; on a connection failure it reconnects
// once and retries.
func (c *Client) do(ctx context.Context, fn func(*sftp.Client) error) error {
	s, err := c.session(ctx, nil)
	if err != nil {
		return err
	}
	err = fn(s.sftp)
	if err == nil || isServerAnswer(err) || isLocal(err) {
		return normalize(err)
	}
	c.log.Debug("connection error, retrying", "err", err)
	s, rerr := c.session(ctx, s)
	if rerr != nil {
		return errors.Join(err, backend.ErrUnavailable)
	}
	if err := fn(s.sftp); err != nil {
		if isServerAnswer(err) || isLocal(err) {
			return normalize(err)
		}
		return errors.Join(err, backend.ErrUnavailable)
	}
	return nil
}

// keepalive pings every 15s so NAT/firewalls keep the tunnel open, and
// drops sessions that stop answering (the next call reconnects).
func (c *Client) keepalive() {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		s := c.cur.Load()
		if s == nil {
			continue
		}
		done := make(chan error, 1) // buffered: the sender never blocks
		go func() {
			_, _, err := s.ssh.SendRequest("keepalive@openssh.com", true, nil)
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				c.log.Warn("keepalive failed", "err", err)
				c.invalidate(s)
			}
		case <-time.After(keepaliveTimeout):
			c.log.Warn("keepalive timed out")
			c.invalidate(s) // closing the conn also unblocks SendRequest
		case <-c.ctx.Done():
			return
		}
	}
}

// isServerAnswer reports errors the server replied with (no such file,
// permission denied, ...). The connection is fine then; reconnecting would
// be wasted work (Explorer/Finder probe many non-existent files).
func isServerAnswer(err error) bool {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrExist) {
		return true
	}
	if se, ok := errors.AsType[*sftp.StatusError](err); ok {
		code := se.FxCode()
		return code != sftp.ErrSSHFxNoConnection && code != sftp.ErrSSHFxConnectionLost
	}
	return false
}

func isLocal(err error) bool {
	_, ok := errors.AsType[*localError](err)
	return ok
}

// kindErr adds a fs.Err* kind to a server error so callers can errors.Is it.
type kindErr struct{ err, kind error }

func (e *kindErr) Error() string   { return e.err.Error() }
func (e *kindErr) Unwrap() []error { return []error{e.err, e.kind} }

func normalize(err error) error {
	if err == nil {
		return nil
	}
	se, ok := errors.AsType[*sftp.StatusError](err)
	if !ok {
		return err
	}
	switch se.FxCode() {
	case sftp.ErrSSHFxNoSuchFile:
		return &kindErr{err, fs.ErrNotExist}
	case sftp.ErrSSHFxPermissionDenied:
		return &kindErr{err, fs.ErrPermission}
	}
	return err
}

// ---- backend.Backend ----

func (c *Client) Stat(ctx context.Context, p string) (fs.FileInfo, error) {
	var fi fs.FileInfo
	err := c.do(ctx, func(sc *sftp.Client) (err error) { fi, err = sc.Stat(p); return })
	return fi, err
}

func (c *Client) ReadDir(ctx context.Context, p string) ([]fs.FileInfo, error) {
	var list []fs.FileInfo
	err := c.do(ctx, func(sc *sftp.Client) (err error) { list, err = sc.ReadDir(p); return })
	return list, err
}

func (c *Client) Mkdir(ctx context.Context, p string) error {
	return c.do(ctx, func(sc *sftp.Client) error {
		err := sc.Mkdir(p)
		if err != nil {
			if fi, serr := sc.Stat(p); serr == nil && fi.IsDir() {
				return &kindErr{err, fs.ErrExist}
			}
		}
		return err
	})
}

func (c *Client) Remove(ctx context.Context, p string) error {
	return c.do(ctx, func(sc *sftp.Client) error { return sc.Remove(p) })
}

func (c *Client) RemoveDir(ctx context.Context, p string) error {
	return c.do(ctx, func(sc *sftp.Client) error { return sc.RemoveDirectory(p) })
}

func (c *Client) Rename(ctx context.Context, oldPath, newPath string) error {
	return c.do(ctx, func(sc *sftp.Client) error { return sc.PosixRename(oldPath, newPath) })
}

func (c *Client) Truncate(ctx context.Context, p string, size int64) error {
	return c.do(ctx, func(sc *sftp.Client) error { return sc.Truncate(p, size) })
}

func (c *Client) Chmod(ctx context.Context, p string, mode fs.FileMode) error {
	return c.do(ctx, func(sc *sftp.Client) error { return sc.Chmod(p, mode) })
}

func (c *Client) Chtimes(ctx context.Context, p string, atime, mtime time.Time) error {
	return c.do(ctx, func(sc *sftp.Client) error { return sc.Chtimes(p, atime, mtime) })
}

func (c *Client) CreateEmpty(ctx context.Context, p string) error {
	return c.do(ctx, func(sc *sftp.Client) error {
		f, err := sc.Create(p)
		if err != nil {
			return err
		}
		return f.Close()
	})
}

func (c *Client) Download(ctx context.Context, p string, w io.Writer) error {
	restart, err := restarter(w, true)
	if err != nil {
		return err
	}
	return c.do(ctx, func(sc *sftp.Client) error {
		if err := restart(); err != nil { // a retry starts over
			return &localError{err}
		}
		f, err := sc.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteTo(w)
		return err
	})
}

func (c *Client) Upload(ctx context.Context, p string, r io.Reader) error {
	restart, err := restarter(r, false)
	if err != nil {
		return err
	}
	return c.do(ctx, func(sc *sftp.Client) error {
		if err := restart(); err != nil {
			return &localError{err}
		}
		f, err := sc.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return err
		}
		_, err = f.ReadFrom(r)
		return errors.Join(err, f.Close())
	})
}

// restarter returns a func that rewinds a stream to where it is now, so a
// retried transfer starts over; truncate also drops what a failed download
// already wrote. Non-seekable streams are not rewound.
func restarter(v any, truncate bool) (func() error, error) {
	s, ok := v.(io.Seeker)
	if !ok {
		return func() error { return nil }, nil
	}
	off, err := s.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	return func() error {
		if t, ok := v.(interface{ Truncate(int64) error }); ok && truncate {
			if err := t.Truncate(off); err != nil {
				return err
			}
		}
		_, err := s.Seek(off, io.SeekStart)
		return err
	}, nil
}

// localError is a failure on our side (local file): never a reason to
// reconnect.
type localError struct{ err error }

func (e *localError) Error() string { return e.err.Error() }
func (e *localError) Unwrap() error { return e.err }

// ---- raw SSH (for the command proxy) ----

// OpenSession opens a new "session" channel multiplexed over the existing
// SSH connection, so proxied commands need no extra login.
func (c *Client) OpenSession(ctx context.Context) (ssh.Channel, <-chan *ssh.Request, error) {
	s, err := c.session(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	ch, reqs, err := s.ssh.OpenChannel("session", nil)
	if err == nil {
		return ch, reqs, nil
	}
	if _, ok := errors.AsType[*ssh.OpenChannelError](err); ok {
		return nil, nil, err // server refused: the connection itself is fine
	}
	if s, err = c.session(ctx, s); err != nil {
		return nil, nil, err
	}
	return s.ssh.OpenChannel("session", nil)
}
