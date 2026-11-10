package proxy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeUpstream is an in-process SSH server that "executes" commands by
// echoing them on stdout, writing "err" on stderr and exiting with 7.
type fakeUpstream struct {
	client *ssh.Client
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, _ := nc.Accept()
					go func() {
						defer ch.Close()
						for r := range creqs {
							if r.Type != "exec" {
								_ = r.Reply(true, nil)
								continue
							}
							var p struct{ Cmd string }
							_ = ssh.Unmarshal(r.Payload, &p)
							_ = r.Reply(true, nil)
							_, _ = ch.Write([]byte("ran:" + p.Cmd))
							_, _ = ch.Stderr().Write([]byte("err"))
							_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{7}))
							return
						}
					}()
				}
			}()
		}
	}()
	cl, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "u", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return &fakeUpstream{client: cl}
}

func (f *fakeUpstream) OpenSession(context.Context) (ssh.Channel, <-chan *ssh.Request, error) {
	return f.client.OpenChannel("session", nil)
}

func TestForwardsOutputStderrAndExitStatus(t *testing.T) {
	px, err := Start(newFakeUpstream(t), 22000, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer px.Close()

	cl, err := ssh.Dial("tcp", px.Addr(), &ssh.ClientConfig{User: "any", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	sess, _ := cl.NewSession()
	var out, errOut bytes.Buffer
	sess.Stdout, sess.Stderr = &out, &errOut
	err = sess.Run("cd '/x' && ls")
	if exit, ok := errors.AsType[*ssh.ExitError](err); !ok || exit.ExitStatus() != 7 {
		t.Fatalf("exit = %v", err)
	}
	if out.String() != "ran:cd '/x' && ls" || errOut.String() != "err" {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestCloseDropsSessionsAndGoroutines(t *testing.T) {
	up := newFakeUpstream(t) // fixture goroutines live until cleanup
	before := runtime.NumGoroutine()
	px, err := Start(up, 22100, nil)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := ssh.Dial("tcp", px.Addr(), &ssh.ClientConfig{User: "any", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := cl.NewSession()
	_ = sess.Shell() // idle interactive session, never exits by itself

	done := make(chan struct{})
	go func() { _ = px.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung with an open session")
	}
	if _, err := ssh.Dial("tcp", px.Addr(), &ssh.ClientConfig{User: "any", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second}); err == nil {
		t.Fatal("proxy still accepting after Close")
	}
	cl.Close()
	// give the runtime a moment, then check nothing piled up
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+2 {
		var b strings.Builder
		buf := make([]byte, 1<<20)
		b.Write(buf[:runtime.Stack(buf, true)])
		t.Fatalf("goroutines %d -> %d after Close\n%s", before, n, b.String())
	}
}
