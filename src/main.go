// linux-tty: console menu for headless Linux appliances.
// Single static binary, no external deps. Runs on RHEL/CentOS/Rocky/Alma and
// Debian/Ubuntu x86_64.
//
// Invoked by systemd on a physical tty, it shows the interactive console menu.
package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// ---------- globals ----------

var (
	rel         = "SSCLOUD Linux-TTY Console"
	myTTY       string // e.g. tty1 / ttyS0
	interrupt   atomic.Int32
	nmActive    atomic.Int32
	stdinIsTty  = false
	eofFlag     atomic.Bool
	origTermios *termios
	netBackend  = "rhel" // "rhel" = ifcfg+nmcli, "debian" = /etc/network/interfaces, "netplan" = /etc/netplan
)

const (
	ifcfgDir    = "/etc/sysconfig/network-scripts"
	bakDir      = "/var/backups/netcfg"
	maxTries    = 3
	interfacesF = "/etc/network/interfaces"
)

// version is overridden at build time via -ldflags "-X main.version=vX.Y.Z"
var version = "v1.0.0"

// ---------- termios ----------

// Linux x86_64 struct termios: tcflag_t is uint32, not uintptr.
type termios struct {
	Iflag, Oflag, Cflag, Lflag uint32
	Line                       byte
	Cc                         [32]byte
	Ispeed, Ospeed             uint32
}

func tcGet(fd int) (*termios, error) {
	t := &termios{}
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(t)))
	if e != 0 {
		return nil, e
	}
	return t, nil
}

func tcSet(fd int, t *termios) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(t)))
	if e != 0 {
		return e
	}
	return nil
}

// enterCbreak puts stdin into cbreak mode: no ICANON (char-by-char), no ECHO
// (ask() does its own echoing), VMIN=0 + VTIME=2 so Read() returns after 200ms
// even with no input. Saves the original termios on first call.
func enterCbreak() {
	if !stdinIsTty {
		return
	}
	fd := int(os.Stdin.Fd())
	if origTermios == nil {
		t, err := tcGet(fd)
		if err != nil {
			return
		}
		origTermios = t
	}
	t := *origTermios
	t.Lflag &^= syscall.ICANON
	t.Lflag &^= syscall.ECHO
	t.Cc[5] = 2 // VTIME = 2 deciseconds = 200ms
	t.Cc[6] = 0 // VMIN = 0
	tcSet(fd, &t)
}

// exitCbreak restores the original termios.
func exitCbreak() {
	if stdinIsTty && origTermios != nil {
		tcSet(int(os.Stdin.Fd()), origTermios)
	}
}

// ---------- small helpers ----------

func run(name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	var b bytes.Buffer
	c.Stdout = &b
	c.Stderr = &b
	err := c.Run()
	return strings.TrimSpace(b.String()), err
}

// runBg runs a command and streams its stdout to our stdout (used by ping).
func runBg(name string, args ...string) int {
	c := exec.Command(name, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	err := c.Run()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func readFileTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---------- signal handling ----------

func initSignals() {
	ch := make(chan os.Signal, 16)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range ch {
			if s == os.Interrupt {
				interrupt.Store(1)
			} else if s == syscall.SIGTERM {
				exitCbreak()
				os.Exit(143)
			} else if s == syscall.SIGHUP {
				exitCbreak()
				os.Exit(0)
			}
		}
	}()
}

// ---------- tty / getty ----------

func detectTTY() {
	out := ""
	// Preferred: readlink our stdin. run("tty") would report "not a tty"
	// because the child's stdin defaults to /dev/null, not our console.
	if link, err := os.Readlink("/proc/self/fd/0"); err == nil {
		out = link
	}
	if !strings.HasPrefix(out, "/dev/") {
		c := exec.Command("tty")
		c.Stdin = os.Stdin
		var b bytes.Buffer
		c.Stdout = &b
		c.Stderr = &b
		c.Run()
		out = strings.TrimSpace(b.String())
	}
	myTTY = strings.TrimPrefix(out, "/dev/")
}

// ---------- migrate old ifcfg backups ----------

var bakNameRe = regexp.MustCompile(`^\.[0-9]{8}_[0-9]{6}$`)

func migrateIfcfgBackups() {
	os.MkdirAll(bakDir, 0700)
	// migrate /root/ifcfg-backup
	if entries, err := os.ReadDir("/root/ifcfg-backup"); err == nil {
		for _, e := range entries {
			os.Rename("/root/ifcfg-backup/"+e.Name(), filepath.Join(bakDir, e.Name()))
		}
		os.RemoveAll("/root/ifcfg-backup")
	}
	entries, err := os.ReadDir(ifcfgDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "ifcfg-") {
			continue
		}
		if name == "ifcfg-lo" {
			continue
		}
		// keep VLAN names like ifcfg-eth0.100, keep files ending in .bak/.old etc (those are already ignored by initscripts)
		switch {
		case strings.HasSuffix(name, "~"),
			strings.HasSuffix(name, ".bak"),
			strings.HasSuffix(name, ".old"),
			strings.HasSuffix(name, ".orig"),
			strings.HasSuffix(name, ".rpmnew"),
			strings.HasSuffix(name, ".rpmorig"),
			strings.HasSuffix(name, ".rpmsave"):
			// these are properly ignored; leave them
		default:
			// timestamped backup suffix: .YYYYMMDD_HHMMSS
			dot := strings.LastIndex(name, ".")
			if dot > 0 && bakNameRe.MatchString(name[dot:]) {
				os.Rename(filepath.Join(ifcfgDir, name), filepath.Join(bakDir, name))
			}
		}
	}
}

// ---------- ask(): char-by-char line editor ----------

// returns (value, interrupted)
func ask(prompt string) (string, bool) {
	fmt.Print(prompt)
	var buf []byte
	readBuf := make([]byte, 1)
	for {
		if eofFlag.Load() {
			return "", false
		}
		if interrupt.Load() == 1 {
			return "", true
		}
		n, err := os.Stdin.Read(readBuf)
		if err != nil {
			// On a tty, never exit on EOF/error - Ctrl-D or transient errors
			// should just return to the menu loop, not kill the process.
			// Only exit when stdin is a pipe and the writer closed it.
			if !stdinIsTty && err == io.EOF {
				eofFlag.Store(true)
				return "", false
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if n == 0 {
			continue // timeout (VMIN=0,VTIME=2): poll again
		}
		c := readBuf[0]
		switch c {
		case '\n', '\r':
			fmt.Println()
			return string(buf), false
		case 0x03: // Ctrl+C
			fmt.Println()
			interrupt.Store(1)
			return "", true
		case 0x1b: // ESC or CSI escape sequence (arrow/Home/End/Delete/PgUp/PgDn)
			peek := make([]byte, 1)
			pn, _ := os.Stdin.Read(peek)
			if pn == 1 && peek[0] == '[' {
				// Consume the rest of the CSI sequence until a final byte
				// (a letter A-Z or '~'). VMIN=0 means Read returns quickly.
				for {
					b := make([]byte, 1)
					bn, _ := os.Stdin.Read(b)
					if bn != 1 {
						break
					}
					c := b[0]
					if (c >= 'A' && c <= 'Z') || c == '~' {
						break
					}
				}
				continue // CSI sequence discarded
			}
			// Bare ESC: treat as interrupt.
			fmt.Println()
			interrupt.Store(1)
			return "", true
		case 0x7f, 0x08: // ^? and ^H
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				fmt.Print("\b \b")
			}
		default:
			if c >= 0x20 {
				buf = append(buf, c)
				fmt.Printf("%c", c)
			}
		}
	}
}

// isVTConsole reports whether tty is a virtual console (tty1, tty2, ...),
// excluding serial (ttyS*, ttyAMA*) and pseudo-terminals.
func isVTConsole(tty string) bool {
	if !strings.HasPrefix(tty, "tty") {
		return false
	}
	rest := tty[3:]
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func pause() {
	fmt.Println()
	ask("Press Enter to return to the menu... ")
}

func confirm(prompt string) bool {
	a, intr := ask(prompt + " [y/N]: ")
	if intr {
		return false
	}
	a = strings.TrimSpace(a)
	return a == "y" || a == "Y" || a == "yes" || a == "YES"
}

// ---------- NIC helpers ----------

func listNics() []string {
	out, _ := run("ip", "-o", "link", "show")
	var nics []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		// 1: lo: <LOOPBACK,...>
		parts := strings.SplitN(line, ": ", 3)
		if len(parts) < 3 {
			continue
		}
		name := strings.SplitN(parts[1], "@", 2)[0]
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" {
			continue
		}
		if matchRe(name, `^(docker|veth|virbr|br-|tun|tap|vnet|kube|cali|flannel|cni)`) {
			continue
		}
		if !seen[name] {
			seen[name] = true
			nics = append(nics, name)
		}
	}
	return nics
}

func matchRe(s, pat string) bool {
	ok, _ := regexp.MatchString(pat, s)
	return ok
}

func nicOperstate(nic string) string {
	return readFileTrim("/sys/class/net/" + nic + "/operstate")
}

// detectDistro reads /etc/os-release and sets netBackend.
// RHEL family (centos/rhel/rocky/almalinux/fedora) -> "rhel"
// Debian family (debian/ubuntu/kali) -> "debian" or "netplan"
//
// A netplan backend is only selected when /etc/netplan contains a YAML with
// an actual interface block (ethernets/bonds/bridges/vlans). Stub files with
// only "version: 2" are ignored, avoiding a conflict with traditional
// /etc/network/interfaces on Debian/Ubuntu systems that ship both.
func detectDistro() {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return
	}
	id := ""
	idLike := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ID=") {
			id = strings.Trim(strings.TrimPrefix(line, "ID="), `"`)
		}
		if strings.HasPrefix(line, "ID_LIKE=") {
			idLike = strings.Trim(strings.TrimPrefix(line, "ID_LIKE="), `"`)
		}
	}
	combined := strings.ToLower(id + " " + idLike)
	// Debian family
	for _, d := range []string{"debian", "ubuntu", "kali"} {
		if strings.Contains(combined, d) {
			netBackend = "debian"
			if entries, err := os.ReadDir("/etc/netplan"); err == nil {
				for _, e := range entries {
					name := e.Name()
					if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
						continue
					}
					b, err := os.ReadFile(filepath.Join("/etc/netplan", name))
					if err != nil {
						continue
					}
					text := string(b)
					if strings.Contains(text, "ethernets:") ||
						strings.Contains(text, "bonds:") ||
						strings.Contains(text, "bridges:") ||
						strings.Contains(text, "vlans:") {
						netBackend = "netplan"
						return
					}
				}
			}
			return
		}
	}
	// RHEL family is default
	netBackend = "rhel"
}

// ---------- Debian /etc/network/interfaces backend ----------

// debianBackupIface backs up the interfaces file before rewriting.
func debianBackupIface() string {
	os.MkdirAll(bakDir, 0700)
	bak := filepath.Join(bakDir, fmt.Sprintf("interfaces.%s", time.Now().Format("20060102_150405")))
	if b, err := os.ReadFile(interfacesF); err == nil {
		os.WriteFile(bak, b, 0600)
	}
	return bak
}

// debianRewriteBlock replaces (or inserts) the auto+iface block for nic in
// /etc/network/interfaces, preserving all other stanzas (lo, source, other NICs).
func debianRewriteBlock(nic, block string) error {
	var existing []string
	if data, err := os.ReadFile(interfacesF); err == nil {
		existing = strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	} else if !os.IsNotExist(err) {
		return err
	}
	// Walk existing lines, drop old auto/allow-hotplug entries mentioning nic,
	// and drop the entire iface <nic> stanza (including indented child lines).
	var kept []string
	for i := 0; i < len(existing); i++ {
		line := existing[i]
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && (fields[0] == "auto" || fields[0] == "allow-hotplug") {
			var remain []string
			for _, name := range fields[1:] {
				if name != nic {
					remain = append(remain, name)
				}
			}
			if len(remain) == 0 {
				continue
			}
			kept = append(kept, fields[0]+" "+strings.Join(remain, " "))
			continue
		}
		if len(fields) >= 2 && fields[0] == "iface" && fields[1] == nic {
			// skip this stanza and all indented continuation lines
			for i+1 < len(existing) && (strings.HasPrefix(existing[i+1], " ") || strings.HasPrefix(existing[i+1], "\t")) {
				i++
			}
			continue
		}
		kept = append(kept, line)
	}
	// Ensure loopback stanza exists
	hasLo := false
	for _, l := range kept {
		if strings.HasPrefix(strings.TrimSpace(l), "iface lo ") {
			hasLo = true
			break
		}
	}
	if !hasLo {
		kept = append([]string{"auto lo", "iface lo inet loopback", ""}, kept...)
	}
	// Append new block
	kept = append(kept, "", block)
	// Collapse 3+ consecutive blank lines into one
	var out []string
	blank := false
	for _, l := range kept {
		if strings.TrimSpace(l) == "" {
			if blank || len(out) == 0 {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, l)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	final := strings.Join(out, "\n") + "\n"
	return os.WriteFile(interfacesF, []byte(final), 0644)
}

// debianWriteStatic writes a static IP stanza into /etc/network/interfaces.
func debianWriteStatic(nic, ip, netmask, gateway, dns1, dns2 string) error {
	block := "auto " + nic + "\n" +
		"iface " + nic + " inet static\n" +
		"    address " + ip + "\n" +
		"    netmask " + netmask + "\n"
	if gateway != "" {
		block += "    gateway " + gateway + "\n"
	}
	if dns1 != "" {
		block += "    dns-nameservers " + dns1
		if dns2 != "" {
			block += " " + dns2
		}
		block += "\n"
	}
	return debianRewriteBlock(nic, block)
}

// debianWriteDHCP writes a DHCP stanza into /etc/network/interfaces.
func debianWriteDHCP(nic string) error {
	block := "auto " + nic + "\n" +
		"iface " + nic + " inet dhcp\n"
	return debianRewriteBlock(nic, block)
}

// debianApply brings the NIC up/down after writing the config.
// Polls operstate to avoid a fixed sleep.
func debianApply(nic string) {
	exec.Command("ifdown", nic).Run()
	// Wait for link to go down (max ~3s)
	for i := 0; i < 15; i++ {
		st := readFileTrim("/sys/class/net/" + nic + "/operstate")
		if st == "down" || st == "unknown" || st == "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := exec.Command("ifup", nic).Run(); err != nil {
		// ifup may be missing on newer Debian/Ubuntu (netplan era)
		exec.Command("systemctl", "restart", "networking").Run()
	}
}

// netplanConfigPath is where we write our own netplan YAML.
const netplanConfigPath = "/etc/netplan/99-linux-tty.yaml"

// netplanBackup backs up existing netplan config before writing ours.
func netplanBackup() string {
	os.MkdirAll(bakDir, 0700)
	bak := filepath.Join(bakDir, fmt.Sprintf("netplan.%s", time.Now().Format("20060102_150405")))
	if entries, err := os.ReadDir("/etc/netplan"); err == nil {
		var merged strings.Builder
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join("/etc/netplan", e.Name()))
			if err != nil {
				continue
			}
			merged.WriteString(fmt.Sprintf("# === %s ===\n", e.Name()))
			merged.Write(data)
			merged.WriteString("\n")
		}
		os.WriteFile(bak, []byte(merged.String()), 0600)
	}
	return bak
}

// netplanWriteStatic writes a static IP netplan YAML and applies it.
func netplanWriteStatic(nic, ip, netmask, gateway, dns1, dns2 string) error {
	maskIP := net.ParseIP(netmask).To4()
	if maskIP == nil {
		return fmt.Errorf("invalid netmask: %s", netmask)
	}
	ones := maskSize(maskIP)
	if ones == 0 || ones > 32 {
		return fmt.Errorf("invalid netmask prefix: %s", netmask)
	}
	prefix := strconv.Itoa(ones)
	yaml := "network:\n  version: 2\n  renderer: networkd\n  ethernets:\n    " + nic + ":\n" +
		"      addresses: [" + ip + "/" + prefix + "]\n"
	if gateway != "" {
		yaml += "      routes:\n        - to: 0.0.0.0/0\n          via: " + gateway + "\n          on-link: true\n"
	}
	if dns1 != "" {
		dnsList := dns1
		if dns2 != "" {
			dnsList += ", " + dns2
		}
		yaml += "      nameservers:\n        addresses: [" + dnsList + "]\n"
	}
	return os.WriteFile(netplanConfigPath, []byte(yaml), 0600)
}

// netplanWriteDHCP writes a DHCP netplan YAML and applies it.
func netplanWriteDHCP(nic string) error {
	yaml := "network:\n  version: 2\n  renderer: networkd\n  ethernets:\n    " + nic + ":\n" +
		"      dhcp4: true\n"
	return os.WriteFile(netplanConfigPath, []byte(yaml), 0600)
}

// netplanApply runs `netplan apply` and reports failures, so the operator
// sees syntax / conflict errors instead of a silent "no IPv4".
func netplanApply() {
	out, err := run("netplan", "apply")
	if err != nil {
		if out != "" {
			fmt.Printf("netplan apply: %s\n", out)
		} else {
			fmt.Printf("netplan apply failed: %v\n", err)
		}
	}
}

// ---------- network apply ----------

func isNMActive() bool {
	c := exec.Command("systemctl", "is-active", "--quiet", "NetworkManager")
	if err := c.Start(); err != nil {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
		return c.ProcessState.Success()
	case <-time.After(3 * time.Second):
		c.Process.Kill()
		return false
	}
}

// nicApply applies the current config for dev. For the RHEL backend it relies
// on nmActive having been pre-set by applyNic() so we don't fork systemctl
// on every retry.
func nicApply(dev string) {
	switch netBackend {
	case "debian":
		debianApply(dev)
		return
	case "netplan":
		netplanApply()
		return
	}
	if nmActive.Load() == 1 {
		exec.Command("nmcli", "con", "reload").Run()
		con, _ := run("nmcli", "-t", "-f", "NAME,DEVICE", "con", "show")
		conName := dev
		for _, line := range strings.Split(con, "\n") {
			ff := strings.SplitN(line, ":", 2)
			if len(ff) == 2 && ff[1] == dev {
				conName = ff[0]
				break
			}
		}
		exec.Command("nmcli", "con", "up", conName).Run()
	} else {
		exec.Command("ifdown", dev).Run()
		exec.Command("ifup", dev).Run()
	}
}

func nicReady(dev string) bool {
	out, _ := run("ip", "-4", "-o", "addr", "show", dev)
	ip4 := ""
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 {
			ip4 = f[3]
			break
		}
	}
	if ip4 == "" {
		return false
	}
	if nmActive.Load() == 1 {
		st, _ := run("nmcli", "-t", "-f", "DEVICE,STATE", "dev", "status")
		good := false
		for _, line := range strings.Split(st, "\n") {
			ff := strings.SplitN(line, ":", 2)
			if len(ff) == 2 && ff[0] == dev && ff[1] == "connected" {
				good = true
				break
			}
		}
		if !good {
			return false
		}
	}
	return true
}

func applyNic(dev, bak string) {
	if dev == "" {
		fmt.Println("FAILED: no interface given.")
		return
	}
	if netBackend == "rhel" {
		if !fileExists(filepath.Join(ifcfgDir, "ifcfg-"+dev)) {
			fmt.Printf("FAILED: %s/ifcfg-%s is missing.\n", ifcfgDir, dev)
			return
		}
	}
	// Cache NM state once (avoids repeated systemctl forks per retry).
	nmUp := netBackend == "rhel" && isNMActive()
	if nmUp {
		nmActive.Store(1)
	} else {
		nmActive.Store(0)
	}
	if netBackend == "debian" {
		fmt.Printf("Applying config on %s (ifdown/ifup) ...\n", dev)
	} else if netBackend == "netplan" {
		fmt.Printf("Applying config on %s (netplan apply) ...\n", dev)
	} else if nmUp {
		fmt.Printf("Applying config on %s (nmcli) ...\n", dev)
	} else {
		fmt.Printf("Applying config on %s (ifdown/ifup) ...\n", dev)
	}
	i := 0
	for i < 3 {
		nicApply(dev)
		if nicReady(dev) {
			break
		}
		i++
		time.Sleep(2 * time.Second)
	}
	if nicReady(dev) {
		ip4, _ := run("ip", "-4", "-o", "addr", "show", dev)
		addr := ""
		for _, f := range strings.Fields(ip4) {
			if strings.Contains(f, "/") {
				addr = f
				break
			}
		}
		fmt.Printf("OK: %s is up (%s)\n", dev, addr)
		return
	}
	ip4, _ := run("ip", "-4", "-o", "addr", "show", dev)
	addr := ""
	for _, f := range strings.Fields(ip4) {
		if strings.Contains(f, "/") {
			addr = f
			break
		}
	}
	if addr == "" {
		fmt.Printf("FAILED: %s has no IPv4 address after %d attempt(s)", dev, maxTries)
		if bak != "" {
			fmt.Printf(" (backup: %s)", bak)
		}
		fmt.Println()
		return
	}
	// Has an IPv4 but is not "fully ready". Explain per backend.
	if nmUp {
		st, _ := run("nmcli", "-t", "-f", "DEVICE,STATE", "dev", "status")
		stt := "unknown"
		for _, line := range strings.Split(st, "\n") {
			ff := strings.SplitN(line, ":", 2)
			if len(ff) == 2 && ff[0] == dev {
				stt = ff[1]
				break
			}
		}
		fmt.Printf("FAILED: %s has %s but NetworkManager state is '%s' after %d attempt(s)", dev, addr, stt, maxTries)
	} else {
		fmt.Printf("FAILED: %s has %s but the interface is not fully ready after %d attempt(s)", dev, addr, maxTries)
	}
	if bak != "" {
		fmt.Printf(" (backup: %s)", bak)
	}
	fmt.Println()
}

// ---------- static IP (formerly ip.sh) ----------

func isValidIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

func staticConfig() {
	fmt.Println("===================== NETWORK CONFIG (static) =====================")
	nics := listNics()
	if len(nics) == 0 {
		fmt.Println("No network interface found.")
		return
	}
	fmt.Println("Interfaces:")
	for i, n := range nics {
		fmt.Printf("  %d. %-16s link=%s\n", i+1, n, nicOperstate(n))
	}
	// choose NIC
	sel, intr := ask("Select interface number: ")
	if intr {
		return
	}
	sel = strings.TrimSpace(sel)
	n, err := strconv.Atoi(sel)
	if err != nil || n < 1 || n > len(nics) {
		fmt.Println("ERROR: invalid interface number.")
		return
	}
	nic := nics[n-1]
	// RHEL backend requires an existing ifcfg file; Debian backend writes
	// /etc/network/interfaces regardless of pre-existing config.
	cfg := filepath.Join(ifcfgDir, "ifcfg-"+nic)
	if netBackend == "rhel" {
		if !fileExists(cfg) {
			fmt.Printf("ERROR: %s does not exist - this tool only edits an existing ifcfg.\n", cfg)
			return
		}
		// refuse bond/vlan/bridge/team
		cfgBytes, _ := os.ReadFile(cfg)
		cfgText := string(cfgBytes)
		badKeys := regexp.MustCompile(`(?m)^\s*(SLAVE|MASTER|BONDING_OPTS|VLAN|BRIDGE|TEAM_MASTER)\s*=`)
		badType := regexp.MustCompile(`(?m)^\s*TYPE\s*=\s*"?(Vlan|Bridge|Bond|Team|IPSEC|IPIP|GRE)`)
		if badKeys.MatchString(cfgText) || badType.MatchString(cfgText) {
			fmt.Printf("ERROR: %s is a bond/vlan/bridge/team member or a special TYPE - refusing to touch it.\n", nic)
			fmt.Println("       Nothing was changed.")
			return
		}
	}
	// IP/CIDR input
	var ipAddr, netmask string
	ipOK := false
	for t := 0; t < maxTries && !ipOK; t++ {
		v, i2 := ask("IP address (CIDR or IP; prefix optional, e.g. 10.0.0.5/24): ")
		if i2 {
			return
		}
		v = strings.TrimSpace(v)
		if v == "" {
			fmt.Printf("  Empty input - try again (%d/%d).\n", t+1, maxTries)
			continue
		}
		if strings.Contains(v, "/") {
			parts := strings.SplitN(v, "/", 2)
			addr, pfx := parts[0], parts[1]
			if !isValidIPv4(addr) {
				fmt.Printf("  Invalid address: %s - try again (%d/%d).\n", addr, t+1, maxTries)
				continue
			}
			p, err := strconv.Atoi(pfx)
			if err != nil || p < 1 || p > 32 {
				fmt.Printf("  Invalid prefix: /%s - try again (%d/%d).\n", pfx, t+1, maxTries)
				continue
			}
			ipAddr = addr
			netmask = prefixToMask(p)
			ipOK = true
		} else {
			if !isValidIPv4(v) {
				fmt.Printf("  Invalid address: %s - try again (%d/%d).\n", v, t+1, maxTries)
				continue
			}
			ipAddr = v
			ipOK = true
		}
	}
	if !ipOK {
		fmt.Println("ERROR: too many invalid inputs - nothing has been changed.")
		return
	}
	// ask mask if not given
	if netmask == "" {
		got := false
		for t := 0; t < maxTries && !got; t++ {
			v, i2 := ask("Subnet mask (e.g. 24 or 255.255.255.0): ")
			if i2 {
				return
			}
			v = strings.TrimSpace(v)
			if v == "" {
				fmt.Printf("  Empty input - try again (%d/%d).\n", t+1, maxTries)
				continue
			}
			if p, err := strconv.Atoi(v); err == nil && p >= 1 && p <= 32 {
				netmask = prefixToMask(p)
				got = true
			} else if isValidIPv4(v) && netmaskIsValid(v) {
				netmask = v
				got = true
			} else {
				fmt.Printf("  Invalid value: %s - try again (%d/%d).\n", v, t+1, maxTries)
			}
		}
		if !got {
			fmt.Println("ERROR: too many invalid inputs - nothing has been changed.")
			return
		}
	}
	// gateway suggestion = network+1
	ip := net.ParseIP(ipAddr).To4()
	mask := net.ParseIP(netmask).To4()
	ipnet := net.IPNet{IP: ip, Mask: net.IPMask(mask)}
	network := ipnet.IP.Mask(ipnet.Mask).To4()
	prefix := maskSize(mask)
	gwSuggest := ""
	if prefix < 31 {
		gwSuggest = net.IPv4(network[0], network[1], network[2], network[3]+1).To4().String()
	} else {
		fmt.Printf("NOTE: /%d has no usable host address - no gateway is suggested.\n", prefix)
	}
	gateway := ""
	gwOK := false
	for t := 0; t < maxTries && !gwOK; t++ {
		var v string
		var i2 bool
		if gwSuggest != "" {
			v, i2 = ask(fmt.Sprintf("Gateway [%s] (Enter = use, 'none' = no gateway, 'auto' = same): ", gwSuggest))
		} else {
			v, i2 = ask("Gateway (blank/'none' = no default route): ")
		}
		if i2 {
			return
		}
		v = strings.TrimSpace(v)
		switch v {
		case "":
			if gwSuggest != "" {
				gateway = gwSuggest
			} else {
				fmt.Println("WARNING: no gateway - the default route will not be configured.")
			}
			gwOK = true
		case "none", "no", "-":
			fmt.Println("WARNING: no gateway - the default route will not be configured.")
			gwOK = true
		case "auto":
			if gwSuggest == "" {
				fmt.Printf("  Cannot derive a gateway for %s/%s - type one.\n", ipAddr, netmask)
				continue
			}
			gateway = gwSuggest
			gwOK = true
		default:
			if isValidIPv4(v) {
				gateway = v
				gwOK = true
			} else {
				fmt.Printf("  Invalid gateway: %s - try again (%d/%d).\n", v, t+1, maxTries)
			}
		}
	}
	if !gwOK {
		fmt.Println("ERROR: too many invalid inputs - nothing has been changed.")
		return
	}
	if gateway == ipAddr {
		fmt.Println("WARNING: gateway equals the IP address - double-check it.")
	}
	if gateway != "" {
		gwIP := net.ParseIP(gateway).To4()
		if !ipnet.Contains(gwIP) {
			fmt.Printf("WARNING: gateway %s is not in %s/%s - the default route may not work.\n", gateway, ipAddr, netmask)
		}
	}
	// DNS: preserve existing
	existingDNS := []string{}
	if rc, _ := os.ReadFile("/etc/resolv.conf"); rc == nil {
		for _, line := range strings.Split(string(rc), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "nameserver") {
				f := strings.Fields(line)
				if len(f) >= 2 {
					existingDNS = append(existingDNS, strings.Trim(f[1], "\""))
				}
			}
		}
	}
	// RHEL backend: also read DNS entries from the existing ifcfg file
	if netBackend == "rhel" && fileExists(cfg) {
		cfgBytes2, _ := os.ReadFile(cfg)
		for _, line := range strings.Split(string(cfgBytes2), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "DNS") {
				f := strings.SplitN(line, "=", 2)
				if len(f) == 2 {
					existingDNS = append(existingDNS, strings.Trim(f[1], "\""))
				}
			}
		}
	}
	// dedupe
	dnsSeen := map[string]bool{}
	var dnsBlock string
	if len(existingDNS) > 0 {
		dnsBlock = "PEERDNS=no"
		i := 0
		for _, d := range existingDNS {
			if dnsSeen[d] || i >= 2 {
				continue
			}
			dnsSeen[d] = true
			i++
			dnsBlock += "\nDNS" + strconv.Itoa(i) + "=" + d
		}
	} else {
		dnsBlock = "PEERDNS=yes\nDNS1=223.6.6.6\nDNS2=223.5.5.5"
	}
	// backup
	os.MkdirAll(bakDir, 0700)
	bak := ""
	switch netBackend {
	case "rhel":
		bak = filepath.Join(bakDir, fmt.Sprintf("ifcfg-%s.%s", nic, time.Now().Format("20060102_150405")))
		if b, err := os.ReadFile(cfg); err == nil {
			os.WriteFile(bak, b, 0600)
		}
	case "debian":
		bak = debianBackupIface()
	case "netplan":
		bak = netplanBackup()
	}
	// write config per backend
	switch netBackend {
	case "rhel":
		gwBlock := ""
		if gateway != "" {
			gwBlock = "GATEWAY=" + gateway + "\n"
		}
		content := fmt.Sprintf("TYPE=Ethernet\nBOOTPROTO=static\nNAME=%s\nDEVICE=%s\nONBOOT=yes\nNM_CONTROLLED=yes\nIPADDR=%s\nNETMASK=%s\n%s%s\n",
			nic, nic, ipAddr, netmask, gwBlock, dnsBlock)
		tmp := filepath.Join(ifcfgDir, fmt.Sprintf(".ifcfg-%s.new.%d", nic, os.Getpid()))
		if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
			fmt.Println("ERROR: cannot write temp config - nothing was changed.")
			return
		}
		os.Rename(tmp, cfg)
	case "debian":
		d1, d2 := "", ""
		if len(existingDNS) > 0 {
			d1 = existingDNS[0]
			if len(existingDNS) > 1 {
				d2 = existingDNS[1]
			}
		}
		if err := debianWriteStatic(nic, ipAddr, netmask, gateway, d1, d2); err != nil {
			fmt.Printf("ERROR: cannot write %s: %v\n", interfacesF, err)
			return
		}
		fmt.Printf("Wrote static config to %s\n", interfacesF)
	case "netplan":
		d1, d2 := "", ""
		if len(existingDNS) > 0 {
			d1 = existingDNS[0]
			if len(existingDNS) > 1 {
				d2 = existingDNS[1]
			}
		}
		if err := netplanWriteStatic(nic, ipAddr, netmask, gateway, d1, d2); err != nil {
			fmt.Printf("ERROR: cannot write %s: %v\n", netplanConfigPath, err)
			return
		}
		fmt.Printf("Wrote static config to %s\n", netplanConfigPath)
	}
	applyNic(nic, bak)
	// quick ping
	pingTarget := gateway
	if pingTarget == "" {
		pingTarget = "223.6.6.6"
	}
	c := exec.Command("ping", "-c", "2", "-W", "2", pingTarget)
	if c.Run() == nil {
		fmt.Println("PING: OK")
	} else {
		fmt.Println("PING: FAILED")
	}
}

func prefixToMask(p int) string {
	m := net.CIDRMask(p, 32)
	return net.IP(m).To4().String()
}

func netmaskIsValid(m string) bool {
	ip := net.ParseIP(m)
	if ip == nil {
		return false
	}
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	// reject 0.0.0.0 and 255.255.255.255
	if v4[0] == 0 && v4[1] == 0 && v4[2] == 0 && v4[3] == 0 {
		return false
	}
	if v4[0] == 255 && v4[1] == 255 && v4[2] == 255 && v4[3] == 255 {
		return false
	}
	// check contiguous
	b := []byte(v4)
	seen0 := false
	for _, by := range b {
		for bit := 7; bit >= 0; bit-- {
			if by&(1<<bit) == 0 {
				seen0 = true
			} else if seen0 {
				return false
			}
		}
	}
	return true
}

func maskSize(m net.IP) int {
	b := []byte(m.To4())
	ones := 0
	for _, by := range b {
		for bit := 7; bit >= 0; bit-- {
			if by&(1<<bit) != 0 {
				ones++
			} else {
				return ones
			}
		}
	}
	return ones
}

// ---------- DHCP ----------

func dhcpConfig() {
	nics := listNics()
	if len(nics) == 0 {
		fmt.Println("No network interface found.")
		pause()
		return
	}
	fmt.Println("===================== NETWORK CONFIG (dhcp) =====================")
	fmt.Println("Interfaces:")
	for i, n := range nics {
		fmt.Printf("  %d. %-16s link=%s\n", i+1, n, nicOperstate(n))
	}
	sel, intr := ask("Select interface number: ")
	if intr {
		return
	}
	sel = strings.TrimSpace(sel)
	n, err := strconv.Atoi(sel)
	if err != nil || n < 1 || n > len(nics) {
		fmt.Println("ERROR: invalid interface number.")
		pause()
		return
	}
	nic := nics[n-1]
	cfg := filepath.Join(ifcfgDir, "ifcfg-"+nic)
	bak := ""
	switch netBackend {
	case "rhel":
		if fileExists(cfg) {
			os.MkdirAll(bakDir, 0700)
			bak = filepath.Join(bakDir, fmt.Sprintf("ifcfg-%s.%s", nic, time.Now().Format("20060102_150405")))
			if b, err := os.ReadFile(cfg); err == nil {
				os.WriteFile(bak, b, 0600)
			}
		}
		content := fmt.Sprintf("TYPE=Ethernet\nBOOTPROTO=dhcp\nDEFROUTE=yes\nIPV4_FAILURE_FATAL=no\nNM_CONTROLLED=yes\nNAME=%s\nDEVICE=%s\nONBOOT=yes\n", nic, nic)
		if err := os.WriteFile(cfg, []byte(content), 0644); err != nil {
			fmt.Printf("ERROR: cannot write %s - nothing was changed.\n", cfg)
			pause()
			return
		}
	case "debian":
		bak = debianBackupIface()
		if err := debianWriteDHCP(nic); err != nil {
			fmt.Printf("ERROR: cannot write %s: %v\n", interfacesF, err)
			pause()
			return
		}
		fmt.Printf("Wrote DHCP config to %s\n", interfacesF)
	case "netplan":
		bak = netplanBackup()
		if err := netplanWriteDHCP(nic); err != nil {
			fmt.Printf("ERROR: cannot write %s: %v\n", netplanConfigPath, err)
			pause()
			return
		}
		fmt.Printf("Wrote DHCP config to %s\n", netplanConfigPath)
	}
	applyNic(nic, bak)
	pause()
}

// ---------- ping ----------

func pingTest() {
	fmt.Println("======================== PING TEST ========================")
	runBg("ping", "-c", "4", "-W", "2", "223.6.6.6")
	fmt.Println("==========================================================================")
	if interrupt.Load() == 1 {
		interrupt.Store(0)
		return
	}
	pause()
}

// ---------- disks ----------

// resolveToPhysicalDisks walks UP /sys/class/block/<dev>/slaves/ recursively
// (BFS), following LVM, dm-crypt, md, and multipath stacks, and returns all
// leaf whole-disk names (sda, sdb, nvme0n1, ...).
//
// multipath / RAID1 have multiple slaves — all of them are returned, so the
// caller can mark every physical member of the system volume as "system".
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
		// Leaf: if it's a partition, peel off the parent whole disk.
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

// systemDisks finds which whole disks host the root filesystem, /boot, and
// /boot/efi. Uses /proc/mounts plus /sys/class/block to resolve the underlying
// physical disks without depending on lsblk. Falls back to /proc/cmdline root=
// when /proc/mounts shows /dev/root (initramfs common).
func systemDisks() []string {
	disks := map[string]bool{}
	addDev := func(device string) {
		if !strings.HasPrefix(device, "/dev/") {
			return
		}
		name := strings.TrimPrefix(device, "/dev/")
		// /dev/mapper/vg-root -> dm-0
		if strings.HasPrefix(name, "mapper/") {
			if link, err := os.Readlink("/dev/" + name); err == nil {
				name = filepath.Base(link)
			}
		}
		// Must be a real block device; /dev/root is a fake path from initramfs.
		if _, err := os.Stat("/sys/class/block/" + name); err != nil {
			return
		}
		for _, phys := range resolveToPhysicalDisks(name) {
			if phys != "" {
				disks[phys] = true
			}
		}
	}
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		device, mountPoint := fields[0], fields[1]
		if mountPoint != "/" && mountPoint != "/boot" && mountPoint != "/boot/efi" {
			continue
		}
		addDev(device)
	}
	// Fallback: /proc/cmdline root= (handles /dev/root, UUID=, LABEL=)
	if len(disks) == 0 {
		if cmd, err := os.ReadFile("/proc/cmdline"); err == nil {
			for _, f := range strings.Fields(string(cmd)) {
				if strings.HasPrefix(f, "root=") {
					r := strings.TrimPrefix(f, "root=")
					if strings.HasPrefix(r, "UUID=") || strings.HasPrefix(r, "LABEL=") {
						if out, err := run("blkid", "-t", r, "-o", "device"); err == nil {
							addDev(strings.TrimSpace(out))
						}
					} else {
						addDev(r)
					}
					break
				}
			}
		}
	}
	var res []string
	for d := range disks {
		res = append(res, d)
	}
	sort.Strings(res)
	return res
}

func diskType(dev string) string {
	if strings.HasPrefix(dev, "nvme") {
		return "NVMe"
	}
	rot := readFileTrim("/sys/block/" + dev + "/queue/rotational")
	switch rot {
	case "0":
		return "SSD"
	case "1":
		return "HDD"
	}
	return "-"
}

func diskInfo(dev string) (bus, model, serial string) {
	props, _ := run("udevadm", "info", "--query=property", "--name=/dev/"+dev)
	for _, line := range strings.Split(props, "\n") {
		switch {
		case strings.HasPrefix(line, "ID_BUS="):
			bus = strings.TrimPrefix(line, "ID_BUS=")
		case strings.HasPrefix(line, "ID_MODEL="):
			model = strings.TrimPrefix(line, "ID_MODEL=")
		case strings.HasPrefix(line, "ID_SERIAL_SHORT="):
			serial = strings.TrimPrefix(line, "ID_SERIAL_SHORT=")
		}
	}
	if serial == "" {
		for _, line := range strings.Split(props, "\n") {
			if strings.HasPrefix(line, "ID_SERIAL=") {
				serial = strings.TrimPrefix(line, "ID_SERIAL=")
			}
		}
	}
	// RAID virtual disks (PERC/H700 etc) expose ID_SERIAL as
	// "controllerSerial_virtual/path" — e.g. "6509a4c0afca2_data_hdd/storage1_hdd".
	// Only strip the trailing virtual-disk path when '/' is present (a virtual
	// separator); normal SATA/NVMe serials (Samsung_SSD_870_...) never contain '/'.
	if strings.Contains(serial, "/") {
		if idx := strings.Index(serial, "_"); idx > 0 {
			serial = serial[:idx]
		}
	}
	if serial == "" {
		serial = readFileTrim("/sys/block/" + dev + "/device/serial")
	}
	if model == "" {
		model = strings.TrimSpace(readFileTrim("/sys/block/" + dev + "/device/model"))
	}
	if bus == "" {
		bus = "-"
	}
	return
}

// allDisks enumerates real whole disks from /sys/block.
// Only devices with a /device symlink (physical backing) are included,
// and partitions are skipped: a whole disk has no /sys/block/<dev>/partition
// attribute file, while partitions (sda1, nvme0n1p1, ...) do.
func allDisks() []string {
	entries, err := os.ReadDir("/sys/block/")
	if err != nil {
		return nil
	}
	var res []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "sr") ||
			strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") ||
			strings.HasPrefix(name, "dm-") || strings.HasPrefix(name, "md") ||
			strings.HasPrefix(name, "nbd") {
			continue
		}
		// skip partitions (e.g. sda1, nvme0n1p1): they carry a "partition"
		// attribute file; whole disks do not.
		if _, err := os.Stat("/sys/block/" + name + "/partition"); err == nil {
			continue
		}
		if _, err := os.Stat("/sys/block/" + name + "/device"); err != nil {
			continue
		}
		res = append(res, name)
	}
	sort.Strings(res)
	return res
}

// diskSizeHuman reads /sys/block/<dev>/size (512-byte sectors) and formats.
func diskSizeHuman(dev string) string {
	sectorsStr := readFileTrim("/sys/block/" + dev + "/size")
	sectors, err := strconv.ParseInt(sectorsStr, 10, 64)
	if err != nil || sectors == 0 {
		return "-"
	}
	bytes := sectors * 512
	gb := float64(bytes) / (1 << 30)
	if gb >= 100 {
		return fmt.Sprintf("%.0fG", gb)
	}
	return fmt.Sprintf("%.1fG", gb)
}

// percDiskInfo holds physical disk info from perccli64.
type percDiskInfo struct {
	Slot   int
	Serial string
	Model  string
}

// diskSlotMap builds DG→slot and DG→serial maps via perccli64.
// In megaraid_sas, sysfs SCSI target id == VD number == DG number.
func diskSlotMap() map[int]string {
	m, _ := diskPercMap()
	res := map[int]string{}
	for dg, info := range m {
		res[dg] = strconv.Itoa(info.Slot)
	}
	return res
}

// diskSerialMap returns DG→serial from perccli64.
func diskSerialMap() map[int]string {
	m, _ := diskPercMap()
	res := map[int]string{}
	for dg, info := range m {
		res[dg] = info.Serial
	}
	return res
}

func diskPercMap() (map[int]percDiskInfo, error) {
	tool := "/opt/MegaRAID/perccli/perccli64"
	if _, err := os.Stat(tool); err != nil {
		return nil, err
	}
	out, err := run(tool, "/c0/eall/sall", "show", "all")
	if err != nil {
		return nil, err
	}
	res := map[int]percDiskInfo{}
	var curSlot, curDG int = -1, -1
	var curSerial, curModel string
	flush := func() {
		if curDG >= 0 {
			res[curDG] = percDiskInfo{Slot: curSlot, Serial: curSerial, Model: curModel}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		trim := strings.TrimSpace(line)
		// New drive block: "Drive /c0/e32/s0:"
		if strings.HasPrefix(trim, "Drive /c") {
			flush()
			curSlot, curDG, curSerial, curModel = -1, -1, "", ""
			re := regexp.MustCompile(`/e(\d+)/s(\d+)`)
			if m := re.FindStringSubmatch(trim); m != nil {
				curSlot, _ = strconv.Atoi(m[2])
			}
			continue
		}
		if curSlot < 0 {
			continue
		}
		if strings.HasPrefix(trim, "DG =") || strings.HasPrefix(trim, "DG:") {
			if n, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(trim, "=", 2)[len(strings.SplitN(trim, "=", 2))-1])); err == nil {
				curDG = n
			}
		} else if strings.HasPrefix(trim, "SN =") || strings.HasPrefix(trim, "SN:") {
			curSerial = strings.TrimSpace(strings.SplitN(trim, "=", 2)[len(strings.SplitN(trim, "=", 2))-1])
		} else if strings.HasPrefix(trim, "Model =") || strings.HasPrefix(trim, "Model:") {
			curModel = strings.TrimSpace(strings.SplitN(trim, "=", 2)[len(strings.SplitN(trim, "=", 2))-1])
		}
	}
	flush()
	return res, nil
}

// sysfsTarget returns the SCSI target id for a block device (== VD/DG number).
func sysfsTarget(dev string) int {
	link, err := os.Readlink("/sys/class/block/" + dev)
	if err != nil {
		return -1
	}
	for _, p := range strings.Split(link, "/") {
		f := strings.Split(p, ":")
		if len(f) == 4 {
			if t, err := strconv.Atoi(f[2]); err == nil {
				return t
			}
		}
	}
	return -1
}

func showDisks() {
	syslist := systemDisks()
	sysSet := map[string]bool{}
	for _, d := range syslist {
		sysSet[d] = true
	}
	if len(syslist) == 0 {
		fmt.Println("WARNING: unable to identify system disk (/) - list below may")
		fmt.Println("         include the disk hosting the OS.")
	}
	fmt.Println("========================== DISKS ===========================")
	fmt.Println()
	fmt.Printf("  %-12s %-6s %-4s %-16s %-3s %s\n", "DEV", "SIZE", "TYPE", "MODEL", "SLOT", "SERIAL")
	fmt.Println("  ------------------------------------------------------------")
	slotMap := diskSlotMap()
	serialMap := diskSerialMap()
	// Dedup by serial: the same physical SSD may appear as multiple block
	// devices (NVMe multi-path / multiple namespaces, e.g. nvme0n1 +
	// nvme0c0n1). Only apply dedup to NVMe devices; RAID virtual disks
	// (sdb, sdc, ...) share the controller serial but are distinct volumes.
	all := allDisks()
	deduped := []string{}
	chosen := map[string]string{} // serial -> chosen device name
	for _, dev := range all {
		if !strings.HasPrefix(dev, "nvme") {
			deduped = append(deduped, dev)
			continue
		}
		_, _, serial := diskInfo(dev)
		key := strings.TrimSpace(serial)
		if key == "" {
			deduped = append(deduped, dev)
			continue
		}
		if prev, ok := chosen[key]; ok {
			if len(dev) < len(prev) {
				for i, d := range deduped {
					if d == prev {
						deduped[i] = dev
						break
					}
				}
				chosen[key] = dev
			}
			continue
		}
		chosen[key] = dev
		deduped = append(deduped, dev)
	}
	shown := 0
	for _, dev := range deduped {
		display := dev
		if sysSet[dev] {
			display = dev + " [SYS]"
		}
		size := diskSizeHuman(dev)
		dt := diskType(dev)
		_, model, serial := diskInfo(dev)
		if serial == "" || serial == "-" {
			if t := sysfsTarget(dev); t >= 0 {
				if serialMap != nil {
					if s, ok := serialMap[t]; ok && s != "" {
						serial = s
					}
				}
			}
		}
		slot := "-"
		if t := sysfsTarget(dev); t >= 0 {
			if slotMap != nil {
				if s, ok := slotMap[t]; ok {
					slot = s
				} else {
					slot = strconv.Itoa(t)
				}
			} else {
				slot = strconv.Itoa(t)
			}
		}
		if len(model) > 16 {
			model = model[:16]
		}
		if len(serial) > 30 {
			serial = serial[:30]
		}
		shown++
		fmt.Printf("  %-12s %-6s %-4s %-16s %-3s %s\n", display, size, dt, model, slot, serial)
		if shown%10 == 0 && shown < len(deduped) {
			a, intr := ask("  -- more -- Press Enter for next page (q+Enter to stop): ")
			if intr {
				return
			}
			if strings.TrimSpace(a) == "q" {
				break
			}
		}
	}
	if shown == 0 {
		fmt.Println("  (no disk found)")
	}
	fmt.Println("==========================================================================")
	pause()
}

// ---------- menu header ----------

func cpuInfo() (sockets, model string) {
	sockets = readFileTrim("/sys/devices/system/cpu/possible")
	out, _ := run("lscpu")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Socket(s):") {
			sockets = strings.TrimSpace(strings.TrimPrefix(line, "Socket(s):"))
		}
		if strings.HasPrefix(line, "Model name:") {
			model = strings.TrimSpace(strings.TrimPrefix(line, "Model name:"))
		}
	}
	if model == "" {
		b, _ := os.ReadFile("/proc/cpuinfo")
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				f := strings.SplitN(line, ":", 2)
				if len(f) == 2 {
					model = strings.TrimSpace(f[1])
				}
				break
			}
		}
	}
	if sockets == "" {
		// count unique physical ids
		seen := map[string]bool{}
		b, _ := os.ReadFile("/proc/cpuinfo")
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "physical id") {
				f := strings.SplitN(line, ":", 2)
				if len(f) == 2 {
					seen[strings.TrimSpace(f[1])] = true
				}
			}
		}
		sockets = strconv.Itoa(len(seen))
		if sockets == "0" {
			sockets = "1"
		}
	}
	if model == "" {
		model = "unknown"
	}
	return
}

// ipEntry is one line of "ip -o addr show" we want to display.
type ipEntry struct {
	nic string
	ip  string
	v6  bool
}

func drawMenu() {
	fmt.Print("\033[2J\033[H")
	ipOut, _ := run("ip", "-o", "addr", "show", "scope", "global")
	var entries []ipEntry
	seen := map[string]bool{}
	for _, line := range strings.Split(ipOut, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		nic := f[1]
		// keep real NICs (have /device) and bond masters (have /bonding); skip bridge/veth/dummy/tun
		if _, err := os.Stat("/sys/class/net/" + nic + "/device"); err != nil {
			if _, err := os.Stat("/sys/class/net/" + nic + "/bonding"); err != nil {
				continue
			}
		}
		ip := f[3]
		key := nic + "/" + ip
		if seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, ipEntry{nic: nic, ip: ip, v6: f[2] == "inet6"})
	}
	// Stable order: by NIC, then IPv4 before IPv6, then by address string.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].nic != entries[j].nic {
			return entries[i].nic < entries[j].nic
		}
		if entries[i].v6 != entries[j].v6 {
			return !entries[i].v6
		}
		return entries[i].ip < entries[j].ip
	})
	var ips []string
	for _, e := range entries {
		if e.v6 {
			ips = append(ips, e.nic+"["+e.ip+"]")
		} else {
			ips = append(ips, e.nic+"("+e.ip+")")
		}
	}
	ipStr := strings.Join(ips, " ")
	if ipStr == "" {
		ipStr = "(no IP address yet)"
	}
	// Clip extremely long multi-IP lines so the menu never wraps.
	if len(ipStr) > 60 {
		ipStr = ipStr[:57] + "..."
	}
	mem := "unknown"
	if memData, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(memData), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if kb, err := strconv.Atoi(fields[1]); err == nil {
						if kb >= 1024*1024 {
							mem = fmt.Sprintf("%.1fG", float64(kb)/(1024*1024))
						} else if kb >= 1024 {
							mem = fmt.Sprintf("%.0fM", float64(kb)/1024)
						} else {
							mem = fmt.Sprintf("%dK", kb)
						}
					}
				}
				break
			}
		}
	}
	boot := "Legacy-BIOS"
	if fileExists("/sys/firmware/efi") {
		boot = "UEFI"
	}
	host, _ := os.Hostname()
	sockets, model := cpuInfo()
	threads := 0
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "processor") {
				threads++
			}
		}
	}
	kernel := readFileTrim("/proc/sys/kernel/osrelease")

	const w = 72
	// Truncate into a LOCAL title so we never mutate the global `rel` across
	// redraws.
	title := rel
	if len(title) > w-4 {
		title = title[:w-4]
	}
	tp := (w - len(title) - 2) / 2
	if tp < 0 {
		tp = 0
	}
	right := w - tp - len(title) - 2
	if right < 0 {
		right = 0
	}
	fmt.Printf("%s %s %s\n", strings.Repeat("=", tp), title, strings.Repeat("=", right))
	fmt.Printf(" Hostname : %s\n", host)
	fmt.Printf(" CPU      : %s x %s (%d threads)\n", sockets, model, threads)
	fmt.Printf(" Memory   : %s\n", mem)
	fmt.Printf(" Kernel   : %-26s Boot : %s\n", kernel, boot)
	fmt.Printf(" Version  : %s\n", version)
	fmt.Printf(" IP       : %s\n", ipStr)
	fmt.Println(strings.Repeat("-", w))
	fmt.Println("  1. Configure network - static IP")
	fmt.Println("  2. Configure network - DHCP")
	fmt.Println("  3. Show disks")
	fmt.Println("  4. Ping test")
	fmt.Println(strings.Repeat("-", w))
	fmt.Println("  5. Reboot")
	fmt.Println("  6. Power off")
	fmt.Println("  7. Exit and enter the system")
	fmt.Println(strings.Repeat("=", w))
}

// ---------- main ----------

func main() {
	// clear screen on startup
	fmt.Print("\033[2J\033[H")
	// root check
	if os.Geteuid() != 0 {
		fmt.Println("ERROR: this menu must run as root.")
		time.Sleep(3 * time.Second)
		os.Exit(1)
	}
	detectDistro()
	rel = readFileTrim("/etc/linux-tty-release")
	if rel == "" {
		rel = "SSCLOUD Linux-TTY Console"
	}
	// TERM fallback
	term := os.Getenv("TERM")
	switch term {
	case "", "dumb", "unknown":
		t, _ := run("tty")
		if strings.HasPrefix(t, "/dev/ttyS") || strings.HasPrefix(t, "/dev/ttyAMA") {
			os.Setenv("TERM", "vt100")
		} else {
			os.Setenv("TERM", "linux")
		}
	}
	// verify terminfo
	if _, err := exec.LookPath("infocmp"); err == nil {
		if c := exec.Command("infocmp", os.Getenv("TERM")); c.Run() != nil {
			os.Setenv("TERM", "vt100")
		}
	} else {
		os.Setenv("TERM", "vt100")
	}
	// detect if stdin is a tty
	if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		stdinIsTty = true
	}
	detectTTY()
	// cbreak mode (char-by-char, no echo, VMIN=0/VTIME=2)
	enterCbreak()
	// disable sysrq, restore on exit (also before syscall.Exec)
	origSysrq := []byte(nil)
	if orig, err := os.ReadFile("/proc/sys/kernel/sysrq"); err == nil {
		origSysrq = orig
		os.WriteFile("/proc/sys/kernel/sysrq", []byte("0"), 0644)
		defer os.WriteFile("/proc/sys/kernel/sysrq", origSysrq, 0644)
	}
	initSignals()
	migrateIfcfgBackups()
	// restore terminal on exit
	defer exitCbreak()

	for {
		drawMenu()
		choice, intr := ask("Select [1-7]: ")
		if eofFlag.Load() {
			return
		}
		if intr {
			interrupt.Store(0)
			continue
		}
		choice = strings.TrimSpace(choice)
		switch choice {
		case "1":
			staticConfig()
			if interrupt.Load() == 1 {
				interrupt.Store(0)
				continue
			}
			pause()
		case "2":
			dhcpConfig()
		case "3":
			showDisks()
		case "4":
			pingTest()
		case "5":
			if confirm("Reboot now?") {
				fmt.Println("Rebooting ...")
				syscall.Sync()
				if err := exec.Command("systemctl", "reboot").Run(); err != nil {
					fmt.Printf("ERROR: reboot failed: %v\n", err)
					pause()
					continue
				}
				time.Sleep(10 * time.Second)
			}
		case "6":
			if confirm("Power off now?") {
				fmt.Println("Powering off ...")
				syscall.Sync()
				if err := exec.Command("systemctl", "poweroff").Run(); err != nil {
					fmt.Printf("ERROR: poweroff failed: %v\n", err)
					pause()
					continue
				}
				time.Sleep(10 * time.Second)
			}
		case "7":
			// Resolve agetty FIRST, so a missing binary does not leave us in
			// canonical mode with no way to get back to cbreak.
			exe, err := exec.LookPath("agetty")
			if err != nil {
				fmt.Println("ERROR: agetty not found - cannot drop to login prompt.")
				pause()
				continue
			}
			exitCbreak()
			if origSysrq != nil {
				os.WriteFile("/proc/sys/kernel/sysrq", origSysrq, 0644)
			}
			argv := []string{"agetty", "--noclear", myTTY, "linux"}
			if err := syscall.Exec(exe, argv, os.Environ()); err != nil {
				// Exec only returns on failure.
				fmt.Printf("ERROR: exec agetty: %v\n", err)
				enterCbreak()
				pause()
				continue
			}
			// Unreachable on success.
		case "666":
			// hidden entry: passwordless root shell (VT consoles only)
			if !isVTConsole(myTTY) {
				fmt.Println("Not available on this console.")
				time.Sleep(1 * time.Second)
				break
			}
			if _, err := os.Stat("/usr/local/sbin/linux-root-shell"); err != nil {
				fmt.Println("ERROR: linux-root-shell not installed.")
				pause()
				continue
			}
			exe, err := exec.LookPath("agetty")
			if err != nil {
				fmt.Println("ERROR: agetty not found - cannot drop to root shell.")
				pause()
				continue
			}
			exitCbreak()
			if origSysrq != nil {
				os.WriteFile("/proc/sys/kernel/sysrq", origSysrq, 0644)
			}
			// Directly exec agetty — replaces this process image,
			// no systemd Conflicts race. agetty runs root shell;
			// on exit, linux-root-shell exec's back to linux-tty.
			argv := []string{
				"agetty", "--autologin", "root",
				"--login-program", "/usr/local/sbin/linux-root-shell",
				"--noclear", myTTY, "linux",
			}
			if err := syscall.Exec(exe, argv, os.Environ()); err != nil {
				fmt.Printf("ERROR: exec agetty: %v\n", err)
				enterCbreak()
				pause()
				continue
			}
			// Unreachable on success.
		default:
			// empty / invalid: loop redraws
		}
		// clear any leftover interrupt flag from sub-functions
		if interrupt.Load() == 1 {
			interrupt.Store(0)
			continue
		}
	}
}
