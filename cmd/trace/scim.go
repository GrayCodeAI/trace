package main

import (
	"encoding/json"
	"errors"
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
			want, filtered, err := parseSCIMEqualityFilter(r.URL.Query().Get("filter"), "displayName", "id")
			if err != nil {
				scimError(w, http.StatusBadRequest, "invalidFilter", err.Error())
				return
			}
			resources := make([]scimGroup, 0, len(db.Teams))
			for name, team := range db.Teams {
				if !filtered || name == want {
					resources = append(resources, scimGroupResource(name, team))
				}
			}
			sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
			start, end, startIndex, err := scimPage(r, len(resources))
			if err != nil {
				scimError(w, http.StatusBadRequest, "invalidValue", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, scimListResponse(len(resources), startIndex, resources[start:end], end-start))
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
			Operations []scimPatchOperation `json:"Operations"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
			scimError(w, http.StatusBadRequest, "invalidSyntax", "invalid SCIM group patch")
			return
		}
		for _, operation := range input.Operations {
			if err := a.applySCIMGroupOperation(name, operation); err != nil {
				scimError(w, http.StatusBadRequest, "invalidValue", err.Error())
				return
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
			want, filtered, err := parseSCIMEqualityFilter(r.URL.Query().Get("filter"), "userName", "id")
			if err != nil {
				scimError(w, http.StatusBadRequest, "invalidFilter", err.Error())
				return
			}
			resources := make([]scimUser, 0, len(db.Users))
			for name, record := range db.Users {
				if !filtered || name == want {
					resources = append(resources, scimUser{Schemas: []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, ID: name, UserName: name, Active: !record.Disabled})
				}
			}
			sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
			start, end, startIndex, err := scimPage(r, len(resources))
			if err != nil {
				scimError(w, http.StatusBadRequest, "invalidValue", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, scimListResponse(len(resources), startIndex, resources[start:end], end-start))
		case http.MethodPost:
			var input struct {
				UserName string          `json:"userName"`
				Active   *bool           `json:"active"`
				Password json.RawMessage `json:"password"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil || !namePattern.MatchString(input.UserName) {
				scimError(w, http.StatusBadRequest, "invalidValue", "userName is required and must be valid")
				return
			}
			// Trace credentials are generated personal tokens (or OIDC); a
			// user-chosen SCIM password would be stored as an unsalted token
			// hash and could be cracked offline, so it is refused.
			if len(input.Password) > 0 && string(input.Password) != "null" && string(input.Password) != `""` {
				scimError(w, http.StatusBadRequest, "invalidValue", "Trace does not accept SCIM passwords; administrators issue personal tokens (trace user rotate) or users sign in with OIDC")
				return
			}
			token, err := a.store.addUser(input.UserName, false)
			if err != nil {
				apiError(w, http.StatusConflict, err.Error())
				return
			}
			// Do not create an active account with no credential that can be
			// delivered by SCIM. An administrator can rotate/enroll a token and
			// reactivate the account.
			_ = a.store.setUserActive(input.UserName, false)
			active := false
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
			Operations []scimPatchOperation `json:"Operations"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
			scimError(w, http.StatusBadRequest, "invalidSyntax", "invalid SCIM patch")
			return
		}
		for _, operation := range input.Operations {
			if !strings.EqualFold(operation.Op, "replace") && !strings.EqualFold(operation.Op, "add") {
				continue
			}
			active, ok, err := scimActiveValue(operation)
			if err != nil {
				scimError(w, http.StatusBadRequest, "invalidValue", err.Error())
				return
			}
			if ok {
				if err := a.store.setUserActive(name, active); err != nil {
					scimError(w, http.StatusBadRequest, "invalidValue", err.Error())
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

// applySCIMGroupOperation applies one group PATCH operation to the team:
// add, replace, and remove of members, including the
// members[value eq "USER"] path form that identity providers send.
func (a *app) applySCIMGroupOperation(team string, operation scimPatchOperation) error {
	op := strings.ToLower(strings.TrimSpace(operation.Op))
	path := strings.TrimSpace(operation.Path)
	if path != "" && !strings.EqualFold(path, "members") {
		if match := scimMemberPathPattern.FindStringSubmatch(path); match != nil && op == "remove" {
			var member string
			if err := json.Unmarshal([]byte(`"`+match[1]+`"`), &member); err != nil {
				return errors.New("invalid member filter")
			}
			return a.store.updateTeamMember(team, member, false)
		}
		// Teams cannot be renamed and carry no other SCIM attributes.
		return nil
	}
	members, err := scimMembersValue(operation.Value)
	if err != nil {
		return err
	}
	current, err := a.store.loadTeams()
	if err != nil {
		return err
	}
	switch op {
	case "add":
	case "replace":
		for member := range current.Teams[team].Members {
			if err := a.store.updateTeamMember(team, member, false); err != nil {
				return err
			}
		}
	case "remove":
		if len(members) == 0 {
			// Removing the members attribute clears the group.
			for member := range current.Teams[team].Members {
				if err := a.store.updateTeamMember(team, member, false); err != nil {
					return err
				}
			}
			return nil
		}
		for _, member := range members {
			if err := a.store.updateTeamMember(team, member.Value, false); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("unsupported SCIM patch operation " + operation.Op)
	}
	for _, member := range members {
		if err := a.store.updateTeamMember(team, member.Value, true); err != nil {
			return err
		}
	}
	return nil
}
