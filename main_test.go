package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tok = "0123456789abcdef0123456789abcdef0123456789abcdef"

func newTestServer(t *testing.T, imgproxy string) *server {
	t.Helper()
	s := &server{key: []byte("k"), salt: []byte("s"), client: http.DefaultClient}
	s.cfg.Store(&config{Imgproxy: imgproxy, Tenants: []tenant{
		{ID: "a", Token: tok, Enabled: true, Domains: []string{"example.com", "*.cdn.example.com"}, Buckets: []string{"pub"}},
		{ID: "off", Token: strings.Repeat("f", 48), Enabled: false},
	}})
	return s
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func get(s *server, path, accept string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestRedirect(t *testing.T) {
	s := newTestServer(t, "")
	cases := []struct{ src, query, accept, want string }{
		{"https://example.com/a.png", "?w=256", "image/avif,image/webp,*/*", "/w256_q75.avif"},
		{"https://example.com/a.png", "?w=257", "image/webp,*/*", "/w384_q75.webp"},
		{"https://example.com/a.png", "?w=256&q=50", "*/*", "/w256_q50.png"},
		{"https://x.cdn.example.com/a.jpg", "?w=9999", "*/*", "/w3840_q75.jpg"},
		{"s3://pub/k.jpg", "?w=640", "", "/w640_q75.jpg"},
	}
	for _, c := range cases {
		w := get(s, "/t/"+tok+"/image/"+b64(c.src)+c.query, c.accept)
		if w.Code != http.StatusFound || !strings.HasSuffix(w.Header().Get("Location"), c.want) {
			t.Errorf("%s%s: got %d %q, want suffix %q", c.src, c.query, w.Code, w.Header().Get("Location"), c.want)
		}
		if w.Header().Get("Vary") != "Accept" {
			t.Errorf("missing Vary: Accept")
		}
	}
}

func TestRejects(t *testing.T) {
	s := newTestServer(t, "")
	cases := []struct {
		path string
		code int
		body string
	}{
		{"/t/" + strings.Repeat("0", 48) + "/image/" + b64("https://example.com/a.png") + "?w=256", 403, "Unknown or disabled tenant"},
		{"/t/" + strings.Repeat("f", 48) + "/image/" + b64("https://example.com/a.png") + "?w=256", 403, "Unknown or disabled tenant"},
		{"/t/" + tok + "/image/" + b64("https://evil.com/a.png") + "?w=256", 403, "Source domain not allowed: evil.com"},
		{"/t/" + tok + "/image/" + b64("https://cdn.example.com.evil.com/a.png") + "?w=256", 403, "Source domain not allowed: cdn.example.com.evil.com"},
		{"/t/" + tok + "/image/" + b64("s3://private/k.jpg") + "?w=256", 403, "Source bucket not allowed: private"},
		{"/t/" + tok + "/image/" + b64("file:///etc/passwd") + "?w=256", 400, "INVALID_SOURCE"},
		{"/t/" + tok + "/image/" + b64("https://example.com/a.png") + "/w255_q75.avif", 404, "NOT_FOUND"},
		{"/t/" + tok + "/image/" + b64("https://example.com/a.png") + "?q=0", 400, "INVALID_QUALITY"},
		{"/", 404, "NOT_FOUND"},
	}
	for _, c := range cases {
		w := get(s, c.path, "")
		if w.Code != c.code || w.Body.String() != c.body {
			t.Errorf("%s: got %d %q, want %d %q", c.path, w.Code, w.Body.String(), c.code, c.body)
		}
	}
}

func TestServeImage(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "image/avif")
		w.Write([]byte("IMG"))
	}))
	defer up.Close()
	s := newTestServer(t, up.URL)
	p := "/t/" + tok + "/image/" + b64("s3://pub/k.jpg") + "/w640_q75.avif"
	w := get(s, p, "")
	if w.Code != 200 || w.Body.String() != "IMG" || w.Header().Get("Content-Type") != "image/avif" {
		t.Fatalf("got %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if !strings.Contains(gotPath, "/rs:fit:640:0/q:75/"+b64("s3://pub/k.jpg")+".avif") {
		t.Errorf("imgproxy path %q", gotPath)
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "immutable") || w.Header().Get("ETag") == "" {
		t.Errorf("headers %v", w.Header())
	}
	r := httptest.NewRequest(http.MethodGet, p, nil)
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w2 := httptest.NewRecorder()
	s.ServeHTTP(w2, r)
	if w2.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: got %d", w2.Code)
	}
}
