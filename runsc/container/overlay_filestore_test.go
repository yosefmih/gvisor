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
	"testing"
)

func TestOverlayUpperPath(t *testing.T) {
	const mountinfo = `24 30 0:22 / /proc rw,nosuid,nodev,noexec,relatime shared:13 - proc proc rw
30 1 259:1 / / rw,noatime shared:1 - xfs /dev/nvme0n1p1 rw,attr2,inode64
81 30 259:2 / /mnt/nvme rw,relatime shared:40 - xfs /dev/nvme1n1 rw
613 30 0:60 / /run/containerd/io.containerd.runtime.v2.task/k8s.io/abc/rootfs rw,relatime shared:250 - overlay overlay rw,lowerdir=/lower1:/lower2,upperdir=/mnt/nvme/containerd/snap/42/fs,workdir=/mnt/nvme/containerd/snap/42/work
614 613 0:61 / /run/containerd/io.containerd.runtime.v2.task/k8s.io/abc/rootfs/nested rw shared:251 - overlay overlay rw,lowerdir=/l,upperdir=/mnt/nvme/nested/fs,workdir=/mnt/nvme/nested/work
700 30 0:70 /  /run/spa\040ce/rootfs rw - overlay overlay rw,lowerdir=/l,upperdir=/mnt/nvme/spa\040ce/fs,workdir=/w
701 30 0:71 / /run/noupper/rootfs rw - overlay overlay rw,lowerdir=/l1:/l2
`
	for _, tc := range []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{
			name: "rootfs",
			path: "/run/containerd/io.containerd.runtime.v2.task/k8s.io/abc/rootfs/.gvisor.filestore.abc",
			want: "/mnt/nvme/containerd/snap/42/fs/.gvisor.filestore.abc",
		},
		{
			name: "longest_prefix_wins",
			path: "/run/containerd/io.containerd.runtime.v2.task/k8s.io/abc/rootfs/nested/f",
			want: "/mnt/nvme/nested/fs/f",
		},
		{
			name: "escaped_space",
			path: "/run/spa ce/rootfs/f",
			want: "/mnt/nvme/spa ce/fs/f",
		},
		{
			name:    "not_under_overlay",
			path:    "/mnt/nvme/filestores/f",
			wantErr: true,
		},
		{
			name:    "overlay_without_upperdir",
			path:    "/run/noupper/rootfs/f",
			wantErr: true,
		},
		{
			name:    "prefix_but_not_path_component",
			path:    "/run/containerd/io.containerd.runtime.v2.task/k8s.io/abc/rootfs2/f",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := overlayUpperPath([]byte(mountinfo), tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("overlayUpperPath(%q) = %q, want error", tc.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("overlayUpperPath(%q) failed: %v", tc.path, err)
			}
			if got != tc.want {
				t.Errorf("overlayUpperPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestUnescapeMountinfo(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/plain/path", "/plain/path"},
		{`/spa\040ce`, "/spa ce"},
		{`/tab\011and\012newline`, "/tab\tand\nnewline"},
		{`back\134slash`, `back\slash`},
		{`/trailing\04`, `/trailing\04`},
	} {
		if got := unescapeMountinfo(tc.in); got != tc.want {
			t.Errorf("unescapeMountinfo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
