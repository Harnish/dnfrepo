package imagemirror

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStartClearsStaging(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "run-old")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	(&Mirrors{StagingDir: dir}).Start(ctx, 0)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale staging kept: %v", err)
	}
}
