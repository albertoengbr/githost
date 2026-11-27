package totp

import (
	"testing"
	"time"

	otplib "github.com/pquerna/otp/totp"
)

func TestCurrentStepAndVerify(t *testing.T) {
	secret, _, err := Generate("alice", "GitHost")
	if err != nil {
		t.Fatal(err)
	}
	step := CurrentStep()
	if want := time.Now().Unix() / 30; step != want {
		t.Fatalf("CurrentStep = %d, want %d", step, want)
	}
	code, err := otplib.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(code, secret) {
		t.Fatal("freshly generated code did not verify")
	}
	if Verify("12345", secret) {
		t.Fatal("malformed code accepted")
	}
}
