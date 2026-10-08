package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAuthenticationSessionAndRecovery(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, err := db.CreateUser("admin", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Authenticate("admin", "wrong password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	authenticated, err := db.Authenticate("admin", "correct horse battery staple")
	if err != nil || authenticated.ID != u.ID {
		t.Fatalf("authentication failed: %v", err)
	}
	session, err := db.NewSession(u.ID, "127.0.0.1", "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := db.UserBySession(session); err != nil || got.ID != u.ID {
		t.Fatalf("session lookup failed: %v", err)
	}
	recovery, err := db.NewRecoveryDevice(u.ID, "test browser")
	if err != nil || !db.RecoveryAvailable(recovery) {
		t.Fatalf("recovery was not created: %v", err)
	}
	if err := db.RecoverPassword(recovery, "admin", "new correct horse password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UserBySession(session); err == nil {
		t.Fatal("password reset did not revoke sessions")
	}
	if _, err := db.Authenticate("admin", "new correct horse password"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := hashPassword("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := verifyPassword("a sufficiently long password", hash); err != nil || !ok {
		t.Fatalf("valid password rejected: %v", err)
	}
	if ok, _ := verifyPassword("not the password", hash); ok {
		t.Fatal("invalid password accepted")
	}
}
