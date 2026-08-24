// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package container

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
)

// maybeReopenFilestoreInUpperLayer returns a file referring directly to the
// host filesystem file backing filestoreFile, when the filestore is inside an
// overlayfs mount (e.g. a container root filesystem set up by containerd with
// a "self"-medium overlay filestore). In that case filestoreFile's FD is an
// overlayfs inode, on which FICLONE always fails with EXDEV, even though the
// file's data lives in the overlay's upper layer; a file in the upper layer
// shares its inode and page cache with the overlayfs file, so an FD opened
// directly on the upper layer path is interchangeable for data operations and
// additionally supports FICLONE if the upper filesystem does.
//
// The filestore was opened at nsPath as seen inside the gofer's mount
// namespace, reached via goferRootfs ("/proc/<gofer pid>/root"); the overlay
// mount is resolved from that namespace's mountinfo, and the upper layer file
// is also opened through goferRootfs so that this works regardless of the
// gofer's mount namespace and chroot configuration.
//
// On success, filestoreFile is closed and the upper layer file is returned.
// On any failure the reopen is skipped and filestoreFile is returned
// unchanged; this only forgoes FICLONE support, which is reported when it is
// actually needed (reflink filesystem checkpoints).
func maybeReopenFilestoreInUpperLayer(filestoreFile *os.File, goferRootfs, nsPath string) *os.File {
	var stfs unix.Statfs_t
	if err := unix.Fstatfs(int(filestoreFile.Fd()), &stfs); err != nil || stfs.Type != unix.OVERLAYFS_SUPER_MAGIC {
		return filestoreFile
	}
	filestorePath := filepath.Join(goferRootfs, nsPath)
	procDir, ok := strings.CutSuffix(goferRootfs, "/root")
	if !ok {
		log.Warningf("Filestore %q is on overlayfs, but %q is not a /proc/[pid]/root path; reflink filesystem checkpoints will not work", filestorePath, goferRootfs)
		return filestoreFile
	}
	mountinfo, err := os.ReadFile(procDir + "/mountinfo")
	if err != nil {
		log.Warningf("Filestore %q is on overlayfs, but reading mountinfo failed: %v; reflink filesystem checkpoints will not work", filestorePath, err)
		return filestoreFile
	}
	upperNSPath, err := overlayUpperPath(mountinfo, nsPath)
	if err != nil {
		log.Warningf("Filestore %q is on overlayfs, but resolving its upper layer file failed: %v; reflink filesystem checkpoints will not work", filestorePath, err)
		return filestoreFile
	}
	upperPath := filepath.Join(goferRootfs, upperNSPath)
	upperFD, err := unix.Open(upperPath, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		log.Warningf("Filestore %q is on overlayfs, but opening its upper layer file %q failed: %v; reflink filesystem checkpoints will not work", filestorePath, upperPath, err)
		return filestoreFile
	}
	// For a file resident in the upper layer, overlayfs reports the
	// underlying file's inode number, so a mismatch indicates that upperPath
	// does not back filestorePath.
	var overlaySt, upperSt unix.Stat_t
	if err := unix.Fstat(int(filestoreFile.Fd()), &overlaySt); err == nil {
		if err := unix.Fstat(upperFD, &upperSt); err == nil {
			if overlaySt.Ino == upperSt.Ino && overlaySt.Size == upperSt.Size {
				filestoreFile.Close()
				log.Infof("Reopened filestore %q via overlay upper layer file %q", filestorePath, upperPath)
				return os.NewFile(uintptr(upperFD), upperPath)
			}
		}
	}
	unix.Close(upperFD)
	log.Warningf("Filestore %q is on overlayfs, but upper layer file %q does not match it; reflink filesystem checkpoints will not work", filestorePath, upperPath)
	return filestoreFile
}

// overlayUpperPath returns the path of the overlay upper layer file backing
// path, which must be inside an overlayfs mount, based on the given
// /proc/[pid]/mountinfo contents.
func overlayUpperPath(mountinfo []byte, path string) (string, error) {
	path = filepath.Clean(path)
	// Find the overlay mount with the longest mount point that is a prefix
	// of path. Among equal mount points, later entries shadow earlier ones.
	bestLen := -1
	bestUpperdir := ""
	bestMountPoint := ""
	for _, line := range strings.Split(string(mountinfo), "\n") {
		// Fields: id parentID major:minor root mountPoint mountOpts
		// [optional...] - fstype source superOpts
		sep := strings.Index(line, " - ")
		if sep < 0 {
			continue
		}
		fields := strings.Fields(line[:sep])
		if len(fields) < 5 {
			continue
		}
		fsFields := strings.Fields(line[sep+3:])
		if len(fsFields) < 3 || fsFields[0] != "overlay" {
			continue
		}
		mountPoint := unescapeMountinfo(fields[4])
		if path != mountPoint && !strings.HasPrefix(path, mountPoint+"/") {
			continue
		}
		if len(mountPoint) < bestLen {
			continue
		}
		upperdir := ""
		for _, opt := range strings.Split(fsFields[2], ",") {
			if val, ok := strings.CutPrefix(opt, "upperdir="); ok {
				upperdir = unescapeMountinfo(val)
				break
			}
		}
		if upperdir == "" {
			continue
		}
		bestLen = len(mountPoint)
		bestUpperdir = upperdir
		bestMountPoint = mountPoint
	}
	if bestLen < 0 {
		return "", fmt.Errorf("no overlay mount with an upperdir found for %q", path)
	}
	return filepath.Join(bestUpperdir, strings.TrimPrefix(path, bestMountPoint)), nil
}

// unescapeMountinfo undoes the octal escaping of space, tab, newline, and
// backslash used in /proc/[pid]/mountinfo fields.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) &&
			s[i+1] >= '0' && s[i+1] <= '3' &&
			s[i+2] >= '0' && s[i+2] <= '7' &&
			s[i+3] >= '0' && s[i+3] <= '7' {
			sb.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
		} else {
			sb.WriteByte(s[i])
		}
	}
	return sb.String()
}
