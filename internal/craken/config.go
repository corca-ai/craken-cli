package craken

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type config struct {
	Profiles map[string]profile `json:"profiles"`
}

type profile struct {
	AgentID     string `json:"agentId,omitempty"`
	AgentName   string `json:"agentName,omitempty"`
	BaseURL     string `json:"baseUrl,omitempty"`
	ClientKind  string `json:"clientKind,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Token       string `json:"token,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
}

func profileName(cmd command) string {
	if value := cmd.string("profile", ""); value != "" {
		return value
	}
	if value := os.Getenv("CRAKEN_PROFILE"); trim(value) != "" {
		return trim(value)
	}
	return defaultProfile
}

func readConfig() (config, error) {
	path, err := configPath()
	if err != nil {
		return config{}, err
	}
	bytes, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config{Profiles: map[string]profile{}}, nil
	}
	if err != nil {
		return config{}, err
	}
	var cfg config
	if err := json.Unmarshal(bytes, &cfg); err != nil {
		return config{}, err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]profile{}
	}
	return cfg, nil
}

func writeConfig(cfg config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]profile{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	bytes, err := json.MarshalIndent(cfg, "", "\t")
	if err != nil {
		return err
	}
	bytes = append(bytes, '\n')
	return os.WriteFile(path, bytes, 0o600)
}

func configPath() (string, error) {
	if dir := os.Getenv("CRAKEN_CONFIG_DIR"); trim(dir) != "" {
		return filepath.Join(dir, "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "craken", "config.json"), nil
}

func selectedBearerToken(cmd command, prof profile) string {
	if value := cmd.string("bearer-token", ""); value != "" {
		return value
	}
	if cmd.Resource == "workspace" && cmd.Action == "accept" {
		if value := os.Getenv("CRAKEN_TOKEN"); trim(value) != "" {
			return trim(value)
		}
		return prof.Token
	}
	if value := cmd.string("token", ""); value != "" {
		return value
	}
	if value := os.Getenv("CRAKEN_TOKEN"); trim(value) != "" {
		return trim(value)
	}
	return prof.Token
}

func requiredTokenOption(cmd command, stdin io.Reader) (string, error) {
	value := cmd.string("token", "")
	if value == "-" {
		bytes, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		return trim(string(bytes)), nil
	}
	if value == "" {
		return "", fmt.Errorf("expected --token")
	}
	return value, nil
}

func saveTokenProfile(client *client, name string, parsed any) error {
	if name == "" {
		return nil
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return fmt.Errorf("--save-token-profile requires a JSON response with a token string")
	}
	token, ok := object["token"].(string)
	if !ok || trim(token) == "" {
		return fmt.Errorf("--save-token-profile requires a JSON response with a token string")
	}
	cfg, err := readConfig()
	if err != nil {
		return err
	}
	existing := cfg.Profiles[name]
	existing.BaseURL = client.baseURL
	existing.Token = token
	cfg.Profiles[name] = existing
	return writeConfig(cfg)
}
