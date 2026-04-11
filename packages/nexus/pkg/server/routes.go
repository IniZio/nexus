package server

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/server/portal"
)

func (s *Server) Start() error {
	if s.lifecycle != nil {
		if err := s.lifecycle.RunPostStart(); err != nil {
			log.Printf("[lifecycle] Post-start hook error: %v", err)
		}
	}

	mux := s.routes()
	addr := fmt.Sprintf(":%d", s.port)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)

	if devUI := os.Getenv("NEXUS_DEV_UI"); devUI != "" {
		target, err := url.Parse(strings.TrimRight(devUI, "/"))
		if err != nil {
			log.Printf("[portal] invalid NEXUS_DEV_UI %q: %v", devUI, err)
		} else {
			proxy := httputil.NewSingleHostReverseProxy(target)
			origDirector := proxy.Director
			proxy.Director = func(req *http.Request) {
				origDirector(req)
				req.Host = target.Host
			}
			proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
				log.Printf("[portal-dev] proxy error for %s: %v", r.URL.Path, err)
				http.Error(w, "Vite dev server unavailable — is `task dev:ui` running?", http.StatusBadGateway)
			}
			mux.Handle("/ui/static/", proxy)
			mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/ui/static/"
				proxy.ServeHTTP(w, r2)
			})
			mux.HandleFunc("/ui/", func(w http.ResponseWriter, r *http.Request) {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/ui/static/"
				proxy.ServeHTTP(w, r2)
			})
			mux.Handle("/@vite/", proxy)
			mux.Handle("/@fs/", proxy)
			mux.Handle("/node_modules/", proxy)
			mux.HandleFunc("/portal/", s.handlePortalUI)
			mux.HandleFunc("/portal", s.handlePortalUI)
			mux.HandleFunc("/", s.handleWebSocket)
			return mux
		}
	}

	if os.Getenv("NEXUS_DEV_UI") == "" {
		log.Printf("[portal] NEXUS_DEV_UI not set; serving embedded UI assets. For hot reload, run daemon with NEXUS_DEV_UI=http://localhost:5173")
	}

	mux.Handle("/portal/static/", http.StripPrefix("/portal/", http.FileServer(http.FS(portal.FS))))
	if uiDist, err := fs.Sub(portal.FS, "ui_dist"); err == nil {
		mux.Handle("/ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(uiDist))))
	} else if staticDist, staticErr := fs.Sub(portal.FS, "static"); staticErr == nil {
		mux.Handle("/ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(staticDist))))
	}
	mux.HandleFunc("/portal/", s.handlePortalUI)
	mux.HandleFunc("/portal", s.handlePortalUI)
	mux.HandleFunc("/ui/", s.handlePortalUI)
	mux.HandleFunc("/ui", s.handlePortalUI)

	mux.HandleFunc("/", s.handleWebSocket)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true,"service":"workspace-daemon"}`))
}

func (s *Server) handlePortalUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/ui/api") {
		http.NotFound(w, r)
		return
	}

	f, err := portal.FS.Open("ui_dist/index.html")
	if err != nil {
		f, err = portal.FS.Open("static/index.html")
	}
	if err != nil {
		http.Error(w, "portal unavailable", http.StatusInternalServerError)
		log.Printf("[portal] failed to open ui_dist index: %v", err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, copyErr := io.Copy(w, f); copyErr != nil {
		log.Printf("[portal] failed to serve ui_dist index: %v", copyErr)
	}
}
