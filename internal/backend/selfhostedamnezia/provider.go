package selfhostedamnezia

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
)

type Config struct {
	Enabled       bool     `yaml:"enabled"`
	StorePath     string   `yaml:"store_path"`
	Container     string   `yaml:"container"`
	Interface     string   `yaml:"interface"`
	EndpointHost  string   `yaml:"endpoint_host"`
	EndpointPort  int      `yaml:"endpoint_port"`
	ConfigPath    string   `yaml:"config_path"`
	ClientsPath   string   `yaml:"clients_path"`
	ServerPubPath string   `yaml:"server_public_key_path"`
	PSKPath       string   `yaml:"preshared_key_path"`
	DNS           []string `yaml:"dns"`
}

type Instance struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Enabled       bool     `json:"enabled"`
	Container     string   `json:"container,omitempty"`
	Interface     string   `json:"interface,omitempty"`
	EndpointHost  string   `json:"endpoint_host"`
	EndpointPort  int      `json:"endpoint_port"`
	ConfigPath    string   `json:"config_path,omitempty"`
	ClientsPath   string   `json:"clients_path,omitempty"`
	ServerPubPath string   `json:"server_public_key_path,omitempty"`
	PSKPath       string   `json:"preshared_key_path,omitempty"`
	DNS           []string `json:"dns,omitempty"`
}

type Store struct {
	Version   int        `json:"version"`
	ActiveID  string     `json:"active_id,omitempty"`
	Instances []Instance `json:"instances"`
}

const DefaultStorePath = "/var/lib/wg-monitor/amnezia-selfhosted.json"

var instanceIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)

func (c Config) withDefaults() Config {
	if c.Container == "" {
		c.Container = "amnezia-awg2"
	}
	if c.Interface == "" {
		c.Interface = "awg0"
	}
	if c.ConfigPath == "" {
		c.ConfigPath = "/opt/amnezia/awg/awg0.conf"
	}
	if c.ClientsPath == "" {
		c.ClientsPath = "/opt/amnezia/awg/clientsTable"
	}
	if c.ServerPubPath == "" {
		c.ServerPubPath = "/opt/amnezia/awg/wireguard_server_public_key.key"
	}
	if c.PSKPath == "" {
		c.PSKPath = "/opt/amnezia/awg/wireguard_psk.key"
	}
	if len(c.DNS) == 0 {
		c.DNS = []string{"1.1.1.1"}
	}
	return c
}

func (c Config) StorePathOrDefault() string {
	if strings.TrimSpace(c.StorePath) != "" {
		return strings.TrimSpace(c.StorePath)
	}
	return DefaultStorePath
}

func (c Config) ProviderConfig(inst Instance) Config {
	out := c
	out.Enabled = inst.Enabled
	if inst.Container != "" {
		out.Container = inst.Container
	}
	if inst.Interface != "" {
		out.Interface = inst.Interface
	}
	if inst.EndpointHost != "" {
		out.EndpointHost = inst.EndpointHost
	}
	if inst.EndpointPort != 0 {
		out.EndpointPort = inst.EndpointPort
	}
	if inst.ConfigPath != "" {
		out.ConfigPath = inst.ConfigPath
	}
	if inst.ClientsPath != "" {
		out.ClientsPath = inst.ClientsPath
	}
	if inst.ServerPubPath != "" {
		out.ServerPubPath = inst.ServerPubPath
	}
	if inst.PSKPath != "" {
		out.PSKPath = inst.PSKPath
	}
	if len(inst.DNS) > 0 {
		out.DNS = append([]string{}, inst.DNS...)
	}
	return out.withDefaults()
}

func (c Config) LegacyInstance() (Instance, bool) {
	cfg := c.withDefaults()
	if !cfg.Ready() {
		return Instance{}, false
	}
	return Instance{
		ID:            "default",
		Label:         "Self-hosted VPS",
		Enabled:       true,
		Container:     cfg.Container,
		Interface:     cfg.Interface,
		EndpointHost:  cfg.EndpointHost,
		EndpointPort:  cfg.EndpointPort,
		ConfigPath:    cfg.ConfigPath,
		ClientsPath:   cfg.ClientsPath,
		ServerPubPath: cfg.ServerPubPath,
		PSKPath:       cfg.PSKPath,
		DNS:           append([]string{}, cfg.DNS...),
	}, true
}

func (c Config) Ready() bool {
	c = c.withDefaults()
	return c.Enabled && c.Container != "" && c.Interface != "" && c.EndpointHost != "" && c.EndpointPort > 0
}

func LoadStore(path string, legacy Config) (Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = legacy.StorePathOrDefault()
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		st := Store{Version: 1}
		if inst, ok := legacy.LegacyInstance(); ok {
			st.Instances = []Instance{inst}
			st.ActiveID = inst.ID
		}
		return st, nil
	}
	if err != nil {
		return Store{}, fmt.Errorf("read self-hosted Amnezia store: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return Store{Version: 1}, nil
	}
	var st Store
	if err := json.Unmarshal(body, &st); err != nil {
		return Store{}, fmt.Errorf("parse self-hosted Amnezia store: %w", err)
	}
	st.normalize()
	return st, nil
}

func SaveStore(path string, st Store) error {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultStorePath
	}
	st.normalize()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create self-hosted Amnezia store dir: %w", err)
	}
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal self-hosted Amnezia store: %w", err)
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(dir, ".amnezia-selfhosted-*.tmp")
	if err != nil {
		return fmt.Errorf("create self-hosted Amnezia store temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod self-hosted Amnezia store temp: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write self-hosted Amnezia store temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close self-hosted Amnezia store temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace self-hosted Amnezia store: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func (s *Store) Upsert(inst Instance) error {
	inst.ID = strings.ToLower(strings.TrimSpace(inst.ID))
	if !instanceIDRe.MatchString(inst.ID) {
		return fmt.Errorf("id %q must match ^[a-z][a-z0-9_-]{1,15}$", inst.ID)
	}
	inst.Label = strings.TrimSpace(inst.Label)
	if inst.Label == "" {
		inst.Label = inst.ID
	}
	inst.EndpointHost = strings.TrimSpace(inst.EndpointHost)
	if inst.EndpointHost == "" {
		return errors.New("endpoint_host is required")
	}
	if inst.EndpointPort <= 0 || inst.EndpointPort > 65535 {
		return errors.New("endpoint_port must be 1..65535")
	}
	for i, dns := range inst.DNS {
		inst.DNS[i] = strings.TrimSpace(dns)
	}
	s.normalize()
	for i := range s.Instances {
		if s.Instances[i].ID == inst.ID {
			if !inst.Enabled {
				inst.Enabled = s.Instances[i].Enabled
			}
			s.Instances[i] = mergeInstance(s.Instances[i], inst)
			if s.ActiveID == "" && s.Instances[i].Enabled {
				s.ActiveID = inst.ID
			}
			s.normalize()
			return nil
		}
	}
	if !inst.Enabled {
		inst.Enabled = true
	}
	s.Instances = append(s.Instances, inst)
	if s.ActiveID == "" {
		s.ActiveID = inst.ID
	}
	s.normalize()
	return nil
}

func (s *Store) Get(id string) (Instance, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	s.normalize()
	if id == "" {
		id = s.ActiveID
	}
	if id == "" && len(s.Instances) == 1 {
		id = s.Instances[0].ID
	}
	for _, inst := range s.Instances {
		if inst.ID == id {
			return inst, true
		}
	}
	return Instance{}, false
}

func (s *Store) EnabledInstances() []Instance {
	s.normalize()
	out := make([]Instance, 0, len(s.Instances))
	for _, inst := range s.Instances {
		if inst.Enabled {
			out = append(out, inst)
		}
	}
	return out
}

func (s *Store) Delete(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	next := s.Instances[:0]
	deleted := false
	for _, inst := range s.Instances {
		if inst.ID == id {
			deleted = true
			continue
		}
		next = append(next, inst)
	}
	s.Instances = next
	if s.ActiveID == id {
		s.ActiveID = ""
	}
	s.normalize()
	return deleted
}

func (s *Store) SetEnabled(id string, enabled bool) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for i := range s.Instances {
		if s.Instances[i].ID == id {
			s.Instances[i].Enabled = enabled
			s.normalize()
			return true
		}
	}
	return false
}

func (s *Store) normalize() {
	if s.Version == 0 {
		s.Version = 1
	}
	seen := map[string]bool{}
	out := s.Instances[:0]
	activeSeen := false
	for _, inst := range s.Instances {
		inst.ID = strings.ToLower(strings.TrimSpace(inst.ID))
		if inst.ID == "" || seen[inst.ID] {
			continue
		}
		seen[inst.ID] = true
		inst.Label = strings.TrimSpace(inst.Label)
		if inst.Label == "" {
			inst.Label = inst.ID
		}
		inst.EndpointHost = strings.TrimSpace(inst.EndpointHost)
		if inst.EndpointHost == "" || inst.EndpointPort <= 0 {
			continue
		}
		if inst.ID == s.ActiveID && inst.Enabled {
			activeSeen = true
		}
		out = append(out, inst)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	s.Instances = out
	if len(s.Instances) == 0 {
		s.ActiveID = ""
		return
	}
	if s.ActiveID == "" || !activeSeen {
		s.ActiveID = ""
		for _, inst := range s.Instances {
			if inst.Enabled {
				s.ActiveID = inst.ID
				return
			}
		}
	}
}

func mergeInstance(old, next Instance) Instance {
	if next.Label == "" {
		next.Label = old.Label
	}
	if next.Container == "" {
		next.Container = old.Container
	}
	if next.Interface == "" {
		next.Interface = old.Interface
	}
	if next.EndpointHost == "" {
		next.EndpointHost = old.EndpointHost
	}
	if next.EndpointPort == 0 {
		next.EndpointPort = old.EndpointPort
	}
	if next.ConfigPath == "" {
		next.ConfigPath = old.ConfigPath
	}
	if next.ClientsPath == "" {
		next.ClientsPath = old.ClientsPath
	}
	if next.ServerPubPath == "" {
		next.ServerPubPath = old.ServerPubPath
	}
	if next.PSKPath == "" {
		next.PSKPath = old.PSKPath
	}
	if len(next.DNS) == 0 {
		next.DNS = old.DNS
	}
	return next
}

type Runner interface {
	Run(ctx context.Context, args []string, stdin []byte) ([]byte, error)
}

type DockerRunner struct {
	Container string
}

func (r DockerRunner) Run(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	base := []string{"exec", "-i", r.Container}
	cmd := exec.CommandContext(ctx, "docker", append(base, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("docker %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

type IssuedConfig struct {
	Name       string
	Address    string
	PublicKey  string
	Config     []byte
	ServerConf []byte
	Clients    []byte
}

func Issue(ctx context.Context, cfg Config, name string) (IssuedConfig, error) {
	cfg = cfg.withDefaults()
	if !cfg.Ready() {
		return IssuedConfig{}, errors.New("self-hosted Amnezia is not configured")
	}
	return IssueWithRunner(ctx, cfg, DockerRunner{Container: cfg.Container}, name, time.Now())
}

func IssueWithRunner(ctx context.Context, cfg Config, runner Runner, name string, now time.Time) (IssuedConfig, error) {
	cfg = cfg.withDefaults()
	name = sanitizeName(name)
	if name == "" {
		name = "router"
	}
	serverConfB, err := runner.Run(ctx, []string{"cat", cfg.ConfigPath}, nil)
	if err != nil {
		return IssuedConfig{}, err
	}
	clientsB, err := runner.Run(ctx, []string{"cat", cfg.ClientsPath}, nil)
	if err != nil {
		return IssuedConfig{}, err
	}
	serverPubB, err := runner.Run(ctx, []string{"cat", cfg.ServerPubPath}, nil)
	if err != nil {
		return IssuedConfig{}, err
	}
	pskB, err := runner.Run(ctx, []string{"cat", cfg.PSKPath}, nil)
	if err != nil {
		return IssuedConfig{}, err
	}

	params, err := parseServerConfig(string(serverConfB))
	if err != nil {
		return IssuedConfig{}, err
	}
	address, err := allocateAddress(params.Prefix, params.Used)
	if err != nil {
		return IssuedConfig{}, err
	}
	priv, pub, err := generateKeypair()
	if err != nil {
		return IssuedConfig{}, err
	}
	serverPub := strings.TrimSpace(string(serverPubB))
	psk := strings.TrimSpace(string(pskB))
	clientConf := buildClientConfig(cfg, params, priv, serverPub, psk, address)
	nextServerConf := appendPeerBlock(strings.TrimRight(string(serverConfB), "\n"), pub, psk, address)
	nextClients, err := appendClient(clientsB, pub, name, address, now)
	if err != nil {
		return IssuedConfig{}, err
	}

	if err := writeAtomic(ctx, runner, cfg.ConfigPath, []byte(nextServerConf)); err != nil {
		return IssuedConfig{}, err
	}
	if err := writeAtomic(ctx, runner, cfg.ClientsPath, nextClients); err != nil {
		return IssuedConfig{}, err
	}
	if _, err := runner.Run(ctx, []string{"wg", "set", cfg.Interface, "peer", pub, "preshared-key", cfg.PSKPath, "allowed-ips", address.String() + "/32"}, nil); err != nil {
		return IssuedConfig{}, err
	}

	return IssuedConfig{
		Name:       name,
		Address:    address.String() + "/32",
		PublicKey:  pub,
		Config:     []byte(clientConf),
		ServerConf: []byte(nextServerConf),
		Clients:    nextClients,
	}, nil
}

type serverParams struct {
	Prefix netip.Prefix
	Fields map[string]string
	Used   map[netip.Addr]bool
}

func parseServerConfig(conf string) (serverParams, error) {
	out := serverParams{Fields: make(map[string]string), Used: make(map[netip.Addr]bool)}
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		switch key {
		case "Address":
			p, err := netip.ParsePrefix(val)
			if err != nil {
				return out, fmt.Errorf("parse server Address: %w", err)
			}
			out.Prefix = p
		case "AllowedIPs":
			for _, part := range strings.Split(val, ",") {
				p, err := netip.ParsePrefix(strings.TrimSpace(part))
				if err == nil {
					out.Used[p.Addr()] = true
				}
			}
		case "Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4":
			out.Fields[key] = val
		}
	}
	if !out.Prefix.IsValid() {
		return out, errors.New("server config missing Address")
	}
	return out, nil
}

func allocateAddress(prefix netip.Prefix, used map[netip.Addr]bool) (netip.Addr, error) {
	if !prefix.Addr().Is4() {
		return netip.Addr{}, errors.New("only IPv4 self-hosted Amnezia pools are supported")
	}
	base := ipv4ToUint32(prefix.Masked().Addr())
	bits := prefix.Bits()
	hostBits := 32 - bits
	if hostBits <= 1 {
		return netip.Addr{}, errors.New("Amnezia IPv4 pool is too small")
	}
	broadcast := base | ((uint32(1) << hostBits) - 1)
	max := base
	for addr := range used {
		if !prefix.Contains(addr) || !addr.Is4() {
			continue
		}
		v := ipv4ToUint32(addr)
		if v > max {
			max = v
		}
	}
	for i := max + 1; i < broadcast; i++ {
		candidate := uint32ToIPv4(i)
		if prefix.Contains(candidate) && !used[candidate] {
			return candidate, nil
		}
	}
	return netip.Addr{}, errors.New("no free IPv4 address in Amnezia pool")
}

func ipv4ToUint32(addr netip.Addr) uint32 {
	a := addr.As4()
	return uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
}

func uint32ToIPv4(v uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func generateKeypair() (privateKey, publicKey string, err error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return "", "", err
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv[:]), base64.StdEncoding.EncodeToString(pub), nil
}

func buildClientConfig(cfg Config, params serverParams, privateKey, serverPublicKey, psk string, address netip.Addr) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s/32\n", privateKey, address)
	if len(cfg.DNS) > 0 {
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(cfg.DNS, ", "))
	}
	keys := []string{"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4"}
	for _, key := range keys {
		if value := params.Fields[key]; value != "" {
			fmt.Fprintf(&b, "%s = %s\n", key, value)
		}
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nEndpoint = %s:%d\nAllowedIPs = 0.0.0.0/0, ::/0\nPersistentKeepalive = 25\n", serverPublicKey, psk, cfg.EndpointHost, cfg.EndpointPort)
	return b.String()
}

func appendPeerBlock(conf, pub, psk string, address netip.Addr) string {
	return fmt.Sprintf("%s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = %s/32\n", conf, pub, psk, address)
}

type clientsTableEntry struct {
	ClientID string         `json:"clientId"`
	UserData map[string]any `json:"userData"`
}

func appendClient(raw []byte, pub, name string, address netip.Addr, now time.Time) ([]byte, error) {
	var entries []clientsTableEntry
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
	}
	entries = append(entries, clientsTableEntry{
		ClientID: pub,
		UserData: map[string]any{
			"allowedIps":      address.String() + "/32",
			"clientName":      name,
			"creationDate":    now.Format("Mon Jan 02 15:04:05 2006"),
			"dataReceived":    "0 B",
			"dataSent":        "0 B",
			"latestHandshake": "never",
		},
	})
	return json.MarshalIndent(entries, "", "    ")
}

func writeAtomic(ctx context.Context, runner Runner, path string, data []byte) error {
	tmp := path + ".wgmon.tmp"
	if _, err := runner.Run(ctx, []string{"sh", "-c", "cat > \"$1\"", "sh", tmp}, data); err != nil {
		return err
	}
	if _, err := runner.Run(ctx, []string{"chmod", "701", tmp}, nil); err != nil {
		return err
	}
	if _, err := runner.Run(ctx, []string{"cp", "-p", path, path + ".wgmon.bak"}, nil); err != nil {
		return err
	}
	if _, err := runner.Run(ctx, []string{"mv", "-f", tmp, path}, nil); err != nil {
		return err
	}
	return nil
}

var unsafeNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = unsafeNameRe.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-_")
	if len(name) > 32 {
		name = name[:32]
	}
	return name
}
