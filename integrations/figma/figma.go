package figma

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"

	mcp "github.com/daltoniam/switchboard"
	"github.com/daltoniam/switchboard/remotemcp"
)

const defaultBaseURL = "https://mcp.figma.com"

// Curated wayfinding text for the most-used hosted tools; every other tool keeps
// the description Figma publishes.
var toolDescriptions = map[mcp.ToolName]string{
	"figma_get_design_context": "Start here for design-to-code: get reference code, a screenshot, and metadata for a Figma design node. Load the figma-design-to-code skill (skill://figma/figma-design-to-code/SKILL.md) first so output adapts to the target project's components and tokens.",
	"figma_get_figjam":         "Read a FigJam whiteboard as structured XML with node IDs, positions, sizes, and screenshots. Start here to inspect an existing FigJam board before editing it or implementing reviewed decisions.",
	"figma_use_figma":          "Create, inspect, edit, or delete content in Figma design files, FigJam boards, and Slides by running Figma Plugin API JavaScript. Load the figma-use skill first (figma-use-figjam for boards, figma-use-slides for decks); use figma_get_design_context or figma_get_figjam first when modifying existing content.",
	"figma_generate_diagram":   "Generate an editable FigJam flowchart, Gantt chart, state diagram, sequence diagram, architecture diagram, or ERD from Mermaid syntax or a natural-language description.",
	"figma_create_new_file":    "Create a blank Figma design file, FigJam board, or Slides deck (editorType) in Drafts or a project. Call figma_whoami first to pick the planKey. Use before figma_use_figma when a workflow needs a new file.",
	"figma_upload_assets":      "Upload PNG, JPG, GIF, or WebP images into a Figma design file or FigJam board, either as new layers or as fills on existing nodes.",
	"figma_get_screenshot":     "Render a PNG screenshot of a node in a Figma design file, FigJam board, or Slides deck. Use figma_get_design_context or figma_get_figjam first to discover a valid node ID.",
	"figma_whoami":             "Get the authenticated Figma user, plans, and seat types. Use to verify access and to pick the planKey before figma_create_new_file.",
}

var (
	_ mcp.Integration          = (*figma)(nil)
	_ mcp.PlainTextCredentials = (*figma)(nil)
	_ mcp.OptionalCredentials  = (*figma)(nil)
	_ io.Closer                = (*figma)(nil)
)

type figma struct {
	mu        sync.RWMutex
	baseURL   string
	remote    mcp.Integration
	newRemote func(string) mcp.Integration

	cfgMu     sync.Mutex
	configSvc mcp.ConfigService
}

func New() mcp.Integration {
	f := &figma{baseURL: defaultBaseURL}
	f.newRemote = func(baseURL string) mcp.Integration {
		return remotemcp.NewWithOptions("figma", baseURL, remotemcp.Options{OnTokenRefresh: f.persistTokens})
	}
	return f
}

func (f *figma) Name() string { return "figma" }

func (f *figma) PlainTextKeys() []string {
	return []string{"base_url", "mcp_client_id", mcp.CredKeyTokenSource}
}

func (f *figma) OptionalKeys() []string {
	return []string{"base_url", "mcp_refresh_token", "mcp_client_id", mcp.CredKeyTokenSource}
}

// SetConfigService lets the adapter persist rotated OAuth tokens.
func SetConfigService(i mcp.Integration, svc mcp.ConfigService) {
	if f, ok := i.(*figma); ok {
		f.cfgMu.Lock()
		f.configSvc = svc
		f.cfgMu.Unlock()
	}
}

func MCPServerURL(integration mcp.Integration) string {
	f, ok := integration.(*figma)
	if !ok {
		return ""
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.baseURL
}

func (f *figma) Configure(ctx context.Context, creds mcp.Credentials) error {
	token := strings.TrimSpace(creds["mcp_access_token"])
	if token == "" {
		return fmt.Errorf("figma: mcp_access_token is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(creds["base_url"]), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remote == nil || f.baseURL != baseURL {
		factory := f.newRemote
		if factory == nil {
			factory = func(serverURL string) mcp.Integration {
				return remotemcp.NewWithOptions("figma", serverURL, remotemcp.Options{OnTokenRefresh: f.persistTokens})
			}
		}
		previous := f.remote
		f.remote = factory(baseURL)
		if err := closeRemote(previous); err != nil {
			return err
		}
	}
	f.baseURL = baseURL
	return f.remote.Configure(ctx, mcp.Credentials{
		"access_token":  token,
		"refresh_token": creds["mcp_refresh_token"],
		"client_id":     creds["mcp_client_id"],
	})
}

func (f *figma) activeRemote() mcp.Integration {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.remote
}

func (f *figma) Healthy(ctx context.Context) bool {
	remote := f.activeRemote()
	return remote != nil && remote.Healthy(ctx)
}

func (f *figma) Tools() []mcp.ToolDefinition {
	remote := f.activeRemote()
	if remote == nil {
		return nil
	}
	tools := slices.Clone(remote.Tools())
	for i := range tools {
		tool := &tools[i]
		tool.Parameters = maps.Clone(tool.Parameters)
		tool.Required = slices.Clone(tool.Required)
		if description, ok := toolDescriptions[tool.Name]; ok {
			tool.Description = description
		}
	}
	return tools
}

func (f *figma) Execute(ctx context.Context, toolName mcp.ToolName, args map[string]any) (*mcp.ToolResult, error) {
	remote := f.activeRemote()
	if remote == nil {
		return mcp.ErrResult(mcp.ErrNotConfigured)
	}
	if !slices.ContainsFunc(remote.Tools(), func(tool mcp.ToolDefinition) bool { return tool.Name == toolName }) {
		return mcp.ErrResult(fmt.Errorf("unknown tool: %s", toolName))
	}
	return remote.Execute(ctx, toolName, args)
}

func (f *figma) persistTokens(set remotemcp.TokenSet) {
	f.cfgMu.Lock()
	svc := f.configSvc
	f.cfgMu.Unlock()
	if svc == nil {
		return
	}
	ic, ok := svc.GetIntegration("figma")
	if !ok || ic == nil {
		return
	}
	if ic.Credentials == nil {
		ic.Credentials = mcp.Credentials{}
	}
	ic.Credentials["mcp_access_token"] = set.AccessToken
	ic.Credentials["mcp_refresh_token"] = set.RefreshToken
	if set.ClientID != "" {
		ic.Credentials["mcp_client_id"] = set.ClientID
	}
	_ = svc.SetIntegration("figma", ic)
}

func (f *figma) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	remote := f.remote
	f.remote = nil
	return closeRemote(remote)
}

func closeRemote(remote mcp.Integration) error {
	if closer, ok := remote.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
