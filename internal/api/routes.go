package api

import "net/http"

// Register は API の経路を mux に登録する。main.go とテストの両方から同じ表を使う。
func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", Health)
}
