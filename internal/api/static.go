package api

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"investhub/internal/web"
)

// webFS resolves to the embedded SPA, unless INVESTHUB_WEB_DIR points at a
// directory on disk (handy while developing the frontend).
var webFS fs.FS = func() fs.FS {
	if dir := os.Getenv("INVESTHUB_WEB_DIR"); dir != "" {
		return os.DirFS(dir)
	}
	return web.FS()
}()

var mimeByExt = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".svg":   "image/svg+xml",
	".ico":   "image/x-icon",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".webp":  "image/webp",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".map":   "application/json; charset=utf-8",
}

// serveStatic ships the SPA with a history-fallback to index.html.
func serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(w, 40401, "接口不存在: "+r.Method+" "+r.URL.Path)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	if strings.HasPrefix(name, "..") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if writeFile(w, name) {
		return
	}
	if !writeFile(w, "index.html") {
		http.NotFound(w, r)
	}
}

func writeFile(w http.ResponseWriter, name string) bool {
	f, err := webFS.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.IsDir() {
		return false
	}
	ct := mimeByExt[strings.ToLower(path.Ext(name))]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	_, _ = io.Copy(w, f) // best-effort; HTTP connection errors surface at the transport layer
	return true
}
