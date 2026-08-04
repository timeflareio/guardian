package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/custody"
)

// The cutover runs after the rotation is irreversibly on-chain, so both keys
// must end up where the daemon and the epoch keyring expect them: the new key
// at the configured path, the outgoing one beside it as .epoch<N>. Getting this
// wrong means a no-reveal slash on every assignment bound to the old epoch.
func TestCutoverPromotesStagedKeyAndRetiresOld(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "private_key")
	stagedPath := keyPath + ".next"

	oldPair, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	newPair, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if err := custody.SaveEncryptedShareKey(keyPath, oldPair.PrivateKey, "pass"); err != nil {
		t.Fatal(err)
	}
	if err := custody.SaveEncryptedShareKey(stagedPath, newPair.PrivateKey, "pass"); err != nil {
		t.Fatal(err)
	}

	retiredPath, err := cutoverEpochKeys(keyPath, stagedPath, 3, "ABC123")
	if err != nil {
		t.Fatalf("cutover failed: %v", err)
	}

	if retiredPath != custody.EpochKeyPath(keyPath, 3) {
		t.Errorf("retired key at %s, want %s", retiredPath, custody.EpochKeyPath(keyPath, 3))
	}
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Error("the staged file survived the cutover; a later rotation would find it in the way")
	}

	current, err := custody.LoadShareKey(keyPath, func() (string, error) { return "pass", nil })
	if err != nil {
		t.Fatalf("promoted key does not load: %v", err)
	}
	if current != newPair.PrivateKey {
		t.Error("the configured path does not hold the new epoch's key")
	}

	retired, err := custody.LoadShareKey(retiredPath, func() (string, error) { return "pass", nil })
	if err != nil {
		t.Fatalf("retired key does not load: %v", err)
	}
	if retired != oldPair.PrivateKey {
		t.Error("the retired path does not hold the outgoing key")
	}
}

// If the first rename fails the transaction has already landed, so the error is
// the operator's only instruction. It must name both moves and leave the key
// that still serves assignments untouched.
func TestCutoverFailureNamesTheManualRecovery(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "private_key")
	stagedPath := keyPath + ".next"

	pair, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if err := custody.SaveEncryptedShareKey(stagedPath, pair.PrivateKey, "pass"); err != nil {
		t.Fatal(err)
	}
	// keyPath deliberately absent: the rename cannot succeed.

	_, err = cutoverEpochKeys(keyPath, stagedPath, 7, "DEADBEEF")
	if err == nil {
		t.Fatal("cutover reported success with no key to retire")
	}
	for _, want := range []string{"SUBMITTED", "DEADBEEF", stagedPath, custody.EpochKeyPath(keyPath, 7)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q — an operator cannot finish the move by hand:\n%v", want, err)
		}
	}
	// The staged key must still be there for that manual move to be possible.
	if _, statErr := os.Stat(stagedPath); statErr != nil {
		t.Error("the staged key was lost, so the recovery the error describes is impossible")
	}
}
