package uiform

import (
	"context"
	"os"
	"testing"

	"charm.land/huh/v2"
)

func TestIsAccessibleMode(t *testing.T) {
	// Save and restore env
	orig, had := os.LookupEnv("ACCESSIBLE")
	defer func() {
		if had {
			os.Setenv("ACCESSIBLE", orig)
		} else {
			os.Unsetenv("ACCESSIBLE")
		}
	}()

	// When not set
	os.Unsetenv("ACCESSIBLE")
	if IsAccessibleMode() {
		t.Error("IsAccessibleMode() = true when ACCESSIBLE unset, want false")
	}

	// When set to "1"
	os.Setenv("ACCESSIBLE", "1")
	if !IsAccessibleMode() {
		t.Error("IsAccessibleMode() = false when ACCESSIBLE=1, want true")
	}

	// When set to any non-empty value
	os.Setenv("ACCESSIBLE", "yes")
	if !IsAccessibleMode() {
		t.Error("IsAccessibleMode() = false when ACCESSIBLE=yes, want true")
	}

	// When set to empty string
	os.Setenv("ACCESSIBLE", "")
	if IsAccessibleMode() {
		t.Error("IsAccessibleMode() = true when ACCESSIBLE=\"\", want false")
	}
}

func TestTheme(t *testing.T) {
	theme := Theme()
	if theme == nil {
		t.Error("Theme() returned nil")
	}
}

func TestNew(t *testing.T) {
	// Save and restore env
	orig, had := os.LookupEnv("ACCESSIBLE")
	defer func() {
		if had {
			os.Setenv("ACCESSIBLE", orig)
		} else {
			os.Unsetenv("ACCESSIBLE")
		}
	}()

	os.Unsetenv("ACCESSIBLE")

	// Create form with a group
	group := huh.NewGroup(huh.NewConfirm().Title("test?").Value(new(bool)))
	form := New(group)
	if form == nil {
		t.Fatal("New() returned nil")
	}

	// With ACCESSIBLE set
	os.Setenv("ACCESSIBLE", "1")
	group2 := huh.NewGroup(huh.NewConfirm().Title("test?").Value(new(bool)))
	form2 := New(group2)
	if form2 == nil {
		t.Fatal("New() with ACCESSIBLE returned nil")
	}
}

func TestPromptYN_ContextCanceled(t *testing.T) {
	// Skip if no TTY available (CI/test environments)
	f, err := os.Open("/dev/tty")
	if err != nil {
		t.Skip("no TTY available, skipping PromptYN test")
	}
	f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	answer, err := PromptYN(ctx, "continue?", true)
	if err != nil {
		t.Errorf("PromptYN with cancelled ctx: unexpected error: %v", err)
	}
	if answer != false {
		t.Errorf("PromptYN with cancelled ctx: answer = %v, want false", answer)
	}
}
