package libghostty

import (
	"testing"
)

func TestKeyEventNewClose(t *testing.T) {
	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
}

func TestKeyEventAction(t *testing.T) {
	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	ev.SetAction(KeyActionPress)
	if got := ev.Action(); got != KeyActionPress {
		t.Fatalf("expected KeyActionPress, got %d", got)
	}

	ev.SetAction(KeyActionRelease)
	if got := ev.Action(); got != KeyActionRelease {
		t.Fatalf("expected KeyActionRelease, got %d", got)
	}
}

func TestKeyEventKey(t *testing.T) {
	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	ev.SetKey(KeyA)
	if got := ev.Key(); got != KeyA {
		t.Fatalf("expected KeyA, got %d", got)
	}
}

func TestKeyEventMods(t *testing.T) {
	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	ev.SetMods(ModCtrl | ModShift)
	if got := ev.Mods(); got != ModCtrl|ModShift {
		t.Fatalf("expected Ctrl|Shift, got %d", got)
	}
}

func TestKeyEventConsumedMods(t *testing.T) {
	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	ev.SetConsumedMods(ModAlt)
	if got := ev.ConsumedMods(); got != ModAlt {
		t.Fatalf("expected ModAlt, got %d", got)
	}
}

func TestKeyEventComposing(t *testing.T) {
	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	ev.SetComposing(true)
	if !ev.Composing() {
		t.Fatal("expected composing to be true")
	}

	ev.SetComposing(false)
	if ev.Composing() {
		t.Fatal("expected composing to be false")
	}
}

func TestKeyEventUTF8(t *testing.T) {
	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	// Default should be empty.
	if got := ev.UTF8(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	text := "a"
	ev.SetUTF8(text)
	if got := ev.UTF8(); got != text {
		t.Fatalf("expected %q, got %q", text, got)
	}
}

func TestKeyEventUnshiftedCodepoint(t *testing.T) {
	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	ev.SetUnshiftedCodepoint('a')
	if got := ev.UnshiftedCodepoint(); got != 'a' {
		t.Fatalf("expected 'a', got %c", got)
	}
}

func TestKeyEncoderNewClose(t *testing.T) {
	enc, err := NewKeyEncoder()
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
}

func TestKeyEncoderEncodeSimpleKey(t *testing.T) {
	enc, err := NewKeyEncoder()
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	// Encode 'a' key press with UTF-8 text.
	ev.SetAction(KeyActionPress)
	ev.SetKey(KeyA)
	ev.SetUTF8("a")

	out, err := enc.Encode(ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty output for 'a' key press")
	}
	if string(out) != "a" {
		t.Fatalf("expected 'a', got %q", string(out))
	}
}

func TestKeyEncoderEncodeArrowKey(t *testing.T) {
	enc, err := NewKeyEncoder()
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	// Arrow up should produce an escape sequence.
	ev.SetAction(KeyActionPress)
	ev.SetKey(KeyArrowUp)

	out, err := enc.Encode(ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty output for arrow up")
	}
	// Default mode: CSI A (\x1b[A)
	if string(out) != "\x1b[A" {
		t.Fatalf("expected \\x1b[A, got %q", string(out))
	}
}

func TestKeyEncoderSetOptFromTerminal(t *testing.T) {
	term, err := NewTerminal(WithSize(80, 24))
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	enc, err := NewKeyEncoder()
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	// Should not panic.
	enc.SetOptFromTerminal(term)

	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	ev.SetAction(KeyActionPress)
	ev.SetKey(KeyA)
	ev.SetUTF8("a")

	out, err := enc.Encode(ev)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a" {
		t.Fatalf("expected 'a', got %q", string(out))
	}
}

func TestKeyEncoderCursorKeyApplicationMode(t *testing.T) {
	enc, err := NewKeyEncoder()
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	// Enable cursor key application mode.
	enc.SetOptBool(KeyEncoderOptCursorKeyApplication, true)

	ev, err := NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()

	ev.SetAction(KeyActionPress)
	ev.SetKey(KeyArrowUp)

	out, err := enc.Encode(ev)
	if err != nil {
		t.Fatal(err)
	}
	// Application mode: SS3 A (\x1bOA)
	if string(out) != "\x1bOA" {
		t.Fatalf("expected \\x1bOA in application mode, got %q", string(out))
	}
}
