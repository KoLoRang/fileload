package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Progress receives the downloaded and total byte counts (total may be 0).
type Progress func(done, total int64)

// downloadClient has no overall timeout (installers are large); the
// context cancels it.
var downloadClient = &http.Client{}

// Download fetches the installer of info into a temp directory, verifies
// its size and sha256 digest (when GitHub provides one) and returns the path.
func Download(ctx context.Context, info Info, progress Progress) (string, error) {
	if info.AssetURL == "" || info.InstallerName == "" {
		return "", errors.New("该版本没有适用于本系统的安装包，请打开发布页手动下载")
	}
	req, err := newRequest(ctx, info.AssetURL, "application/octet-stream")
	if err != nil {
		return "", err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}

	dir := filepath.Join(os.TempDir(), "fileload-update")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filepath.Base(info.InstallerName))
	tmp, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename

	total := firstPositive(resp.ContentLength, info.InstallerSize)
	h := sha256.New()
	pw := &progressWriter{total: total, fn: progress}
	n, err := io.Copy(io.MultiWriter(tmp, h, pw), resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("下载失败：%w", err)
	}
	if info.InstallerSize > 0 && n != info.InstallerSize {
		return "", fmt.Errorf("下载不完整（%d/%d 字节），请重试", n, info.InstallerSize)
	}
	if want, ok := strings.CutPrefix(info.Digest, "sha256:"); ok && !strings.EqualFold(want, hex.EncodeToString(h.Sum(nil))) {
		return "", errors.New("安装包校验失败（sha256 不匹配），请重试")
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

func firstPositive(a, b int64) int64 {
	if a > 0 {
		return a
	}
	return max(b, 0)
}

type progressWriter struct {
	done, total, last int64
	fn                Progress
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	// report about every 256 KiB and at the end
	if p.fn != nil && (p.done-p.last >= 256<<10 || p.done == p.total) {
		p.last = p.done
		p.fn(p.done, p.total)
	}
	return len(b), nil
}
