package main

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
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

func TestRunContext_HasCancelRoutesEnabledField(t *testing.T) {
	_, ok := reflect.TypeOf(RunContext{}).FieldByName("CancelRoutesEnabled")
	if !ok {
		t.Error("RunContext must have a CancelRoutesEnabled field")
	}
}

func TestRunContext_NoPublicListenEnabledField(t *testing.T) {
	_, ok := reflect.TypeOf(RunContext{}).FieldByName("PublicListenEnabled")
	if ok {
		t.Error("RunContext must not have a PublicListenEnabled field; use CancelRoutesEnabled instead")
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

func TestConfigReloader_OnWatchEvent_Success_CallsReload(t *testing.T) {
	calls := 0
	r := &configReloader{
		reload: func() error {
			calls++
			return nil
		},
		registerWatch: func(cb func(event any, err error)) error { return nil },
	}

	r.onWatchEvent(nil, nil)

	if calls != 1 {
		t.Errorf("expected reload to be called once, got %d", calls)
	}
}

func TestConfigReloader_OnWatchEvent_ReloadFailure_DoesNotReregister(t *testing.T) {
	registered := 0
	r := &configReloader{
		reload: func() error { return fmt.Errorf("bad yaml") },
		registerWatch: func(cb func(event any, err error)) error {
			registered++
			return nil
		},
	}

	// A bad edit on a live watch event (err == nil, reload itself fails)
	// should just be logged and left for the next edit — the underlying
	// watcher is still alive in this case, so no re-registration is needed.
	r.onWatchEvent(nil, nil)

	if registered != 0 {
		t.Errorf("a failed reload from a live watch event should not re-register the watcher, got %d calls", registered)
	}
}

// TestConfigReloader_OnWatchEvent_AnyWatchError_Recovers is the regression
// test for the bug where only a "file was removed" error triggered
// recovery. koanf's file provider goroutine exits after calling back with
// ANY error (not just removal), so any error must trigger the same
// reload-and-re-register recovery or the watcher dies permanently and
// silently.
func TestConfigReloader_OnWatchEvent_AnyWatchError_Recovers(t *testing.T) {
	errs := []string{
		"file /etc/rss4transmission/config.yaml was removed",
		"fsnotify watch channel closed",
		"fsnotify err channel closed",
		"some other transient watch error",
	}
	for _, errMsg := range errs {
		t.Run(errMsg, func(t *testing.T) {
			reregistered := make(chan struct{}, 1)
			r := &configReloader{
				reload: func() error { return nil },
				registerWatch: func(cb func(event any, err error)) error {
					reregistered <- struct{}{}
					return nil
				},
				retryInterval: 0,
			}

			r.onWatchEvent(nil, fmt.Errorf("%s", errMsg))

			select {
			case <-reregistered:
			case <-time.After(2 * time.Second):
				t.Fatalf("expected watcher to be re-registered after error %q, but it was not", errMsg)
			}
		})
	}
}

func TestConfigReloader_Recover_RetriesUntilReloadSucceeds(t *testing.T) {
	calls := 0
	registered := 0
	r := &configReloader{
		reload: func() error {
			calls++
			if calls < 3 {
				return fmt.Errorf("not ready")
			}
			return nil
		},
		registerWatch: func(cb func(event any, err error)) error {
			registered++
			return nil
		},
		retryInterval: 0,
	}

	r.recover()

	if calls != 3 {
		t.Errorf("expected 3 reload attempts, got %d", calls)
	}
	if registered != 1 {
		t.Errorf("expected watcher to be re-registered once after recovery, got %d", registered)
	}
}

func TestConfigReloader_Recover_ReregisterFailureIsLoggedNotFatal(t *testing.T) {
	r := &configReloader{
		reload: func() error { return nil },
		registerWatch: func(cb func(event any, err error)) error {
			return fmt.Errorf("watch registration failed")
		},
		retryInterval: 0,
	}

	// Must not panic even though re-registration itself fails.
	r.recover()
}

// --- notifyReload wiring ---

func TestConfigReloader_OnWatchEvent_NilNotifyReload_DoesNotPanic(t *testing.T) {
	r := &configReloader{
		reload:        func() error { return nil },
		registerWatch: func(cb func(event any, err error)) error { return nil },
	}

	// notifyReload is unset (zero value nil); this must not panic.
	r.onWatchEvent(nil, nil)
}

func TestConfigReloader_Recover_NilNotifyReload_DoesNotPanic(t *testing.T) {
	r := &configReloader{
		reload:        func() error { return nil },
		registerWatch: func(cb func(event any, err error)) error { return nil },
		retryInterval: 0,
	}

	// notifyReload is unset (zero value nil); this must not panic.
	r.recover()
}

func TestConfigReloader_OnWatchEvent_Success_NotifiesWithNilError(t *testing.T) {
	var notified []error
	r := &configReloader{
		reload:        func() error { return nil },
		registerWatch: func(cb func(event any, err error)) error { return nil },
		notifyReload: func(err error) {
			notified = append(notified, err)
		},
	}

	r.onWatchEvent(nil, nil)

	if len(notified) != 1 {
		t.Fatalf("expected notifyReload to be called once, got %d", len(notified))
	}
	if notified[0] != nil {
		t.Errorf("expected notifyReload to be called with nil error, got %v", notified[0])
	}
}

func TestConfigReloader_OnWatchEvent_ReloadFailure_NotifiesWithError(t *testing.T) {
	wantErr := fmt.Errorf("bad yaml")
	var notified []error
	r := &configReloader{
		reload:        func() error { return wantErr },
		registerWatch: func(cb func(event any, err error)) error { return nil },
		notifyReload: func(err error) {
			notified = append(notified, err)
		},
	}

	r.onWatchEvent(nil, nil)

	if len(notified) != 1 {
		t.Fatalf("expected notifyReload to be called once, got %d", len(notified))
	}
	if notified[0] == nil || notified[0].Error() != wantErr.Error() {
		t.Errorf("expected notifyReload to be called with %v, got %v", wantErr, notified[0])
	}
}

// TestConfigReloader_OnWatchEvent_WatchError_DoesNotNotify confirms the
// err != nil (watcher-death) branch itself never calls notifyReload
// directly/synchronously — any notification only comes from recover(), and
// only once its reload has actually completed. reload here blocks until the
// test signals it to proceed, so we can observe the state in between: after
// recover() has started but before reload returns, notifyReload must still
// be unfired.
func TestConfigReloader_OnWatchEvent_WatchError_DoesNotNotify(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})
	reregistered := make(chan struct{}, 1)
	var notifyCount int
	var mu sync.Mutex
	r := &configReloader{
		reload: func() error {
			close(started)
			<-proceed
			return nil
		},
		registerWatch: func(cb func(event any, err error)) error {
			reregistered <- struct{}{}
			return nil
		},
		retryInterval: 0,
		notifyReload: func(err error) {
			mu.Lock()
			notifyCount++
			mu.Unlock()
		},
	}

	r.onWatchEvent(nil, fmt.Errorf("fsnotify watch channel closed"))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected recover() to invoke reload, but it never started")
	}

	mu.Lock()
	got := notifyCount
	mu.Unlock()
	if got != 0 {
		t.Fatalf("notifyReload must not fire before reload completes, got %d calls", got)
	}

	close(proceed)

	select {
	case <-reregistered:
	case <-time.After(2 * time.Second):
		t.Fatal("expected watcher to be re-registered after error, but it was not")
	}

	mu.Lock()
	got = notifyCount
	mu.Unlock()
	if got != 1 {
		t.Errorf("expected notifyReload to be called once (by recover(), after reload completed), got %d", got)
	}
}

func TestConfigReloader_Recover_NotifiesSuccessOnceAfterRetries(t *testing.T) {
	calls := 0
	var notified []error
	r := &configReloader{
		reload: func() error {
			calls++
			if calls < 3 {
				return fmt.Errorf("not ready")
			}
			return nil
		},
		registerWatch: func(cb func(event any, err error)) error { return nil },
		retryInterval: 0,
		notifyReload: func(err error) {
			notified = append(notified, err)
		},
	}

	r.recover()

	if len(notified) != 1 {
		t.Fatalf("expected notifyReload to be called exactly once, got %d", len(notified))
	}
	if notified[0] != nil {
		t.Errorf("expected notifyReload to be called with nil error, got %v", notified[0])
	}
}

func TestConfigReloader_Recover_DoesNotNotifyPerFailedAttempt(t *testing.T) {
	calls := 0
	notifyCalls := 0
	r := &configReloader{
		reload: func() error {
			calls++
			if calls < 3 {
				return fmt.Errorf("not ready")
			}
			return nil
		},
		registerWatch: func(cb func(event any, err error)) error { return nil },
		retryInterval: 0,
		notifyReload: func(err error) {
			notifyCalls++
		},
	}

	r.recover()

	if notifyCalls != 1 {
		t.Errorf("expected notifyReload to be called once (not per retry attempt), got %d", notifyCalls)
	}
}
