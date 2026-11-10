//go:build darwin

package darwin

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"fileload/internal/platform"
)

// runShim executes the generated wrapper with a fake ssh that prints its
// last argument (the remote command line).
func runShim(t *testing.T, bin, name, cwd string) string {
	t.Helper()
	fake := t.TempDir()
	_ = os.WriteFile(filepath.Join(fake, "ssh"), []byte("#!/bin/sh\nfor a; do last=$a; done\nprintf '%s' \"$last\"\n"), 0o755)
	cmd := exec.Command(filepath.Join(bin, name))
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PATH="+fake+":/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shim failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestShimMapsCwdToRemoteSubdir(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // the "proxy" for nc -z
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	base, _ := filepath.EvalSymlinks(t.TempDir()) // /var -> /private/var: PWD is resolved
	mnt := filepath.Join(base, "mnt point")       // spaces must survive
	sub := filepath.Join(mnt, "it's", "deep")
	_ = os.MkdirAll(sub, 0o755)
	bin := filepath.Join(base, "bin")
	profile := filepath.Join(base, "zprofile")

	s := &shims{bin: bin, profiles: []string{profile}}
	err = s.Install([]platform.ShimSpec{{
		Name: "rcd", RemoteLine: "exec bash -l", Target: mnt, Root: "/home/u/proj",
		ProxyAddr: ln.Addr().String(), User: "u",
	}})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := runShim(t, bin, "rcd", mnt), "cd '/home/u/proj' && exec bash -l"; got != want {
		t.Errorf("at mount root:\n got %q\nwant %q", got, want)
	}
	if got, want := runShim(t, bin, "rcd", sub), `cd '/home/u/proj/it'\''s/deep' && exec bash -l`; got != want {
		t.Errorf("in sub dir:\n got %q\nwant %q", got, want)
	}
	if got, want := runShim(t, bin, "rcd", base), "cd '/home/u/proj' && exec bash -l"; got != want {
		t.Errorf("outside mount:\n got %q\nwant %q", got, want)
	}

	// PATH block: added once, idempotent, user content preserved
	_ = os.WriteFile(profile, []byte("export FOO=1"), 0o644)
	for range 2 {
		if err := ensureBlock(profile, bin); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(profile)
	text := string(data)
	if strings.Count(text, blockStart) != 1 || !strings.HasPrefix(text, "export FOO=1\n") || !strings.Contains(text, bin) {
		t.Fatalf("profile:\n%s", text)
	}

	// Remove drops generated scripts only
	_ = os.WriteFile(filepath.Join(bin, "mine"), []byte("#!/bin/sh\n"), 0o755)
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bin, "rcd")); !os.IsNotExist(err) {
		t.Fatal("generated script not removed")
	}
	if _, err := os.Stat(filepath.Join(bin, "mine")); err != nil {
		t.Fatal("user file removed")
	}
}

func TestShimReportsMissingProxy(t *testing.T) {
	bin := t.TempDir()
	s := &shims{bin: bin}
	_ = s.Install([]platform.ShimSpec{{Name: "rcd", RemoteLine: "x", Target: "/m", Root: "/r", ProxyAddr: "127.0.0.1:1", User: "u"}})
	out, err := exec.Command(filepath.Join(bin, "rcd")).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "本地代理未运行") {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestTargetsAndRelPath(t *testing.T) {
	home := t.TempDir()
	p := &Platform{home: home}
	got := p.Targets("/home/u/my proj/")
	if len(got) != 1 || got[0].Value != filepath.Join(home, "FileLoad", "my proj") || got[0].Label != "~/FileLoad/my proj" {
		t.Fatalf("targets = %+v", got)
	}
	// occupied (non-empty) -> next free name
	_ = os.MkdirAll(filepath.Join(home, "FileLoad", "my proj", "x"), 0o755)
	if got := p.Targets("/home/u/my proj"); got[0].Value != filepath.Join(home, "FileLoad", "my proj-2") {
		t.Fatalf("busy target reused: %+v", got)
	}
	if rel, ok := p.RelPath("/a/b", "/a/b/c/d"); !ok || rel != "c/d" {
		t.Fatal(rel, ok)
	}
	if _, ok := p.RelPath("/a/b", "/a/bc"); ok {
		t.Fatal("/a/bc is not inside /a/b")
	}
	tgt, err := p.PrepareTarget("~/FileLoad/new")
	if err != nil || tgt != filepath.Join(home, "FileLoad", "new") {
		t.Fatal(tgt, err)
	}
	p.ReleaseTarget(tgt)
	if _, err := os.Stat(tgt); !os.IsNotExist(err) {
		t.Fatal("empty target dir not removed")
	}
}
