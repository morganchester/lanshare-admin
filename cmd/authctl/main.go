package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"lanshare-admin/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	database := envOr("DATABASE_PATH", "/var/lib/lanshare-admin/lanshare.db")
	db, err := store.Open(database)
	if err != nil {
		fatal(err)
	}
	defer db.Close()
	if os.Args[1] == "count" {
		count, err := db.UserCount()
		if err != nil {
			fatal(err)
		}
		fmt.Println(count)
		return
	}
	if len(os.Args) < 3 {
		usage()
	}
	command, username := os.Args[1], strings.ToLower(strings.TrimSpace(os.Args[2]))
	password := ""
	if len(os.Args) >= 4 {
		password = os.Args[3]
	} else {
		password = generatedPassword()
	}

	switch command {
	case "create":
		if _, err := db.CreateUser(username, password); err != nil {
			fatal(err)
		}
	case "reset":
		users, err := db.ListUsers()
		if err != nil {
			fatal(err)
		}
		found := false
		for _, item := range users {
			if strings.EqualFold(item.Username, username) {
				if err := db.AdminResetPassword(item.ID, password); err != nil {
					fatal(err)
				}
				found = true
				break
			}
		}
		if !found {
			fatal(errors.New("user not found"))
		}
	default:
		fatal(errors.New("unknown command"))
	}
	fmt.Printf("username=%s\npassword=%s\n", username, password)
}

func usage() {
	fatal(errors.New("usage: lanshare-auth count | create|reset <username> [password]"))
}

func generatedPassword() string {
	data := make([]byte, 18)
	if _, err := rand.Read(data); err != nil {
		fatal(err)
	}
	return hex.EncodeToString(data)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
