package figma

import (
	"context"
	"sync"
	"testing"

	mcp "github.com/daltoniam/switchboard"
	"github.com/daltoniam/switchboard/remotemcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRemote struct {
	configured mcp.Credentials
	executed   mcp.ToolName
	args       map[string]any
	closed     int
}

func (f *fakeRemote) Name() string { return "figma" }

func (f *fakeRemote) Configure(_ context.Context, creds mcp.Credentials) error {
	f.configured = creds
	return nil
}

func (f *fakeRemote) Healthy(context.Context) bool { return true }

func (f *fakeRemote) Close() error {
	f.closed++
	return nil
}

func (f *fakeRemote) Tools() []mcp.ToolDefinition {
	return []mcp.ToolDefinition{
		{Name: "figma_get_figjam", Description: "upstream read", Parameters: map[string]string{"fileKey": "file key"}, Required: []string{"fileKey"}},
		{Name: "figma_use_figma", Description: "upstream write", Parameters: map[string]string{"fileKey": "file key", "skillNames": "skills"}, Required: []string{"fileKey"}},
		{Name: "figma_generate_diagram", Description: "upstream diagram"},
		{Name: "figma_create_new_file", Description: "upstream create"},
		{Name: "figma_upload_assets", Description: "upstream upload"},
		{Name: "figma_get_screenshot", Description: "upstream screenshot"},
		{Name: "figma_whoami", Description: "upstream account"},
		{Name: "figma_get_design_context", Description: "upstream design context"},
		{Name: "figma_search_design_system", Description: "upstream search"},
	}
}

func (f *fakeRemote) Execute(_ context.Context, name mcp.ToolName, args map[string]any) (*mcp.ToolResult, error) {
	f.executed = name
	f.args = args
	return mcp.RawResult([]byte("ok"))
}

type fakeConfig struct {
	mu   sync.Mutex
	ic   *mcp.IntegrationConfig
	sets int
}

func (c *fakeConfig) Load() error                                          { return nil }
func (c *fakeConfig) Save() error                                          { return nil }
func (c *fakeConfig) Get() *mcp.Config                                     { return nil }
func (c *fakeConfig) Update(*mcp.Config) error                             { return nil }
func (c *fakeConfig) SetWasmModules([]mcp.WasmModuleConfig) error          { return nil }
func (c *fakeConfig) EnabledIntegrations() []string                        { return nil }
func (c *fakeConfig) DefaultCredentialKeys(string) []string                { return nil }
func (c *fakeConfig) GetIntegration(string) (*mcp.IntegrationConfig, bool) { return c.ic, c.ic != nil }
func (c *fakeConfig) SetIntegration(_ string, ic *mcp.IntegrationConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ic = ic
	c.sets++
	return nil
}

func newTestFigma(remote mcp.Integration) *figma {
	return &figma{baseURL: defaultBaseURL, newRemote: func(string) mcp.Integration { return remote }}
}

func TestNew(t *testing.T) {
	integration := New()
	require.NotNil(t, integration)
	assert.Equal(t, "figma", integration.Name())
	assert.Equal(t, defaultBaseURL, MCPServerURL(integration))
	assert.Empty(t, MCPServerURL(&fakeRemote{}))
}

func TestConfigure(t *testing.T) {
	remote := &fakeRemote{}
	integration := newTestFigma(remote)

	err := integration.Configure(context.Background(), mcp.Credentials{
		"mcp_access_token": " token ", "mcp_refresh_token": "refresh", "mcp_client_id": "client",
	})
	require.NoError(t, err)
	assert.Equal(t, mcp.Credentials{"access_token": "token", "refresh_token": "refresh", "client_id": "client"}, remote.configured)

	err = integration.Configure(context.Background(), mcp.Credentials{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mcp_access_token is required")
}

func TestConfigure_BaseURLChangeReplacesRemote(t *testing.T) {
	first := &fakeRemote{}
	second := &fakeRemote{}
	remotes := []mcp.Integration{first, second}
	integration := &figma{baseURL: defaultBaseURL, newRemote: func(string) mcp.Integration {
		next := remotes[0]
		remotes = remotes[1:]
		return next
	}}
	require.NoError(t, integration.Configure(context.Background(), mcp.Credentials{"mcp_access_token": "token"}))
	require.NoError(t, integration.Configure(context.Background(), mcp.Credentials{"mcp_access_token": "token", "base_url": "https://proxy.example.com/"}))
	assert.Equal(t, "https://proxy.example.com", MCPServerURL(integration))
	assert.Equal(t, 1, first.closed)
	assert.Equal(t, "token", second.configured["access_token"])
}

func TestCredentialMetadata(t *testing.T) {
	integration := New()
	plainText := integration.(mcp.PlainTextCredentials)
	optional := integration.(mcp.OptionalCredentials)
	assert.Equal(t, []string{"base_url", "mcp_client_id", mcp.CredKeyTokenSource}, plainText.PlainTextKeys())
	assert.Equal(t, []string{"base_url", "mcp_refresh_token", "mcp_client_id", mcp.CredKeyTokenSource}, optional.OptionalKeys())
}

func TestToolsPassThroughWithCuratedDescriptions(t *testing.T) {
	remote := &fakeRemote{}
	integration := newTestFigma(remote)
	require.NoError(t, integration.Configure(context.Background(), mcp.Credentials{"mcp_access_token": "token"}))

	tools := integration.Tools()
	names := make(map[mcp.ToolName]mcp.ToolDefinition, len(tools))
	for _, tool := range tools {
		names[tool.Name] = tool
	}

	assert.Len(t, tools, len(remote.Tools()), "every hosted tool is exposed")
	assert.Contains(t, names, mcp.ToolName("figma_get_design_context"))
	assert.Contains(t, names["figma_get_design_context"].Description, "Start here")
	assert.Contains(t, names["figma_get_figjam"].Description, "Start here")
	assert.Contains(t, names["figma_use_figma"].Description, "FigJam")
	assert.Contains(t, names["figma_use_figma"].Description, "design")
	assert.NotContains(t, names["figma_create_new_file"].Description, "blank FigJam whiteboard")
	assert.Equal(t, "upstream search", names["figma_search_design_system"].Description, "unknown hosted tools keep Figma's own description")
	assert.Equal(t, []string{"fileKey"}, names["figma_get_figjam"].Required)
	upstream := remote.Tools()[0]
	assert.Equal(t, "upstream read", upstream.Description, "curation must not mutate the remote's cached definitions")
}

func TestExecute(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tool      mcp.ToolName
		args      map[string]any
		wantError string
		wantArgs  map[string]any
	}{
		{name: "design tool forwards", tool: "figma_get_design_context", args: map[string]any{"fileKey": "abc", "nodeId": "1:2"}, wantArgs: map[string]any{"fileKey": "abc", "nodeId": "1:2"}},
		{name: "use_figma forwards without injecting a skill", tool: "figma_use_figma", args: map[string]any{"fileKey": "abc"}, wantArgs: map[string]any{"fileKey": "abc"}},
		{name: "explicit skills preserved", tool: "figma_use_figma", args: map[string]any{"skillNames": "resource:figma-use"}, wantArgs: map[string]any{"skillNames": "resource:figma-use"}},
		{name: "tool the server does not offer", tool: "figma_export_video", wantError: "unknown tool"},
		{name: "foreign prefix", tool: "metabase_search", wantError: "unknown tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := &fakeRemote{}
			integration := newTestFigma(remote)
			require.NoError(t, integration.Configure(context.Background(), mcp.Credentials{"mcp_access_token": "token"}))

			result, err := integration.Execute(context.Background(), tc.tool, tc.args)
			require.NoError(t, err)
			if tc.wantError != "" {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Data, tc.wantError)
				assert.Empty(t, remote.executed)
				return
			}
			require.False(t, result.IsError)
			assert.Equal(t, tc.tool, remote.executed)
			assert.Equal(t, tc.wantArgs, remote.args)
		})
	}
}

func TestExecuteRequiresConfiguration(t *testing.T) {
	integration := New()

	result, err := integration.Execute(context.Background(), "figma_get_figjam", nil)
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Data, mcp.ErrNotConfigured.Error())
	assert.False(t, integration.Healthy(context.Background()))
	assert.Nil(t, integration.Tools())
}

func TestPersistTokens(t *testing.T) {
	integration := New().(*figma)
	integration.persistTokens(remotemcp.TokenSet{AccessToken: "a"})

	cfg := &fakeConfig{ic: &mcp.IntegrationConfig{Enabled: true, Credentials: mcp.Credentials{"base_url": "", "mcp_access_token": "old", mcp.CredKeyTokenSource: "oauth"}}}
	SetConfigService(integration, cfg)
	SetConfigService(&fakeRemote{}, cfg)
	integration.persistTokens(remotemcp.TokenSet{AccessToken: "new", RefreshToken: "rotated", ClientID: "client"})

	assert.Equal(t, 1, cfg.sets)
	assert.Equal(t, mcp.Credentials{"base_url": "", "mcp_access_token": "new", "mcp_refresh_token": "rotated", "mcp_client_id": "client", mcp.CredKeyTokenSource: "oauth"}, cfg.ic.Credentials)
	assert.True(t, cfg.ic.Enabled)
}

func TestDefaultRemoteIsRemoteMCP(t *testing.T) {
	integration := New()
	require.NoError(t, integration.Configure(context.Background(), mcp.Credentials{"mcp_access_token": "token", "base_url": "https://mcp.example.com"}))
	assert.Equal(t, "https://mcp.example.com", remotemcp.ServerURL(integration.(*figma).remote))
}
