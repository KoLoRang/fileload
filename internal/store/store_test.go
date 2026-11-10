package store

import (
	"os"
	"path/filepath"
	"testing"

	"fileload/internal/model"
)

// v1.x wrote these exact shapes; they must keep loading.
const v1Mounts = `[
  {"name":"a@h","host":"h","port":22,"user":"a","password":"pw","keyPath":"",
   "drive":"Z:","root":"/srv","jmsAsset":"node-1"}
]`

func TestLoadsV1Files(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "mounts.json"), []byte(v1Mounts), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "servers.json"), []byte(`[{"name":"a@h","host":"h","port":22,"user":"a","password":"pw","keyPath":""}]`), 0o600)
	s := Open(dir)
	m := s.Mounts()
	if len(m) != 1 || m[0].Target != "Z:" || m[0].Root != "/srv" || m[0].Password != "pw" || m[0].Port != 22 {
		t.Fatalf("mounts = %+v", m)
	}
	if sv := s.Servers(); len(sv) != 1 || sv[0].Host != "h" {
		t.Fatalf("servers = %+v", sv)
	}
}

func TestUpsertAndPersist(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	p := model.Server{Name: "x", Host: "h", Port: 22, User: "u", Password: "1"}
	if err := s.UpsertServer(p); err != nil {
		t.Fatal(err)
	}
	p.Password = "2"
	if err := s.UpsertServer(p); err != nil { // same account: replaced
		t.Fatal(err)
	}
	s2 := Open(dir) // fresh instance reads the file
	if got := s2.Servers(); len(got) != 1 || got[0].Password != "2" {
		t.Fatalf("servers = %+v", got)
	}
	if err := s2.DeleteServer(5); err != ErrNotFound {
		t.Fatalf("delete out of range = %v", err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestCommandsValidation(t *testing.T) {
	s := Open(t.TempDir())
	cases := []struct {
		c       model.Command
		wantErr bool
	}{
		{model.Command{Name: "rcd", Command: "x"}, true}, // builtin
		{model.Command{Name: "9x", Command: "x"}, true},  // bad name
		{model.Command{Name: "gs", Command: "  "}, true}, // empty command
		{model.Command{Name: "gs", Command: "git status"}, false},
	}
	for _, tc := range cases {
		if err := s.SaveCommand("", tc.c); (err != nil) != tc.wantErr {
			t.Errorf("SaveCommand(%+v) err=%v", tc.c, err)
		}
	}
	if err := s.SaveCommand("", model.Command{Name: "GS", Command: "y"}); err == nil {
		t.Error("duplicate (case-insensitive) accepted")
	}
	if err := s.SaveCommand("gs", model.Command{Name: "gst", Command: "git status -s"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	cmds := s.Commands()
	if len(cmds) != 2 || cmds[0].Name != "rcd" || !cmds[0].Builtin || cmds[1].Name != "gst" {
		t.Fatalf("commands = %+v", cmds)
	}
	if err := s.DeleteCommand("GST"); err != nil || len(s.Commands()) != 1 {
		t.Fatalf("delete: %v %+v", err, s.Commands())
	}
}
