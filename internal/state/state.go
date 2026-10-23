package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// ModelLimits defines token limits for a model.
type ModelLimits struct {
	MaxContextWindowTokens int         `json:"max_context_window_tokens"`
	MaxOutputTokens        int         `json:"max_output_tokens"`
	MaxNonStreamingOutput  int         `json:"max_non_streaming_output_tokens"`
	MaxPromptTokens        int         `json:"max_prompt_tokens"`
	MaxInputs              int         `json:"max_inputs"`
	Vision                 VisionLimit `json:"vision"`
}

// VisionLimit describes image input limits. Zero values mean the model has none.
type VisionLimit struct {
	MaxPromptImageSize  int      `json:"max_prompt_image_size"`
	MaxPromptImages     int      `json:"max_prompt_images"`
	SupportedMediaTypes []string `json:"supported_media_types"`
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
	OwnedBy            string            `json:"owned_by"`
	Vendor             string            `json:"vendor"`
	ModelPickerEnabled bool              `json:"model_picker_enabled"`
	Preview            bool              `json:"preview"`
	Policy             ModelPolicy       `json:"policy"`
	Billing            ModelBilling      `json:"billing"`
	Capabilities       ModelCapabilities `json:"capabilities"`
	SupportedEndpoints []string          `json:"supported_endpoints"`
}

// ModelPolicy is Copilot's per-model admin policy. State is "enabled" when the
// org allows the model; other values (e.g. "disabled") mean it is restricted.
type ModelPolicy struct {
	State string `json:"state"`
}

// ModelBilling holds Copilot's per-model pricing. TokenPrices has a "default"
// tier and, for models with an extended window, a "long_context" tier; the
// official clients treat the tier as a prompt budget the user opts into.
type ModelBilling struct {
	AutoDiscount float64              `json:"auto_discount,omitempty"`
	TokenPrices  map[string]PriceTier `json:"-"`
}

// PriceTier is one context tier of a model's token pricing.
type PriceTier struct {
	MaxPromptTokens int     `json:"max_prompt_tokens"`
	InputPrice      float64 `json:"input_price"`
	OutputPrice     float64 `json:"output_price"`
	CacheReadPrice  float64 `json:"cache_read_price"`
	CacheWritePrice float64 `json:"cache_write_price"`
}

// UnmarshalJSON reads token_prices, skipping its non-tier keys (batch_size).
func (b *ModelBilling) UnmarshalJSON(data []byte) error {
	var raw struct {
		AutoDiscount float64                    `json:"auto_discount"`
		TokenPrices  map[string]json.RawMessage `json:"token_prices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	b.AutoDiscount = raw.AutoDiscount
	b.TokenPrices = nil
	for name, msg := range raw.TokenPrices {
		var tier PriceTier
		if json.Unmarshal(msg, &tier) != nil || tier.MaxPromptTokens == 0 {
			continue
		}
		if b.TokenPrices == nil {
			b.TokenPrices = map[string]PriceTier{}
		}
		b.TokenPrices[name] = tier
	}
	return nil
}

// DefaultPromptTokens is the prompt budget of the default (cheaper) tier when
// the model also has a long_context tier, or 0 when there is only one tier.
func (m Model) DefaultPromptTokens() int {
	if _, ok := m.Billing.TokenPrices["long_context"]; !ok {
		return 0
	}
	return m.Billing.TokenPrices["default"].MaxPromptTokens
}

// ModelsResponse is the response from the Copilot models API.
type ModelsResponse struct {
	Data []Model `json:"data"`
}

// State holds the global application state.
type State struct {
	mu sync.RWMutex

	githubToken   string
	copilotToken  string
	accountType   string
	models        []Model
	vsCodeVersion string
	verbose       bool
	showToken     bool
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
	return cloneModels(s.models)
}

func (s *State) SetModels(m []Model) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models = cloneModels(m)
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
			model := cloneModel(s.models[i])
			return &model
		}
	}
	return nil
}

func cloneModels(models []Model) []Model {
	cloned := make([]Model, len(models))
	for i := range models {
		cloned[i] = cloneModel(models[i])
	}
	return cloned
}

func cloneModel(model Model) Model {
	model.SupportedEndpoints = append([]string(nil), model.SupportedEndpoints...)
	return model
}

// --- Paths ---

const appName = "copilot-proxy-go"

func AppDir() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", appName)
	case "windows":
		if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
			return filepath.Join(appData, appName)
		}
		return filepath.Join(home, "AppData", "Local", appName)
	default: // linux and others
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, appName)
		}
		return filepath.Join(home, ".local", "share", appName)
	}
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
