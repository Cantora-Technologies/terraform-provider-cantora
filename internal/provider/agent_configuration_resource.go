package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/Cantora-Technologies/terraform-provider-cantora/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

var _ resource.Resource = &agentConfigurationResource{}
var _ resource.ResourceWithConfigure = &agentConfigurationResource{}
var _ resource.ResourceWithImportState = &agentConfigurationResource{}
var _ resource.ResourceWithModifyPlan = &agentConfigurationResource{}
var _ resource.ResourceWithValidateConfig = &agentConfigurationResource{}

var dependencyKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type agentConfigurationResource struct {
	data *providerData
}

type agentConfigurationModel struct {
	ID                          types.String `tfsdk:"id"`
	OrganizationID              types.String `tfsdk:"organization_id"`
	ProjectID                   types.String `tfsdk:"project_id"`
	Key                         types.String `tfsdk:"key"`
	DisplayName                 types.String `tfsdk:"display_name"`
	Manifest                    types.Object `tfsdk:"manifest"`
	TestRelease                 types.Object `tfsdk:"test_release"`
	AgentDefinitionID           types.String `tfsdk:"agent_definition_id"`
	TestAgentPrincipalID        types.String `tfsdk:"test_agent_principal_id"`
	AgentVersionID              types.String `tfsdk:"agent_version_id"`
	TestAgentReleaseID          types.String `tfsdk:"test_agent_release_id"`
	ManifestDigest              types.String `tfsdk:"manifest_digest"`
	ReleaseGeneration           types.Int64  `tfsdk:"release_generation"`
	ReleaseETag                 types.String `tfsdk:"release_etag"`
	ConfigurationRevisionID     types.String `tfsdk:"configuration_revision_id"`
	ConfigurationRevisionStatus types.String `tfsdk:"configuration_revision_status"`
	DefinitionStatus            types.String `tfsdk:"definition_status"`
	ConfigurationAuthority      types.String `tfsdk:"configuration_authority"`
	PlanMetadata                types.Object `tfsdk:"plan_metadata"`
}

type manifestModel struct {
	SchemaVersion            types.String `tfsdk:"schema_version"`
	Instructions             types.String `tfsdk:"instructions"`
	ClassifierModelReleaseID types.String `tfsdk:"classifier_model_release_id"`
	WorkerModelReleaseID     types.String `tfsdk:"worker_model_release_id"`
	Tools                    types.Set    `tfsdk:"tools"`
	RequiredBindings         types.Set    `tfsdk:"required_bindings"`
	Budgets                  types.Object `tfsdk:"budgets"`
}

type budgetModel struct {
	MaxModelSteps          types.Int64 `tfsdk:"max_model_steps"`
	MaxToolCalls           types.Int64 `tfsdk:"max_tool_calls"`
	TotalWallTimeMS        types.Int64 `tfsdk:"total_wall_time_ms"`
	ModelStepTimeMS        types.Int64 `tfsdk:"model_step_time_ms"`
	StreamStallTimeMS      types.Int64 `tfsdk:"stream_stall_time_ms"`
	MaxInputTokens         types.Int64 `tfsdk:"max_input_tokens"`
	MaxOutputTokens        types.Int64 `tfsdk:"max_output_tokens"`
	MaxReasoningTokens     types.Int64 `tfsdk:"max_reasoning_tokens"`
	MaxTotalTokens         types.Int64 `tfsdk:"max_total_tokens"`
	MaxToolResultBytes     types.Int64 `tfsdk:"max_tool_result_bytes"`
	MaxEstimatedCostMicros types.Int64 `tfsdk:"max_estimated_cost_micros"`
}

type testReleaseModel struct {
	EnvironmentID types.String `tfsdk:"environment_id"`
	Reason        types.String `tfsdk:"reason"`
}

type planMetadataModel struct {
	ManifestDigest    types.String `tfsdk:"manifest_digest"`
	PreconditionsJSON types.String `tfsdk:"preconditions_json"`
	Actions           types.List   `tfsdk:"actions"`
	RequestID         types.String `tfsdk:"request_id"`
	SourceJSON        types.String `tfsdk:"source_json"`
}

func newAgentConfigurationResource() resource.Resource {
	return &agentConfigurationResource{}
}

func (r *agentConfigurationResource) Metadata(
	_ context.Context,
	request resource.MetadataRequest,
	response *resource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_agent_configuration"
}

func (r *agentConfigurationResource) Schema(
	_ context.Context,
	_ resource.SchemaRequest,
	response *resource.SchemaResponse,
) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	response.Schema = schema.Schema{
		MarkdownDescription: "One stable Agent Definition and its complete desired Test behavior. Cantora retains immutable Version and Release history when this resource changes or leaves Terraform state.",
		Attributes: map[string]schema.Attribute{
			"id":                            computedString("Complete scoped identity used by Terraform state."),
			"organization_id":               requiredString("Organization that owns the Agent Definition.", replace),
			"project_id":                    requiredString("Project that owns the Agent Definition.", replace),
			"key":                           requiredString("Stable Project-unique Agent Definition key.", replace),
			"display_name":                  requiredString("Human-readable Agent Definition name.", nil),
			"agent_definition_id":           computedString("Stable server-minted Agent Definition identifier."),
			"test_agent_principal_id":       computedString("Environment Agent Principal receiving Test releases."),
			"agent_version_id":              computedString("Current immutable Agent Version identifier."),
			"test_agent_release_id":         computedString("Current append-only Test Agent Release identifier."),
			"manifest_digest":               computedString("Canonical SHA-256 Agent Version digest."),
			"release_generation":            computedInt64("Current Test Agent release generation."),
			"release_etag":                  computedString("Strong ETag for the Test Agent release generation."),
			"configuration_revision_id":     computedString("Latest durable desired-state apply record."),
			"configuration_revision_status": computedString("Terminal status of the apply represented in state."),
			"definition_status":             computedString("Current Agent Definition lifecycle status."),
			"configuration_authority":       computedString("Current Agent Definition configuration authority."),
			"plan_metadata": schema.SingleNestedAttribute{
				MarkdownDescription: "Service-observed digest, preconditions, proposed actions, and retry identity carried by the reviewed Terraform plan. Terraform manages this block; configuration must not set it.",
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"manifest_digest":    computedString("Canonical digest reviewed in this plan."),
					"preconditions_json": computedString("Canonical service preconditions reviewed in this plan."),
					"actions": schema.ListAttribute{
						MarkdownDescription: "Creates, reuses, updates, and activations proposed by the service.",
						Computed:            true,
						ElementType:         types.StringType,
					},
					"request_id":  computedString("Opaque idempotency identifier bound to this planned apply."),
					"source_json": computedString("Canonical source provenance bound to this planned apply."),
				},
			},
		},
		Blocks: map[string]schema.Block{
			"manifest": schema.SingleNestedBlock{
				MarkdownDescription: "Complete immutable Agent Version manifest selected by this desired configuration.",
				Attributes:          manifestAttributes(),
				Blocks: map[string]schema.Block{
					"budgets": schema.SingleNestedBlock{
						MarkdownDescription: "Explicit finite runtime limits.",
						Attributes:          budgetAttributes(),
					},
				},
			},
			"test_release": schema.SingleNestedBlock{
				MarkdownDescription: "Test Environment activation declared by this desired configuration.",
				Attributes: map[string]schema.Attribute{
					"environment_id": requiredString("Project Test Environment identifier.", nil),
					"reason":         requiredString("Human-readable activation reason retained on the append-only Release.", nil),
				},
			},
		},
	}
}

func manifestAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"schema_version":              requiredString("Agent Version manifest schema version.", nil),
		"instructions":                requiredString("Complete instructions retained in plans and state as customer data.", nil),
		"classifier_model_release_id": requiredString("Exact immutable classifier Model Release identifier.", nil),
		"worker_model_release_id":     requiredString("Exact immutable worker Model Release identifier.", nil),
		"tools": schema.SetAttribute{
			MarkdownDescription: "Enabled Tool keys. Cantora canonicalizes byte order before hashing.",
			Required:            true,
			ElementType:         types.StringType,
		},
		"required_bindings": schema.SetAttribute{
			MarkdownDescription: "Required Environment binding keys. Cantora canonicalizes byte order before hashing.",
			Required:            true,
			ElementType:         types.StringType,
		},
	}
}

func budgetAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"max_model_steps":           requiredInt64("Maximum model steps."),
		"max_tool_calls":            requiredInt64("Maximum Tool calls."),
		"total_wall_time_ms":        requiredInt64("Total Run wall-time limit in milliseconds."),
		"model_step_time_ms":        requiredInt64("Per-model-step time limit in milliseconds."),
		"stream_stall_time_ms":      requiredInt64("Model stream stall limit in milliseconds."),
		"max_input_tokens":          requiredInt64("Maximum input tokens."),
		"max_output_tokens":         requiredInt64("Maximum output tokens."),
		"max_reasoning_tokens":      requiredInt64("Maximum reasoning tokens."),
		"max_total_tokens":          requiredInt64("Maximum total tokens."),
		"max_tool_result_bytes":     requiredInt64("Maximum bytes returned by one Tool call."),
		"max_estimated_cost_micros": requiredInt64("Maximum estimated cost in micros."),
	}
}

func requiredString(description string, modifiers []planmodifier.String) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: description,
		Required:            true,
		PlanModifiers:       modifiers,
	}
}

func computedString(description string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: description, Computed: true}
}

func requiredInt64(description string) schema.Int64Attribute {
	return schema.Int64Attribute{MarkdownDescription: description, Required: true}
}

func computedInt64(description string) schema.Int64Attribute {
	return schema.Int64Attribute{MarkdownDescription: description, Computed: true}
}

func (r *agentConfigurationResource) Configure(
	_ context.Context,
	request resource.ConfigureRequest,
	response *resource.ConfigureResponse,
) {
	if request.ProviderData == nil {
		return
	}
	configured, err := clientFromProviderData(request.ProviderData)
	if err != nil {
		response.Diagnostics.AddError("Unexpected provider data", err.Error())
		return
	}
	r.data = configured
}

func (r *agentConfigurationResource) ValidateConfig(
	ctx context.Context,
	request resource.ValidateConfigRequest,
	response *resource.ValidateConfigResponse,
) {
	var configuration agentConfigurationModel
	response.Diagnostics.Append(request.Config.Get(ctx, &configuration)...)
	if response.Diagnostics.HasError() {
		return
	}
	if configuration.Manifest.IsNull() {
		response.Diagnostics.AddAttributeError(
			path.Root("manifest"),
			"Missing manifest block",
			"Configure exactly one manifest block for this Agent Configuration.",
		)
	} else if !configuration.Manifest.IsUnknown() {
		var manifest manifestModel
		response.Diagnostics.Append(configuration.Manifest.As(ctx, &manifest, basetypes.ObjectAsOptions{})...)
		if response.Diagnostics.HasError() {
			return
		}
		if manifest.Budgets.IsNull() {
			response.Diagnostics.AddAttributeError(
				path.Root("manifest").AtName("budgets"),
				"Missing budgets block",
				"Configure exactly one budgets block with explicit finite runtime limits.",
			)
		} else if !manifest.Budgets.IsUnknown() {
			var budgets budgetModel
			response.Diagnostics.Append(manifest.Budgets.As(ctx, &budgets, basetypes.ObjectAsOptions{})...)
			if !response.Diagnostics.HasError() {
				validateBudgets(budgets, &response.Diagnostics)
			}
		}
		validateManifestStructure(manifest, &response.Diagnostics)
	}
	if configuration.TestRelease.IsNull() {
		response.Diagnostics.AddAttributeError(
			path.Root("test_release"),
			"Missing test_release block",
			"Configure exactly one test_release block for the Project's Test Environment.",
		)
	} else if !configuration.TestRelease.IsUnknown() {
		var release testReleaseModel
		response.Diagnostics.Append(configuration.TestRelease.As(ctx, &release, basetypes.ObjectAsOptions{})...)
		if !response.Diagnostics.HasError() {
			validateIdentifier(release.EnvironmentID, "env", path.Root("test_release").AtName("environment_id"), &response.Diagnostics)
			validateTrimmedLength(release.Reason, 1, 500, "Test release reason", path.Root("test_release").AtName("reason"), &response.Diagnostics)
		}
	}

	validateIdentifier(configuration.OrganizationID, "org", path.Root("organization_id"), &response.Diagnostics)
	validateIdentifier(configuration.ProjectID, "proj", path.Root("project_id"), &response.Diagnostics)
	validateTrimmedLength(configuration.Key, 1, 100, "Agent Definition key", path.Root("key"), &response.Diagnostics)
	validateTrimmedLength(configuration.DisplayName, 1, 200, "display name", path.Root("display_name"), &response.Diagnostics)
}

func validateManifestStructure(manifest manifestModel, diagnostics *diag.Diagnostics) {
	validateExactString(
		manifest.SchemaVersion,
		"config.cantora.ai/agent-version/v1alpha1",
		"manifest schema version",
		path.Root("manifest").AtName("schema_version"),
		diagnostics,
	)
	validateMaximumLength(
		manifest.Instructions,
		262_144,
		"instructions",
		path.Root("manifest").AtName("instructions"),
		diagnostics,
	)
	validateIdentifier(
		manifest.ClassifierModelReleaseID,
		"model_release",
		path.Root("manifest").AtName("classifier_model_release_id"),
		diagnostics,
	)
	validateIdentifier(
		manifest.WorkerModelReleaseID,
		"model_release",
		path.Root("manifest").AtName("worker_model_release_id"),
		diagnostics,
	)
	validateDependencySet(manifest.Tools, path.Root("manifest").AtName("tools"), diagnostics)
	validateDependencySet(manifest.RequiredBindings, path.Root("manifest").AtName("required_bindings"), diagnostics)
}

func validateIdentifier(value types.String, prefix string, attributePath path.Path, diagnostics *diag.Diagnostics) {
	if value.IsNull() || value.IsUnknown() {
		return
	}
	identifier := value.ValueString()
	if stringLength(identifier) > 64 || !strings.HasPrefix(identifier, prefix+"_") || identifier == prefix+"_" {
		diagnostics.AddAttributeError(
			attributePath,
			"Invalid Cantora identifier",
			fmt.Sprintf("Expected an identifier no longer than 64 characters and prefixed with %q.", prefix+"_"),
		)
	}
}

func validateTrimmedLength(
	value types.String,
	minimum int,
	maximum int,
	label string,
	attributePath path.Path,
	diagnostics *diag.Diagnostics,
) {
	if value.IsNull() || value.IsUnknown() {
		return
	}
	text := value.ValueString()
	length := stringLength(text)
	if strings.TrimSpace(text) != text || length < minimum || length > maximum {
		diagnostics.AddAttributeError(
			attributePath,
			"Invalid "+label,
			fmt.Sprintf("%s must be trimmed and contain between %d and %d characters.", label, minimum, maximum),
		)
	}
}

func validateMaximumLength(
	value types.String,
	maximum int,
	label string,
	attributePath path.Path,
	diagnostics *diag.Diagnostics,
) {
	if value.IsNull() || value.IsUnknown() {
		return
	}
	if stringLength(value.ValueString()) > maximum {
		diagnostics.AddAttributeError(
			attributePath,
			"Invalid "+label,
			fmt.Sprintf("%s must contain no more than %d characters.", label, maximum),
		)
	}
}

func validateExactString(
	value types.String,
	expected string,
	label string,
	attributePath path.Path,
	diagnostics *diag.Diagnostics,
) {
	if value.IsNull() || value.IsUnknown() {
		return
	}
	if value.ValueString() != expected {
		diagnostics.AddAttributeError(
			attributePath,
			"Invalid "+label,
			fmt.Sprintf("Expected %q.", expected),
		)
	}
}

func validateDependencySet(value types.Set, attributePath path.Path, diagnostics *diag.Diagnostics) {
	if value.IsNull() || value.IsUnknown() {
		return
	}
	if len(value.Elements()) > 100 {
		diagnostics.AddAttributeError(attributePath, "Too many dependency keys", "Configure no more than 100 keys.")
	}
	for _, element := range value.Elements() {
		key, ok := element.(basetypes.StringValue)
		if !ok || key.IsNull() || key.IsUnknown() {
			continue
		}
		text := key.ValueString()
		if stringLength(text) > 100 || !dependencyKeyPattern.MatchString(text) {
			diagnostics.AddAttributeError(
				attributePath,
				"Invalid dependency key",
				"Dependency keys must start with a lowercase letter, contain only lowercase letters, digits, and underscores, and contain no more than 100 characters.",
			)
			return
		}
	}
}

func validateBudgets(budgets budgetModel, diagnostics *diag.Diagnostics) {
	budgetPath := path.Root("manifest").AtName("budgets")
	validateIntegerRange(budgets.MaxModelSteps, 1, 100, budgetPath.AtName("max_model_steps"), diagnostics)
	validateIntegerRange(budgets.MaxToolCalls, 0, 100, budgetPath.AtName("max_tool_calls"), diagnostics)
	validateIntegerRange(budgets.TotalWallTimeMS, 1_000, 300_000, budgetPath.AtName("total_wall_time_ms"), diagnostics)
	validateIntegerRange(budgets.ModelStepTimeMS, 1_000, 300_000, budgetPath.AtName("model_step_time_ms"), diagnostics)
	validateIntegerRange(budgets.StreamStallTimeMS, 1_000, 300_000, budgetPath.AtName("stream_stall_time_ms"), diagnostics)
	validateIntegerRange(budgets.MaxInputTokens, 1, 10_000_000, budgetPath.AtName("max_input_tokens"), diagnostics)
	validateIntegerRange(budgets.MaxOutputTokens, 1, 1_000_000, budgetPath.AtName("max_output_tokens"), diagnostics)
	validateIntegerRange(budgets.MaxReasoningTokens, 0, 1_000_000, budgetPath.AtName("max_reasoning_tokens"), diagnostics)
	validateIntegerRange(budgets.MaxTotalTokens, 1, 10_000_000, budgetPath.AtName("max_total_tokens"), diagnostics)
	validateIntegerRange(budgets.MaxToolResultBytes, 1, 10_000_000, budgetPath.AtName("max_tool_result_bytes"), diagnostics)
	validateIntegerRange(budgets.MaxEstimatedCostMicros, 1, 1_000_000_000, budgetPath.AtName("max_estimated_cost_micros"), diagnostics)

	if knownGreaterThan(budgets.ModelStepTimeMS, budgets.TotalWallTimeMS) {
		diagnostics.AddAttributeError(budgetPath.AtName("model_step_time_ms"), "Invalid model-step timeout", "model_step_time_ms must not exceed total_wall_time_ms.")
	}
	if knownGreaterThan(budgets.StreamStallTimeMS, budgets.ModelStepTimeMS) {
		diagnostics.AddAttributeError(budgetPath.AtName("stream_stall_time_ms"), "Invalid stream-stall timeout", "stream_stall_time_ms must not exceed model_step_time_ms.")
	}
	if allKnown(budgets.MaxTotalTokens, budgets.MaxInputTokens, budgets.MaxOutputTokens, budgets.MaxReasoningTokens) &&
		budgets.MaxTotalTokens.ValueInt64() < max(
			budgets.MaxInputTokens.ValueInt64(),
			budgets.MaxOutputTokens.ValueInt64(),
			budgets.MaxReasoningTokens.ValueInt64(),
		) {
		diagnostics.AddAttributeError(budgetPath.AtName("max_total_tokens"), "Invalid total-token budget", "max_total_tokens must cover every individual token limit.")
	}
}

func validateIntegerRange(value types.Int64, minimum int64, maximum int64, attributePath path.Path, diagnostics *diag.Diagnostics) {
	if value.IsNull() || value.IsUnknown() {
		return
	}
	if value.ValueInt64() < minimum || value.ValueInt64() > maximum {
		diagnostics.AddAttributeError(
			attributePath,
			"Invalid finite budget",
			fmt.Sprintf("Expected an integer between %d and %d.", minimum, maximum),
		)
	}
}

func knownGreaterThan(left types.Int64, right types.Int64) bool {
	return !left.IsNull() && !left.IsUnknown() && !right.IsNull() && !right.IsUnknown() && left.ValueInt64() > right.ValueInt64()
}

func allKnown(values ...types.Int64) bool {
	for _, value := range values {
		if value.IsNull() || value.IsUnknown() {
			return false
		}
	}
	return true
}

func stringLength(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func (r *agentConfigurationResource) ModifyPlan(
	ctx context.Context,
	request resource.ModifyPlanRequest,
	response *resource.ModifyPlanResponse,
) {
	if request.Plan.Raw.IsNull() {
		if !request.State.Raw.IsNull() {
			response.Diagnostics.AddWarning(
				"Terraform will relinquish this Agent Configuration",
				"Cantora retains the Agent Definition, Environment Agent, Agent Versions, Agent Releases, ConfigurationRevisions, and runtime behavior. Removing this resource performs no remote delete, retirement, rollback, or release.",
			)
		}
		return
	}
	if r.data == nil {
		response.Diagnostics.AddError("Provider is not configured", "Configure the Cantora provider before planning resources.")
		return
	}

	var plan agentConfigurationModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	intent := "create"
	var prior agentConfigurationModel
	if !request.State.Raw.IsNull() {
		response.Diagnostics.Append(request.State.Get(ctx, &prior)...)
		if response.Diagnostics.HasError() {
			return
		}
		intent = "update"
		if desiredModelsEqual(ctx, plan, prior, &response.Diagnostics) {
			plan.PlanMetadata = prior.PlanMetadata
			response.Diagnostics.Append(response.Plan.Set(ctx, &plan)...)
			return
		}
	}
	desired, ok := desiredFromModel(ctx, plan, intent, prior.AgentDefinitionID, &response.Diagnostics)
	if !ok {
		return
	}
	preview, err := r.data.client.PreviewAgentConfiguration(ctx, scopeFromModel(plan), desired)
	if err != nil {
		response.Diagnostics.AddError("Could not preview Agent Configuration", err.Error())
		return
	}
	requestID, diagnostics := plannedRequestID(scopeFromModel(plan), desired, preview, r.data.source)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() {
		return
	}
	metadata, diagnostics := planMetadataValue(ctx, preview, r.data.source, requestID)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() {
		return
	}
	plan.PlanMetadata = metadata
	for _, warning := range preview.Warnings {
		response.Diagnostics.AddWarning(warning.Code, warning.Message)
	}
	response.Diagnostics.Append(response.Plan.Set(ctx, &plan)...)
}

func plannedRequestID(
	scope client.Scope,
	desired client.DesiredAgentConfiguration,
	preview client.Preview,
	source client.Source,
) (string, diag.Diagnostics) {
	var diagnostics diag.Diagnostics
	payload, err := json.Marshal(struct {
		Version        string                           `json:"version"`
		Scope          client.Scope                     `json:"scope"`
		Desired        client.DesiredAgentConfiguration `json:"desired"`
		ManifestDigest string                           `json:"manifestDigest"`
		Preconditions  client.Preconditions             `json:"preconditions"`
		Source         client.Source                    `json:"source"`
	}{
		Version:        "cantora-agent-configuration-apply/v1",
		Scope:          scope,
		Desired:        desired,
		ManifestDigest: preview.ManifestDigest,
		Preconditions:  preview.Preconditions,
		Source:         source,
	})
	if err != nil {
		diagnostics.AddError("Could not encode planned apply identity", err.Error())
		return "", diagnostics
	}
	digest := sha256.Sum256(payload)
	digest[6] = (digest[6] & 0x0f) | 0x80
	digest[8] = (digest[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(digest[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32]), diagnostics
}

func (r *agentConfigurationResource) Create(
	ctx context.Context,
	request resource.CreateRequest,
	response *resource.CreateResponse,
) {
	var plan agentConfigurationModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	r.apply(ctx, &plan, "create", types.StringNull(), &response.Diagnostics)
	if response.Diagnostics.HasError() {
		return
	}
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *agentConfigurationResource) Update(
	ctx context.Context,
	request resource.UpdateRequest,
	response *resource.UpdateResponse,
) {
	var plan agentConfigurationModel
	var state agentConfigurationModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	r.apply(ctx, &plan, "update", state.AgentDefinitionID, &response.Diagnostics)
	if response.Diagnostics.HasError() {
		return
	}
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *agentConfigurationResource) apply(
	ctx context.Context,
	state *agentConfigurationModel,
	intent string,
	agentDefinitionID types.String,
	diagnostics interface {
		AddError(string, string)
		Append(...diag.Diagnostic)
		HasError() bool
	},
) {
	if r.data == nil {
		diagnostics.AddError("Provider is not configured", "Configure the Cantora provider before applying resources.")
		return
	}
	desired, ok := desiredFromModel(ctx, *state, intent, agentDefinitionID, diagnostics)
	if !ok {
		return
	}
	preview, requestID, source, ok := previewFromPlanMetadata(ctx, state.PlanMetadata, diagnostics)
	if !ok {
		return
	}
	applied, err := r.data.client.ApplyAgentConfiguration(
		ctx,
		scopeFromModel(*state),
		requestID,
		desired,
		preview,
		source,
	)
	if err != nil {
		diagnostics.AddError("Could not apply Agent Configuration", err.Error())
		return
	}
	setConfigurationState(ctx, state, applied.Configuration, &applied.Revision, diagnostics)
}

func (r *agentConfigurationResource) Read(
	ctx context.Context,
	request resource.ReadRequest,
	response *resource.ReadResponse,
) {
	var state agentConfigurationModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	if r.data == nil {
		response.Diagnostics.AddError("Provider is not configured", "Configure the Cantora provider before refreshing resources.")
		return
	}
	remote, err := r.data.client.GetAgentConfiguration(ctx, scopeFromModel(state), state.AgentDefinitionID.ValueString())
	if client.IsNotFound(err) {
		response.Diagnostics.AddError(
			"Agent Configuration is missing",
			"Cantora could not read the Agent Definition recorded in state. Restore or import the correct scoped identity; the provider will not infer deletion or discard state.",
		)
		return
	}
	if err != nil {
		response.Diagnostics.AddError("Could not read Agent Configuration", err.Error())
		return
	}
	var revision *client.ConfigurationRevision
	if remote.LatestConfigurationRevisionID != nil {
		observed, revisionError := r.data.client.GetConfigurationRevision(
			ctx,
			scopeFromModel(state),
			*remote.LatestConfigurationRevisionID,
		)
		if revisionError != nil {
			response.Diagnostics.AddError("Could not read Configuration Revision", revisionError.Error())
			return
		}
		revision = &observed
	}
	setConfigurationState(ctx, &state, remote, revision, &response.Diagnostics)
	if response.Diagnostics.HasError() {
		return
	}
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (r *agentConfigurationResource) Delete(
	_ context.Context,
	_ resource.DeleteRequest,
	_ *resource.DeleteResponse,
) {
	// Terraform removes only its local binding. Cantora owns remote lifecycle and immutable history.
}

func (r *agentConfigurationResource) ImportState(
	ctx context.Context,
	request resource.ImportStateRequest,
	response *resource.ImportStateResponse,
) {
	parts := strings.Split(request.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		response.Diagnostics.AddError(
			"Unexpected import identifier",
			fmt.Sprintf("Expected organization_id/project_id/agent_definition_id, got %q.", request.ID),
		)
		return
	}
	response.Diagnostics.Append(response.State.SetAttribute(ctx, path.Root("id"), request.ID)...)
	response.Diagnostics.Append(response.State.SetAttribute(ctx, path.Root("organization_id"), parts[0])...)
	response.Diagnostics.Append(response.State.SetAttribute(ctx, path.Root("project_id"), parts[1])...)
	response.Diagnostics.Append(response.State.SetAttribute(ctx, path.Root("agent_definition_id"), parts[2])...)
}

func scopeFromModel(model agentConfigurationModel) client.Scope {
	return client.Scope{
		OrganizationID: model.OrganizationID.ValueString(),
		ProjectID:      model.ProjectID.ValueString(),
	}
}

func desiredFromModel(
	ctx context.Context,
	model agentConfigurationModel,
	intent string,
	agentDefinitionID types.String,
	diagnostics interface {
		Append(...diag.Diagnostic)
		HasError() bool
	},
) (client.DesiredAgentConfiguration, bool) {
	var manifestState manifestModel
	diagnostics.Append(model.Manifest.As(ctx, &manifestState, basetypes.ObjectAsOptions{})...)
	var budgets budgetModel
	diagnostics.Append(manifestState.Budgets.As(ctx, &budgets, basetypes.ObjectAsOptions{})...)
	var release testReleaseModel
	diagnostics.Append(model.TestRelease.As(ctx, &release, basetypes.ObjectAsOptions{})...)
	var tools []string
	var bindings []string
	diagnostics.Append(manifestState.Tools.ElementsAs(ctx, &tools, false)...)
	diagnostics.Append(manifestState.RequiredBindings.ElementsAs(ctx, &bindings, false)...)
	if diagnostics.HasError() {
		return client.DesiredAgentConfiguration{}, false
	}
	sort.Strings(tools)
	sort.Strings(bindings)
	var definitionID *string
	if intent == "update" && !agentDefinitionID.IsNull() && !agentDefinitionID.IsUnknown() {
		value := agentDefinitionID.ValueString()
		definitionID = &value
	}
	return client.DesiredAgentConfiguration{
		Intent:            intent,
		AgentDefinitionID: definitionID,
		Key:               model.Key.ValueString(),
		DisplayName:       model.DisplayName.ValueString(),
		Manifest: client.Manifest{
			SchemaVersion: manifestState.SchemaVersion.ValueString(),
			Instructions:  manifestState.Instructions.ValueString(),
			Models: client.Models{
				Classifier: manifestState.ClassifierModelReleaseID.ValueString(),
				Worker:     manifestState.WorkerModelReleaseID.ValueString(),
			},
			Tools:            tools,
			RequiredBindings: bindings,
			Budgets: client.Budgets{
				MaxModelSteps:          budgets.MaxModelSteps.ValueInt64(),
				MaxToolCalls:           budgets.MaxToolCalls.ValueInt64(),
				TotalWallTimeMS:        budgets.TotalWallTimeMS.ValueInt64(),
				ModelStepTimeMS:        budgets.ModelStepTimeMS.ValueInt64(),
				StreamStallTimeMS:      budgets.StreamStallTimeMS.ValueInt64(),
				MaxInputTokens:         budgets.MaxInputTokens.ValueInt64(),
				MaxOutputTokens:        budgets.MaxOutputTokens.ValueInt64(),
				MaxReasoningTokens:     budgets.MaxReasoningTokens.ValueInt64(),
				MaxTotalTokens:         budgets.MaxTotalTokens.ValueInt64(),
				MaxToolResultBytes:     budgets.MaxToolResultBytes.ValueInt64(),
				MaxEstimatedCostMicros: budgets.MaxEstimatedCostMicros.ValueInt64(),
			},
		},
		TestRelease: client.TestRelease{
			EnvironmentID: release.EnvironmentID.ValueString(),
			Reason:        release.Reason.ValueString(),
		},
	}, true
}

func desiredModelsEqual(
	ctx context.Context,
	left agentConfigurationModel,
	right agentConfigurationModel,
	diagnostics interface {
		Append(...diag.Diagnostic)
		HasError() bool
	},
) bool {
	leftDesired, leftOK := desiredFromModel(ctx, left, "update", right.AgentDefinitionID, diagnostics)
	rightDesired, rightOK := desiredFromModel(ctx, right, "update", right.AgentDefinitionID, diagnostics)
	if !leftOK || !rightOK {
		return false
	}
	return reflect.DeepEqual(leftDesired, rightDesired)
}

func planMetadataValue(
	ctx context.Context,
	preview client.Preview,
	source client.Source,
	requestID string,
) (types.Object, diag.Diagnostics) {
	preconditions, err := json.Marshal(preview.Preconditions)
	if err != nil {
		var diagnostics diag.Diagnostics
		diagnostics.AddError("Could not encode plan preconditions", err.Error())
		return types.ObjectNull(planMetadataAttributeTypes()), diagnostics
	}
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		var diagnostics diag.Diagnostics
		diagnostics.AddError("Could not encode planned source provenance", err.Error())
		return types.ObjectNull(planMetadataAttributeTypes()), diagnostics
	}
	actions, actionDiagnostics := types.ListValueFrom(ctx, types.StringType, preview.Actions)
	value, diagnostics := types.ObjectValueFrom(ctx, planMetadataAttributeTypes(), planMetadataModel{
		ManifestDigest:    types.StringValue(preview.ManifestDigest),
		PreconditionsJSON: types.StringValue(string(preconditions)),
		Actions:           actions,
		RequestID:         types.StringValue(requestID),
		SourceJSON:        types.StringValue(string(sourceJSON)),
	})
	diagnostics.Append(actionDiagnostics...)
	return value, diagnostics
}

func previewFromPlanMetadata(
	ctx context.Context,
	metadata types.Object,
	diagnostics interface {
		AddError(string, string)
		Append(...diag.Diagnostic)
		HasError() bool
	},
) (client.Preview, string, client.Source, bool) {
	var stored planMetadataModel
	diagnostics.Append(metadata.As(ctx, &stored, basetypes.ObjectAsOptions{})...)
	if diagnostics.HasError() {
		return client.Preview{}, "", client.Source{}, false
	}
	var preconditions client.Preconditions
	if err := json.Unmarshal([]byte(stored.PreconditionsJSON.ValueString()), &preconditions); err != nil {
		diagnostics.AddError("Could not decode planned preconditions", err.Error())
		return client.Preview{}, "", client.Source{}, false
	}
	var source client.Source
	if err := json.Unmarshal([]byte(stored.SourceJSON.ValueString()), &source); err != nil {
		diagnostics.AddError("Could not decode planned source provenance", err.Error())
		return client.Preview{}, "", client.Source{}, false
	}
	var actions []string
	diagnostics.Append(stored.Actions.ElementsAs(ctx, &actions, false)...)
	if diagnostics.HasError() {
		return client.Preview{}, "", client.Source{}, false
	}
	return client.Preview{
		ManifestDigest: stored.ManifestDigest.ValueString(),
		Preconditions:  preconditions,
		Actions:        actions,
	}, stored.RequestID.ValueString(), source, true
}

func setConfigurationState(
	ctx context.Context,
	state *agentConfigurationModel,
	remote client.AgentConfiguration,
	revision *client.ConfigurationRevision,
	diagnostics interface {
		AddError(string, string)
		Append(...diag.Diagnostic)
		HasError() bool
	},
) {
	if remote.TestEnvironmentID == nil || remote.TestAgentPrincipalID == nil || remote.CurrentAgentVersionID == nil ||
		remote.CurrentManifest == nil || remote.CurrentManifestDigest == nil || remote.CurrentTestAgentReleaseID == nil ||
		remote.CurrentTestAgentReleaseReason == nil {
		diagnostics.AddError(
			"Agent Configuration is incomplete",
			"The Agent Definition exists but has no complete current Test Agent, Version, and Release. Repair the remote configuration before managing it with this resource.",
		)
		return
	}
	manifest, manifestDiagnostics := manifestValue(ctx, *remote.CurrentManifest)
	release, releaseDiagnostics := types.ObjectValueFrom(ctx, testReleaseAttributeTypes(), testReleaseModel{
		EnvironmentID: types.StringValue(*remote.TestEnvironmentID),
		Reason:        types.StringValue(*remote.CurrentTestAgentReleaseReason),
	})
	diagnostics.Append(manifestDiagnostics...)
	diagnostics.Append(releaseDiagnostics...)
	if diagnostics.HasError() {
		return
	}
	state.ID = types.StringValue(resourceIdentity(remote.OrganizationID, remote.ProjectID, remote.AgentDefinitionID))
	state.OrganizationID = types.StringValue(remote.OrganizationID)
	state.ProjectID = types.StringValue(remote.ProjectID)
	state.Key = types.StringValue(remote.Key)
	state.DisplayName = types.StringValue(remote.DisplayName)
	state.Manifest = manifest
	state.TestRelease = release
	state.AgentDefinitionID = types.StringValue(remote.AgentDefinitionID)
	state.TestAgentPrincipalID = types.StringValue(*remote.TestAgentPrincipalID)
	state.AgentVersionID = types.StringValue(*remote.CurrentAgentVersionID)
	state.TestAgentReleaseID = types.StringValue(*remote.CurrentTestAgentReleaseID)
	state.ManifestDigest = types.StringValue(*remote.CurrentManifestDigest)
	state.ReleaseGeneration = types.Int64Value(remote.TestReleaseGeneration)
	state.ReleaseETag = types.StringValue(remote.TestReleaseETag)
	state.DefinitionStatus = types.StringValue(remote.Status)
	state.ConfigurationAuthority = types.StringValue(remote.ConfigurationAuthority)
	if remote.LatestConfigurationRevisionID == nil {
		state.ConfigurationRevisionID = types.StringNull()
	} else {
		state.ConfigurationRevisionID = types.StringValue(*remote.LatestConfigurationRevisionID)
	}
	if revision != nil {
		state.ConfigurationRevisionStatus = types.StringValue(revision.Status)
	} else if state.ConfigurationRevisionStatus.IsUnknown() {
		state.ConfigurationRevisionStatus = types.StringNull()
	}
}

func manifestValue(ctx context.Context, manifest client.Manifest) (types.Object, diag.Diagnostics) {
	tools, toolDiagnostics := types.SetValueFrom(ctx, types.StringType, manifest.Tools)
	bindings, bindingDiagnostics := types.SetValueFrom(ctx, types.StringType, manifest.RequiredBindings)
	budgets, budgetDiagnostics := types.ObjectValueFrom(ctx, budgetAttributeTypes(), budgetModel{
		MaxModelSteps:          types.Int64Value(manifest.Budgets.MaxModelSteps),
		MaxToolCalls:           types.Int64Value(manifest.Budgets.MaxToolCalls),
		TotalWallTimeMS:        types.Int64Value(manifest.Budgets.TotalWallTimeMS),
		ModelStepTimeMS:        types.Int64Value(manifest.Budgets.ModelStepTimeMS),
		StreamStallTimeMS:      types.Int64Value(manifest.Budgets.StreamStallTimeMS),
		MaxInputTokens:         types.Int64Value(manifest.Budgets.MaxInputTokens),
		MaxOutputTokens:        types.Int64Value(manifest.Budgets.MaxOutputTokens),
		MaxReasoningTokens:     types.Int64Value(manifest.Budgets.MaxReasoningTokens),
		MaxTotalTokens:         types.Int64Value(manifest.Budgets.MaxTotalTokens),
		MaxToolResultBytes:     types.Int64Value(manifest.Budgets.MaxToolResultBytes),
		MaxEstimatedCostMicros: types.Int64Value(manifest.Budgets.MaxEstimatedCostMicros),
	})
	value, diagnostics := types.ObjectValueFrom(ctx, manifestAttributeTypes(), manifestModel{
		SchemaVersion:            types.StringValue(manifest.SchemaVersion),
		Instructions:             types.StringValue(manifest.Instructions),
		ClassifierModelReleaseID: types.StringValue(manifest.Models.Classifier),
		WorkerModelReleaseID:     types.StringValue(manifest.Models.Worker),
		Tools:                    tools,
		RequiredBindings:         bindings,
		Budgets:                  budgets,
	})
	diagnostics.Append(toolDiagnostics...)
	diagnostics.Append(bindingDiagnostics...)
	diagnostics.Append(budgetDiagnostics...)
	return value, diagnostics
}

func manifestAttributeTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"schema_version":              types.StringType,
		"instructions":                types.StringType,
		"classifier_model_release_id": types.StringType,
		"worker_model_release_id":     types.StringType,
		"tools":                       types.SetType{ElemType: types.StringType},
		"required_bindings":           types.SetType{ElemType: types.StringType},
		"budgets":                     types.ObjectType{AttrTypes: budgetAttributeTypes()},
	}
}

func budgetAttributeTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"max_model_steps":           types.Int64Type,
		"max_tool_calls":            types.Int64Type,
		"total_wall_time_ms":        types.Int64Type,
		"model_step_time_ms":        types.Int64Type,
		"stream_stall_time_ms":      types.Int64Type,
		"max_input_tokens":          types.Int64Type,
		"max_output_tokens":         types.Int64Type,
		"max_reasoning_tokens":      types.Int64Type,
		"max_total_tokens":          types.Int64Type,
		"max_tool_result_bytes":     types.Int64Type,
		"max_estimated_cost_micros": types.Int64Type,
	}
}

func testReleaseAttributeTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"environment_id": types.StringType,
		"reason":         types.StringType,
	}
}

func planMetadataAttributeTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"manifest_digest":    types.StringType,
		"preconditions_json": types.StringType,
		"actions":            types.ListType{ElemType: types.StringType},
		"request_id":         types.StringType,
		"source_json":        types.StringType,
	}
}

func resourceIdentity(organizationID string, projectID string, agentDefinitionID string) string {
	return strings.Join([]string{organizationID, projectID, agentDefinitionID}, "/")
}
