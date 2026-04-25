package libghostty

import (
	"testing"
)

func TestFocusEncodeGained(t *testing.T) {
	out, err := FocusEncode(FocusGained)
	if err != nil {
		t.Fatal(err)
	}
	// Focus gained: CSI I = \x1b[I
	expected := "\x1b[I"
	if string(out) != expected {
		t.Fatalf("expected %q, got %q", expected, string(out))
	}
}

func TestFocusEncodeLost(t *testing.T) {
	out, err := FocusEncode(FocusLost)
	if err != nil {
		t.Fatal(err)
	}
	// Focus lost: CSI O = \x1b[O
	expected := "\x1b[O"
	if string(out) != expected {
		t.Fatalf("expected %q, got %q", expected, string(out))
	}
}
