package agent

import "github.com/GrayCodeAI/trace/cli/agent/types"

const (
	SkillEventTypePromptInvocation = types.SkillEventTypePromptInvocation
	SkillEventTypeToolInvocation   = types.SkillEventTypeToolInvocation

	SkillSignalPiInputSlashCommand = types.SkillSignalPiInputSlashCommand
	SkillSignalPromptSlashCommand  = types.SkillSignalPromptSlashCommand
	SkillSignalClaudeSkillToolUse  = types.SkillSignalClaudeSkillToolUse

	SkillConfidenceExplicit = types.SkillConfidenceExplicit

	SkillCollapseTargetUserMessage = types.SkillCollapseTargetUserMessage
	SkillCollapseTargetToolPair    = types.SkillCollapseTargetToolPair
)

type (
	SkillEvent                 = types.SkillEvent
	SkillEventSkill            = types.SkillEventSkill
	SkillEventSource           = types.SkillEventSource
	SkillEventTranscriptAnchor = types.SkillEventTranscriptAnchor
	SkillEventCollapse         = types.SkillEventCollapse
)

// SkillEventExtractor is implemented by agents that can derive native skill events
// from their transcript format.
type SkillEventExtractor interface {
	ExtractSkillEvents(transcriptData []byte, fromOffset int) ([]SkillEvent, error)
}
