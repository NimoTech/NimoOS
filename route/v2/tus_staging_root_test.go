package v2

import "testing"

func TestResolveStagingRoot(t *testing.T) {
	mounts := []MountEntry{
		{Mountpoint: "/", FSType: "overlay"},
		{Mountpoint: "/DATA", FSType: "ext4"},
		{Mountpoint: "/media/RAID_0", FSType: "btrfs"},
		{Mountpoint: "/media/Cloud", FSType: "fuse.rclone"},
		{Mountpoint: "/media/MergerPool", FSType: "fuse.mergerfs"},
	}

	cases := []struct {
		name         string
		targetPath   string
		wantRoot     string
		wantFellBack bool
	}{
		{"DATA subpath resolves to DATA", "/DATA/Documents/a.txt", "/DATA", false},
		{"DATA itself resolves to DATA", "/DATA", "/DATA", false},
		{"RAID subpath resolves to RAID root", "/media/RAID_0/photos/x.jpg", "/media/RAID_0", false},
		{"fuse.rclone falls back to DATA", "/media/Cloud/x.jpg", "/DATA", true},
		{"mergerfs is allowed, resolves to its own root", "/media/MergerPool/x.jpg", "/media/MergerPool", false},
		{"no matching mount falls back to DATA", "/opt/somewhere/x.jpg", "/DATA", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, fellBack := resolveStagingRoot(c.targetPath, mounts)
			if root != c.wantRoot {
				t.Errorf("root = %q, want %q", root, c.wantRoot)
			}
			if fellBack != c.wantFellBack {
				t.Errorf("fellBack = %v, want %v", fellBack, c.wantFellBack)
			}
		})
	}
}

// 最长前缀优先:/media/RAID_0 与 / 同时匹配时,选更深的 /media/RAID_0。
func TestResolveStagingRootLongestPrefixWins(t *testing.T) {
	mounts := []MountEntry{
		{Mountpoint: "/", FSType: "ext4"}, // 根本身也是允许类型,但更浅
		{Mountpoint: "/media/RAID_0", FSType: "ext4"},
	}
	root, fellBack := resolveStagingRoot("/media/RAID_0/x.jpg", mounts)
	if root != "/media/RAID_0" || fellBack {
		t.Fatalf("got root=%q fellBack=%v, want /media/RAID_0,false", root, fellBack)
	}
}

// 无任何挂载点信息(空快照)时必须安全回退,不panic。
func TestResolveStagingRootEmptyMounts(t *testing.T) {
	root, fellBack := resolveStagingRoot("/DATA/x.jpg", nil)
	if root != "/DATA" || !fellBack {
		t.Fatalf("got root=%q fellBack=%v, want /DATA,true", root, fellBack)
	}
}
