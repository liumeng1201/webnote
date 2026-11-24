package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

//go:embed web
var webFS embed.FS

// ---------- server ----------

type Server struct {
	cfg     *Config
	store   *Store
	limiter *limiter
	secret  []byte
}

func NewServer(cfg *Config, store *Store) *Server {
	secret, _ := hex.DecodeString(cfg.Secret)
	return &Server{cfg: cfg, store: store, limiter: newLimiter(), secret: secret}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// pages & static
	mux.HandleFunc("GET /admin", s.pageAdmin)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><rect width="100" height="100" rx="20" fill="#4f46e5"/><text x="50" y="68" font-size="52" text-anchor="middle" fill="#fff" font-family="sans-serif">W</text></svg>`))
	})
	staticSub, _ := fs.Sub(webFS, "web")
	staticSrv := http.StripPrefix("/static/", http.FileServer(http.FS(staticSub)))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// embedded files carry no modtime, so without this header browsers
		// heuristically cache stale JS/CSS across deployments
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		staticSrv.ServeHTTP(w, r)
	}))
	// space API
	mux.HandleFunc("GET /api/spaces/{name}/state", s.apiState)
	mux.HandleFunc("POST /api/spaces/{name}/unlock", s.apiUnlock)
	mux.HandleFunc("POST /api/spaces/{name}/password", s.apiSetPassword)
	mux.HandleFunc("POST /api/spaces/{name}/readonly", s.apiSetReadonly)
	mux.HandleFunc("PUT /api/spaces/{name}/settings", s.apiSettings)
	mux.HandleFunc("DELETE /api/spaces/{name}", s.apiDeleteSpace)
	mux.HandleFunc("POST /api/spaces/{name}/tabs", s.apiCreateTab)
	mux.HandleFunc("PUT /api/spaces/{name}/tabs/{id}", s.apiSaveTab)
	mux.HandleFunc("DELETE /api/spaces/{name}/tabs/{id}", s.apiDeleteTab)
	mux.HandleFunc("POST /api/spaces/{name}/tabs/order", s.apiOrderTabs)
	mux.HandleFunc("POST /api/spaces/{name}/files/init", s.apiFileInit)
	mux.HandleFunc("PUT /api/spaces/{name}/files/{upload}/chunk", s.apiFileChunk)
	mux.HandleFunc("POST /api/spaces/{name}/files/{upload}/complete", s.apiFileComplete)
	mux.HandleFunc("DELETE /api/spaces/{name}/uploads/{upload}", s.apiFileAbort)
	mux.HandleFunc("GET /api/spaces/{name}/files/{id}/download", s.apiFileDownload)
	mux.HandleFunc("DELETE /api/spaces/{name}/files/{id}", s.apiFileDelete)
	// admin API
	mux.HandleFunc("POST /api/admin/login", s.apiAdminLogin)
	mux.HandleFunc("POST /api/admin/logout", s.apiAdminLogout)
	mux.HandleFunc("GET /api/admin/overview", s.apiAdminOverview)
	mux.HandleFunc("GET /api/admin/backup", s.apiAdminBackup)
	mux.HandleFunc("DELETE /api/admin/spaces/{name}", s.apiAdminDeleteSpace)
	mux.HandleFunc("DELETE /api/admin/spaces/{name}/tabs/{id}", s.apiAdminDeleteTab)
	mux.HandleFunc("DELETE /api/admin/spaces/{name}/files/{id}", s.apiAdminDeleteFile)
	// home & space pages (catch-all)
	mux.HandleFunc("GET /{rest...}", s.pageHomeOrSpace)
	return mux
}

// ---------- small helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func readBodyJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不正确")
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip, _, _ := strings.Cut(xff, ","); ip != "" {
			return strings.TrimSpace(ip)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------- rate limiter ----------

type attempt struct {
	fails       int
	lockedUntil time.Time
}

type limiter struct {
	mu sync.Mutex
	m  map[string]*attempt
}

func newLimiter() *limiter { return &limiter{m: map[string]*attempt{}} }

func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.m[key]
	return a != nil && time.Now().Before(a.lockedUntil)
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.m[key]
	if a == nil {
		a = &attempt{}
		l.m[key] = a
	}
	a.fails++
	if a.fails >= 8 {
		a.lockedUntil = time.Now().Add(5 * time.Minute)
		a.fails = 0
	}
}

func (l *limiter) ok(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, key)
}

// ---------- session tokens ----------

type sessionPayload struct {
	T   string `json:"t"` // "s" space, "a" admin
	N   string `json:"n,omitempty"`
	Exp int64  `json:"e"`
}

func (s *Server) signToken(p sessionPayload) string {
	raw, _ := json.Marshal(p)
	sig := hmac.New(sha256.New, s.secret)
	sig.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(sig.Sum(nil))
}

func (s *Server) parseToken(tok string) (sessionPayload, bool) {
	var p sessionPayload
	rawPart, sigPart, ok := strings.Cut(tok, ".")
	if !ok {
		return p, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(rawPart)
	if err != nil {
		return p, false
	}
	sigGot, err := base64.RawURLEncoding.DecodeString(sigPart)
	if err != nil {
		return p, false
	}
	sig := hmac.New(sha256.New, s.secret)
	sig.Write(raw)
	if !hmac.Equal(sigGot, sig.Sum(nil)) {
		return p, false
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, false
	}
	if time.Now().Unix() > p.Exp {
		return p, false
	}
	return p, true
}

func spaceCookieName(name string) string { return "wn_s_" + spaceKey(name)[:12] }

func (s *Server) setSpaceCookie(w http.ResponseWriter, name string) {
	tok := s.signToken(sessionPayload{T: "s", N: name, Exp: time.Now().Add(30 * 24 * time.Hour).Unix()})
	http.SetCookie(w, &http.Cookie{
		Name: spaceCookieName(name), Value: tok, Path: "/",
		MaxAge: 30 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) spaceUnlocked(r *http.Request, sp *Space) bool {
	if sp.PasswordHash == "" {
		return true
	}
	c, err := r.Cookie(spaceCookieName(sp.Name))
	if err != nil {
		return false
	}
	p, ok := s.parseToken(c.Value)
	return ok && p.T == "s" && p.N == sp.Name
}

func (s *Server) setAdminCookie(w http.ResponseWriter) {
	tok := s.signToken(sessionPayload{T: "a", Exp: time.Now().Add(2 * time.Hour).Unix()})
	http.SetCookie(w, &http.Cookie{
		Name: "wn_admin", Value: tok, Path: "/",
		MaxAge: 2 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) adminAuthed(r *http.Request) bool {
	c, err := r.Cookie("wn_admin")
	if err != nil {
		return false
	}
	p, ok := s.parseToken(c.Value)
	return ok && p.T == "a"
}

// ---------- name validation ----------

var reservedNames = map[string]bool{"api": true, "admin": true, "static": true, "favicon.ico": true, "robots.txt": true}

func validSpaceName(name string) bool {
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 64 {
		return false
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return false
	}
	if reservedNames[strings.ToLower(name)] {
		return false
	}
	for _, ch := range name {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

// ---------- pages ----------

func (s *Server) servePage(w http.ResponseWriter, file string) {
	raw, err := webFS.ReadFile(file)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "page missing")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(raw)
}

func (s *Server) pageAdmin(w http.ResponseWriter, r *http.Request) { s.servePage(w, "web/admin.html") }

func (s *Server) pageHomeOrSpace(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	if rest == "" {
		s.servePage(w, "web/index.html")
		return
	}
	if strings.Contains(rest, "/") {
		http.NotFound(w, r)
		return
	}
	// /<name>@<password> direct-unlock link
	if name, pw, found := strings.Cut(rest, "@"); found {
		if !validSpaceName(name) {
			http.NotFound(w, r)
			return
		}
		s.store.mu.Lock()
		sp, err := s.store.LoadSpace(name)
		if err == nil && sp != nil && sp.PasswordHash != "" && verifySecret(pw, sp.PasswordHash) {
			s.setSpaceCookie(w, name)
		}
		s.store.mu.Unlock()
		http.Redirect(w, r, "/"+url.PathEscape(name), http.StatusFound)
		return
	}
	if !validSpaceName(rest) {
		http.NotFound(w, r)
		return
	}
	s.servePage(w, "web/space.html")
}

// ---------- DTOs ----------

type tabDTO struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Sort      int       `json:"sort"`
	Rev       int64     `json:"rev"`
	UpdatedAt time.Time `json:"updated_at"`
}

type fileDTO struct {
	ID         string    `json:"id"`
	Filename   string    `json:"filename"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploaded_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func toTabDTO(t *Tab) tabDTO {
	return tabDTO{ID: t.ID, Title: t.Title, Content: t.Content, Sort: t.Sort, Rev: t.Rev, UpdatedAt: t.UpdatedAt}
}

func toFileDTO(f *FileMeta) fileDTO {
	return fileDTO{ID: f.ID, Filename: f.Filename, Size: f.Size, UploadedAt: f.UploadedAt, ExpiresAt: f.ExpiresAt}
}

// loadSpaceForAPI resolves and locks; caller must unlock store.mu.
func (s *Server) resolveSpace(w http.ResponseWriter, r *http.Request) (*Space, bool) {
	name := r.PathValue("name")
	if !validSpaceName(name) {
		writeErr(w, http.StatusNotFound, "便签不存在")
		return nil, false
	}
	s.store.mu.Lock()
	sp, err := s.store.EnsureSpace(name)
	if err != nil {
		s.store.mu.Unlock()
		writeErr(w, http.StatusInternalServerError, "服务器错误")
		return nil, false
	}
	return sp, true
}

func (s *Server) requireUnlocked(w http.ResponseWriter, r *http.Request, sp *Space) bool {
	if !s.spaceUnlocked(r, sp) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"locked": true, "has_password": true, "name": sp.Name})
		return false
	}
	return true
}

func (s *Server) requireMutable(w http.ResponseWriter, r *http.Request, sp *Space) bool {
	if !s.requireUnlocked(w, r, sp) {
		return false
	}
	if sp.Readonly {
		writeErr(w, http.StatusForbidden, "该便签处于只读模式")
		return false
	}
	return true
}

// ---------- space API ----------

func (s *Server) apiState(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireUnlocked(w, r, sp) {
		return
	}
	s.store.Touch(sp)
	tabs := []tabDTO{}
	for _, t := range sp.sortedTabs() {
		tabs = append(tabs, toTabDTO(t))
	}
	files := []fileDTO{}
	for _, f := range sp.Files {
		files = append(files, toFileDTO(f))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": sp.Name, "has_password": sp.PasswordHash != "", "readonly": sp.Readonly,
		"tabs": tabs, "files": files, "chunk_size": s.cfg.ChunkBytes(), "file_max": s.cfg.FileMaxBytes(),
	})
}

func (s *Server) apiUnlock(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	key := "unlock|" + clientIP(r) + "|" + sp.Name
	if s.limiter.blocked(key) {
		writeErr(w, http.StatusTooManyRequests, "尝试过于频繁，请 5 分钟后再试")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readBodyJSON(w, r, &body, 1<<20) {
		return
	}
	if sp.PasswordHash == "" || verifySecret(body.Password, sp.PasswordHash) {
		s.limiter.ok(key)
		s.setSpaceCookie(w, sp.Name)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	s.limiter.fail(key)
	writeErr(w, http.StatusUnauthorized, "密码不正确")
}

func (s *Server) apiSetPassword(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireUnlocked(w, r, sp) {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readBodyJSON(w, r, &body, 1<<20) {
		return
	}
	if utf8.RuneCountInString(body.Password) > 128 {
		writeErr(w, http.StatusBadRequest, "密码过长")
		return
	}
	if body.Password == "" {
		sp.PasswordHash = ""
	} else {
		sp.PasswordHash = hashSecret(body.Password)
	}
	if err := s.store.SaveSpace(sp); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	if sp.PasswordHash != "" {
		s.setSpaceCookie(w, sp.Name) // keep the owner logged in
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "has_password": sp.PasswordHash != ""})
}

func (s *Server) apiSetReadonly(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireUnlocked(w, r, sp) {
		return
	}
	var body struct {
		Readonly bool `json:"readonly"`
	}
	if !readBodyJSON(w, r, &body, 1<<20) {
		return
	}
	sp.Readonly = body.Readonly
	if err := s.store.SaveSpace(sp); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "readonly": sp.Readonly})
}

// apiSettings is the combined settings endpoint used by the two-column UI:
// one PUT may set a new password, clear it, and/or toggle readonly.
// The older /password and /readonly POST endpoints remain for compatibility.
func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireUnlocked(w, r, sp) {
		return
	}
	var body struct {
		Password      *string `json:"password"`
		ClearPassword bool    `json:"clear_password"`
		Readonly      *bool   `json:"readonly"`
	}
	if !readBodyJSON(w, r, &body, 1<<20) {
		return
	}
	if body.ClearPassword {
		sp.PasswordHash = ""
	} else if body.Password != nil {
		if utf8.RuneCountInString(*body.Password) > 128 {
			writeErr(w, http.StatusBadRequest, "密码过长")
			return
		}
		if *body.Password == "" {
			sp.PasswordHash = ""
		} else {
			sp.PasswordHash = hashSecret(*body.Password)
		}
	}
	if body.Readonly != nil {
		sp.Readonly = *body.Readonly
	}
	if err := s.store.SaveSpace(sp); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	if sp.PasswordHash != "" {
		s.setSpaceCookie(w, sp.Name) // keep the owner logged in
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "has_password": sp.PasswordHash != "", "readonly": sp.Readonly})
}

// apiDeleteSpace lets a space's own user delete the whole space from the
// space page (the admin backend has the same capability via DeleteSpace).
// A password-protected space must be unlocked first. Deletion is permanent:
// the space file and all staged file blobs are removed.
func (s *Server) apiDeleteSpace(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireUnlocked(w, r, sp) {
		return
	}
	if err := s.store.DeleteSpace(sp.Name); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除失败")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: spaceCookieName(sp.Name), Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiCreateTab(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireMutable(w, r, sp) {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	_ = readBodyJSON(w, r, &body, 1<<20)
	maxSort := -1
	for _, t := range sp.Tabs {
		if t.Sort > maxSort {
			maxSort = t.Sort
		}
	}
	t := &Tab{ID: randID(), Title: strings.TrimSpace(body.Title), Sort: maxSort + 1, Rev: 1, UpdatedAt: time.Now()}
	sp.Tabs = append(sp.Tabs, t)
	if err := s.store.SaveSpace(sp); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusCreated, toTabDTO(t))
}

func (s *Server) apiSaveTab(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireMutable(w, r, sp) {
		return
	}
	t := sp.findTab(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, "标签不存在")
		return
	}
	var body struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Rev     int64  `json:"rev"`
	}
	if !readBodyJSON(w, r, &body, 4<<20) {
		return
	}
	if len(body.Content) > 2<<20 {
		writeErr(w, http.StatusRequestEntityTooLarge, "内容过大（上限 2 MB）")
		return
	}
	if utf8.RuneCountInString(body.Title) > 200 {
		writeErr(w, http.StatusBadRequest, "标题过长")
		return
	}
	if body.Rev != t.Rev {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "内容已在其他设备更新", "tab": toTabDTO(t)})
		return
	}
	t.Title = strings.TrimSpace(body.Title)
	t.Content = body.Content
	t.Rev++
	t.UpdatedAt = time.Now()
	if err := s.store.SaveSpace(sp); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rev": t.Rev, "updated_at": t.UpdatedAt})
}

func (s *Server) apiDeleteTab(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireMutable(w, r, sp) {
		return
	}
	id := r.PathValue("id")
	for i, t := range sp.Tabs {
		if t.ID == id {
			sp.Tabs = append(sp.Tabs[:i], sp.Tabs[i+1:]...)
			if err := s.store.SaveSpace(sp); err != nil {
				writeErr(w, http.StatusInternalServerError, "保存失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "标签不存在")
}

func (s *Server) apiOrderTabs(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireMutable(w, r, sp) {
		return
	}
	var body struct {
		Order []string `json:"order"`
	}
	if !readBodyJSON(w, r, &body, 1<<20) {
		return
	}
	pos := map[string]int{}
	for i, id := range body.Order {
		pos[id] = i
	}
	next := len(body.Order)
	for _, t := range sp.Tabs {
		if p, ok := pos[t.ID]; ok {
			t.Sort = p
		} else {
			t.Sort = next
			next++
		}
	}
	if err := s.store.SaveSpace(sp); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- file API ----------

func (s *Server) apiFileInit(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireMutable(w, r, sp) {
		return
	}
	var body struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
		Days     int    `json:"days"`
	}
	if !readBodyJSON(w, r, &body, 1<<20) {
		return
	}
	name := filepath.Base(strings.TrimSpace(body.Filename))
	if name == "" || name == "." || utf8.RuneCountInString(name) > 255 {
		writeErr(w, http.StatusBadRequest, "文件名不合法")
		return
	}
	if body.Size < 1 || body.Size > s.cfg.FileMaxBytes() {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("文件大小需在 1 字节到 %d MB 之间", s.cfg.FileMaxMB))
		return
	}
	if body.Days < 1 || body.Days > 3 {
		writeErr(w, http.StatusBadRequest, "有效期只能是 1 到 3 天")
		return
	}
	total, err := s.store.TotalFileSize()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	if total+body.Size > s.cfg.FileCapBytes() {
		writeErr(w, http.StatusRequestEntityTooLarge, "文件暂存总容量已满")
		return
	}
	if free, err := DiskFree(s.cfg.DataDir); err == nil && free < body.Size+(200<<20) {
		writeErr(w, http.StatusRequestEntityTooLarge, "服务器磁盘空间不足")
		return
	}
	up := &upload{ID: randID(), SpaceName: sp.Name, Filename: name, Size: body.Size, Days: body.Days, CreatedAt: time.Now()}
	f, err := os.OpenFile(s.store.partPath(up.ID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	_ = f.Close()
	s.store.uploads[up.ID] = up
	writeJSON(w, http.StatusCreated, map[string]any{"upload_id": up.ID, "chunk_size": s.cfg.ChunkBytes()})
}

func (s *Server) apiFileChunk(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireMutable(w, r, sp) {
		return
	}
	up := s.store.uploads[r.PathValue("upload")]
	if up == nil || up.SpaceName != sp.Name {
		writeErr(w, http.StatusNotFound, "上传会话不存在")
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset != up.Received {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "偏移不一致", "received": up.Received})
		return
	}
	f, err := os.OpenFile(s.store.partPath(up.ID), os.O_WRONLY, 0o600)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, s.cfg.ChunkBytes()+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "写入失败")
		return
	}
	if n > s.cfg.ChunkBytes() || up.Received+n > up.Size {
		writeErr(w, http.StatusBadRequest, "分片大小不合法")
		return
	}
	up.Received += n
	writeJSON(w, http.StatusOK, map[string]any{"received": up.Received})
}

func (s *Server) apiFileComplete(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireMutable(w, r, sp) {
		return
	}
	up := s.store.uploads[r.PathValue("upload")]
	if up == nil || up.SpaceName != sp.Name {
		writeErr(w, http.StatusNotFound, "上传会话不存在")
		return
	}
	if up.Received != up.Size {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "文件不完整", "received": up.Received, "size": up.Size})
		return
	}
	part := s.store.partPath(up.ID)
	if err := os.Truncate(part, up.Size); err != nil {
		writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	blob := s.store.blobPath(sp.Name, up.ID)
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	if err := os.Rename(part, blob); err != nil {
		writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	now := time.Now()
	fm := &FileMeta{ID: up.ID, Filename: up.Filename, Size: up.Size, UploadedAt: now, ExpiresAt: now.Add(time.Duration(up.Days) * 24 * time.Hour)}
	sp.Files = append(sp.Files, fm)
	delete(s.store.uploads, up.ID)
	if err := s.store.SaveSpace(sp); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusCreated, toFileDTO(fm))
}

func (s *Server) apiFileAbort(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireMutable(w, r, sp) {
		return
	}
	id := r.PathValue("upload")
	if up := s.store.uploads[id]; up != nil && up.SpaceName == sp.Name {
		_ = os.Remove(s.store.partPath(id))
		delete(s.store.uploads, id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiFileDownload(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireUnlocked(w, r, sp) {
		return
	}
	fm := sp.findFile(r.PathValue("id"))
	if fm == nil {
		writeErr(w, http.StatusNotFound, "文件不存在")
		return
	}
	blob := s.store.blobPath(sp.Name, fm.ID)
	f, err := os.Open(blob)
	if err != nil {
		writeErr(w, http.StatusNotFound, "文件不存在")
		return
	}
	defer f.Close()
	disp := mime.FormatMediaType("attachment", map[string]string{"filename": fm.Filename})
	w.Header().Set("Content-Disposition", disp)
	http.ServeContent(w, r, fm.Filename, fm.UploadedAt, f)
}

func (s *Server) apiFileDelete(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	if !s.requireMutable(w, r, sp) {
		return
	}
	id := r.PathValue("id")
	for i, f := range sp.Files {
		if f.ID == id {
			_ = os.Remove(s.store.blobPath(sp.Name, id))
			sp.Files = append(sp.Files[:i], sp.Files[i+1:]...)
			if err := s.store.SaveSpace(sp); err != nil {
				writeErr(w, http.StatusInternalServerError, "保存失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "文件不存在")
}

// ---------- admin API ----------

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.AdminHash == "" {
		writeErr(w, http.StatusServiceUnavailable, "管理员功能未启用（启动时未指定管理员密码）")
		return false
	}
	if !s.adminAuthed(r) {
		writeErr(w, http.StatusUnauthorized, "需要管理员登录")
		return false
	}
	return true
}

func (s *Server) apiAdminLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminHash == "" {
		writeErr(w, http.StatusServiceUnavailable, "管理员功能未启用（启动时未指定管理员密码）")
		return
	}
	key := "admin|" + clientIP(r)
	if s.limiter.blocked(key) {
		writeErr(w, http.StatusTooManyRequests, "尝试过于频繁，请 5 分钟后再试")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readBodyJSON(w, r, &body, 1<<20) {
		return
	}
	if verifySecret(body.Password, s.cfg.AdminHash) {
		s.limiter.ok(key)
		s.setAdminCookie(w)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	s.limiter.fail(key)
	writeErr(w, http.StatusUnauthorized, "密码不正确")
}

func (s *Server) apiAdminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "wn_admin", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type adminTabDTO struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Size  int    `json:"size"`
}

type adminSpaceDTO struct {
	Name       string        `json:"name"`
	CreatedAt  time.Time     `json:"created_at"`
	LastAccess time.Time     `json:"last_access"`
	HasPw      bool          `json:"has_password"`
	Readonly   bool          `json:"readonly"`
	Tabs       []adminTabDTO `json:"tabs"`
	Files      []fileDTO     `json:"files"`
	TabsSize   int64         `json:"tabs_size"`
	FilesSize  int64         `json:"files_size"`
}

func (s *Server) apiAdminOverview(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	names, err := s.store.ListSpaceNames()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	out := []adminSpaceDTO{}
	var totalTabs, totalFiles int64
	for _, n := range names {
		sp, err := s.store.EnsureSpace(n)
		if err != nil {
			continue
		}
		dto := adminSpaceDTO{Name: sp.Name, CreatedAt: sp.CreatedAt, LastAccess: sp.LastAccess, HasPw: sp.PasswordHash != "", Readonly: sp.Readonly, Tabs: []adminTabDTO{}, Files: []fileDTO{}}
		for _, t := range sp.sortedTabs() {
			sz := len([]byte(t.Content))
			dto.Tabs = append(dto.Tabs, adminTabDTO{ID: t.ID, Title: t.Title, Size: sz})
			dto.TabsSize += int64(sz)
		}
		for _, f := range sp.Files {
			dto.Files = append(dto.Files, toFileDTO(f))
			dto.FilesSize += f.Size
		}
		totalTabs += dto.TabsSize
		totalFiles += dto.FilesSize
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"spaces": out, "total_tabs_size": totalTabs, "total_files_size": totalFiles})
}

func (s *Server) apiAdminBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	names, err := s.store.ListSpaceNames()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		raw, err := os.ReadFile(s.store.spacePath(n))
		if err != nil {
			continue
		}
		hdr := &tar.Header{Name: "spaces/" + filepath.Base(s.store.spacePath(n)), Mode: 0o600, Size: int64(len(raw))}
		if err := tw.WriteHeader(hdr); err != nil {
			writeErr(w, http.StatusInternalServerError, "备份失败")
			return
		}
		if _, err := tw.Write(raw); err != nil {
			writeErr(w, http.StatusInternalServerError, "备份失败")
			return
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	fname := "webnote-backup-" + time.Now().Format("20060102-150405") + ".tar.gz"
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fname}))
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) apiAdminDeleteSpace(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	name := r.PathValue("name")
	if !validSpaceName(name) {
		writeErr(w, http.StatusNotFound, "便签不存在")
		return
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.store.DeleteSpace(name); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiAdminDeleteTab(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	id := r.PathValue("id")
	for i, t := range sp.Tabs {
		if t.ID == id {
			sp.Tabs = append(sp.Tabs[:i], sp.Tabs[i+1:]...)
			if err := s.store.SaveSpace(sp); err != nil {
				writeErr(w, http.StatusInternalServerError, "保存失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "标签不存在")
}

func (s *Server) apiAdminDeleteFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	sp, ok := s.resolveSpace(w, r)
	if !ok {
		return
	}
	defer s.store.mu.Unlock()
	id := r.PathValue("id")
	for i, f := range sp.Files {
		if f.ID == id {
			_ = os.Remove(s.store.blobPath(sp.Name, id))
			sp.Files = append(sp.Files[:i], sp.Files[i+1:]...)
			if err := s.store.SaveSpace(sp); err != nil {
				writeErr(w, http.StatusInternalServerError, "保存失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "文件不存在")
}
