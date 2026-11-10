// Package proxy runs a loopback SSH server that forwards every session to
// the upstream server over the app's already-authenticated connection, so
// local command wrappers can `ssh -p <port> x@127.0.0.1` without a password.
package proxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// Upstream opens session channels on the real server.
type Upstream interface {
	OpenSession(ctx context.Context) (ssh.Channel, <-chan *ssh.Request, error)
}

// Server is the loopback SSH proxy.
type Server struct {
	up  Upstream
	cfg *ssh.ServerConfig
	ln  net.Listener
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// Start listens on 127.0.0.1:preferPort, or the next free port of the
// following 20.
func Start(up Upstream, preferPort int, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	// loopback only: any client is accepted. The host key is ephemeral, so
	// wrappers connect with StrictHostKeyChecking=no.
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	var ln net.Listener
	for i := range 20 {
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferPort+i)); err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("本地代理端口不可用: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{up: up, cfg: cfg, ln: ln, log: log.With("component", "proxy"),
		ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}}
	s.wg.Go(s.accept)
	return s, nil
}

// Addr is "127.0.0.1:port".
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops listening, closes every proxied connection and waits for all
// session goroutines to end.
func (s *Server) Close() error {
	s.cancel()
	err := s.ln.Close()
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

func (s *Server) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.ctx.Err() == nil {
				s.log.Warn("accept failed", "err", err)
			}
			return
		}
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Go(func() {
			s.serve(c)
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
		})
	}
}

func (s *Server) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second)) // bound the handshake
	sc, chans, reqs, err := ssh.NewServerConn(c, s.cfg)
	if err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	defer sc.Close()
	go ssh.DiscardRequests(reqs) // ends when the connection closes

	var wg sync.WaitGroup
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		wg.Go(func() { s.session(nc) })
	}
	wg.Wait()
}

// session forwards one channel verbatim: requests (pty-req, env, exec,
// shell, window-change, signals) both ways, stdin/stdout/stderr, and the
// exit status.
func (s *Server) session(nc ssh.NewChannel) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer ch.Close()

	up, upReqs, err := s.up.OpenSession(s.ctx)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "fileload: %v\r\n", err)
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{255}))
		go ssh.DiscardRequests(reqs)
		return
	}
	defer up.Close()

	// replies to client requests (e.g. "exec") that are still on their
	// way back must reach the client before ch is closed
	var pending atomic.Int32
	go forward(reqs, up, &pending) // ends when ch closes
	upDone := make(chan struct{})
	go func() { forward(upReqs, ch, nil); close(upDone) }()

	// output is done once both stdout and stderr hit EOF; closing ch
	// earlier would cut off the tail of stderr
	var outputs sync.WaitGroup
	outputs.Go(func() { _, _ = io.Copy(ch, up) })
	outputs.Go(func() { _, _ = io.Copy(ch.Stderr(), up.Stderr()) })
	output := make(chan struct{})
	go func() { outputs.Wait(); close(output) }()
	go func() {
		_, _ = io.Copy(up, ch)
		_ = up.CloseWrite()
	}()

	select {
	case <-output: // command finished
	case <-s.ctx.Done():
		return
	}
	// let exit-status and pending replies reach the client before closing
	deadline := time.After(time.Second)
	select {
	case <-upDone:
	case <-deadline:
		return
	}
	for pending.Load() > 0 {
		select {
		case <-deadline:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// forward relays requests until reqs closes; failures are answered as
// refused so the sender never blocks. pending, if set, counts requests
// whose reply has not been sent back yet.
func forward(reqs <-chan *ssh.Request, dst ssh.Channel, pending *atomic.Int32) {
	for r := range reqs {
		if pending != nil {
			pending.Add(1)
		}
		ok, err := dst.SendRequest(r.Type, r.WantReply, r.Payload)
		if r.WantReply {
			_ = r.Reply(ok && err == nil, nil)
		}
		if pending != nil {
			pending.Add(-1)
		}
	}
}
