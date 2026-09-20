package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

const agentBundleVersion = 1
const maxAgentBundleBytes = 4 << 20

type agentBundlePayload struct {
	Version   int            `json:"version"`
	Repo      string         `json:"repo"`
	NodeID    string         `json:"node_id"`
	CreatedAt time.Time      `json:"created_at"`
	Sessions  []agentSession `json:"sessions"`
}

type signedAgentBundle struct {
	Payload   agentBundlePayload `json:"payload"`
	PublicKey string             `json:"public_key"`
	Signature string             `json:"signature"`
}

type agentBundleImportResult struct {
	SessionsCreated  int `json:"sessions_created"`
	CheckpointsAdded int `json:"checkpoints_added"`
}

func (s *store) exportAgentBundle(repo string) (signedAgentBundle, error) {
	if !validRepoName(repo) {
		return signedAgentBundle{}, errors.New("invalid repository")
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return signedAgentBundle{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return signedAgentBundle{}, errors.New("repository not found")
	}
	sessions, err := s.listAgentSessions(repo)
	if err != nil {
		return signedAgentBundle{}, err
	}
	native := make([]agentSession, 0, len(sessions))
	for _, session := range sessions {
		if session.OriginNodeID != "" {
			continue
		}
		copySession := session
		copySession.Summary = redactAgentText(session.Summary)
		copySession.Redacted = true
		copySession.Checkpoints = append([]agentCheckpoint(nil), session.Checkpoints...)
		for i := range copySession.Checkpoints {
			copySession.Checkpoints[i].Summary = redactAgentText(copySession.Checkpoints[i].Summary)
			copySession.Checkpoints[i].State = redactAgentText(copySession.Checkpoints[i].State)
			copySession.Checkpoints[i].Redacted = true
		}
		native = append(native, copySession)
	}
	if len(native) > 5000 {
		return signedAgentBundle{}, errors.New("agent bundle exceeds 5000 sessions")
	}
	sort.Slice(native, func(i, j int) bool { return native[i].ID < native[j].ID })
	private, err := loadOrCreateNodeSigner(s.root)
	if err != nil {
		return signedAgentBundle{}, err
	}
	public := base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey))
	payload := agentBundlePayload{Version: agentBundleVersion, Repo: repo, NodeID: public, CreatedAt: time.Now().UTC().Truncate(time.Second), Sessions: native}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return signedAgentBundle{}, err
	}
	if len(encoded) > maxAgentBundleBytes {
		return signedAgentBundle{}, errors.New("agent bundle exceeds 4 MiB")
	}
	return signedAgentBundle{Payload: payload, PublicKey: public, Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, encoded))}, nil
}

func verifyAgentBundle(bundle signedAgentBundle, expectedNodeID, repo string) error {
	if !validFederationNodeID(expectedNodeID) || !validRepoName(repo) {
		return errors.New("expected source node ID and repository are required")
	}
	publicBytes, err := base64.RawURLEncoding.DecodeString(bundle.PublicKey)
	if err != nil || len(publicBytes) != ed25519.PublicKeySize {
		return errors.New("invalid bundle public key")
	}
	if bundle.PublicKey != expectedNodeID || bundle.Payload.NodeID != expectedNodeID {
		return errors.New("agent bundle source identity does not match expected node ID")
	}
	signature, err := base64.RawURLEncoding.DecodeString(bundle.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid bundle signature")
	}
	if bundle.Payload.Version != agentBundleVersion || bundle.Payload.Repo != repo || bundle.Payload.CreatedAt.IsZero() {
		return errors.New("unsupported or mismatched agent bundle")
	}
	encoded, err := json.Marshal(bundle.Payload)
	if err != nil || len(encoded) > maxAgentBundleBytes || !ed25519.Verify(ed25519.PublicKey(publicBytes), encoded, signature) {
		return errors.New("agent bundle signature verification failed")
	}
	return nil
}

func (s *store) importAgentBundle(repo, expectedNodeID string, bundle signedAgentBundle) (agentBundleImportResult, error) {
	if err := verifyAgentBundle(bundle, expectedNodeID, repo); err != nil {
		return agentBundleImportResult{}, err
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return agentBundleImportResult{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return agentBundleImportResult{}, errors.New("repository not found")
	}
	if len(bundle.Payload.Sessions) > 5000 {
		return agentBundleImportResult{}, errors.New("agent bundle exceeds 5000 sessions")
	}
	seen := make(map[int]struct{}, len(bundle.Payload.Sessions))
	for _, session := range bundle.Payload.Sessions {
		if session.ID < 1 || session.Repo != repo || session.OriginNodeID != "" || session.OriginSessionID != 0 || session.Agent == "" || len(session.Agent) > 100 || session.CreatedBy == "" || len(session.CreatedBy) > 100 || session.Status != "active" || session.CreatedAt.IsZero() || session.UpdatedAt.Before(session.CreatedAt) || !session.Redacted || len(session.Summary) > 20000 || session.Summary != redactAgentText(session.Summary) || len(session.Checkpoints) > 1000 || (session.CaptureSessionKey != "" && (!agentCaptureID.MatchString(session.CaptureSessionKey) || !agentCaptureID.MatchString(session.CaptureFirstEventID))) || (session.CaptureSessionKey == "" && session.CaptureFirstEventID != "") {
			return agentBundleImportResult{}, errors.New("invalid agent session in bundle")
		}
		if _, exists := seen[session.ID]; exists {
			return agentBundleImportResult{}, errors.New("duplicate agent session ID in bundle")
		}
		seen[session.ID] = struct{}{}
		if _, err := resolveAgentCommit(path, session.Commit); err != nil {
			return agentBundleImportResult{}, fmt.Errorf("agent session %d commit missing from repository", session.ID)
		}
		lastCheckpointID := 0
		for _, checkpoint := range session.Checkpoints {
			if checkpoint.ID != lastCheckpointID+1 || checkpoint.CreatedAt.IsZero() || !checkpoint.Redacted || len(checkpoint.Summary) > 20000 || checkpoint.Summary != redactAgentText(checkpoint.Summary) || len(checkpoint.State) > 20000 || checkpoint.State != redactAgentText(checkpoint.State) || (checkpoint.CaptureEventID != "" && (!agentCaptureID.MatchString(checkpoint.CaptureEventID) || session.CaptureSessionKey == "")) {
				return agentBundleImportResult{}, errors.New("invalid checkpoint in bundle")
			}
			if _, err := resolveAgentCommit(path, checkpoint.Commit); err != nil {
				return agentBundleImportResult{}, fmt.Errorf("checkpoint %d commit missing from repository", checkpoint.ID)
			}
			lastCheckpointID = checkpoint.ID
		}
	}
	result := agentBundleImportResult{}
	err = s.updateAgentSessions(func(db *agentSessionDB) error {
		for _, source := range bundle.Payload.Sessions {
			var found *agentSession
			for i := range db.Sessions {
				candidate := &db.Sessions[i]
				if candidate.OriginNodeID == expectedNodeID && candidate.OriginSessionID == source.ID && candidate.Repo == repo {
					if found != nil {
						return errors.New("duplicate imported agent session in local store")
					}
					found = candidate
				}
			}
			if found == nil {
				imported := source
				imported.ID = db.NextID
				imported.OriginNodeID = expectedNodeID
				imported.OriginSessionID = source.ID
				imported.Checkpoints = append([]agentCheckpoint(nil), source.Checkpoints...)
				db.NextID++
				db.Sessions = append(db.Sessions, imported)
				result.SessionsCreated++
				result.CheckpointsAdded += len(imported.Checkpoints)
				continue
			}
			if found.Agent != source.Agent || found.Commit != source.Commit || found.Status != source.Status || found.Summary != source.Summary || found.CreatedBy != source.CreatedBy || !found.CreatedAt.Equal(source.CreatedAt) || found.CaptureSessionKey != source.CaptureSessionKey || found.CaptureFirstEventID != source.CaptureFirstEventID {
				return fmt.Errorf("imported agent session %d changed its signed origin fields", source.ID)
			}
			for _, checkpoint := range source.Checkpoints {
				if checkpoint.ID <= len(found.Checkpoints) {
					existing := found.Checkpoints[checkpoint.ID-1]
					if existing.ID != checkpoint.ID || existing.Commit != checkpoint.Commit || existing.Summary != checkpoint.Summary || existing.State != checkpoint.State || existing.CaptureEventID != checkpoint.CaptureEventID || !existing.CreatedAt.Equal(checkpoint.CreatedAt) {
						return fmt.Errorf("imported checkpoint %d changed its signed origin fields", checkpoint.ID)
					}
					continue
				}
				if checkpoint.ID != len(found.Checkpoints)+1 {
					return errors.New("checkpoint IDs must be contiguous for incremental import")
				}
				found.Checkpoints = append(found.Checkpoints, checkpoint)
				result.CheckpointsAdded++
			}
			if source.UpdatedAt.After(found.UpdatedAt) {
				found.UpdatedAt = source.UpdatedAt
			}
		}
		return nil
	})
	if err != nil {
		return agentBundleImportResult{}, err
	}
	return result, nil
}
