package nntp

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/zalando/go-keyring"
)

// Logins saved by mazama live in the system keyring (Secret Service such as
// gnome-keyring or KWallet on Linux, Keychain on macOS, Credential Manager
// on Windows), one entry per server.
const keyringService = "mazama"

func keyringKey(host string) string { return "news:" + strings.ToLower(host) }

// KeyringLookup finds a login saved with SaveLogin. A missing or
// unreachable keyring counts as no login.
func KeyringLookup(host string) (user, pass string, ok bool) {
	secret, err := keyring.Get(keyringService, keyringKey(host))
	if err != nil {
		return "", "", false
	}
	return decodeLogin(secret)
}

// SaveLogin stores a login for host in the system keyring.
func SaveLogin(host, user, pass string) error {
	secret, err := encodeLogin(user, pass)
	if err != nil {
		return err
	}
	return keyring.Set(keyringService, keyringKey(host), secret)
}

const loginPrefix = "mazama1:"

// encodeLogin packs a login into one keyring secret made only of base64
// characters. gnome-keyring's unencrypted keyring file stores secrets as
// key-file lines: a raw line break makes the daemon reject the whole keyring
// at startup, and backslash escapes are unescaped when read back.
func encodeLogin(user, pass string) (string, error) {
	if user == "" {
		return "", errors.New("empty user name")
	}
	enc := base64.StdEncoding.EncodeToString
	return loginPrefix + enc([]byte(user)) + ":" + enc([]byte(pass)), nil
}

func decodeLogin(secret string) (user, pass string, ok bool) {
	if rest, found := strings.CutPrefix(secret, loginPrefix); found {
		u64, p64, _ := strings.Cut(rest, ":")
		u, err1 := base64.StdEncoding.DecodeString(u64)
		p, err2 := base64.StdEncoding.DecodeString(p64)
		if err1 != nil || err2 != nil || len(u) == 0 {
			return "", "", false
		}
		return string(u), string(p), true
	}
	// Older versions stored "user\npass".
	user, pass, ok = strings.Cut(secret, "\n")
	return user, pass, ok && user != ""
}

// DeleteLogin removes a saved login; a missing one is not an error.
func DeleteLogin(host string) error {
	err := keyring.Delete(keyringService, keyringKey(host))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Lookups chains login lookups, returning the first hit.
func Lookups(fns ...func(string) (string, string, bool)) func(string) (string, string, bool) {
	return func(host string) (string, string, bool) {
		for _, f := range fns {
			if f == nil {
				continue
			}
			if u, p, ok := f(host); ok {
				return u, p, true
			}
		}
		return "", "", false
	}
}
