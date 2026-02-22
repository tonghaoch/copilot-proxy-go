package state

import (
	"os"
	"path/filepath"
	"sync"
)

// ModelLimits defines token limits for a model.
type ModelLimits struct {
	MaxContextWindowTokens int `json:"max_context_window_tokens"`
	MaxOutputTokens        int `json:"max_output_tokens"`
	MaxPromptTokens        int `json:"max_prompt_tokens"`
	MaxInputs              int `json:"max_inputs"`
}

// ModelSupports defines what features a model supports.
type ModelSupports struct {
	MaxThinkingBudget int  `json:"max_thinking_budget"`
	MinThinkingBudget int  `json:"min_thinking_budget"`
	ToolCalls         bool `json:"tool_calls"`
	ParallelToolCalls bool `json:"parallel_tool_calls"`
	Streaming         bool `json:"streaming"`
	StructuredOutputs bool `json:"structured_outputs"`
	Vision            bool `json:"vision"`
	AdaptiveThinking  bool `json:"adaptive_thinking"`
}

// ModelCapabilities describes a model's capabilities.
type ModelCapabilities struct {
	Family    string        `json:"family"`
	Type      string        `json:"type"`
	Tokenizer string        `json:"tokenizer"`
	Limits    ModelLimits   `json:"limits"`
	Supports  ModelSupports `json:"supports"`
}

// Model represents a Copilot model.
type Model struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Version            string            `json:"version"`
	Object             string            `json:"object"`
	Type               string            `json:"type"`
	Created            int               `json:"created"`
	OwnedBy           string            `json:"owned_by"`
	ModelPickerEnabled bool              `json:"model_picker_enabled"`
	Preview            bool              `json:"preview"`
	Capabilities       ModelCapabilities `json:"capabilities"`
	SupportedEndpoints []string          `json:"supported_endpoints"`
}

// ModelsResponse is the response from the Copilot models API.
type ModelsResponse struct {
	Data []Model `json:"data"`
}

// State holds the global application state.
type State struct {
	mu sync.RWMutex

	githubToken  string
	copilotToken string
	accountType  string
	models       []Model
	vsCodeVersion string
	verbose      bool
	showToken    bool
}

// Global is the singleton state instance.
var Global = &State{
	accountType:   "individual",
	vsCodeVersion: "1.109.3",
}

func (s *State) GetGithubToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.githubToken
}

func (s *State) SetGithubToken(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.githubToken = t
}

func (s *State) GetCopilotToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.copilotToken
}

func (s *State) SetCopilotToken(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.copilotToken = t
}

func (s *State) GetAccountType() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accountType
}

func (s *State) SetAccountType(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountType = t
}

func (s *State) GetModels() []Model {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.models
}

func (s *State) SetModels(m []Model) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models = m
}

func (s *State) GetVSCodeVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.vsCodeVersion
}

func (s *State) SetVSCodeVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vsCodeVersion = v
}

func (s *State) GetVerbose() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.verbose
}

func (s *State) SetVerbose(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verbose = v
}

func (s *State) GetShowToken() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.showToken
}

func (s *State) SetShowToken(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.showToken = v
}

// FindModel looks up a model by ID.
func (s *State) FindModel(id string) *Model {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.models {
		if s.models[i].ID == id {
			return &s.models[i]
		}
	}
	return nil
}

// --- Paths ---

const appName = "copilot-api"

func AppDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", appName)
}

func TokenPath() string {
	return filepath.Join(AppDir(), "github_token")
}

func ConfigPath() string {
	return filepath.Join(AppDir(), "config.json")
}

func LogDir() string {
	return filepath.Join(AppDir(), "logs")
}

// EnsurePaths creates the app directory and ensures token/config files exist.
func EnsurePaths() error {
	dir := AppDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(LogDir(), 0700); err != nil {
		return err
	}
	// Touch token file if it doesn't exist
	tokenPath := TokenPath()
	if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
		if err := os.WriteFile(tokenPath, []byte(""), 0600); err != nil {
			return err
		}
	}
	return nil
}
