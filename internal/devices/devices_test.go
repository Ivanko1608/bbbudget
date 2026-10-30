package devices

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var start = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// createTestRegistry returns a registry whose clock the test moves with advance.
func createTestRegistry(t *testing.T) (r *Registry, advance func(time.Duration)) {
	t.Helper()
	r = Open(filepath.Join(t.TempDir(), "devices.json"))
	now := start
	r.now = func() time.Time { return now }
	return r, func(d time.Duration) { now = now.Add(d) }
}

func createPairing(t *testing.T, r *Registry) Pairing {
	t.Helper()
	p, err := r.CreatePairing()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func pairDevice(t *testing.T, r *Registry, name string) PairingResult {
	t.Helper()
	paired, err := r.Pair(createPairing(t, r).Code, name)
	if err != nil {
		t.Fatal(err)
	}
	return paired
}

func TestPairedTokenAuthenticatesItsDevice(t *testing.T) {
	r, _ := createTestRegistry(t)

	paired := pairDevice(t, r, "Pixel")

	d, err := r.Authenticate(paired.Token)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != paired.Device.ID {
		t.Errorf("token belongs to %q, want %q", d.ID, paired.Device.ID)
	}
	if d.Name != "Pixel" {
		t.Errorf("name = %q, want Pixel", d.Name)
	}
}

func TestCodeIsAcceptedHoweverItIsTyped(t *testing.T) {
	r, _ := createTestRegistry(t)
	code := createPairing(t, r).Code
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))

	_, err := r.Pair(typed, "Pixel")

	if err != nil {
		t.Errorf("Pair(%q) for code %q: %v", typed, code, err)
	}
}

func TestCodeWorksOnlyOnce(t *testing.T) {
	r, _ := createTestRegistry(t)
	code := createPairing(t, r).Code
	if _, err := r.Pair(code, "first"); err != nil {
		t.Fatal(err)
	}

	_, err := r.Pair(code, "second")

	if !errors.Is(err, ErrBadCode) {
		t.Errorf("second use: %v, want ErrBadCode", err)
	}
}

func TestCodeExpires(t *testing.T) {
	r, advance := createTestRegistry(t)
	code := createPairing(t, r).Code

	advance(PairingTTL)
	_, err := r.Pair(code, "late")

	if !errors.Is(err, ErrBadCode) {
		t.Errorf("expired code: %v, want ErrBadCode", err)
	}
}

func TestWrongCodeIsRejected(t *testing.T) {
	r, _ := createTestRegistry(t)
	createPairing(t, r)

	_, err := r.Pair("AAAAA-AAAAA", "guess")

	if !errors.Is(err, ErrBadCode) {
		t.Errorf("wrong code: %v, want ErrBadCode", err)
	}
}

func TestRevokedTokenStopsWorking(t *testing.T) {
	r, _ := createTestRegistry(t)
	kept, revoked := pairDevice(t, r, "laptop"), pairDevice(t, r, "lost phone")

	if err := r.Revoke(revoked.Device.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Authenticate(revoked.Token); !errors.Is(err, ErrBadToken) {
		t.Errorf("revoked token: %v, want ErrBadToken", err)
	}
	if _, err := r.Authenticate(kept.Token); err != nil {
		t.Errorf("other device's token: %v", err)
	}
}

func TestRevokeUnknownDevice(t *testing.T) {
	r, _ := createTestRegistry(t)

	err := r.Revoke("nope")

	if !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("Revoke: %v, want ErrDeviceNotFound", err)
	}
}

func TestUnknownTokenIsRejected(t *testing.T) {
	r, _ := createTestRegistry(t)
	pairDevice(t, r, "Pixel")

	_, err := r.Authenticate("bbd_NOTATOKEN")

	if !errors.Is(err, ErrBadToken) {
		t.Errorf("Authenticate: %v, want ErrBadToken", err)
	}
}

func TestFileHoldsNoSecrets(t *testing.T) {
	r, _ := createTestRegistry(t)
	code := createPairing(t, r).Code
	paired := pairDevice(t, r, "Pixel")

	raw, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(raw), paired.Token) {
		t.Error("devices file holds a token")
	}
	if strings.Contains(string(raw), normalizeCode(code)) {
		t.Error("devices file holds a pairing code")
	}
}

func TestFileIsPrivate(t *testing.T) {
	r, _ := createTestRegistry(t)
	pairDevice(t, r, "Pixel")

	info, err := os.Stat(r.path)
	if err != nil {
		t.Fatal(err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("devices file mode = %o, want 600", perm)
	}
}

func TestRevocationReachesOtherProcesses(t *testing.T) {
	server, _ := createTestRegistry(t)
	cli := Open(server.path)
	paired := pairDevice(t, server, "Pixel")

	if err := cli.Revoke(paired.Device.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := server.Authenticate(paired.Token); !errors.Is(err, ErrBadToken) {
		t.Errorf("token revoked elsewhere: %v, want ErrBadToken", err)
	}
}

func TestDeviceNames(t *testing.T) {
	long := strings.Repeat("я", maxNameLength+10)
	cases := []struct{ typed, want string }{
		{"  Pixel 8 ", "Pixel 8"},
		{"", defaultName},
		{"Pixel\x1b[2J\n", "Pixel[2J"},
		{long, strings.Repeat("я", maxNameLength)},
	}
	for _, c := range cases {
		if got := cleanName(c.typed); got != c.want {
			t.Errorf("cleanName(%q) = %q, want %q", c.typed, got, c.want)
		}
	}
}
