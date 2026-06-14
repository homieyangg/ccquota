// Package update 檢查 GitHub 最新 release 並自我更新:
// 下載對應平台的 binary、驗 SHA256、原子替換目前執行檔、re-exec 重啟。
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

const apiBase = "https://api.github.com"

// maxDownload 限制下載大小(binary ~20M,留餘裕)。
const maxDownload = 80 << 20

// Release 是一個 GitHub release 的精簡資訊。
type Release struct {
	Tag      string
	NotesURL string
	Assets   map[string]string // asset 名 -> 下載 URL
}

// parseRelease 解析 GitHub releases/latest 的 JSON。
func parseRelease(b []byte) (Release, error) {
	var doc struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return Release{}, err
	}
	r := Release{Tag: doc.TagName, NotesURL: doc.HTMLURL, Assets: map[string]string{}}
	for _, a := range doc.Assets {
		r.Assets[a.Name] = a.URL
	}
	return r, nil
}

// Latest 取得 repo(如 "homieyangg/ccquota")的最新 release。
func Latest(ctx context.Context, repo string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Release{}, err
	}
	return parseRelease(b)
}

// AssetName 回傳本平台對應的 release 資產名。
func AssetName() string {
	return fmt.Sprintf("ccquota-%s-%s", runtime.GOOS, runtime.GOARCH)
}

// Newer 回報 latest 是否比 current 新。current 為 "dev" 或空時,只要 latest 合法即視為有更新。
func Newer(current, latest string) bool {
	latest = strings.TrimPrefix(strings.TrimSpace(latest), "v")
	if latest == "" {
		return false
	}
	cur := strings.TrimPrefix(strings.TrimSpace(current), "v")
	if cur == "" || cur == "dev" {
		return true
	}
	return compareSemver(latest, cur) > 0
}

// compareSemver 比較 a、b(僅取 major.minor.patch,忽略 pre-release 標記)。
func compareSemver(a, b string) int {
	ap := strings.Split(strings.SplitN(a, "-", 2)[0], ".")
	bp := strings.Split(strings.SplitN(b, "-", 2)[0], ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(ap) {
			x, _ = strconv.Atoi(ap[i])
		}
		if i < len(bp) {
			y, _ = strconv.Atoi(bp[i])
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

// checksumFor 從 SHA256SUMS 內容找出 asset 的雜湊(格式:"<hex>  <name>")。
func checksumFor(sums, asset string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == asset {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found", asset)
}

// Apply 下載 rel 對應本平台的 binary,驗 checksum,原子替換目前執行檔。
// 成功後呼叫端可用 Restart() re-exec 套用新版。
func Apply(ctx context.Context, rel Release) error {
	name := AssetName()
	binURL := rel.Assets[name]
	if binURL == "" {
		return fmt.Errorf("no asset %s in release %s", name, rel.Tag)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, e := filepath.EvalSymlinks(self); e == nil {
		self = resolved
	}

	tmp := self + ".new"
	if err := download(ctx, binURL, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)

	if sumURL := rel.Assets["SHA256SUMS"]; sumURL != "" {
		sums, err := fetchText(ctx, sumURL)
		if err != nil {
			return err
		}
		want, err := checksumFor(sums, name)
		if err != nil {
			return err
		}
		got, err := sha256File(tmp)
		if err != nil {
			return err
		}
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("checksum mismatch: got %s want %s", got, want)
		}
	} else {
		return fmt.Errorf("release has no SHA256SUMS; refusing unverified update")
	}

	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, self)
}

// Restart re-exec 目前執行檔(同 PID,systemd Type=simple 無感)。
func Restart() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(self, os.Args, os.Environ())
}

func download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(resp.Body, maxDownload))
	return err
}

func fetchText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
