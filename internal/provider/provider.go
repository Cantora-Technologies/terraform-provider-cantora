package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Cantora-Technologies/terraform-provider-cantora/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &cantoraProvider{}

type cantoraProvider struct {
	version    string
	httpClient *http.Client
}

type providerModel struct {
	Endpoint         types.String `tfsdk:"endpoint"`
	APIKey           types.String `tfsdk:"api_key"`
	SourceRepository types.String `tfsdk:"source_repository"`
	SourceCommit     types.String `tfsdk:"source_commit"`
	SourcePath       types.String `tfsdk:"source_path"`
	SourceWorkflow   types.String `tfsdk:"source_workflow"`
	SourceRun        types.String `tfsdk:"source_run"`
	SourceActor      types.String `tfsdk:"source_actor"`
	RequestTimeout   types.String `tfsdk:"request_timeout"`
}

type providerData struct {
	client *client.Client
	source client.Source
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &cantoraProvider{version: version, httpClient: http.DefaultClient}
	}
}

func NewWithHTTPClient(version string, httpClient *http.Client) func() provider.Provider {
	return func() provider.Provider {
		return &cantoraProvider{version: version, httpClient: httpClient}
	}
}

func (p *cantoraProvider) Metadata(
	_ context.Context,
	_ provider.MetadataRequest,
	response *provider.MetadataResponse,
) {
	response.TypeName = "cantora"
	response.Version = p.version
}

func (p *cantoraProvider) Schema(
	_ context.Context,
	_ provider.SchemaRequest,
	response *provider.SchemaResponse,
) {
	response.Schema = schema.Schema{
		MarkdownDescription: "Manage Cantora Agent Configuration through the Management API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Cantora Management API base URL. May also be set with `CANTORA_ENDPOINT`.",
				Optional:            true,
			},
			"api_key": schema.StringAttribute{
				MarkdownDescription: "Cantora ServicePrincipal API key. May also be set with `CANTORA_API_KEY`.",
				Optional:            true,
				Sensitive:           true,
			},
			"request_timeout": schema.StringAttribute{
				MarkdownDescription: "Maximum duration of one Management API request as a Go duration. May also be set with `CANTORA_REQUEST_TIMEOUT`; defaults to `30s`.",
				Optional:            true,
			},
			"source_repository": optionalSourceAttribute("Source repository retained only when a resource change applies. May also be set with `CANTORA_SOURCE_REPOSITORY`."),
			"source_commit":     optionalSourceAttribute("Source commit retained only when a resource change applies. May also be set with `CANTORA_SOURCE_COMMIT`."),
			"source_path":       optionalSourceAttribute("Source path retained only when a resource change applies. May also be set with `CANTORA_SOURCE_PATH`."),
			"source_workflow":   optionalSourceAttribute("Source workflow retained only when a resource change applies. May also be set with `CANTORA_SOURCE_WORKFLOW`."),
			"source_run":        optionalSourceAttribute("Source run retained only when a resource change applies. May also be set with `CANTORA_SOURCE_RUN`."),
			"source_actor":      optionalSourceAttribute("Initiating actor retained only when a resource change applies. May also be set with `CANTORA_SOURCE_ACTOR`."),
		},
	}
}

func optionalSourceAttribute(description string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: description, Optional: true}
}

func (p *cantoraProvider) Configure(
	ctx context.Context,
	request provider.ConfigureRequest,
	response *provider.ConfigureResponse,
) {
	var configuration providerModel
	response.Diagnostics.Append(request.Config.Get(ctx, &configuration)...)
	if response.Diagnostics.HasError() {
		return
	}
	if configuration.Endpoint.IsUnknown() || configuration.APIKey.IsUnknown() ||
		configuration.SourceRepository.IsUnknown() || configuration.SourceCommit.IsUnknown() ||
		configuration.SourcePath.IsUnknown() || configuration.SourceWorkflow.IsUnknown() ||
		configuration.SourceRun.IsUnknown() || configuration.SourceActor.IsUnknown() ||
		configuration.RequestTimeout.IsUnknown() {
		return
	}

	endpoint := os.Getenv("CANTORA_ENDPOINT")
	if !configuration.Endpoint.IsNull() {
		endpoint = configuration.Endpoint.ValueString()
	}
	if endpoint == "" {
		endpoint = "https://api.cantora.ai"
	}

	apiKey := os.Getenv("CANTORA_API_KEY")
	if !configuration.APIKey.IsNull() {
		apiKey = configuration.APIKey.ValueString()
	}
	requestTimeout, err := time.ParseDuration(configuredOrEnvironment(
		configuration.RequestTimeout,
		"CANTORA_REQUEST_TIMEOUT",
		client.DefaultRequestTimeout.String(),
	))
	if err != nil || requestTimeout <= 0 {
		response.Diagnostics.AddError(
			"Invalid Cantora provider configuration",
			"request_timeout must be a positive Go duration such as `30s` or `2m`.",
		)
		return
	}
	configuredHTTPClient := p.httpClient
	if configuredHTTPClient == nil {
		configuredHTTPClient = http.DefaultClient
	}
	boundedHTTPClient := *configuredHTTPClient
	boundedHTTPClient.Timeout = requestTimeout

	managementClient, err := client.New(endpoint, apiKey, p.version, &boundedHTTPClient)
	if err != nil {
		response.Diagnostics.AddError("Invalid Cantora provider configuration", err.Error())
		return
	}
	response.ResourceData = &providerData{
		client: managementClient,
		source: client.Source{
			Repository: configuredOrEnvironment(configuration.SourceRepository, "CANTORA_SOURCE_REPOSITORY", "unknown"),
			Commit:     configuredOrEnvironment(configuration.SourceCommit, "CANTORA_SOURCE_COMMIT", "unknown"),
			Path:       configuredOrEnvironment(configuration.SourcePath, "CANTORA_SOURCE_PATH", "terraform"),
			Workflow:   configuredOrEnvironment(configuration.SourceWorkflow, "CANTORA_SOURCE_WORKFLOW", "terraform"),
			Run:        configuredOrEnvironment(configuration.SourceRun, "CANTORA_SOURCE_RUN", "local"),
			Actor:      configuredOrEnvironment(configuration.SourceActor, "CANTORA_SOURCE_ACTOR", "local"),
		},
	}
}

func configuredOrEnvironment(value types.String, environment string, fallback string) string {
	if !value.IsNull() {
		return value.ValueString()
	}
	if configured := os.Getenv(environment); configured != "" {
		return configured
	}
	return fallback
}

func (p *cantoraProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{newAgentConfigurationResource}
}

func (p *cantoraProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

func clientFromProviderData(raw any) (*providerData, error) {
	configured, ok := raw.(*providerData)
	if !ok {
		return nil, fmt.Errorf("expected *providerData, got %T", raw)
	}
	return configured, nil
}
