package main

import (
	"reflect"
	"testing"
)

func TestWatchCmd_HasHistoryFileField(t *testing.T) {
	_, ok := reflect.TypeOf(WatchCmd{}).FieldByName("HistoryFile")
	if !ok {
		t.Error("WatchCmd must have a HistoryFile field")
	}
}

func TestConfig_NoHistoryFileField(t *testing.T) {
	_, ok := reflect.TypeOf(Config{}).FieldByName("HistoryFile")
	if ok {
		t.Error("Config must not have a HistoryFile field; it belongs on WatchCmd")
	}
}
