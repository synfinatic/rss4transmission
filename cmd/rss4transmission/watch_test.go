package main

import (
	"fmt"
	"reflect"
	"testing"
)

func TestWatchCmd_HasHistoryFileField(t *testing.T) {
	_, ok := reflect.TypeOf(WatchCmd{}).FieldByName("HistoryFile")
	if !ok {
		t.Error("WatchCmd must have a HistoryFile field")
	}
}

func TestWatchCmd_HasAccessLogField(t *testing.T) {
	_, ok := reflect.TypeOf(WatchCmd{}).FieldByName("AccessLog")
	if !ok {
		t.Error("WatchCmd must have an AccessLog field")
	}
}

func TestWatchCmd_HasPrivateListenField(t *testing.T) {
	_, ok := reflect.TypeOf(WatchCmd{}).FieldByName("PrivateListen")
	if !ok {
		t.Error("WatchCmd must have a PrivateListen field")
	}
}

func TestWatchCmd_HasPublicListenField(t *testing.T) {
	_, ok := reflect.TypeOf(WatchCmd{}).FieldByName("PublicListen")
	if !ok {
		t.Error("WatchCmd must have a PublicListen field")
	}
}

func TestWatchCmd_NoHistoryListenField(t *testing.T) {
	_, ok := reflect.TypeOf(WatchCmd{}).FieldByName("HistoryListen")
	if ok {
		t.Error("WatchCmd must not have a HistoryListen field; use PrivateListen instead")
	}
}

func TestWatchCmd_NoCancelListenField(t *testing.T) {
	_, ok := reflect.TypeOf(WatchCmd{}).FieldByName("CancelListen")
	if ok {
		t.Error("WatchCmd must not have a CancelListen field; use PublicListen instead")
	}
}

func TestConfig_NoHistoryFileField(t *testing.T) {
	_, ok := reflect.TypeOf(Config{}).FieldByName("HistoryFile")
	if ok {
		t.Error("Config must not have a HistoryFile field; it belongs on WatchCmd")
	}
}

func TestRetryLoadConfig_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	attempt := retryLoadConfig(func() error {
		calls++
		return nil
	}, 0)

	if attempt != 1 {
		t.Errorf("expected attempt=1, got %d", attempt)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryLoadConfig_SuccessAfterFailures(t *testing.T) {
	calls := 0
	attempt := retryLoadConfig(func() error {
		calls++
		if calls < 3 {
			return fmt.Errorf("not ready yet")
		}
		return nil
	}, 0)

	if attempt != 3 {
		t.Errorf("expected attempt=3, got %d", attempt)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}
