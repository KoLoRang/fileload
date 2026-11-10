package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.10.0", "1.9.2", 1}, {"2.0", "2.0.0", 0}, {"v1.2.3", "1.2.4", -1}} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestInstallerFor(t *testing.T) {
	if !installerFor("windows", "FileLoad-Setup-2.0.1.exe") || installerFor("windows", "FileLoad.exe") {
		t.Error("windows")
	}
	if !installerFor("darwin", "FileLoad-2.0.1-macos.dmg") || installerFor("darwin", "x.exe") {
		t.Error("darwin")
	}
}

func TestDownload(t *testing.T) {
	payload := []byte("installer-bytes")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "bad headers", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	info := Info{AssetURL: srv.URL, InstallerName: "FileLoad-Setup-9.9.9.exe", InstallerSize: int64(len(payload)),
		Digest: "sha256:" + hex.EncodeToString(sum[:])}
	var last int64
	path, err := Download(context.Background(), info, func(done, _ int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if got, _ := os.ReadFile(path); string(got) != string(payload) || last != int64(len(payload)) {
		t.Fatalf("content %q progress %d", got, last)
	}

	info.Digest = "sha256:00"
	if _, err := Download(context.Background(), info, nil); err == nil {
		t.Fatal("digest mismatch not detected")
	}
}
