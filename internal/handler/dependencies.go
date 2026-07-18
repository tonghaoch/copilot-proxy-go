package handler

import (
	"context"
	"io"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// ModelStore is the model-catalog behavior required by HTTP handlers.
type ModelStore interface {
	GetModels() []state.Model
	SetModels([]state.Model)
	FindModel(string) *state.Model
}

// ApplicationState is the runtime state required by non-model endpoints.
type ApplicationState interface {
	ModelStore
	GetGithubToken() string
	GetCopilotToken() string
	GetAccountType() string
	GetVSCodeVersion() string
}

// MetricsStore captures request and session metrics.
type MetricsStore interface {
	RecordRequest(state.RequestRecord)
	UpdateSession(state.SessionSnapshot)
	Snapshot() state.MetricsSnapshot
}

// CopilotClient describes upstream operations used by handlers.
type CopilotClient interface {
	FetchModels(context.Context) ([]state.Model, error)
	ProxyChatCompletionEx(context.Context, []byte, bool, bool) (*http.Response, error)
	ProxyMessages(context.Context, []byte, string, bool, bool) (*http.Response, error)
	ProxyResponses(context.Context, []byte, bool, bool) (*http.Response, error)
	ProxyEmbeddings(context.Context, []byte) (*http.Response, error)
}

// HTTPDoer is used for GitHub endpoints that are not part of CopilotClient.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// StreamDecoder owns SSE framing independently of protocol translation.
type StreamDecoder interface {
	Read(io.Reader, func(eventType, data string) error) error
}

// RuntimeConfig supplies immutable snapshots of request-time configuration.
type RuntimeConfig interface {
	Snapshot() *config.Config
	APIKeys() []string
	ExtraPrompt(string) string
	ReasoningEffort(string) string
}

type defaultRuntimeConfig struct{}

func (defaultRuntimeConfig) Snapshot() *config.Config        { return config.Get() }
func (defaultRuntimeConfig) APIKeys() []string               { return config.GetAPIKeys() }
func (defaultRuntimeConfig) ExtraPrompt(model string) string { return config.GetExtraPrompt(model) }
func (defaultRuntimeConfig) ReasoningEffort(model string) string {
	return config.GetReasoningEffort(model)
}

type defaultStreamDecoder struct{}

func (defaultStreamDecoder) Read(body io.Reader, consume func(string, string) error) error {
	return readSSE(body, consume)
}

type defaultHTTPDoer struct{}

func (defaultHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	return api.HTTPClient().Do(req)
}

// Dependencies contains the process services consumed by Handler.
type Dependencies struct {
	State     ApplicationState
	Metrics   MetricsStore
	Copilot   CopilotClient
	HTTP      HTTPDoer
	Anthropic AnthropicAdapter
	Streams   StreamDecoder
	Config    RuntimeConfig
}

// Handler is the HTTP adapter for all public proxy endpoints.
type Handler struct {
	state     ApplicationState
	metrics   MetricsStore
	copilot   CopilotClient
	http      HTTPDoer
	anthropic AnthropicAdapter
	streams   StreamDecoder
	config    RuntimeConfig
}

func New(deps Dependencies) *Handler {
	if deps.State == nil {
		deps.State = state.Global
	}
	if deps.Metrics == nil {
		deps.Metrics = state.Metrics
	}
	if deps.Copilot == nil {
		deps.Copilot = service.DefaultClient()
	}
	if deps.HTTP == nil {
		deps.HTTP = defaultHTTPDoer{}
	}
	if deps.Streams == nil {
		deps.Streams = defaultStreamDecoder{}
	}
	if deps.Config == nil {
		deps.Config = defaultRuntimeConfig{}
	}
	if deps.Anthropic == nil {
		deps.Anthropic = defaultAnthropicAdapter{models: deps.State, config: deps.Config}
	}
	return &Handler{
		state: deps.State, metrics: deps.Metrics, copilot: deps.Copilot,
		http: deps.HTTP, anthropic: deps.Anthropic, streams: deps.Streams, config: deps.Config,
	}
}

var defaultHandler = New(Dependencies{})
