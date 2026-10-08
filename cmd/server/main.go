package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lanshare-admin/internal/store"
	"lanshare-admin/locales"
	webassets "lanshare-admin/web"
)

const sessionCookie = "lanshare_session"
const recoveryCookie = "lanshare_recovery"

var deviceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,22}$`)

type contextKey string

const userContextKey contextKey = "user"

type fileConfig struct {
	ListenAddr       string   `json:"listen_addr"`
	PublicURL        string   `json:"public_url"`
	AccountPrefix    string   `json:"account_prefix"`
	SMBServer        string   `json:"smb_server"`
	SambaConfig      string   `json:"samba_config"`
	ManagedShareFile string   `json:"managed_share_file"`
	AllowedRoots     []string `json:"allowed_roots"`
	Shares           []share  `json:"shares"`
}

type share struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"-"`
	UNC      string `json:"unc"`
	Path     string `json:"path,omitempty"`
	ReadOnly bool   `json:"read_only"`
	Managed  bool   `json:"managed"`
}

type runtimeConfig struct {
	fileConfig
	ConfigPath string
	HelperPath string
	DBPath     string
}

type server struct {
	cfg        runtimeConfig
	db         *store.Store
	log        *slog.Logger
	web        http.Handler
	mu         sync.Mutex
	loginMu    sync.Mutex
	loginFails map[string]loginFailure
}

type loginFailure struct {
	Count   int
	Blocked time.Time
}

type device struct {
	Name     string    `json:"name"`
	Username string    `json:"username"`
	Platform string    `json:"platform"`
	Shares   []string  `json:"shares"`
	Created  time.Time `json:"created,omitempty"`
	Updated  time.Time `json:"updated,omitempty"`
}

type helperDevice struct {
	Name     string   `json:"name"`
	Username string   `json:"username"`
	Shares   []string `json:"shares"`
}

type deviceRequest struct {
	Name     string   `json:"name"`
	Platform string   `json:"platform"`
	Shares   []string `json:"shares"`
}

type deviceSharesRequest struct {
	Shares []string `json:"shares"`
}

type newShareRequest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
}

type credentialResponse struct {
	Device   device `json:"device"`
	Password string `json:"password"`
}

func main() {
	cfgPath := envOr("LANSHARE_CONFIG", "/etc/lanshare-admin/config.json")
	fc, err := readFileConfig(cfgPath)
	if err != nil {
		fatal("read config", err)
	}
	cfg := runtimeConfig{
		fileConfig: fc, ConfigPath: cfgPath,
		HelperPath: envOr("DEVICE_HELPER", "/opt/lanshare-admin/lanshare-helper"),
		DBPath:     envOr("DATABASE_PATH", "/var/lib/lanshare-admin/lanshare.db"),
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()
	webRoot, err := fs.Sub(webassets.Files, ".")
	if err != nil {
		fatal("prepare web assets", err)
	}
	s := &server{
		cfg: cfg, db: db, log: slog.New(slog.NewJSONHandler(os.Stdout, nil)),
		web: http.FileServer(http.FS(webRoot)), loginFails: make(map[string]loginFailure),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/locales/{locale}", s.localeCatalog)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /api/auth/recovery", s.recoveryStatus)
	mux.HandleFunc("POST /api/auth/recover", s.recoverPassword)
	mux.HandleFunc("POST /api/auth/logout", s.requireAuth(s.logout))
	mux.HandleFunc("GET /api/state", s.requireAuth(s.state))
	mux.HandleFunc("POST /api/auth/password", s.requireAuth(s.changePassword))
	mux.HandleFunc("GET /api/recovery-devices", s.requireAuth(s.recoveryDevices))
	mux.HandleFunc("DELETE /api/recovery-devices/{id}", s.requireAuth(s.deleteRecoveryDevice))
	mux.HandleFunc("GET /api/users", s.requireAuth(s.listUsers))
	mux.HandleFunc("POST /api/users", s.requireAuth(s.addUser))
	mux.HandleFunc("POST /api/users/{id}/reset", s.requireAuth(s.resetUserPassword))
	mux.HandleFunc("DELETE /api/users/{id}", s.requireAuth(s.deleteUser))
	mux.HandleFunc("POST /api/devices", s.requireAuth(s.addDevice))
	mux.HandleFunc("POST /api/devices/{name}/rotate", s.requireAuth(s.rotateDevice))
	mux.HandleFunc("PUT /api/devices/{name}/shares", s.requireAuth(s.setDeviceShares))
	mux.HandleFunc("DELETE /api/devices/{name}", s.requireAuth(s.revokeDevice))
	mux.HandleFunc("GET /api/directories", s.requireAuth(s.directories))
	mux.HandleFunc("POST /api/shares", s.requireAuth(s.addShare))
	mux.HandleFunc("DELETE /api/shares/{id}", s.requireAuth(s.removeShare))
	mux.HandleFunc("GET /", s.frontend)

	h := http.MaxBytesHandler(requestLog(s.log, securityHeaders(mux)), 1<<20)
	httpServer := &http.Server{
		Addr: cfg.ListenAddr, Handler: h, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	s.log.Info("LanShare Admin starting", "address", cfg.ListenAddr, "shares", len(cfg.Shares))
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		fatal("serve", err)
	}
}

func readFileConfig(path string) (fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, err
	}
	var cfg fileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fileConfig{}, err
	}
	if cfg.ListenAddr == "" || len(cfg.Shares) == 0 {
		return fileConfig{}, errors.New("listen_addr and at least one share are required")
	}
	return cfg, nil
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) localeCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"locale": locales.Normalize(r.PathValue("locale")), "messages": locales.Catalog(r.PathValue("locale")), "supported": locales.Supported()})
}

func (s *server) frontend(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/", "/users", "/shares":
		r.URL.Path = "/"
	case "/app.js", "/output.css":
	default:
		http.NotFound(w, r)
		return
	}
	s.web.ServeHTTP(w, r)
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r.RemoteAddr)
	if wait := s.loginBlocked(ip); wait > 0 {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		writeErrorKey(w, r, http.StatusTooManyRequests, "errors.rateLimited")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Trust    bool   `json:"trust"`
		Label    string `json:"label"`
	}
	if decodeJSON(r, &in) != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.badRequest")
		return
	}
	u, err := s.db.Authenticate(strings.ToLower(in.Username), in.Password)
	if err != nil {
		s.recordLoginFailure(ip)
		writeErrorKey(w, r, http.StatusUnauthorized, "login.invalid")
		return
	}
	s.clearLoginFailures(ip)
	token, err := s.db.NewSession(u.ID, ip, r.UserAgent(), 30*24*time.Hour)
	if err != nil {
		writeErrorKey(w, r, http.StatusInternalServerError, "common.error")
		return
	}
	setCookie(w, r, sessionCookie, token, 30*24*time.Hour)
	if in.Trust {
		existing, cookieErr := r.Cookie(recoveryCookie)
		if cookieErr != nil || !s.db.RecoveryBelongsTo(existing.Value, u.ID) {
			label := strings.TrimSpace(in.Label)
			if label == "" {
				label = browserLabel(r.UserAgent(), localeOf(r))
			}
			if recovery, err := s.db.NewRecoveryDevice(u.ID, label); err == nil {
				setCookie(w, r, recoveryCookie, recovery, 365*24*time.Hour)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.db.DeleteSession(cookie.Value)
	}
	clearCookie(w, r, sessionCookie)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) recoveryStatus(w http.ResponseWriter, r *http.Request) {
	available := false
	if cookie, err := r.Cookie(recoveryCookie); err == nil {
		available = s.db.RecoveryAvailable(cookie.Value)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"available": available})
}

func (s *server) recoverPassword(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErrorKey(w, r, http.StatusForbidden, "errors.forbidden")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decodeJSON(r, &in) != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.badRequest")
		return
	}
	cookie, err := r.Cookie(recoveryCookie)
	if err != nil || s.db.RecoverPassword(cookie.Value, strings.ToLower(in.Username), in.Password) != nil {
		writeErrorKey(w, r, http.StatusForbidden, "recovery.unavailable")
		return
	}
	clearCookie(w, r, sessionCookie)
	writeJSON(w, http.StatusOK, map[string]bool{"reset": true})
}

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeErrorKey(w, r, http.StatusUnauthorized, "errors.unauthorized")
			return
		}
		u, err := s.db.UserBySession(cookie.Value)
		if err != nil {
			clearCookie(w, r, sessionCookie)
			writeErrorKey(w, r, http.StatusUnauthorized, "errors.unauthorized")
			return
		}
		if r.Method != http.MethodGet && !sameOrigin(r) {
			writeErrorKey(w, r, http.StatusForbidden, "errors.forbidden")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userContextKey, u)))
	}
}

func currentUser(r *http.Request) store.User { return r.Context().Value(userContextKey).(store.User) }

func (s *server) state(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	devices, err := s.listDevices()
	if err != nil {
		writeErrorKey(w, r, http.StatusInternalServerError, "errors.deviceList")
		return
	}
	users, _ := s.db.ListUsers()
	recovery, _ := s.db.ListRecoveryDevices(currentUser(r).ID)
	writeJSON(w, http.StatusOK, map[string]any{"shares": s.cfg.Shares, "devices": devices, "users": users, "recovery_devices": recovery, "me": currentUser(r), "public_url": s.cfg.PublicURL})
}

func (s *server) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if decodeJSON(r, &in) != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.badRequest")
		return
	}
	if err := store.ValidatePassword(in.Password); err != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.passwordShort")
		return
	}
	if s.db.ChangePassword(currentUser(r).ID, in.Current, in.Password) != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "login.invalid")
		return
	}
	clearCookie(w, r, sessionCookie)
	writeJSON(w, http.StatusOK, map[string]bool{"changed": true})
}

func (s *server) recoveryDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.db.ListRecoveryDevices(currentUser(r).ID)
	if err != nil {
		writeErrorKey(w, r, http.StatusInternalServerError, "common.error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (s *server) deleteRecoveryDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || s.db.DeleteRecoveryDevice(id, currentUser(r).ID) != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.badRequest")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.ListUsers()
	if err != nil {
		writeErrorKey(w, r, http.StatusInternalServerError, "common.error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "me": currentUser(r)})
}

func (s *server) addUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decodeJSON(r, &in) != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.badRequest")
		return
	}
	generated := false
	if in.Password == "" {
		in.Password, generated = generatedPassword(), true
	}
	u, err := s.db.CreateUser(strings.ToLower(in.Username), in.Password)
	if err != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.userExists")
		return
	}
	response := map[string]any{"user": u}
	if generated {
		response["password"] = in.Password
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *server) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.badRequest")
		return
	}
	if id == currentUser(r).ID {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.currentUserReset")
		return
	}
	password := generatedPassword()
	if err := s.db.AdminResetPassword(id, password); err != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "common.error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"password": password})
}

func (s *server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.badRequest")
		return
	}
	if err := s.db.DeleteUser(id, currentUser(r).ID); err != nil {
		key := "errors.currentUserDelete"
		if errors.Is(err, store.ErrLastAdmin) {
			key = "errors.lastAdmin"
		}
		writeErrorKey(w, r, http.StatusBadRequest, key)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) addDevice(w http.ResponseWriter, r *http.Request) {
	var in deviceRequest
	if decodeJSON(r, &in) != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.badRequest")
		return
	}
	s.createOrRotate(w, r, strings.ToLower(strings.TrimSpace(in.Name)), normalizePlatform(in.Platform), in.Shares, false)
}

func (s *server) rotateDevice(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	s.mu.Lock()
	devices, err := s.listDevices()
	s.mu.Unlock()
	if err != nil {
		writeErrorKey(w, r, http.StatusInternalServerError, "errors.deviceList")
		return
	}
	for _, d := range devices {
		if d.Name == name {
			s.createOrRotate(w, r, name, d.Platform, d.Shares, true)
			return
		}
	}
	writeErrorKey(w, r, http.StatusNotFound, "errors.notFound")
}

func (s *server) createOrRotate(w http.ResponseWriter, r *http.Request, name, platform string, shares []string, rotate bool) {
	if !deviceNamePattern.MatchString(name) {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.invalidDevice")
		return
	}
	if s.validateShares(shares) != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.selectShare")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.runHelper("add", name, strings.Join(shares, ","))
	if err != nil {
		s.log.Error("credential operation", "device", name, "rotate", rotate, "error", err)
		writeErrorKey(w, r, http.StatusInternalServerError, "errors.deviceCreate")
		return
	}
	_ = s.db.UpsertDevice(name, platform)
	platform, created, updated := s.db.DeviceMetadata(name)
	status := http.StatusCreated
	if rotate {
		status = http.StatusOK
	}
	writeJSON(w, status, credentialResponse{Device: device{Name: name, Username: values["username"], Platform: platform, Shares: shares, Created: created, Updated: updated}, Password: values["password"]})
}

func (s *server) setDeviceShares(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	var in deviceSharesRequest
	if !deviceNamePattern.MatchString(name) || decodeJSON(r, &in) != nil || s.validateShares(in.Shares) != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.selectShare")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.runHelper("set-shares", name, strings.Join(in.Shares, ",")); err != nil {
		writeErrorKey(w, r, http.StatusInternalServerError, "errors.deviceUpdate")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	if !deviceNamePattern.MatchString(name) {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.invalidDevice")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.runHelper("revoke", name); err != nil {
		writeErrorKey(w, r, http.StatusInternalServerError, "errors.deviceRevoke")
		return
	}
	s.db.DeleteDeviceMetadata(name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) directories(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" && len(s.cfg.AllowedRoots) > 0 {
		path = s.cfg.AllowedRoots[0]
	}
	output, err := s.helperOutput("browse", path)
	if err != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.directory")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(output)
}

func (s *server) addShare(w http.ResponseWriter, r *http.Request) {
	var in newShareRequest
	if decodeJSON(r, &in) != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.badRequest")
		return
	}
	in.ID, in.Name, in.Path = strings.ToLower(strings.TrimSpace(in.ID)), strings.TrimSpace(in.Name), strings.TrimSpace(in.Path)
	mode := "rw"
	if in.ReadOnly {
		mode = "ro"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.helperOutput("add-share", in.ID, in.Name, in.Path, mode); err != nil {
		s.log.Error("add share", "id", in.ID, "error", err)
		writeErrorKey(w, r, http.StatusBadRequest, "errors.shareCreate")
		return
	}
	if err := s.reloadFileConfig(); err != nil {
		writeErrorKey(w, r, http.StatusInternalServerError, "common.error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"created": true})
}

func (s *server) removeShare(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.helperOutput("remove-share", id); err != nil {
		writeErrorKey(w, r, http.StatusBadRequest, "errors.shareRemove")
		return
	}
	if err := s.reloadFileConfig(); err != nil {
		writeErrorKey(w, r, http.StatusInternalServerError, "common.error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) validateShares(ids []string) error {
	if len(ids) == 0 {
		return errors.New("no shares")
	}
	valid, seen := make(map[string]bool), make(map[string]bool)
	for _, item := range s.cfg.Shares {
		valid[item.ID] = true
	}
	for _, id := range ids {
		if !valid[id] || seen[id] {
			return errors.New("invalid share")
		}
		seen[id] = true
	}
	return nil
}

func (s *server) listDevices() ([]device, error) {
	output, err := s.helperOutput("list")
	if err != nil {
		return nil, err
	}
	var raw []helperDevice
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, err
	}
	devices := make([]device, 0, len(raw))
	for _, item := range raw {
		platform, created, updated := s.db.DeviceMetadata(item.Name)
		devices = append(devices, device{Name: item.Name, Username: item.Username, Shares: item.Shares, Platform: platform, Created: created, Updated: updated})
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
	return devices, nil
}

func (s *server) runHelper(args ...string) (map[string]string, error) {
	output, err := s.helperOutput(args...)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	return values, nil
}

func (s *server) helperOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("sudo", append([]string{"-n", s.cfg.HelperPath}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("helper: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func (s *server) reloadFileConfig() error {
	next, err := readFileConfig(s.cfg.ConfigPath)
	if err != nil {
		return err
	}
	s.cfg.fileConfig = next
	return nil
}

func (s *server) loginBlocked(ip string) time.Duration {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	f := s.loginFails[ip]
	if time.Now().Before(f.Blocked) {
		return time.Until(f.Blocked)
	}
	return 0
}

func (s *server) recordLoginFailure(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	f := s.loginFails[ip]
	f.Count++
	if f.Count >= 5 {
		f.Blocked, f.Count = time.Now().Add(5*time.Minute), 0
	}
	s.loginFails[ip] = f
}

func (s *server) clearLoginFailures(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.loginFails, ip)
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, lifetime time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: int(lifetime.Seconds()), Expires: time.Now().Add(lifetime), HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

func generatedPassword() string {
	data := make([]byte, 18)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data)
}

func browserLabel(agent, locale string) string {
	switch {
	case strings.Contains(agent, "Macintosh"):
		return locales.T(locale, "recovery.macBrowser")
	case strings.Contains(agent, "Windows"):
		return locales.T(locale, "recovery.windowsBrowser")
	case strings.Contains(agent, "Android"):
		return locales.T(locale, "recovery.androidBrowser")
	default:
		return locales.T(locale, "recovery.browser")
	}
}

func normalizePlatform(value string) string {
	switch strings.ToLower(value) {
	case "macos", "windows", "linux":
		return strings.ToLower(value)
	default:
		return "other"
	}
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeErrorKey(w http.ResponseWriter, r *http.Request, status int, key string) {
	writeJSON(w, status, map[string]string{"error": locales.T(localeOf(r), key), "key": key})
}

func localeOf(r *http.Request) string {
	if value := r.Header.Get("X-Locale"); value != "" {
		return locales.Normalize(value)
	}
	return locales.Normalize(r.Header.Get("Accept-Language"))
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func requestLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Info("request", "method", r.Method, "path", r.URL.Path, "remote", remoteIP(r.RemoteAddr), "duration_ms", time.Since(start).Milliseconds())
	})
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func fatal(message string, err error) {
	slog.Error(message, "error", err)
	os.Exit(1)
}
