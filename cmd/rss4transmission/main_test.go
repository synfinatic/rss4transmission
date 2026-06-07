package main

import (
	"io"
	"os"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	log = logrus.New()
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

func TestGetPath_Tilde(t *testing.T) {
	home := os.Getenv("HOME")
	got := GetPath("~/foo/bar")
	want := home + "/foo/bar"
	if got != want {
		t.Errorf("GetPath(~/foo/bar) = %q, want %q", got, want)
	}
}

func TestGetPath_NoTilde(t *testing.T) {
	got := GetPath("/etc/hosts")
	if got != "/etc/hosts" {
		t.Errorf("GetPath(/etc/hosts) = %q, want /etc/hosts", got)
	}
}

func TestGetPath_RelativePath(t *testing.T) {
	got := GetPath("relative/path")
	if got != "relative/path" {
		t.Errorf("GetPath(relative/path) = %q, want relative/path", got)
	}
}
