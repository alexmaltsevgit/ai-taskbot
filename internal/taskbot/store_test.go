package taskbot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsCrashBarrierAndOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	draft := Draft{Token: "a", Source: "s", State: "preview", Created: time.Now()}
	if err = store.Save(draft); err != nil {
		t.Fatal(err)
	}
	ok, err := store.Transition("a", "preview", "creating", "")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = store.SetOffset(42); err != nil {
		t.Fatal(err)
	}
	loaded, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := loaded.Get("a")
	if !ok || row.State != "creating" || loaded.Offset() != 42 {
		t.Fatal(row)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
func TestStoreDoesNotRecoverCorruptionAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`broken`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("corrupted state silently reset")
	}
}
func TestStateLockRejectsSecondProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	lock, err := LockState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	second, err := LockState(path)
	if err == nil {
		second.Close()
		t.Fatal("second lock acquired")
	}
}
