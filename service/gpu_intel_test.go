package service

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSysfs(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanIntelGpus_ReadsTempFreqAndSkipsNonIntel(t *testing.T) {
	drm := t.TempDir()

	// card0: Intel GPU with an xe hwmon (pkg + vram temps) and xe frequency.
	c0 := filepath.Join(drm, "card0", "device")
	writeSysfs(t, filepath.Join(c0, "vendor"), "0x8086\n")
	writeSysfs(t, filepath.Join(c0, "hwmon", "hwmon0", "temp2_input"), "45000\n")
	writeSysfs(t, filepath.Join(c0, "hwmon", "hwmon0", "temp2_label"), "pkg\n")
	writeSysfs(t, filepath.Join(c0, "hwmon", "hwmon0", "temp3_input"), "60000\n")
	writeSysfs(t, filepath.Join(c0, "hwmon", "hwmon0", "temp3_label"), "vram\n")
	writeSysfs(t, filepath.Join(c0, "tile0", "gt0", "freq0", "act_freq"), "1200\n")

	// card1: non-Intel (NVIDIA vendor id) — the Intel scan must skip it.
	writeSysfs(t, filepath.Join(drm, "card1", "device", "vendor"), "0x10de\n")

	// renderD128: a render node, not a primary card — must be skipped.
	writeSysfs(t, filepath.Join(drm, "renderD128", "device", "vendor"), "0x8086\n")

	gpus := scanIntelGpus(drm)
	if len(gpus) != 1 {
		t.Fatalf("want exactly 1 Intel GPU, got %d: %+v", len(gpus), gpus)
	}
	g := gpus[0]
	if g.Vendor != "intel" {
		t.Errorf("Vendor = %q, want %q", g.Vendor, "intel")
	}
	if g.Temperature != 45 { // prefers the "pkg" sensor over the hotter "vram" one
		t.Errorf("Temperature = %v, want 45", g.Temperature)
	}
	if g.FreqMHz != 1200 {
		t.Errorf("FreqMHz = %v, want 1200", g.FreqMHz)
	}
}

func TestScanIntelGpus_MissingDirReturnsNil(t *testing.T) {
	if got := scanIntelGpus(filepath.Join(t.TempDir(), "does-not-exist")); got != nil {
		t.Errorf("want nil for missing drm dir, got %+v", got)
	}
}

func TestComputeBusyPct(t *testing.T) {
	cases := []struct {
		idleDelta, elapsed int64
		want               float64
	}{
		{1000, 1000, 0},   // fully idle
		{0, 1000, 100},    // never idle => fully busy
		{500, 1000, 50},   // half busy
		{1200, 1000, 0},   // idle slightly exceeds interval (jitter) => clamp to 0
		{-50, 1000, 100},  // negative delta => clamp to 100
		{500, 0, 0},       // no elapsed time
	}
	for _, c := range cases {
		if got := computeBusyPct(c.idleDelta, c.elapsed); got != c.want {
			t.Errorf("computeBusyPct(%d,%d) = %v, want %v", c.idleDelta, c.elapsed, got, c.want)
		}
	}
}

func TestReadGpuIdleResidenciesMs(t *testing.T) {
	// xe layout with two GTs (render + media).
	dev := filepath.Join(t.TempDir(), "device")
	writeSysfs(t, filepath.Join(dev, "tile0", "gt0", "gtidle", "idle_residency_ms"), "4800711\n")
	writeSysfs(t, filepath.Join(dev, "tile0", "gt1", "gtidle", "idle_residency_ms"), "123456\n")
	got := readGpuIdleResidenciesMs(dev)
	if len(got) != 2 || got[0] != 4800711 || got[1] != 123456 {
		t.Errorf("xe idle residencies = %v, want [4800711 123456]", got)
	}
	// missing => empty
	if got := readGpuIdleResidenciesMs(filepath.Join(t.TempDir(), "device")); len(got) != 0 {
		t.Errorf("missing counters = %v, want empty", got)
	}
}

func TestParseVramMM(t *testing.T) {
	content := `  use_type: 1
  use_tt: 0
  size: 25669140480
  usage: 21970649088
default_page_size: 4KiB
visible_avail: 3527MiB
visible_size: 24480MiB
chunk_size: 4KiB, total: 24480MiB, free: 3527MiB, clear_free: 0MiB
order-19 free:     2048 MiB, blocks: 1
`
	total, used := parseVramMM(content)
	if total != 25669140480 {
		t.Errorf("total = %d, want 25669140480", total)
	}
	if used != 21970649088 {
		t.Errorf("used = %d, want 21970649088", used)
	}
	// empty content (integrated GPU / debugfs unreadable) => 0, 0
	if tot, u := parseVramMM(""); tot != 0 || u != 0 {
		t.Errorf("empty => (%d,%d), want (0,0)", tot, u)
	}
}

func TestIsDrmCard(t *testing.T) {
	cases := map[string]bool{"card0": true, "card1": true, "card12": true,
		"renderD128": false, "card": false, "cardX": false, "controlD64": false}
	for name, want := range cases {
		if got := isDrmCard(name); got != want {
			t.Errorf("isDrmCard(%q) = %v, want %v", name, got, want)
		}
	}
}
