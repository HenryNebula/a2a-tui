package render

import (
	"os"
	"testing"
)

// readFileForTest is a tiny helper so the render tests can read fixtures
// from the parent package's testdata directory.
func readFileForTest(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
