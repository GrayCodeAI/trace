package agent

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var skillSlashCommandPattern = regexp.MustCompile(`^/([A-Za-z0-9][A-Za-z0-9._:/-]*)`)

var filesystemRoots = map[string]struct{}{
	"users": {}, "home": {}, "tmp": {}, "usr": {}, "var": {}, "etc": {},
	"opt": {}, "mnt": {}, "private": {}, "volumes": {}, "library": {},
	"applications": {}, "system": {}, "bin": {}, "sbin": {}, "dev": {},
	"proc": {}, "sys": {}, "root": {}, "srv": {}, "run": {}, "boot": {},
	"lib": {}, "media": {}, "network": {}, "cores": {},
}

func SkillEventFromPromptSlashCommand(agentName, prompt string, timestamp time.Time) (SkillEvent, bool) {
	trimmed := strings.TrimLeft(prompt, " \t\r\n")
	match := skillSlashCommandPattern.FindStringSubmatch(trimmed)
	if match == nil {
		return SkillEvent{}, false
	}

	token := strings.Trim(match[1], "/")
	if token == "" || isFilesystemPath(match[1]) {
		return SkillEvent{}, false
	}

	name := token
	if rest, ok := strings.CutPrefix(token, "skill:"); ok {
		if rest == "" {
			return SkillEvent{}, false
		}
		name = rest
	}

	command := "/" + token
	event := SkillEvent{
		ID:        promptSkillEventID(agentName, name, timestamp),
		EventType: SkillEventTypePromptInvocation,
		Skill: SkillEventSkill{
			Name: name,
		},
		Source: SkillEventSource{
			Agent:      agentName,
			Signal:     SkillSignalPromptSlashCommand,
			Confidence: SkillConfidenceExplicit,
		},
		Native: map[string]string{
			"command": command,
		},
		Collapse: SkillEventCollapse{
			Target:           SkillCollapseTargetUserMessage,
			Label:            command,
			DefaultCollapsed: true,
		},
	}
	if !timestamp.IsZero() {
		event.Timestamp = timestamp.UTC().Format(time.RFC3339Nano)
	}
	return event, true
}

func isFilesystemPath(raw string) bool {
	first, rest, found := strings.Cut(raw, "/")
	if !found || rest == "" {
		return false
	}
	_, ok := filesystemRoots[strings.ToLower(first)]
	return ok
}

func AppendPromptSlashCommandSkillEvent(events []SkillEvent, agentName, prompt string, timestamp time.Time) []SkillEvent {
	event, ok := SkillEventFromPromptSlashCommand(agentName, prompt, timestamp)
	if !ok {
		return events
	}
	if hasEquivalentPromptSkillEvent(events, event) {
		return events
	}
	return append(events, event)
}

func promptSkillEventID(agentName, skillName string, timestamp time.Time) string {
	if timestamp.IsZero() {
		return ""
	}
	return fmt.Sprintf("prompt-skill-%s-%s-%s", agentName, skillName, timestamp.UTC().Format(time.RFC3339Nano))
}

func hasEquivalentPromptSkillEvent(events []SkillEvent, candidate SkillEvent) bool {
	candidateCommand := ""
	if candidate.Native != nil {
		candidateCommand = candidate.Native["command"]
	}
	for _, existing := range events {
		if existing.EventType != SkillEventTypePromptInvocation || existing.Skill.Name != candidate.Skill.Name {
			continue
		}
		if existing.ID != "" && candidate.ID != "" && existing.ID == candidate.ID {
			return true
		}
		if candidateCommand != "" && existing.Native != nil && existing.Native["command"] == candidateCommand {
			return true
		}
		if existing.Source.Signal == SkillSignalPiInputSlashCommand || existing.Source.Signal == SkillSignalPromptSlashCommand {
			return true
		}
	}
	return false
}
