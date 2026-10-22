// mediacdn-gateway: tenant-scoped image URLs in front of imgproxy.
//
//	GET /t/<token>/image/<b64url(source)>?w=640&q=75  -> 302 /t/<token>/image/<b64>/w640_q75.<avif|webp|jpg|png> (by Accept)
//	GET /t/<token>/image/<b64>/w640_q75.avif           -> image, immutable, ETag
//
// source is https://<allowed domain>/... or s3://<allowed bucket>/key, per tenant.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Next.js default imageSizes + deviceSizes.
var widths = []int{16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840}

const (
	defaultQuality = 75
	maxBody        = 32 << 20
)

type tenant struct {
	ID      string   `json:"id"`
	Token   string   `json:"token"`
	Enabled bool     `json:"enabled"`
	Domains []string `json:"domains"`
	Buckets []string `json:"buckets"`
}

type config struct {
	Listen   string   `json:"listen"`
	Imgproxy string   `json:"imgproxy"`
	Tenants  []tenant `json:"tenants"`
}

type server struct {
	cfgPath   string
	cfg       atomic.Pointer[config]
	key, salt []byte
	client    *http.Client
}

var (
	reRedirect  = regexp.MustCompile(`^/t/([0-9a-f]{48})/image/([A-Za-z0-9_-]+={0,2})$`)
	reCanonical = regexp.MustCompile(`^/t/([0-9a-f]{48})/image/([A-Za-z0-9_-]+={0,2})/w(\d+)_q(\d+)\.(avif|webp|jpg|png)$`)
)

func main() {
	cfgPath := "/etc/mediacdn/gateway.json"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	key, err := hex.DecodeString(os.Getenv("IMGPROXY_KEY"))
	if err != nil || len(key) == 0 {
		log.Fatal("IMGPROXY_KEY missing or not hex")
	}
	salt, err := hex.DecodeString(os.Getenv("IMGPROXY_SALT"))
	if err != nil || len(salt) == 0 {
		log.Fatal("IMGPROXY_SALT missing or not hex")
	}
	s := &server{cfgPath: cfgPath, key: key, salt: salt, client: &http.Client{Timeout: 30 * time.Second}}
	if err := s.load(); err != nil {
		log.Fatal(err)
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := s.load(); err != nil {
				log.Printf("reload failed, keeping old config: %v", err)
			} else {
				log.Printf("config reloaded")
			}
		}
	}()
	srv := &http.Server{Addr: s.cfg.Load().Listen, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}

func (s *server) load() error {
	b, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return err
	}
	var c config
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	if c.Listen == "" {
		c.Listen = ":8090"
	}
	if c.Imgproxy == "" {
		c.Imgproxy = "http://127.0.0.1:8080"
	}
	s.cfg.Store(&c)
	return nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		text(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if r.URL.Path == "/healthz" {
		text(w, http.StatusOK, "OK")
		return
	}
	if m := reCanonical.FindStringSubmatch(r.URL.Path); m != nil {
		s.serveImage(w, r, m[1], m[2], m[3], m[4], m[5])
		return
	}
	if m := reRedirect.FindStringSubmatch(r.URL.Path); m != nil {
		s.redirect(w, r, m[1], m[2])
		return
	}
	text(w, http.StatusNotFound, "NOT_FOUND")
}

func (s *server) redirect(w http.ResponseWriter, r *http.Request, token, b64 string) {
	src, ok := s.authorize(w, token, b64)
	if !ok {
		return
	}
	width := 0
	if v := r.URL.Query().Get("w"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			text(w, http.StatusBadRequest, "INVALID_WIDTH")
			return
		}
		width = snapWidth(n)
	} else {
		width = widths[len(widths)-1]
	}
	q := defaultQuality
	if v := r.URL.Query().Get("q"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			text(w, http.StatusBadRequest, "INVALID_QUALITY")
			return
		}
		q = n
	}
	h := w.Header()
	h.Set("Location", fmt.Sprintf("/t/%s/image/%s/w%d_q%d.%s", token, b64, width, q, pickFormat(r.Header.Get("Accept"), src)))
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("Vary", "Accept")
	w.WriteHeader(http.StatusFound)
}

func (s *server) serveImage(w http.ResponseWriter, r *http.Request, token, b64, ws, qs, ext string) {
	src, ok := s.authorize(w, token, b64)
	if !ok {
		return
	}
	width, _ := strconv.Atoi(ws)
	q, _ := strconv.Atoi(qs)
	if !slices.Contains(widths, width) || q < 1 || q > 100 {
		text(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	path := fmt.Sprintf("/rs:fit:%d:0/q:%d/%s.%s", width, q, base64.RawURLEncoding.EncodeToString([]byte(src)), ext)
	mac := hmac.New(sha256.New, s.key)
	mac.Write(s.salt)
	mac.Write([]byte(path))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.Load().Imgproxy+"/"+sig+path, nil)
	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("imgproxy: %v", err)
		text(w, http.StatusBadGateway, "UPSTREAM_ERROR")
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		text(w, http.StatusNotFound, "NOT_FOUND")
		return
	default:
		log.Printf("imgproxy %d for %s", resp.StatusCode, src)
		text(w, http.StatusBadGateway, "UPSTREAM_ERROR")
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		text(w, http.StatusBadGateway, "UPSTREAM_ERROR")
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	h := w.Header()
	h.Set("Content-Type", resp.Header.Get("Content-Type"))
	h.Set("Cache-Control", "public, s-maxage=604800, max-age=31536000, immutable")
	h.Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		io.Copy(w, bytes.NewReader(body))
	}
}

// authorize resolves the tenant and decoded source, writing the error response itself when it fails.
func (s *server) authorize(w http.ResponseWriter, token, b64 string) (string, bool) {
	var t *tenant
	for i := range s.cfg.Load().Tenants {
		if c := &s.cfg.Load().Tenants[i]; hmac.Equal([]byte(c.Token), []byte(token)) {
			t = c
			break
		}
	}
	if t == nil || !t.Enabled {
		text(w, http.StatusForbidden, "Unknown or disabled tenant")
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(b64, "="))
	if err != nil {
		text(w, http.StatusBadRequest, "INVALID_SOURCE")
		return "", false
	}
	u, err := url.Parse(string(raw))
	if err != nil || u.Host == "" {
		text(w, http.StatusBadRequest, "INVALID_SOURCE")
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	switch u.Scheme {
	case "https", "http":
		if !domainAllowed(t.Domains, host) {
			text(w, http.StatusForbidden, "Source domain not allowed: "+host)
			return "", false
		}
	case "s3":
		if !slices.Contains(t.Buckets, host) {
			text(w, http.StatusForbidden, "Source bucket not allowed: "+host)
			return "", false
		}
	default:
		text(w, http.StatusBadRequest, "INVALID_SOURCE")
		return "", false
	}
	return string(raw), true
}

func domainAllowed(domains []string, host string) bool {
	for _, d := range domains {
		d = strings.ToLower(d)
		if host == d || (strings.HasPrefix(d, "*.") && strings.HasSuffix(host, d[1:])) {
			return true
		}
	}
	return false
}

func snapWidth(n int) int {
	for _, v := range widths {
		if v >= n {
			return v
		}
	}
	return widths[len(widths)-1]
}

// pickFormat: AVIF/WebP when the browser accepts them, else PNG for sources that may carry alpha, else JPEG.
func pickFormat(accept, src string) string {
	accept = strings.ToLower(accept)
	switch {
	case strings.Contains(accept, "image/avif"):
		return "avif"
	case strings.Contains(accept, "image/webp"):
		return "webp"
	}
	p := strings.ToLower(src)
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if strings.HasSuffix(p, ".png") || strings.HasSuffix(p, ".gif") || strings.HasSuffix(p, ".svg") || strings.HasSuffix(p, ".webp") {
		return "png"
	}
	return "jpg"
}

func text(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	w.WriteHeader(code)
	io.WriteString(w, msg)
}
