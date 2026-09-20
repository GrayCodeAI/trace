package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

func (a *app) apiProjects(w http.ResponseWriter, r *http.Request, u userRecord, username, repo string, tail []string) {
	if len(tail) == 0 {
		switch r.Method {
		case http.MethodGet:
			projects, err := a.store.listProjects(repo)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot list projects")
				return
			}
			writeJSON(w, http.StatusOK, projects)
		case http.MethodPost:
			if !u.canWrite(repo) {
				apiError(w, http.StatusForbidden, "write access required")
				return
			}
			var input struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "invalid project JSON")
				return
			}
			created, err := a.store.createProject(repo, input.Name, input.Description)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "project.create", repo, strconv.Itoa(created.ID), nil)
			writeJSON(w, http.StatusCreated, created)
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	projectID, err := strconv.Atoi(tail[0])
	if err != nil || projectID < 1 {
		apiError(w, http.StatusNotFound, "project not found")
		return
	}
	if len(tail) == 1 {
		projects, err := a.store.listProjects(repo)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot list projects")
			return
		}
		for _, project := range projects {
			if project.ID == projectID {
				writeJSON(w, http.StatusOK, project)
				return
			}
		}
		apiError(w, http.StatusNotFound, "project not found")
		return
	}
	if tail[1] != "cards" {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	if len(tail) == 2 {
		if r.Method != http.MethodPost || !u.canWrite(repo) {
			apiError(w, http.StatusForbidden, "write access required")
			return
		}
		var input projectCard
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid card JSON")
			return
		}
		card, err := a.store.addProjectCard(repo, projectID, input.Kind, input.Number, input.Title, input.Column)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "project.card.add", repo, strconv.Itoa(projectID), map[string]any{"card": card.ID})
		writeJSON(w, http.StatusCreated, card)
		return
	}
	if len(tail) == 3 {
		cardID, err := strconv.Atoi(tail[2])
		if err != nil || cardID < 1 {
			apiError(w, http.StatusNotFound, "card not found")
			return
		}
		if r.Method != http.MethodPatch || !u.canWrite(repo) {
			apiError(w, http.StatusForbidden, "write access required")
			return
		}
		var input struct {
			Column string `json:"column"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "column is required")
			return
		}
		card, err := a.store.moveProjectCard(repo, projectID, cardID, input.Column)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "project.card.move", repo, strconv.Itoa(projectID), map[string]any{"card": card.ID, "column": card.Column})
		writeJSON(w, http.StatusOK, card)
		return
	}
	apiError(w, http.StatusNotFound, "not found")
}
