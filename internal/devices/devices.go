// Package devices lets phones and other computers use a bbbudget server
// remotely. A device pairs once with a short-lived pairing code and then
// sends its token with every request.
//
// Only hashes of codes and tokens are stored, so reading the devices file
// does not let anyone sign in.
package devices

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"bbbudget/internal/filelock"
)

var (
	// ErrBadCode means a pairing code is wrong, expired or already used.
	ErrBadCode = errors.New("pairing code is wrong or expired; make a new one with `bbbudget pair`")
	// ErrBadToken means a device token is unknown or was revoked.
	ErrBadToken = errors.New("device token is unknown or revoked; pair the device again")
	// ErrNotFound means no paired device has the given id.
	ErrNotFound = errors.New("no paired device has this id")
)

const (
	// PairingTTL is how long a pairing code can be used.
	PairingTTL = 10 * time.Minute
	// codeLength characters of base32 carry 50 bits: guessing one in
	// PairingTTL is hopeless even at thousands of tries a second.
	codeLength    = 10
	codeGroup     = 5
	tokenPrefix   = "bbd_"
	maxNameLength = 64
	defaultName   = "device"
)

// Device is a paired phone or computer.
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	PairedAt  time.Time `json:"paired_at"`
	TokenHash string    `json:"token_hash"`
}

// Pairing is a pairing code waiting to be used.
type Pairing struct {
	// Code is shown to the person pairing, grouped for reading ("ABCDE-FGHIJ").
	Code      string
	ExpiresAt time.Time
}

// Paired is the result of using a pairing code.
type Paired struct {
	Device Device
	// Token is shown to the device once and never stored.
	Token string
}

type pendingCode struct {
	CodeHash  string    `json:"code_hash"`
	ExpiresAt time.Time `json:"expires_at"`
}

type state struct {
	Devices []Device      `json:"devices"`
	Codes   []pendingCode `json:"pairing_codes"`
}

// Registry keeps the paired devices in one JSON file. Every call reads the
// file again, so a device revoked from the command line is locked out of a
// running server at once.
type Registry struct {
	path string
	now  func() time.Time
	mu   sync.Mutex // serialises this process; the file lock serialises processes
}

// Open returns the registry stored at path. The file is created on the
// first write.
func Open(path string) *Registry {
	return &Registry{path: path, now: time.Now}
}

// NewPairing makes a pairing code that pairs one device within PairingTTL.
func (r *Registry) NewPairing() (Pairing, error) {
	code := rand.Text()[:codeLength]
	expires := r.now().Add(PairingTTL)
	err := r.update(func(s *state) error {
		s.Codes = append(s.Codes, pendingCode{CodeHash: hash(code), ExpiresAt: expires})
		return nil
	})
	if err != nil {
		return Pairing{}, err
	}
	return Pairing{Code: groupCode(code), ExpiresAt: expires}, nil
}

// Pair uses up a pairing code and pairs a device called name.
func (r *Registry) Pair(code, name string) (Paired, error) {
	token := tokenPrefix + rand.Text()
	device := Device{
		ID:        strings.ToLower(rand.Text()[:8]),
		Name:      cleanName(name),
		PairedAt:  r.now(),
		TokenHash: hash(token),
	}
	codeHash := hash(normalizeCode(code))
	err := r.update(func(s *state) error {
		used := slices.IndexFunc(s.Codes, func(c pendingCode) bool { return c.CodeHash == codeHash })
		if used < 0 {
			return ErrBadCode
		}
		s.Codes = slices.Delete(s.Codes, used, used+1)
		s.Devices = append(s.Devices, device)
		return nil
	})
	if err != nil {
		return Paired{}, err
	}
	return Paired{Device: device, Token: token}, nil
}

// Authenticate returns the device a token belongs to.
func (r *Registry) Authenticate(token string) (Device, error) {
	s, err := r.read()
	if err != nil {
		return Device{}, err
	}
	tokenHash := hash(token)
	for _, d := range s.Devices {
		if d.TokenHash == tokenHash {
			return d, nil
		}
	}
	return Device{}, ErrBadToken
}

// List returns the paired devices, oldest first.
func (r *Registry) List() ([]Device, error) {
	s, err := r.read()
	if err != nil {
		return nil, err
	}
	return s.Devices, nil
}

// Revoke unpairs a device: its token stops working at once.
func (r *Registry) Revoke(id string) error {
	return r.update(func(s *state) error {
		i := slices.IndexFunc(s.Devices, func(d Device) bool { return d.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		s.Devices = slices.Delete(s.Devices, i, i+1)
		return nil
	})
}

func (r *Registry) read() (state, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	unlock, err := r.lock(false)
	if err != nil {
		return state{}, err
	}
	defer unlock()
	return r.load()
}

// update runs edit on the current state and saves the result, dropping
// expired pairing codes on the way. Nothing is saved when edit fails.
func (r *Registry) update(edit func(*state) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	unlock, err := r.lock(true)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := r.load()
	if err != nil {
		return err
	}
	s.Codes = slices.DeleteFunc(s.Codes, r.expired)
	if err := edit(&s); err != nil {
		return err
	}
	return r.save(s)
}

func (r *Registry) expired(c pendingCode) bool { return !r.now().Before(c.ExpiresAt) }

func (r *Registry) lock(exclusive bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return nil, err
	}
	return filelock.Lock(r.path+".lock", exclusive)
}

func (r *Registry) load() (state, error) {
	var s state
	raw, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return state{}, err
	}
	return s, nil
}

// save writes the file atomically, readable only by its owner.
func (r *Registry) save(s state) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(r.path)
	tmp, err := os.CreateTemp(dir, ".devices.*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := writeAndClose(tmp, append(raw, '\n')); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), r.path); err != nil {
		return err
	}
	return filelock.SyncDir(dir)
}

func writeAndClose(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func groupCode(code string) string {
	return code[:codeGroup] + "-" + code[codeGroup:]
}

// normalizeCode accepts a code however it was typed: in any case, with or
// without the dash and spaces.
func normalizeCode(typed string) string {
	code := strings.ToUpper(typed)
	return strings.NewReplacer("-", "", " ", "").Replace(code)
}

// cleanName drops control characters, because `bbbudget devices` prints the
// name in a terminal, where they could pass as escape sequences.
func cleanName(name string) string {
	name = strings.TrimSpace(strings.Map(dropControl, name))
	if name == "" {
		return defaultName
	}
	runes := []rune(name)
	if len(runes) > maxNameLength {
		return string(runes[:maxNameLength])
	}
	return name
}

func dropControl(r rune) rune {
	if unicode.IsControl(r) {
		return -1
	}
	return r
}
