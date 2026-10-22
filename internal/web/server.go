// Package web implements the HTTP console: routes, form handling, result
// rendering and artifact downloads.
package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"np2/appgo/internal/config"
	"np2/appgo/internal/modules"
	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// artifactLink is an artifact exposed to the browser.
type artifactLink struct {
	ID       string
	Name     string
	Filename string
	Note     string
	Size     int
}

// view is the data model handed to every template.
type view struct {
	Title  string
	Active string
	Target string
	Mode   string

	Flash string
	Error string

	Report *op.Report

	// Result extras.
	Links []artifactLink

	// Page specific data.
	Installed       []string
	InstalledState  string
	Users           []modules.User
	Services        []modules.Service
	Backends        []modules.Backend
	CurrentHostname string
	HostsContent    string
	IssueContent    string
	SSHStatus       string
	Preview         string
	Form            map[string]string
	Fields          []string
}

// Server wires the modules to HTTP handlers.
type Server struct {
	cfg       config.Config
	runner    runner.Runner
	pkgs      *modules.PackagesManager
	rootSSH   *modules.RootSSHManager
	issue     *modules.IssueManager
	hostname  *modules.HostnameManager
	users     *modules.UserManager
	systemd   *modules.SystemdManager
	haproxy   *modules.HAProxyManager
	templates map[string]*template.Template
	artifacts *artifactStore
}

// New builds the server for a configuration and transport.
func New(cfg config.Config, r runner.Runner) (*Server, error) {
	s := &Server{
		cfg:       cfg,
		runner:    r,
		pkgs:      modules.NewPackagesManager(r),
		rootSSH:   modules.NewRootSSHManager(r),
		issue:     modules.NewIssueManager(r),
		hostname:  modules.NewHostnameManager(r),
		users:     modules.NewUserManager(r),
		systemd:   modules.NewSystemdManager(r),
		haproxy:   modules.NewHAProxyManager(r, cfg.HAProxy.ConfigPath, cfg.HAProxy.Service),
		artifacts: newArtifactStore(30 * time.Minute),
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

var pages = []string{
	"dashboard", "packages", "rootssh", "issue", "hostname",
	"users", "services", "haproxy",
}

func (s *Server) parseTemplates() error {
	funcs := template.FuncMap{
		"join":  strings.Join,
		"lower": strings.ToLower,
	}
	s.templates = make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		t, err := template.New(p).Funcs(funcs).ParseFS(templateFS,
			"templates/layout.html", "templates/partials.html", "templates/"+p+".html")
		if err != nil {
			return fmt.Errorf("parse template %s: %w", p, err)
		}
		s.templates[p] = t
	}
	return nil
}

// Handler returns the complete HTTP handler, including static assets and
// optional basic authentication.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handleDashboard)

	mux.HandleFunc("GET /sistema/paquetes", s.handlePackages)
	mux.HandleFunc("POST /sistema/paquetes/instalar", s.handlePackageInstall)
	mux.HandleFunc("POST /sistema/paquetes/eliminar", s.handlePackageRemove)

	mux.HandleFunc("GET /sistema/ssh-root", s.handleRootSSH)
	mux.HandleFunc("POST /sistema/ssh-root/configurar", s.handleRootSSHConfigure)

	mux.HandleFunc("GET /sistema/issue", s.handleIssue)
	mux.HandleFunc("POST /sistema/issue/vista-previa", s.handleIssuePreview)
	mux.HandleFunc("POST /sistema/issue/aplicar", s.handleIssueApply)

	mux.HandleFunc("GET /sistema/hostname", s.handleHostname)
	mux.HandleFunc("POST /sistema/hostname/asignar", s.handleHostnameSet)

	mux.HandleFunc("GET /usuarios", s.handleUsers)
	mux.HandleFunc("POST /usuarios/crear", s.handleUserCreate)
	mux.HandleFunc("POST /usuarios/eliminar", s.handleUserDelete)

	mux.HandleFunc("GET /servicios", s.handleServices)
	mux.HandleFunc("POST /servicios/accion", s.handleServiceAction)
	mux.HandleFunc("POST /servicios/desplegar", s.handleServiceDeploy)
	mux.HandleFunc("POST /servicios/eliminar", s.handleServiceRemove)

	mux.HandleFunc("GET /haproxy", s.handleHAProxy)
	mux.HandleFunc("POST /haproxy/agregar", s.handleHAProxyAdd)
	mux.HandleFunc("POST /haproxy/eliminar", s.handleHAProxyRemove)

	mux.HandleFunc("GET /descargas/{id}", s.handleDownload)
	mux.HandleFunc("GET /salud", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "ok target=%s mode=%s\n", s.runner.Target(), s.runner.Kind())
	})

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("static assets: %v", err)
	}
	mux.Handle("GET /estaticos/", http.StripPrefix("/estaticos/", http.FileServer(http.FS(staticSub))))

	var h http.Handler = mux
	h = s.recoverer(h)
	h = s.logRequests(h)
	if s.cfg.AuthUser != "" {
		h = s.basicAuth(h)
	}
	return h
}

// Close releases the transport.
func (s *Server) Close() error { return s.runner.Close() }

func (s *Server) newView(page, title, active string) *view {
	return &view{
		Title:  title,
		Active: active,
		Target: s.runner.Target(),
		Mode:   string(s.runner.Kind()),
		Form:   map[string]string{},
	}
}

func (s *Server) render(w http.ResponseWriter, page string, v *view) {
	t, ok := s.templates[page]
	if !ok {
		http.Error(w, "template no encontrada: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", v); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

// finalize stores report artifacts and prepares the links shown to the user.
func (s *Server) finalize(v *view) {
	if v.Report == nil {
		return
	}
	v.Links = s.artifacts.put(v.Report.Artifacts)
	if !v.Report.OK {
		v.Error = v.Report.Summary
	}
}

// opContext derives the execution context for a request.
func (s *Server) opContext(r *http.Request) (context.Context, context.CancelFunc) {
	timeout := s.cfg.CommandTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return context.WithTimeout(r.Context(), timeout)
}

// --- middleware --------------------------------------------------------------

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic en %s %s: %v", r.Method, r.URL.Path, rec)
				http.Error(w, "Error interno del servidor. Revise los registros de la consola.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s) target=%s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond), s.runner.Target())
	})
}

func (s *Server) basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != s.cfg.AuthUser || pass != s.cfg.AuthPass {
			w.Header().Set("WWW-Authenticate", `Basic realm="Consola NP2"`)
			http.Error(w, "autenticación requerida", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- artifact store ----------------------------------------------------------

type storedArtifact struct {
	artifact  op.Artifact
	expiresAt time.Time
}

type artifactStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]storedArtifact
}

func newArtifactStore(ttl time.Duration) *artifactStore {
	return &artifactStore{ttl: ttl, items: make(map[string]storedArtifact)}
}

func (a *artifactStore) put(arts []op.Artifact) []artifactLink {
	if len(arts) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.purgeLocked()
	links := make([]artifactLink, 0, len(arts))
	for _, art := range arts {
		id := randomID()
		a.items[id] = storedArtifact{artifact: art, expiresAt: time.Now().Add(a.ttl)}
		links = append(links, artifactLink{
			ID:       id,
			Name:     art.Name,
			Filename: art.Filename,
			Note:     art.Note,
			Size:     len(art.Data),
		})
	}
	return links
}

func (a *artifactStore) get(id string) (op.Artifact, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	item, ok := a.items[id]
	if !ok || time.Now().After(item.expiresAt) {
		delete(a.items, id)
		return op.Artifact{}, false
	}
	return item.artifact, true
}

func (a *artifactStore) purgeLocked() {
	now := time.Now()
	for id, item := range a.items {
		if now.After(item.expiresAt) {
			delete(a.items, id)
		}
	}
}

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
