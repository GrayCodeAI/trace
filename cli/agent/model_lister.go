package agent

import "context"

type ModelInfo struct {
	ID   string
	Note string
}

type ModelLister interface {
	Agent

	ListModels(ctx context.Context) ([]ModelInfo, error)
}

func AsModelLister(ag Agent) (ModelLister, bool) {
	if ag == nil {
		return nil, false
	}
	ml, ok := ag.(ModelLister)
	return ml, ok
}
