//go:build linux

package store

import "os"

// plantDirLink creates a link at link pointing at the directory target. It is
// the Linux half of the platform pair spikemigration_test.go's shared tests
// use to plant a decoy: there is only one link flavour here, so this is
// os.Symlink. See the Windows half for why the pair exists at all (a junction
// needs no privilege, so a junction-flavoured decoy keeps the shared tests
// running on a Developer-Mode-off box instead of skipping).
func plantDirLink(link, target string) error { return os.Symlink(target, link) }
