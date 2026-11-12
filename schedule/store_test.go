package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreStartsEmptyWithoutAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if got := store.Schedule(); len(got.Rules) != 0 {
		t.Errorf("a new store has %d rules", len(got.Rules))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("OpenStore created the file before anything was saved (stat error: %v)", err)
	}
}

func TestStoreLoadsAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(path, []byte(validSchedule), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if got := store.Schedule(); len(got.Rules) != 3 || got.Rules[0].ID != "evening-on" {
		t.Errorf("store loaded %+v", got)
	}
}

func TestStoreRejectsABadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(path, []byte(`{"rules": [{"id": "a"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("OpenStore error = %v, want one naming the file", err)
	}
}

func TestStoreReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	next := eveningSchedule()
	if err := store.Replace(next); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := store.Schedule(); got.Digest() != next.Digest() {
		t.Errorf("the store holds %+v after Replace", got)
	}

	// The file holds the same schedule, in a form Load accepts.
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load of the saved file: %v", err)
	}
	if loaded.Digest() != next.Digest() {
		t.Errorf("the file holds %+v", loaded)
	}

	// A change is announced.
	select {
	case <-store.Changes():
	default:
		t.Error("Replace did not announce the change")
	}

	// Nothing else was left in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries after Replace, want only the schedule file", len(entries))
	}
}

func TestStoreReplaceWithAnEmptyScheduleWritesAList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Replace(Schedule{}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"rules": []`) {
		t.Errorf("an empty schedule was written as:\n%s", data)
	}
}

func TestStoreKeepsTheOldScheduleWhenReplaceFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	good := eveningSchedule()
	if err := store.Replace(good); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	<-store.Changes()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	bad := Schedule{Rules: []Rule{{ID: "no-days", Time: TimeOfDay{8, 0}, Action: On}}}
	if err := store.Replace(bad); err == nil {
		t.Fatal("Replace accepted an invalid schedule")
	}

	if got := store.Schedule(); got.Digest() != good.Digest() {
		t.Error("a failed Replace changed the store's schedule")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("a failed Replace changed the file")
	}
	select {
	case <-store.Changes():
		t.Error("a failed Replace announced a change")
	default:
	}
}

func TestStoreReplaceFailsWhenTheFileCannotBeWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "schedule.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Replace(eveningSchedule()); err == nil {
		t.Fatal("Replace succeeded although the file could not be written")
	}
	if got := store.Schedule(); len(got.Rules) != 0 {
		t.Error("the store took on a schedule it could not save")
	}
}

func TestStoreHandsOutCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	original := eveningSchedule()
	if err := store.Replace(original); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	// Changing what was passed in, or what was handed out, must not reach
	// the store.
	original.Rules[0].Action = Off
	handed := store.Schedule()
	handed.Rules[1].Action = On

	if got := store.Schedule(); got.Digest() != eveningSchedule().Digest() {
		t.Errorf("the store's schedule was changed from outside: %+v", got)
	}
}

func TestStoreIsSafeForConcurrentUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := store.Replace(eveningSchedule()); err != nil {
				t.Errorf("Replace: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			_ = store.Schedule().Digest()
		}()
	}
	wg.Wait()

	if _, err := Load(path); err != nil {
		t.Errorf("the file is not a valid schedule after concurrent writes: %v", err)
	}
}
