package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var staticFS embed.FS

// assetVersion 是所有靜態檔內容的短雜湊。注入 index.html 的資產 URL(?v=),
// 內容一變雜湊就變,URL 跟著變,自動讓 CDN / 瀏覽器快取失效(部署後立即生效)。
var assetVersion string

// indexHTML 是把 __V__ 佔位換成 assetVersion 後的首頁。
var indexHTML []byte

func init() {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	// 以所有靜態檔內容算雜湊,任一資產變動都會改版本
	h := sha256.New()
	_ = fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, e := fs.ReadFile(sub, path)
		if e != nil {
			return e
		}
		h.Write([]byte(path))
		h.Write(b)
		return nil
	})
	assetVersion = hex.EncodeToString(h.Sum(nil))[:10]

	raw, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic(err)
	}
	indexHTML = []byte(strings.ReplaceAll(string(raw), "__V__", assetVersion))
}

// Handler 提供 embed 的靜態檔。index.html(/ 與 /index.html)會注入資產版本;
// 帶 ?v= 的資產設長快取(內容雜湊保證唯一),其餘設 no-cache 讓 CDN 重新驗證。
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(indexHTML)
			return
		}
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
	return mux
}
