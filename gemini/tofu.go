package gemini

import (
	"bufio"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// TOFU is a trust-on-first-use store mapping host:port to the SHA-256 of the
// server's public key. Pinning the key rather than the whole certificate
// means routine certificate renewals with the same key keep working.
type TOFU struct {
	path  string
	mu    sync.Mutex
	hosts map[string]string
}

// ErrCertChanged is returned when a host presents a different key than the
// one pinned on first use.
var ErrCertChanged = errors.New("server certificate changed")

// DefaultTOFUPath is $XDG_DATA_HOME/mazama/known_hosts, falling back to
// ~/.local/share/mazama/known_hosts.
func DefaultTOFUPath() (string, error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "mazama", "known_hosts"), nil
}

// LoadTOFU reads the store at path. A missing file is an empty store.
func LoadTOFU(path string) (*TOFU, error) {
	t := &TOFU{path: path, hosts: map[string]string{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			t.hosts[fields[0]] = fields[1]
		}
	}
	return t, sc.Err()
}

// Path is where the store is saved.
func (t *TOFU) Path() string { return t.path }

// Check pins cert for hostport on first sight and verifies it afterwards.
func (t *TOFU) Check(hostport string, cert *x509.Certificate) error {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	fp := "sha256:" + hex.EncodeToString(sum[:])

	t.mu.Lock()
	defer t.mu.Unlock()
	known, ok := t.hosts[hostport]
	if ok {
		if known != fp {
			return fmt.Errorf("%w for %s: if you trust the new one, delete its line from %s",
				ErrCertChanged, hostport, t.path)
		}
		return nil
	}
	t.hosts[hostport] = fp
	return t.save()
}

// save writes the store atomically. Called with mu held.
func (t *TOFU) save() error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# mazama gemini known hosts: host:port sha256-of-public-key\n")
	for _, h := range slices.Sorted(maps.Keys(t.hosts)) {
		fmt.Fprintf(&b, "%s %s\n", h, t.hosts[h])
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}
