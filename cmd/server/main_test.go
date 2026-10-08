package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizePlatform(t *testing.T) {
	for input, expected := range map[string]string{
		"macOS": "macos", "WINDOWS": "windows", "linux": "linux", "phone": "other",
	} {
		if actual := normalizePlatform(input); actual != expected {
			t.Fatalf("normalizePlatform(%q)=%q, want %q", input, actual, expected)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://admin.example.com:8097/api/devices", nil)
	r.Header.Set("Origin", "http://admin.example.com:8097")
	if !sameOrigin(r) {
		t.Fatal("same origin rejected")
	}
	r.Header.Set("Origin", "http://evil.example")
	if sameOrigin(r) {
		t.Fatal("cross origin accepted")
	}
}

func TestDevicePattern(t *testing.T) {
	valid := []string{"mac", "windows-laptop", "pc2"}
	invalid := []string{"", "Mac", "-pc", "pc_name", "../../root", "abcdefghijklmnopqrstuvwxyz"}
	for _, value := range valid {
		if !deviceNamePattern.MatchString(value) {
			t.Errorf("valid device rejected: %q", value)
		}
	}
	for _, value := range invalid {
		if deviceNamePattern.MatchString(value) {
			t.Errorf("invalid device accepted: %q", value)
		}
	}
}
