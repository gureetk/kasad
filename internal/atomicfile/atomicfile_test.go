package atomicfile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gureetk/kasad/internal/atomicfile"
)

func TestWriteReplacesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.json")

	for _, content := range []string{"first\n", "second, and longer\n", ""} {
		if err := atomicfile.Write(path, []byte(content)); err != nil {
			t.Fatalf("Write(%q): %v", content, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != content {
			t.Errorf("the file holds %q, want %q", got, content)
		}
	}

	// Nothing is left beside the file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "notes.json" {
		t.Errorf("the directory holds %v, want only notes.json", entries)
	}
}

func TestWriteKeepsTheFileToItsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes mean something else on Windows")
	}
	path := filepath.Join(t.TempDir(), "notes.json")
	if err := atomicfile.Write(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the file's mode is %o, want 600", mode)
	}
}

func TestWriteFailureLeavesTheFileAlone(t *testing.T) {
	dir := t.TempDir()

	// There is no directory to write the temporary file in.
	missing := filepath.Join(dir, "missing", "notes.json")
	if err := atomicfile.Write(missing, []byte("x")); err == nil {
		t.Error("Write into a directory that does not exist reported no error")
	}

	// The name is taken by a directory that is not empty, so the rename
	// fails. What was there stays, and the temporary file is cleared away.
	taken := filepath.Join(dir, "taken")
	if err := os.MkdirAll(filepath.Join(taken, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(taken, []byte("x")); err == nil {
		t.Error("Write over a directory reported no error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "taken" {
		t.Errorf("the directory holds %v, want only taken", entries)
	}
}
