package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func resolveToPhysicalDisks(dev string) []string {
	seen := map[string]bool{}
	var out []string
	queue := []string{dev}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == "" || seen[cur] {
			continue
		}
		seen[cur] = true
		entries, err := os.ReadDir("/sys/class/block/" + cur + "/slaves")
		if err == nil && len(entries) > 0 {
			for _, e := range entries {
				queue = append(queue, e.Name())
			}
			continue
		}
		if _, err := os.Stat("/sys/class/block/" + cur + "/partition"); err == nil {
			if link, err := os.Readlink("/sys/class/block/" + cur); err == nil {
				parent := filepath.Base(filepath.Dir(link))
				if parent != "" && parent != "." && parent != "/" {
					cur = parent
				}
			}
		}
		out = append(out, cur)
	}
	return out
}

func main() {
	fmt.Println("=== /proc/mounts root entries ===")
	data, _ := os.ReadFile("/proc/mounts")
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if f[1] == "/" || f[1] == "/boot" || f[1] == "/boot/efi" {
			fmt.Printf("  device=%s mount=%s\n", f[0], f[1])
			name := strings.TrimPrefix(f[0], "/dev/")
			if strings.HasPrefix(name, "mapper/") {
				if link, err := os.Readlink("/dev/" + name); err == nil {
					name = filepath.Base(link)
				}
			}
			fmt.Printf("  resolved name=%q\n", name)
			fmt.Printf("  physical disks=%v\n", resolveToPhysicalDisks(name))
		}
	}
	fmt.Println("=== /proc/cmdline ===")
	cmd, _ := os.ReadFile("/proc/cmdline")
	fmt.Printf("  %s\n", string(cmd))
	fmt.Println("=== /sys/block listing ===")
	entries, _ := os.ReadDir("/sys/block/")
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	fmt.Printf("  %v\n", names)
}
