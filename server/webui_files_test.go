package server

import (
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestWebUIFileCachePolicy(t *testing.T) {
	files := fstest.MapFS{
		"_next/static/chunks/hash.js": {Data: []byte("console.log(1)")},
		"index.html":                  {Data: []byte("<html></html>")},
		"tasks/__next._tree.txt":      {Data: []byte("route")},
		"logo.png":                    {Data: []byte("image")},
	}
	for _, name := range []string{"_next/static/chunks/hash.js", "index.html", "tasks/__next._tree.txt", "logo.png", "missing.js"} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			// Use / for index.html to avoid ServeFileFS's canonical URL redirect.
			r := httptest.NewRequest("GET", "/", nil)
			found := serveFileIfExists(w, r, files, name)
			want := "no-cache"
			if name == "_next/static/chunks/hash.js" {
				want = "public, max-age=31536000, immutable"
			}
			if name == "missing.js" {
				want = ""
			}
			if found != (name != "missing.js") || w.Header().Get("Cache-Control") != want {
				t.Fatalf("found=%v cache=%q, want cache=%q", found, w.Header().Get("Cache-Control"), want)
			}
		})
	}
}
