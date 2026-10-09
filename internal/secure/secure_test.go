package secure

import (
	"bytes"
	"testing"
	"time"
)

// Vetores do RFC 6238 (SHA1, segredo "12345678901234567890"), truncados para 6 dígitos.
func TestTOTPRFC6238(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	cases := map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	}
	for ts, want := range cases {
		got, err := TOTPCode(secret, time.Unix(ts, 0))
		if err != nil || got != want {
			t.Errorf("t=%d got %s want %s (%v)", ts, got, want, err)
		}
	}
}

func TestVerifyTOTPReplayAndSkew(t *testing.T) {
	s := NewTOTPSecret()
	now := time.Unix(1_800_000_000, 0)
	code, _ := TOTPCode(s, now.Add(-30*time.Second))
	step, ok := VerifyTOTP(s, code, now, 0)
	if !ok {
		t.Fatal("código da janela anterior deveria ser aceito")
	}
	if _, ok := VerifyTOTP(s, code, now, step); ok {
		t.Fatal("replay deveria ser rejeitado")
	}
	old, _ := TOTPCode(s, now.Add(-5*time.Minute))
	if _, ok := VerifyTOTP(s, old, now, 0); ok {
		t.Fatal("código antigo deveria ser rejeitado")
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("S3nha-Forte!")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "S3nha-Forte!") || CheckPassword(h, "errada") {
		t.Fatal("verificação de senha incorreta")
	}
	if err := ValidatePasswordPolicy("curta"); err == nil {
		t.Fatal("senha curta deveria falhar")
	}
	if err := ValidatePasswordPolicy(RandomPassword()); err != nil {
		t.Fatalf("senha aleatória fora da política: %v", err)
	}
}

func TestBox(t *testing.T) {
	k := bytes.Repeat([]byte{7}, 32)
	b, _ := NewBox(k)
	ct, _ := b.Seal([]byte("segredo"))
	pt, err := b.Open(ct)
	if err != nil || string(pt) != "segredo" {
		t.Fatal("roundtrip falhou")
	}
	ct[len(ct)-1] ^= 1
	if _, err := b.Open(ct); err == nil {
		t.Fatal("adulteração não detectada")
	}
}
