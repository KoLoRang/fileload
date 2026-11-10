// Package update checks GitHub Releases for a newer version, downloads the
// installer that matches this OS and applies it (see apply_*.go).
package update

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Version is the app version. It is the single source of truth; packaging
// scripts read it (or override it with -ldflags "-X fileload/internal/update.Version=1.2.3").
var Version = "1.0.0"

// EmbeddedToken is an optional read-only GitHub token baked in at build time
// (-ldflags "-X fileload/internal/update.EmbeddedToken=github_pat_...") so
// users of the private repository need not enter one. A token saved in the
// About page takes precedence.
var EmbeddedToken = ""

const (
	Owner          = "KoLoRang"
	Repo           = "fileload"
	RepoURL        = "https://github.com/" + Owner + "/" + Repo
	ReleasesWebURL = RepoURL + "/releases"
	apiBase        = "https://api.github.com/repos/" + Owner + "/" + Repo
)

// Info describes the latest release.
type Info struct {
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	Name       string `json:"name"`
	ReleasedAt string `json:"releasedAt"`
	Notes      string `json:"notes"`
	HasUpdate  bool   `json:"hasUpdate"`
	PageURL    string `json:"pageUrl"`

	// installer asset for this OS; AssetURL is the API URL (works for
	// private repositories with a token)
	InstallerName string `json:"installerName"`
	InstallerSize int64  `json:"installerSize"`
	AssetURL      string `json:"assetUrl"`
	Digest        string `json:"-"` // "sha256:<hex>" when GitHub provides it
}

var client = &http.Client{Timeout: 15 * time.Second}

func newRequest(ctx context.Context, url, token, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "FileLoad/"+Version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

// Check asks GitHub for the latest (non-draft, non-prerelease) release.
// token may be "" for a public repository.
func Check(ctx context.Context, token string) (Info, error) {
	req, err := newRequest(ctx, apiBase+"/releases/latest", token, "application/vnd.github+json")
	if err != nil {
		return Info{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("无法连接 GitHub：%w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return Info{}, errors.New("GitHub 访问令牌无效或已过期，请重新填写")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return Info{}, fmt.Errorf("GitHub 拒绝访问（HTTP %d，可能是令牌无权限或请求过于频繁），请稍后重试", resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound && token == "":
		return Info{}, errors.New("仓库为私有：请在下方填写 GitHub 访问令牌（对本仓库有 Contents 只读权限），或点「打开 Releases 页」手动查看")
	case resp.StatusCode == http.StatusNotFound:
		return Info{}, errors.New("没有找到已发布的版本（或令牌无权访问该仓库）")
	case resp.StatusCode != http.StatusOK:
		return Info{}, fmt.Errorf("检查更新失败：HTTP %d", resp.StatusCode)
	}

	var rel struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		HTMLURL     string `json:"html_url"`
		Assets      []struct {
			Name   string `json:"name"`
			URL    string `json:"url"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return Info{}, fmt.Errorf("解析版本信息失败：%w", err)
	}
	info := Info{
		Current: Version, Latest: strings.TrimPrefix(rel.TagName, "v"),
		Name: rel.Name, Notes: rel.Body, PageURL: cmp.Or(rel.HTMLURL, ReleasesWebURL),
	}
	if t, err := time.Parse(time.RFC3339, rel.PublishedAt); err == nil {
		info.ReleasedAt = t.Local().Format("2006-01-02")
	}
	for _, a := range rel.Assets {
		if installerFor(runtime.GOOS, a.Name) {
			info.InstallerName, info.InstallerSize = a.Name, a.Size
			info.AssetURL, info.Digest = a.URL, a.Digest
			break
		}
	}
	info.HasUpdate = Compare(info.Latest, Version) > 0
	return info, nil
}

// installerFor reports whether asset name is the installer for goos.
func installerFor(goos, name string) bool {
	n := strings.ToLower(name)
	switch goos {
	case "windows":
		return strings.Contains(n, "setup") && strings.HasSuffix(n, ".exe")
	case "darwin":
		return strings.HasSuffix(n, ".dmg")
	}
	return false
}

// Compare compares dotted numeric versions ("1.10.0" > "1.9.2").
func Compare(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.TrimLeft(pa[i], "v"))
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.TrimLeft(pb[i], "v"))
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}
