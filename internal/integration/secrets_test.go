package integration_test

import (
	"io"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/measure"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/textguard"
)

// A brain is a file that gets backed up, exported and handed around, so credential-shaped
// text is refused on every write path. Every text field is scanned, not only the title and
// body, and a false positive always has a way through.

func TestCredentialShapedTextIsRefused(t *testing.T) {
	s := newTestStore(t)
	for _, body := range []string{
		"password=hunter2hunter2",
		"-----BEGIN RSA PRIVATE KEY-----",
		"AKIAIOSFODNN7EXAMPLE",
		"ghp_0123456789abcdefghijklmnopqrstuvwx",
	} {
		if err := memory.New(s).Remember(io.Discard, memory.RememberArgs{Scope: "global", Type: "note",
			Title: "creds", Body: body, Importance: 3}); err == nil {
			t.Errorf("stored a secret: %q", body)
		}
	}
	// A false positive must not be a dead end.
	if err := memory.New(s).Remember(io.Discard, memory.RememberArgs{Scope: "global", Type: "note",
		Title: "creds", Body: "password=hunter2hunter2", Importance: 3,
		AllowSecret: true}); err != nil {
		t.Errorf("--allow-secret should store it: %v", err)
	}
}

// Payment-provider keys, ko_-prefixed platform keys, passwords in URL userinfo and bearer
// tokens once went straight in. Prose that merely talks about them must still pass.
func TestTheSecretScannerKnowsTheCommonLiveKeyFormats(t *testing.T) {
	// The payment-key fixtures are split so no key-shaped literal sits in the source, which
	// a host's push protection would rightly refuse. The value tested is the same.
	for _, s := range []string{
		"sk_" + "live_51Hxabcdefghijklmnopqrstu",
		"rk_" + "test_51Hxabcdefghijklmnopqrstu",
		"ko_" + strings.Repeat("a1B2", 16),
		"postgres://admin:S3cretPa55@db.internal:5432/prod",
		"Authorization: Bearer abcDEF123456ghiJKL789mno",
		"curl -H 'bearer 9f8e7d6c5b4a39281706f5e4d3c2b1a0'",
	} {
		if len(textguard.ScanSecrets(s)) == 0 {
			t.Errorf("missed: %s", s)
		}
	}
	for _, s := range []string{
		"bearer tokens expire hourly and are refreshed by the client",
		"postgres://user:$DB_PASS@db.internal/prod",
		"postgres://user:<password>@host/db",
		"https://example.com/path:with:colons@nothing",
		"the sk_live keys live in the vault under stripe/live",
		"we knocked out ko_ prefixes from the list",
		"see https://docs.example.com/auth for the Authorization header format",
	} {
		if hits := textguard.ScanSecrets(s); len(hits) > 0 {
			t.Errorf("false positive %v on ordinary prose: %s", hits, s)
		}
	}
}

// update's --title and --source, and measure's --label, --source and limit --why, were
// once written without a scan.
func TestUpdateAndMeasureScanEveryTextField(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "an ordinary fact to edit"})
	id := idOf(t, s, "an ordinary fact to edit")
	secret := "db creds password=Hunter2xyz99 for prod"
	if err := memory.New(s).Update(io.Discard, memory.UpdateArgs{ID: id, Title: &secret}); err == nil {
		t.Error("update --title stored a credential")
	}
	src := "api_key: AKIAABCDEFGHIJKLMNOP"
	if err := memory.New(s).Update(io.Discard, memory.UpdateArgs{ID: id, Source: &src}); err == nil {
		t.Error("update --source stored a credential")
	}
	if err := measure.New(s).Measure(io.Discard, measure.MeasureArgs{Sub: "add", Key: "k", Value: 1, Label: secret}); err == nil {
		t.Error("measure --label stored a credential")
	}
	if err := measure.New(s).Measure(io.Discard, measure.MeasureArgs{Sub: "add", Key: "k", Value: 1, Source: src}); err == nil {
		t.Error("measure --source stored a credential")
	}
	lo := 1.0
	if err := measure.New(s).Measure(io.Discard, measure.MeasureArgs{Sub: "limit", Key: "k", Low: &lo, Why: secret}); err == nil {
		t.Error("measure limit --why stored a credential")
	}
}

// The secret guard must stop a JWT and a password written out in prose, and must not fire on
// ordinary sentences about credentials.
func TestSecretGuardCatchesJWTsAndProsePasswords(t *testing.T) {
	blocked := []string{
		"Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTYifQ.dozjgNryP4J3jVmNHl0w5N",
		"The password is hunter2longenough for the staging box.",
		"passphrase was c0rrect-horse-battery",
	}
	for _, txt := range blocked {
		if len(textguard.ScanSecrets(txt)) == 0 {
			t.Errorf("stored a secret: %q", txt)
		}
	}
	// A guard that fires on ordinary prose about credentials gets --allow-secret pasted
	// in reflexively, which is worse than no guard.
	allowed := []string{
		"The token is refreshed hourly by the sidecar.",
		"The password is stored in Vault, never in the repo.",
		"Every secret is rotated quarterly by the platform team.",
		"The api key is provisioned per environment at deploy time.",
	}
	for _, txt := range allowed {
		if hits := textguard.ScanSecrets(txt); len(hits) > 0 {
			t.Errorf("false positive %v on ordinary prose: %q", hits, txt)
		}
	}
}
