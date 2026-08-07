package cli

import (
	"context"

	"github.com/GrayCodeAI/trace/cli/settings"

	"github.com/GrayCodeAI/trace/cli/telemetry"
	"github.com/GrayCodeAI/trace/cli/versioninfo"
)

// emitCheckpointPolicyBlocked reports a checkpoint_policy_blocked telemetry
// event when telemetry is opted in (settings.Telemetry == true). Best-effort
// and non-blocking; failures to load settings simply suppress the event.
func emitCheckpointPolicyBlocked(ctx context.Context, event telemetry.CheckpointPolicyBlockedEvent) {
	s, err := settings.Load(ctx)
	if err != nil || s.Telemetry == nil || !*s.Telemetry {
		return
	}
	telemetry.TrackCheckpointPolicyBlocked(event, versioninfo.Version)
}
