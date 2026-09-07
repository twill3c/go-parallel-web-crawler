package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/crawler"
	"github.com/twill3c/go-parallel-web-crawler/internal/model"
	"github.com/twill3c/go-parallel-web-crawler/internal/security"
)

// 入力の境界(SPEC §2.5)。
const (
	MinWorkers, MaxWorkers       = 1, 20
	MinPages, MaxPages           = 1, 100
	MinDelayMs, MaxDelayMs       = 0, 5000
	DefaultWorkers               = 5
	DefaultMaxPages              = 50
	DefaultDelayMs               = 200
	maxRequestBody         int64 = 4096
)

// Server は API の状態(台帳・検証器)。
type Server struct {
	Validator *security.Validator
	registry  *registry
	// AllowPrivate はテスト(httptest = 127.0.0.1)のためだけにある。実サーバでは false のまま。
	AllowPrivate bool
}

// NewServer は既定の Server。
func NewServer() *Server {
	return &Server{Validator: &security.Validator{}, registry: newRegistry()}
}

// Register は API の経路を mux に登録する。
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", Health)
	mux.HandleFunc("POST /api/crawl", s.Crawl)
	mux.HandleFunc("GET /api/crawl/{id}", s.Result)
	mux.HandleFunc("POST /api/crawl/{id}/stop", s.Stop)
}

// Register は main.go 用の便宜関数(既定の Server)。
func Register(mux *http.ServeMux) { NewServer().Register(mux) }

// CrawlRequest は POST /api/crawl の本体(SPEC F-21)。
type CrawlRequest struct {
	URL            string `json:"url"`
	Workers        int    `json:"workers"`
	MaxPages       int    `json:"maxPages"`
	RequestDelayMs int    `json:"requestDelayMs"`
	// IgnoreRobots が真のときだけ robots.txt を無視する。**既定は従う**ので、
	// 欄が無い古いクライアントからの要求も従う側になる(安全側が既定)
	IgnoreRobots bool `json:"ignoreRobots"`
	// UseSitemap はサイトマップから種 URL を足すか(原本 §34 B)。**既定は使わない** ——
	// 足すと対象サイトへのリクエストが 1 件増え、辿る範囲も広がるので、頼まれたときだけ行う
	UseSitemap bool `json:"useSitemap"`
}

// validate は境界(§2.5)と URL(§2.4)を検査し、crawler.Config を組む。
func (s *Server) validate(ctx context.Context, req CrawlRequest) (crawler.Config, error) {
	var cfg crawler.Config
	if req.Workers < MinWorkers || req.Workers > MaxWorkers {
		return cfg, fmt.Errorf("workers must be %d..%d", MinWorkers, MaxWorkers)
	}
	if req.MaxPages < MinPages || req.MaxPages > MaxPages {
		return cfg, fmt.Errorf("maxPages must be %d..%d", MinPages, MaxPages)
	}
	if req.RequestDelayMs < MinDelayMs || req.RequestDelayMs > MaxDelayMs {
		return cfg, fmt.Errorf("requestDelayMs must be %d..%d", MinDelayMs, MaxDelayMs)
	}
	if strings.TrimSpace(req.URL) == "" {
		return cfg, errors.New("url is required")
	}
	var startURL string
	if s.AllowPrivate {
		startURL = req.URL
	} else {
		u, err := s.Validator.ValidateURL(ctx, req.URL)
		if err != nil {
			return cfg, fmt.Errorf("url rejected: %w", err)
		}
		startURL = u.String()
	}
	if _, err := crawler.Normalize(startURL); err != nil {
		return cfg, fmt.Errorf("url is invalid: %v", err)
	}
	cfg = crawler.Config{
		StartURL:      startURL,
		Workers:       req.Workers,
		MaxPages:      req.MaxPages,
		RequestDelay:  time.Duration(req.RequestDelayMs) * time.Millisecond,
		Timeout:       crawler.DefaultTimeout,
		Deadline:      crawler.DefaultDeadline,
		RespectRobots: !req.IgnoreRobots,
		UseSitemap:    req.UseSitemap,
	}
	if s.AllowPrivate {
		cfg.Client = crawler.NewClient(startURL, crawler.ClientOptions{Timeout: cfg.Timeout, AllowPrivate: true})
	}
	return cfg, nil
}

// Crawl は POST /api/crawl。検証に通ればその場で SSE を流し始め、crawl_completed で閉じる(D-03)。
//
//	Browser ── POST ──▶ Crawl ──▶ crawler.Run ──▶ events ──▶ SSE ──▶ Browser
//	Browser ── abort ─▶ r.Context() が cancel ─▶ Run の ctx が cancel ─▶ 全 Worker 停止(F-09)
//	Browser ── /stop ─▶ registry.stop ─────────▶ 同じ ctx が cancel
func (s *Server) Crawl(w http.ResponseWriter, r *http.Request) {
	var req CrawlRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil || json.Unmarshal(body, &req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body must be json {url, workers, maxPages, requestDelayMs}"})
		return
	}
	cfg, err := s.validate(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithCancel(r.Context()) // 切断でも /stop でも同じ ctx が cancel される
	defer cancel()
	cfg.CrawlID = s.registry.start(cancel)

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sse := newSSEWriter(w)

	res := crawler.Run(ctx, cfg, func(e model.Event) { sse.send(e) })
	s.registry.finish(cfg.CrawlID, res)
}

// Stop は POST /api/crawl/{id}/stop(F-23)。
func (s *Server) Stop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.registry.stop(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown crawlId"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping", "crawlId": id})
}

// Result は GET /api/crawl/{id}(F-24)。
func (s *Server) Result(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, ok := s.registry.result(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown crawlId"})
		return
	}
	if res.Pages == nil {
		res.Pages = []model.Page{}
	}
	if res.Links == nil {
		res.Links = []model.Link{}
	}
	writeJSON(w, http.StatusOK, res)
}
