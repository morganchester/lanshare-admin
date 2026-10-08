package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,22}$`)
var groupPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)
var prefixPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,14}-$`)

type configuration struct {
	AccountPrefix    string   `json:"account_prefix"`
	SMBServer        string   `json:"smb_server"`
	SambaConfig      string   `json:"samba_config"`
	ManagedShareFile string   `json:"managed_share_file"`
	AllowedRoots     []string `json:"allowed_roots"`
	Shares           []share  `json:"shares"`
}

type share struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Group     string `json:"group"`
	UNC       string `json:"unc"`
	Path      string `json:"path,omitempty"`
	ReadOnly  bool   `json:"read_only"`
	Managed   bool   `json:"managed"`
	ForceUser string `json:"force_user,omitempty"`
}

type directory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type listedDevice struct {
	Name     string   `json:"name"`
	Username string   `json:"username"`
	Shares   []string `json:"shares"`
}

func main() {
	if os.Geteuid() != 0 {
		fatal(errors.New("helper must run as root"))
	}
	if len(os.Args) < 2 {
		fatal(errors.New("usage: lanshare-helper add|set-shares|revoke|list ..."))
	}
	cfgPath := envOr("LANSHARE_CONFIG", "/etc/lanshare-admin/config.json")
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		fatal(err)
	}

	switch os.Args[1] {
	case "add":
		if len(os.Args) != 4 {
			fatal(errors.New("usage: add <device> <share-ids>"))
		}
		name := validateName(os.Args[2])
		shares := selectedShares(cfg, os.Args[3])
		account := cfg.AccountPrefix + name
		ensureBaseGroup()
		ensureAccount(account)
		setGroups(account, shares)
		password := randomPassword()
		cmd := exec.Command("smbpasswd", "-s", "-a", account)
		cmd.Stdin = strings.NewReader(password + "\n" + password + "\n")
		cmd.Stdout = io.Discard
		cmd.Stderr = os.Stderr
		mustRun(cmd)
		mustRun(exec.Command("smbpasswd", "-e", account))
		fmt.Printf("username=%s\npassword=%s\n", account, password)
	case "set-shares":
		if len(os.Args) != 4 {
			fatal(errors.New("usage: set-shares <device> <share-ids>"))
		}
		account := cfg.AccountPrefix + validateName(os.Args[2])
		mustExist(account)
		setGroups(account, selectedShares(cfg, os.Args[3]))
	case "revoke":
		if len(os.Args) != 3 {
			fatal(errors.New("usage: revoke <device>"))
		}
		account := cfg.AccountPrefix + validateName(os.Args[2])
		_ = exec.Command("smbpasswd", "-x", account).Run()
		_ = exec.Command("userdel", account).Run()
	case "list":
		if len(os.Args) != 2 {
			fatal(errors.New("usage: list"))
		}
		list(cfg)
	case "browse":
		if len(os.Args) != 3 {
			fatal(errors.New("usage: browse <path>"))
		}
		browse(cfg, os.Args[2])
	case "add-share":
		if len(os.Args) != 6 {
			fatal(errors.New("usage: add-share <id> <name> <path> <rw|ro>"))
		}
		addShare(cfgPath, cfg, os.Args[2], os.Args[3], os.Args[4], os.Args[5])
	case "remove-share":
		if len(os.Args) != 3 {
			fatal(errors.New("usage: remove-share <id>"))
		}
		removeShare(cfgPath, cfg, os.Args[2])
	default:
		fatal(errors.New("unknown command"))
	}
}

func loadConfig(path string) (configuration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return configuration{}, fmt.Errorf("read config: %w", err)
	}
	var cfg configuration
	if err := json.Unmarshal(data, &cfg); err != nil {
		return configuration{}, fmt.Errorf("parse config: %w", err)
	}
	if len(cfg.Shares) == 0 {
		return configuration{}, errors.New("no shares configured")
	}
	if cfg.AccountPrefix == "" {
		cfg.AccountPrefix = "lanshare-"
	}
	if cfg.SambaConfig == "" {
		cfg.SambaConfig = "/etc/samba/smb.conf"
	}
	if cfg.ManagedShareFile == "" {
		cfg.ManagedShareFile = "/etc/samba/lanshare-admin-shares.conf"
	}
	if len(cfg.AllowedRoots) == 0 {
		return configuration{}, errors.New("allowed_roots must contain at least one directory")
	}
	if !prefixPattern.MatchString(cfg.AccountPrefix) {
		return configuration{}, errors.New("account_prefix must be lowercase and end with a dash")
	}
	seen := make(map[string]bool)
	for _, item := range cfg.Shares {
		if !namePattern.MatchString(item.ID) || !groupPattern.MatchString(item.Group) || item.Name == "" || item.UNC == "" {
			return configuration{}, fmt.Errorf("invalid share definition: %q", item.ID)
		}
		if seen[item.ID] {
			return configuration{}, fmt.Errorf("duplicate share id: %q", item.ID)
		}
		seen[item.ID] = true
	}
	return cfg, nil
}

func browse(cfg configuration, requested string) {
	path, err := allowedPath(cfg, requested)
	if err != nil {
		fatal(err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		fatal(err)
	}
	result := []directory{}
	if parent := filepath.Dir(path); parent != path {
		if _, err := allowedPath(cfg, parent); err == nil {
			result = append(result, directory{Name: "..", Path: parent})
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		child := filepath.Join(path, entry.Name())
		if resolved, err := allowedPath(cfg, child); err == nil {
			result = append(result, directory{Name: entry.Name(), Path: resolved})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"path": path, "directories": result})
}

func addShare(cfgPath string, cfg configuration, id, name, path, mode string) {
	id = validateName(id)
	if name == "" || strings.ContainsAny(name, "[]\r\n;#") || len([]rune(name)) > 64 {
		fatal(errors.New("invalid share name"))
	}
	if mode != "rw" && mode != "ro" {
		fatal(errors.New("mode must be rw or ro"))
	}
	for _, item := range cfg.Shares {
		if item.ID == id || strings.EqualFold(item.Name, name) {
			fatal(errors.New("share id or name already exists"))
		}
	}
	resolved, err := allowedPath(cfg, path)
	if err != nil {
		fatal(err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		fatal(errors.New("share path is not a directory"))
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		fatal(errors.New("cannot determine directory owner"))
	}
	owner, err := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10))
	if err != nil || owner.Username == "root" {
		fatal(errors.New("directory must be owned by a non-root local user"))
	}
	group := "lanshare-" + id
	if len(group) > 31 {
		fatal(errors.New("share id is too long for its Unix group"))
	}
	if _, err := user.LookupGroup(group); err != nil {
		mustRun(exec.Command("groupadd", "--system", group))
	}
	item := share{
		ID: id, Name: name, Group: group, Path: resolved,
		UNC:      `\\` + cfg.SMBServer + `\` + name,
		ReadOnly: mode == "ro", Managed: true, ForceUser: owner.Username,
	}
	previous := cfg
	cfg.Shares = append(cfg.Shares, item)
	if err := commitShares(cfgPath, previous, cfg); err != nil {
		_ = exec.Command("groupdel", group).Run()
		fatal(err)
	}
	data, _ := json.Marshal(item)
	fmt.Println(string(data))
}

func removeShare(cfgPath string, cfg configuration, id string) {
	id = validateName(id)
	previous := cfg
	removed := share{}
	remaining := make([]share, 0, len(cfg.Shares))
	for _, item := range cfg.Shares {
		if item.ID == id {
			removed = item
			continue
		}
		remaining = append(remaining, item)
	}
	if removed.ID == "" {
		fatal(errors.New("share not found"))
	}
	if !removed.Managed {
		fatal(errors.New("externally managed share cannot be removed"))
	}
	cfg.Shares = remaining
	if err := commitShares(cfgPath, previous, cfg); err != nil {
		fatal(err)
	}
	_ = exec.Command("groupdel", removed.Group).Run()
}

func allowedPath(cfg configuration, requested string) (string, error) {
	if requested == "" {
		requested = cfg.AllowedRoots[0]
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(requested))
	if err != nil {
		return "", errors.New("directory does not exist")
	}
	for _, configuredRoot := range cfg.AllowedRoots {
		root, err := filepath.EvalSymlinks(filepath.Clean(configuredRoot))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", errors.New("directory is outside allowed_roots")
}

func commitShares(cfgPath string, previous, next configuration) error {
	if err := writeConfig(cfgPath, next); err != nil {
		return err
	}
	if err := writeManagedShares(next.ManagedShareFile, next.Shares); err != nil {
		_ = writeConfig(cfgPath, previous)
		return err
	}
	if output, err := exec.Command("testparm", "-s", next.SambaConfig).CombinedOutput(); err != nil {
		_ = writeConfig(cfgPath, previous)
		_ = writeManagedShares(previous.ManagedShareFile, previous.Shares)
		return fmt.Errorf("testparm failed: %s", strings.TrimSpace(string(output)))
	}
	if err := exec.Command("smbcontrol", "all", "reload-config").Run(); err != nil {
		_ = writeConfig(cfgPath, previous)
		_ = writeManagedShares(previous.ManagedShareFile, previous.Shares)
		return fmt.Errorf("reload samba: %w", err)
	}
	return nil
}

func writeManagedShares(path string, shares []share) error {
	var out strings.Builder
	out.WriteString("# Generated by LanShare Admin. Do not edit manually.\n")
	for _, item := range shares {
		if !item.Managed {
			continue
		}
		mode := "no"
		if item.ReadOnly {
			mode = "yes"
		}
		fmt.Fprintf(&out, "\n[%s]\n    path = %s\n    browseable = yes\n    read only = %s\n    guest ok = no\n    valid users = @%s\n    force user = %s\n    follow symlinks = no\n    wide links = no\n", item.Name, item.Path, mode, item.Group, item.ForceUser)
		if !item.ReadOnly {
			out.WriteString("    create mask = 0664\n    force create mode = 0660\n    directory mask = 0775\n    force directory mode = 0770\n")
		}
	}
	return atomicWrite(path, []byte(out.String()), 0o644)
}

func writeConfig(path string, cfg configuration) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(path, data, 0o640)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	info, statErr := os.Stat(path)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lanshare-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if statErr == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			_ = tmp.Chown(int(stat.Uid), int(stat.Gid))
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func selectedShares(cfg configuration, csv string) []share {
	wanted := make(map[string]bool)
	for _, id := range strings.Split(csv, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		fatal(errors.New("select at least one share"))
	}
	var result []share
	for _, item := range cfg.Shares {
		if wanted[item.ID] {
			result = append(result, item)
			delete(wanted, item.ID)
		}
	}
	if len(wanted) != 0 {
		fatal(errors.New("unknown share id"))
	}
	return result
}

func validateName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !namePattern.MatchString(value) {
		fatal(errors.New("invalid device name"))
	}
	return value
}

func ensureBaseGroup() {
	if _, err := user.LookupGroup("lanshare-devices"); err == nil {
		return
	}
	mustRun(exec.Command("groupadd", "--system", "lanshare-devices"))
}

func ensureAccount(account string) {
	if _, err := user.Lookup(account); err == nil {
		mustRun(exec.Command("usermod", "-g", "lanshare-devices", account))
		return
	}
	mustRun(exec.Command("useradd", "--system", "--no-create-home", "--shell", "/sbin/nologin", "--gid", "lanshare-devices", account))
}

func mustExist(account string) {
	if _, err := user.Lookup(account); err != nil {
		fatal(errors.New("device does not exist"))
	}
}

func setGroups(account string, shares []share) {
	groups := make([]string, 0, len(shares))
	for _, item := range shares {
		if _, err := user.LookupGroup(item.Group); err != nil {
			mustRun(exec.Command("groupadd", "--system", item.Group))
		}
		groups = append(groups, item.Group)
	}
	mustRun(exec.Command("usermod", "-g", "lanshare-devices", "-G", strings.Join(groups, ","), account))
}

func list(cfg configuration) {
	output, err := exec.Command("pdbedit", "-L").Output()
	if err != nil {
		fatal(err)
	}
	groupToShare := make(map[string]string)
	for _, item := range cfg.Shares {
		groupToShare[item.Group] = item.ID
	}
	var result []listedDevice
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		username, _, _ := strings.Cut(line, ":")
		if !strings.HasPrefix(username, cfg.AccountPrefix) {
			continue
		}
		groupOutput, _ := exec.Command("id", "-nG", username).Output()
		var shareIDs []string
		for _, group := range strings.Fields(string(groupOutput)) {
			if id := groupToShare[group]; id != "" {
				shareIDs = append(shareIDs, id)
			}
		}
		sort.Strings(shareIDs)
		result = append(result, listedDevice{
			Name: strings.TrimPrefix(username, cfg.AccountPrefix), Username: username, Shares: shareIDs,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
}

func randomPassword() string {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		fatal(err)
	}
	return hex.EncodeToString(data)
}

func mustRun(cmd *exec.Cmd) {
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fatal(err)
	}
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
