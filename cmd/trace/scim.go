package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

type scimUser struct {
	Schemas  []string `json:"schemas"`
	ID       string   `json:"id"`
	UserName string   `json:"userName"`
	Active   bool     `json:"active"`
}

type scimGroupMember struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
}

type scimGroup struct {
	Schemas     []string          `json:"schemas"`
	ID          string            `json:"id"`
	DisplayName string            `json:"displayName"`
	Members     []scimGroupMember `json:"members,omitempty"`
}

const scimGroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"

func scimGroupResource(name string, record teamRecord) scimGroup {
	members := make([]scimGroupMember, 0, len(record.Members))
	for member := range record.Members {
		members = append(members, scimGroupMember{Value: member, Display: member})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Value < members[j].Value })
	return scimGroup{Schemas: []string{scimGroupSchema}, ID: name, DisplayName: name, Members: members}
}

func (a *app) scimGroups(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/scim/v2/Groups")
	db, err := a.store.loadTeams()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "cannot load groups")
		return
	}
	if path == "" || path == "/" {
		switch r.Method {
		case http.MethodGet:
			resources := make([]scimGroup, 0, len(db.Teams))
			for name, team := range db.Teams {
				resources = append(resources, scimGroupResource(name, team))
			}
			sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
			writeJSON(w, http.StatusOK, map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"}, "totalResults": len(resources), "Resources": resources})
		case http.MethodPost:
			var input struct {
				DisplayName string            `json:"displayName"`
				Members     []scimGroupMember `json:"members"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil || !namePattern.MatchString(input.DisplayName) {
				apiError(w, http.StatusBadRequest, "displayName is required and must be a valid team name")
				return
			}
			if err := a.store.createTeam(input.DisplayName); err != nil {
				apiError(w, http.StatusConflict, err.Error())
				return
			}
			for _, member := range input.Members {
				if err := a.store.updateTeamMember(input.DisplayName, member.Value, true); err != nil {
					_ = a.store.deleteTeam(input.DisplayName)
					apiError(w, http.StatusBadRequest, err.Error())
					return
				}
			}
			updated, _ := a.store.loadTeams()
			w.Header().Set("Location", "/scim/v2/Groups/"+input.DisplayName)
			writeJSON(w, http.StatusCreated, scimGroupResource(input.DisplayName, updated.Teams[input.DisplayName]))
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	name := strings.Trim(path, "/")
	if !namePattern.MatchString(name) {
		apiError(w, http.StatusNotFound, "group not found")
		return
	}
	record, exists := db.Teams[name]
	if !exists {
		apiError(w, http.StatusNotFound, "group not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, scimGroupResource(name, record))
	case http.MethodPatch:
		var input struct {
			Operations []struct {
				Op    string            `json:"op"`
				Value []scimGroupMember `json:"value"`
			} `json:"Operations"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid SCIM group patch")
			return
		}
		for _, operation := range input.Operations {
			if !strings.EqualFold(operation.Op, "add") && !strings.EqualFold(operation.Op, "replace") {
				continue
			}
			if strings.EqualFold(operation.Op, "replace") {
				for member := range record.Members {
					if err := a.store.updateTeamMember(name, member, false); err != nil {
						apiError(w, http.StatusBadRequest, err.Error())
						return
					}
				}
			}
			for _, member := range operation.Value {
				if err := a.store.updateTeamMember(name, member.Value, true); err != nil {
					apiError(w, http.StatusBadRequest, err.Error())
					return
				}
			}
		}
		updated, _ := a.store.loadTeams()
		writeJSON(w, http.StatusOK, scimGroupResource(name, updated.Teams[name]))
	case http.MethodDelete:
		if err := a.store.deleteTeam(name); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusNoContent, map[string]string{})
	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *app) scim(w http.ResponseWriter, r *http.Request, db userDB) {
	username, u, ok := a.webIdentity(r, db)
	if !ok || !u.Admin {
		w.Header().Set("WWW-Authenticate", `Basic realm="Trace SCIM"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "administrator authentication required"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/scim/v2/Groups") {
		a.scimGroups(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/scim/v2/Users")
	if path == "" || path == "/" {
		switch r.Method {
		case http.MethodGet:
			resources := make([]scimUser, 0, len(db.Users))
			for name, record := range db.Users {
				resources = append(resources, scimUser{Schemas: []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, ID: name, UserName: name, Active: !record.Disabled})
			}
			writeJSON(w, http.StatusOK, map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"}, "totalResults": len(resources), "Resources": resources})
		case http.MethodPost:
			var input struct {
				UserName string `json:"userName"`
				Active   *bool  `json:"active"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil || !namePattern.MatchString(input.UserName) {
				apiError(w, http.StatusBadRequest, "userName is required and must be valid")
				return
			}
			token, err := a.store.addUser(input.UserName, false)
			if err != nil {
				apiError(w, http.StatusConflict, err.Error())
				return
			}
			active := input.Active == nil || *input.Active
			if input.Password != "" {
				_ = a.store.updateUsers(func(users *userDB) error {
					record := users.Users[input.UserName]
					record.Hash = hashToken(input.Password)
					record.Disabled = !active
					users.Users[input.UserName] = record
					return nil
				})
			} else {
				// Do not create an active account with no credential that can be
				// delivered by SCIM. An administrator can rotate/enroll a token.
				_ = a.store.setUserActive(input.UserName, false)
				active = false
			}
			// SCIM must not return a Trace personal token. Provisioning systems
			// should deliver a token through their own secure enrollment flow.
			_ = token
			w.Header().Set("Location", "/scim/v2/Users/"+input.UserName)
			writeJSON(w, http.StatusCreated, scimUser{Schemas: []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, ID: input.UserName, UserName: input.UserName, Active: active})
		case http.MethodDelete:
			apiError(w, http.StatusMethodNotAllowed, "user id required")
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	name := strings.Trim(path, "/")
	if !namePattern.MatchString(name) {
		apiError(w, http.StatusNotFound, "user not found")
		return
	}
	record, exists := db.Users[name]
	if !exists {
		apiError(w, http.StatusNotFound, "user not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, scimUser{Schemas: []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, ID: name, UserName: name, Active: !record.Disabled})
	case http.MethodPatch:
		var input struct {
			Operations []struct {
				Op    string `json:"op"`
				Value struct {
					Active *bool `json:"active"`
				} `json:"value"`
			} `json:"Operations"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid SCIM patch")
			return
		}
		for _, operation := range input.Operations {
			if operation.Value.Active != nil {
				if err := a.store.setUserActive(name, *operation.Value.Active); err != nil {
					apiError(w, http.StatusBadRequest, err.Error())
					return
				}
			}
		}
		updated, _ := a.store.loadUsers()
		writeJSON(w, http.StatusOK, scimUser{Schemas: []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, ID: name, UserName: name, Active: !updated.Users[name].Disabled})
	case http.MethodDelete:
		if err := a.store.removeUser(name); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusNoContent, map[string]string{})
	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
	_ = username
}
