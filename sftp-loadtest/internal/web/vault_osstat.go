package web

import "os"

// osStatImpl is the real os.Stat. Lives in its own file so a future
// test can swap a stub via a separate _test.go. Keeps vault_handlers.go
// importable without an os dependency on the test seam.
func osStatImpl(path string) (any, error) {
	return os.Stat(path)
}
