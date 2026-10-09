//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package art

import "os"

// CellSizeOf reports false: the cell size is not available here.
func CellSizeOf(f *os.File) (CellSize, bool) { return CellSize{}, false }

// OpenTTY returns nil: terminals are not queried on this platform.
func OpenTTY(in, out *os.File) (Terminal, func()) { return nil, func() {} }
