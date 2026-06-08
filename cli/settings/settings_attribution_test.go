package settings

import (
	"context"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestAttributionAccessors_Defaults(t *testing.T) {
	// nil settings, nil Attribution, and nil individual fields all fall back to
	// the Aider-compatible defaults: co-authored-by on, author/committer off.
	var nilSettings *TraceSettings
	if !nilSettings.AttributeCoAuthoredBy() {
		t.Errorf("nil settings: AttributeCoAuthoredBy = false, want true")
	}
	if nilSettings.AttributeAuthor() {
		t.Errorf("nil settings: AttributeAuthor = true, want false")
	}
	if nilSettings.AttributeCommitter() {
		t.Errorf("nil settings: AttributeCommitter = true, want false")
	}

	s := &TraceSettings{}
	if !s.AttributeCoAuthoredBy() {
		t.Errorf("empty settings: AttributeCoAuthoredBy = false, want true")
	}
	if s.AttributeAuthor() || s.AttributeCommitter() {
		t.Errorf("empty settings: author/committer attribution should default off")
	}

	s.Attribution = &AttributionSettings{}
	if !s.AttributeCoAuthoredBy() {
		t.Errorf("empty Attribution: AttributeCoAuthoredBy = false, want true")
	}
	if s.AttributeAuthor() || s.AttributeCommitter() {
		t.Errorf("empty Attribution: author/committer attribution should default off")
	}
}

func TestAttributionAccessors_Explicit(t *testing.T) {
	s := &TraceSettings{
		Attribution: &AttributionSettings{
			AttributeAuthor:       boolPtr(true),
			AttributeCommitter:    boolPtr(true),
			AttributeCoAuthoredBy: boolPtr(false),
		},
	}
	if !s.AttributeAuthor() {
		t.Errorf("AttributeAuthor = false, want true")
	}
	if !s.AttributeCommitter() {
		t.Errorf("AttributeCommitter = false, want true")
	}
	if s.AttributeCoAuthoredBy() {
		t.Errorf("AttributeCoAuthoredBy = true, want false")
	}
}

func TestDirtyCommitsEnabled_Default(t *testing.T) {
	var nilSettings *TraceSettings
	if !nilSettings.DirtyCommitsEnabled() {
		t.Errorf("nil settings: DirtyCommitsEnabled = false, want true")
	}
	s := &TraceSettings{}
	if !s.DirtyCommitsEnabled() {
		t.Errorf("empty settings: DirtyCommitsEnabled = false, want true")
	}
	s.DirtyCommits = boolPtr(false)
	if s.DirtyCommitsEnabled() {
		t.Errorf("dirty_commits=false: DirtyCommitsEnabled = true, want false")
	}
}

func TestLoad_AttributionFromFile(t *testing.T) {
	base := `{"enabled": true, "attribution": {"attribute_author": true, "attribute_co_authored_by": false}, "dirty_commits": false}`
	setupSettingsDir(t, base, "")

	s, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !s.AttributeAuthor() {
		t.Errorf("AttributeAuthor = false, want true")
	}
	if s.AttributeCoAuthoredBy() {
		t.Errorf("AttributeCoAuthoredBy = true, want false")
	}
	// attribute_committer was not set in the file -> default off.
	if s.AttributeCommitter() {
		t.Errorf("AttributeCommitter = true, want false (unset)")
	}
	if s.DirtyCommitsEnabled() {
		t.Errorf("DirtyCommitsEnabled = true, want false")
	}
}

func TestLoad_AttributionLocalOverrideIsIndependent(t *testing.T) {
	// Base turns committer on; local override flips only co-authored-by off.
	// The committer flag must survive the merge (field-level, not wholesale).
	base := `{"enabled": true, "attribution": {"attribute_committer": true}}`
	local := `{"attribution": {"attribute_co_authored_by": false}}`
	setupSettingsDir(t, base, local)

	s, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !s.AttributeCommitter() {
		t.Errorf("AttributeCommitter = false, want true (set in base, untouched by local)")
	}
	if s.AttributeCoAuthoredBy() {
		t.Errorf("AttributeCoAuthoredBy = true, want false (set by local override)")
	}
}
