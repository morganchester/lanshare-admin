package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

var ErrInvalidCredentials = errors.New("invalid credentials")
var ErrRecoveryUnavailable = errors.New("recovery unavailable")
var ErrLastAdmin = errors.New("cannot remove the last active administrator")
var ErrNotFound = errors.New("not found")

const (
	argonMemory      = 64 * 1024
	argonIterations  = 3
	argonParallelism = 2
	argonSaltLength  = 16
	argonKeyLength   = 32
)

type Store struct {
	db *sql.DB
}

type User struct {
	ID        int64      `json:"id"`
	Username  string     `json:"username"`
	Role      string     `json:"role"`
	Disabled  bool       `json:"disabled"`
	CreatedAt time.Time  `json:"created_at"`
	LastLogin *time.Time `json:"last_login,omitempty"`
}

type RecoveryDevice struct {
	ID        int64      `json:"id"`
	Label     string     `json:"label"`
	CreatedAt time.Time  `json:"created_at"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL COLLATE NOCASE UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'admin' CHECK(role IN ('admin')),
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  last_login_at INTEGER
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash BLOB PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  remote_addr TEXT,
  user_agent TEXT
);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS recovery_devices (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_hash BLOB NOT NULL UNIQUE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  label TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER
);
CREATE TABLE IF NOT EXISTS device_metadata (
  name TEXT PRIMARY KEY,
  platform TEXT NOT NULL DEFAULT 'other',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
`)
	return err
}

func (s *Store) UserCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

func (s *Store) CreateUser(username, password string) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if err := ValidateUsername(username); err != nil {
		return User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := time.Now().Unix()
	result, err := s.db.Exec(`INSERT INTO users(username,password_hash,role,created_at,updated_at) VALUES(?,?,'admin',?,?)`, username, hash, now, now)
	if err != nil {
		return User{}, err
	}
	id, _ := result.LastInsertId()
	return User{ID: id, Username: username, Role: "admin", CreatedAt: time.Unix(now, 0).UTC()}, nil
}

func (s *Store) Authenticate(username, password string) (User, error) {
	var u User
	var hash string
	var disabled int
	var created, last sql.NullInt64
	err := s.db.QueryRow(`SELECT id,username,password_hash,role,disabled,created_at,last_login_at FROM users WHERE username=?`, strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &hash, &u.Role, &disabled, &created, &last)
	if err != nil || disabled != 0 {
		fakePasswordCheck(password)
		return User{}, ErrInvalidCredentials
	}
	ok, err := verifyPassword(password, hash)
	if err != nil || !ok {
		return User{}, ErrInvalidCredentials
	}
	u.Disabled = disabled != 0
	u.CreatedAt = time.Unix(created.Int64, 0).UTC()
	if last.Valid {
		value := time.Unix(last.Int64, 0).UTC()
		u.LastLogin = &value
	}
	now := time.Now().Unix()
	_, _ = s.db.Exec(`UPDATE users SET last_login_at=? WHERE id=?`, now, u.ID)
	return u, nil
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id,username,role,disabled,created_at,last_login_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []User
	for rows.Next() {
		var u User
		var disabled int
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &disabled, &created, &last); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		u.CreatedAt = time.Unix(created, 0).UTC()
		if last.Valid {
			value := time.Unix(last.Int64, 0).UTC()
			u.LastLogin = &value
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

func (s *Store) ChangePassword(userID int64, current, next string) error {
	var hash string
	if err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id=? AND disabled=0`, userID).Scan(&hash); err != nil {
		return ErrInvalidCredentials
	}
	ok, _ := verifyPassword(current, hash)
	if !ok {
		return ErrInvalidCredentials
	}
	return s.setPassword(userID, next, true)
}

func (s *Store) AdminResetPassword(userID int64, next string) error {
	return s.setPassword(userID, next, true)
}

func (s *Store) setPassword(userID int64, next string, revokeSessions bool) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	hash, err := hashPassword(next)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE users SET password_hash=?,updated_at=? WHERE id=?`, hash, time.Now().Unix(), userID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return ErrNotFound
	}
	if revokeSessions {
		if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id=?`, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteUser(id, actorID int64) error {
	if id == actorID {
		return errors.New("cannot delete current user")
	}
	var active int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE disabled=0`).Scan(&active); err != nil {
		return err
	}
	if active <= 1 {
		return ErrLastAdmin
	}
	_, err := s.db.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

func (s *Store) NewSession(userID int64, remote, agent string, lifetime time.Duration) (string, error) {
	token := randomToken()
	now := time.Now()
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash,user_id,created_at,expires_at,last_seen_at,remote_addr,user_agent) VALUES(?,?,?,?,?,?,?)`, tokenHash(token), userID, now.Unix(), now.Add(lifetime).Unix(), now.Unix(), remote, agent)
	return token, err
}

func (s *Store) UserBySession(token string) (User, error) {
	var u User
	var disabled int
	var created int64
	err := s.db.QueryRow(`SELECT u.id,u.username,u.role,u.disabled,u.created_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>?`, tokenHash(token), time.Now().Unix()).Scan(&u.ID, &u.Username, &u.Role, &disabled, &created)
	if err != nil || disabled != 0 {
		return User{}, ErrInvalidCredentials
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	_, _ = s.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE token_hash=?`, time.Now().Unix(), tokenHash(token))
	return u, nil
}

func (s *Store) DeleteSession(token string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash(token))
}

func (s *Store) NewRecoveryDevice(userID int64, label string) (string, error) {
	token := randomToken()
	_, err := s.db.Exec(`INSERT INTO recovery_devices(token_hash,user_id,label,created_at) VALUES(?,?,?,?)`, tokenHash(token), userID, strings.TrimSpace(label), time.Now().Unix())
	return token, err
}

func (s *Store) RecoveryAvailable(token string) bool {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM recovery_devices r JOIN users u ON u.id=r.user_id WHERE r.token_hash=? AND u.disabled=0`, tokenHash(token)).Scan(&count)
	return err == nil && count == 1
}

func (s *Store) RecoveryBelongsTo(token string, userID int64) bool {
	if token == "" {
		return false
	}
	var found int
	err := s.db.QueryRow(`SELECT 1 FROM recovery_devices WHERE token_hash=? AND user_id=?`, tokenHash(token), userID).Scan(&found)
	return err == nil
}

func (s *Store) RecoverPassword(token, username, next string) error {
	var userID int64
	err := s.db.QueryRow(`SELECT u.id FROM recovery_devices r JOIN users u ON u.id=r.user_id WHERE r.token_hash=? AND u.username=? AND u.disabled=0`, tokenHash(token), strings.TrimSpace(username)).Scan(&userID)
	if err != nil {
		return ErrRecoveryUnavailable
	}
	if err := s.setPassword(userID, next, true); err != nil {
		return err
	}
	_, _ = s.db.Exec(`UPDATE recovery_devices SET last_used_at=? WHERE token_hash=?`, time.Now().Unix(), tokenHash(token))
	return nil
}

func (s *Store) ListRecoveryDevices(userID int64) ([]RecoveryDevice, error) {
	rows, err := s.db.Query(`SELECT id,label,created_at,last_used_at FROM recovery_devices WHERE user_id=? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RecoveryDevice
	for rows.Next() {
		var d RecoveryDevice
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Label, &created, &last); err != nil {
			return nil, err
		}
		d.CreatedAt = time.Unix(created, 0).UTC()
		if last.Valid {
			value := time.Unix(last.Int64, 0).UTC()
			d.LastUsed = &value
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *Store) DeleteRecoveryDevice(id, userID int64) error {
	_, err := s.db.Exec(`DELETE FROM recovery_devices WHERE id=? AND user_id=?`, id, userID)
	return err
}

func (s *Store) UpsertDevice(name, platform string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO device_metadata(name,platform,created_at,updated_at) VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET platform=excluded.platform,updated_at=excluded.updated_at`, name, platform, now, now)
	return err
}

func (s *Store) DeviceMetadata(name string) (platform string, created, updated time.Time) {
	var c, u int64
	if err := s.db.QueryRow(`SELECT platform,created_at,updated_at FROM device_metadata WHERE name=?`, name).Scan(&platform, &c, &u); err != nil {
		return "other", time.Time{}, time.Time{}
	}
	return platform, time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
}

func (s *Store) DeleteDeviceMetadata(name string) {
	_, _ = s.db.Exec(`DELETE FROM device_metadata WHERE name=?`, name)
}

func ValidateUsername(value string) error {
	if len(value) < 3 || len(value) > 32 {
		return errors.New("username must contain 3 to 32 characters")
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
			return errors.New("username contains unsupported characters")
		}
	}
	return nil
}

func ValidatePassword(value string) error {
	if len(value) < 12 {
		return errors.New("password must contain at least 12 characters")
	}
	if len(value) > 256 {
		return errors.New("password is too long")
	}
	return nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonIterations, argonParallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errors.New("invalid password hash")
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(actual, want) == 1, nil
}

func fakePasswordCheck(password string) {
	salt := []byte("0123456789abcdef")
	_ = argon2.IDKey([]byte(password), salt, 1, 8*1024, 1, 32)
}

func randomToken() string {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func ParseUserID(value string) (int64, error) {
	return strconv.ParseInt(value, 10, 64)
}
