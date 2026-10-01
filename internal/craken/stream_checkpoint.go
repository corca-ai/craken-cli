package craken

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type streamCheckpoint struct {
	Version int    `json:"version"`
	Scope   string `json:"scope"`
	Cursor  int64  `json:"cursor"`
}

func streamCheckpointPath(c *client, cmd command, path string, query map[string]any, resolved map[string]string, output *streamOutput) (string, string, error) {
	config, err := configPath()
	if err != nil {
		return "", "", err
	}
	server, err := url.Parse(c.baseURL)
	if err != nil {
		return "", "", err
	}
	server.Scheme = strings.ToLower(server.Scheme)
	server.Host = strings.ToLower(server.Host)
	server.Path = strings.TrimRight(server.Path, "/")
	server.RawQuery = ""
	server.Fragment = ""
	server.User = nil
	selection := map[string]any{}
	for key, value := range query {
		if key != output.plan.ResumeQuery {
			selection[key] = value
		}
	}
	identity := streamIdentity(c.token)
	raw, err := json.Marshal(map[string]any{"server": server.String(), "profile": profileName(cmd), "identity": identity, "path": path, "resolved": resolved, "selection": selection, "messages": output.messages, "fields": cmd.string("fields", "")})
	if err != nil {
		return "", "", err
	}
	hash := sha256.Sum256(raw)
	scope := hex.EncodeToString(hash[:])
	return filepath.Join(filepath.Dir(config), "streams", scope+".json"), scope, nil
}

func streamIdentity(token string) string {
	if claims, ok := decodeSessionClaims(token); ok {
		if agent := claims.DelegatedAgent; agent != nil {
			return "agent:" + agent.WorkspaceID + ":" + agent.AgentID
		}
		if claims.Email != "" {
			return "user:" + claims.Provider + ":" + claims.Email
		}
	}
	hash := sha256.Sum256([]byte(token))
	return "opaque:" + hex.EncodeToString(hash[:])
}

func readStreamCheckpoint(path, scope string) (int64, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	var checkpoint streamCheckpoint
	if err = json.Unmarshal(raw, &checkpoint); err != nil {
		return 0, false, fmt.Errorf("invalid stream checkpoint %s: %w", path, err)
	}
	if checkpoint.Version != 1 || checkpoint.Scope != scope || checkpoint.Cursor < 0 || checkpoint.Cursor > 9007199254740991 {
		return 0, false, fmt.Errorf("invalid stream checkpoint %s", path)
	}
	return checkpoint.Cursor, true, nil
}

func writeStreamCheckpoint(path, scope string, cursor int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(streamCheckpoint{Version: 1, Scope: scope, Cursor: cursor})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer func() { _ = os.Remove(temp) }()
	if _, err = file.Write(append(raw, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp, path)
}
