//go:build !unix

package collect

import "io/fs"

// inode is unknown here: links are named by the file's name alone.
func inode(fs.FileInfo) (uint64, bool) { return 0, false }
