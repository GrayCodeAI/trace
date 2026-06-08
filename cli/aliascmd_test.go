package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestHideAsAlias_HidesAndDeprecates(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "rewind"}
	got := hideAsAlias(cmd, "trace checkpoint rewind")

	if got != cmd {
		t.Fatal("hideAsAlias should return the same command instance")
	}
	if !cmd.Hidden {
		t.Error("expected Hidden=true")
	}
	if !strings.Contains(cmd.Deprecated, "trace checkpoint rewind") {
		t.Errorf("Deprecated message missing canonical command, got %q", cmd.Deprecated)
	}
}

func TestHideAsAlias_DifferentCanonicalsDontShareState(t *testing.T) {
	t.Parallel()

	a := hideAsAlias(&cobra.Command{Use: "rewind"}, "trace checkpoint rewind")
	b := hideAsAlias(&cobra.Command{Use: "resume"}, "trace session resume")

	if a.Deprecated == b.Deprecated {
		t.Errorf("hints leaked between commands: %q == %q", a.Deprecated, b.Deprecated)
	}
}
