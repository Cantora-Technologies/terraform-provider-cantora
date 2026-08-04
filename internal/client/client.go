package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const DefaultRequestTimeout = 30 * time.Second

var dependencyKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	userAgent  string
}

type Scope struct {
	OrganizationID string `json:"organizationId"`
	ProjectID      string `json:"projectId"`
}

type Source struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Path       string `json:"path"`
	Workflow   string `json:"workflow"`
	Run        string `json:"run"`
	Actor      string `json:"actor"`
}

type Models struct {
	Classifier string `json:"classifier"`
	Worker     string `json:"worker"`
}

type Budgets struct {
	MaxModelSteps          int64 `json:"maxModelSteps"`
	MaxToolCalls           int64 `json:"maxToolCalls"`
	TotalWallTimeMS        int64 `json:"totalWallTimeMs"`
	ModelStepTimeMS        int64 `json:"modelStepTimeMs"`
	StreamStallTimeMS      int64 `json:"streamStallTimeMs"`
	MaxInputTokens         int64 `json:"maxInputTokens"`
	MaxOutputTokens        int64 `json:"maxOutputTokens"`
	MaxReasoningTokens     int64 `json:"maxReasoningTokens"`
	MaxTotalTokens         int64 `json:"maxTotalTokens"`
	MaxToolResultBytes     int64 `json:"maxToolResultBytes"`
	MaxEstimatedCostMicros int64 `json:"maxEstimatedCostMicros"`
}

type Manifest struct {
	SchemaVersion    string   `json:"schemaVersion"`
	Instructions     string   `json:"instructions"`
	Models           Models   `json:"models"`
	Tools            []string `json:"tools"`
	RequiredBindings []string `json:"requiredBindings"`
	Budgets          Budgets  `json:"budgets"`
}

type TestRelease struct {
	EnvironmentID string `json:"environmentId"`
	Reason        string `json:"reason"`
}

type DesiredAgentConfiguration struct {
	Intent            string      `json:"intent"`
	AgentDefinitionID *string     `json:"agentDefinitionId"`
	Key               string      `json:"key"`
	DisplayName       string      `json:"displayName"`
	Manifest          Manifest    `json:"manifest"`
	TestRelease       TestRelease `json:"testRelease"`
}

type Preconditions struct {
	AgentDefinitionID      *string `json:"agentDefinitionId"`
	DefinitionKey          *string `json:"definitionKey"`
	DefinitionDisplayName  *string `json:"definitionDisplayName"`
	DefinitionStatus       *string `json:"definitionStatus"`
	ConfigurationAuthority *string `json:"configurationAuthority"`
	TestAgentPrincipalID   *string `json:"testAgentPrincipalId"`
	TestReleaseGeneration  int64   `json:"testReleaseGeneration"`
	CurrentManifestDigest  *string `json:"currentManifestDigest"`
}

type Preview struct {
	ManifestDigest string        `json:"manifestDigest"`
	Actions        []string      `json:"actions"`
	Preconditions  Preconditions `json:"preconditions"`
	Warnings       []Warning     `json:"warnings"`
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RevisionProgress struct {
	AgentDefinition string `json:"agentDefinition"`
	TestAgent       string `json:"testAgent"`
	AgentVersion    string `json:"agentVersion"`
	TestRelease     string `json:"testRelease"`
}

type ConfigurationRevision struct {
	ConfigurationRevisionID string           `json:"configurationRevisionId"`
	RequestID               string           `json:"requestId"`
	OrganizationID          string           `json:"organizationId"`
	ProjectID               string           `json:"projectId"`
	AgentDefinitionID       *string          `json:"agentDefinitionId"`
	Status                  string           `json:"status"`
	Progress                RevisionProgress `json:"progress"`
}

type AgentConfiguration struct {
	OrganizationID                string    `json:"organizationId"`
	ProjectID                     string    `json:"projectId"`
	AgentDefinitionID             string    `json:"agentDefinitionId"`
	Key                           string    `json:"key"`
	DisplayName                   string    `json:"displayName"`
	Status                        string    `json:"status"`
	ConfigurationAuthority        string    `json:"configurationAuthority"`
	TestEnvironmentID             *string   `json:"testEnvironmentId"`
	TestAgentPrincipalID          *string   `json:"testAgentPrincipalId"`
	CurrentAgentVersionID         *string   `json:"currentAgentVersionId"`
	CurrentManifest               *Manifest `json:"currentManifest"`
	CurrentManifestDigest         *string   `json:"currentManifestDigest"`
	CurrentTestAgentReleaseID     *string   `json:"currentTestAgentReleaseId"`
	CurrentTestAgentReleaseReason *string   `json:"currentTestAgentReleaseReason"`
	TestReleaseGeneration         int64     `json:"testReleaseGeneration"`
	TestReleaseETag               string    `json:"testReleaseEtag"`
	LatestConfigurationRevisionID *string   `json:"latestConfigurationRevisionId"`
}

type ApplyResult struct {
	Revision      ConfigurationRevision `json:"revision"`
	Configuration AgentConfiguration    `json:"configuration"`
}

type APIError struct {
	StatusCode            int
	Tag                   string
	Resource              string
	Reason                string
	CurrentAgentReleaseID *string
	CurrentETag           string
	Body                  string
}

type transportError struct {
	cause error
}

func (e *transportError) Error() string {
	return fmt.Sprintf("call Cantora Management API: %v", e.cause)
}

func (e *transportError) Unwrap() error {
	return e.cause
}

func (e *APIError) Error() string {
	detail := e.Reason
	if detail == "" {
		detail = e.Body
	}
	if detail == "" {
		detail = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("Cantora Management API returned %d: %s", e.StatusCode, detail)
}

func IsNotFound(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) && apiError.StatusCode == http.StatusNotFound
}

func New(baseURL string, apiKey string, version string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("endpoint must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("endpoint must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("endpoint must not contain credentials, a query, or a fragment")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("api key must not be empty")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	configuredHTTPClient := *httpClient
	if configuredHTTPClient.Timeout <= 0 {
		configuredHTTPClient.Timeout = DefaultRequestTimeout
	}
	return &Client{
		baseURL:    parsed.String(),
		apiKey:     apiKey,
		httpClient: &configuredHTTPClient,
		userAgent:  "terraform-provider-cantora/" + version,
	}, nil
}

func (c *Client) PreviewAgentConfiguration(
	ctx context.Context,
	scope Scope,
	desired DesiredAgentConfiguration,
) (Preview, error) {
	var response Preview
	err := c.request(ctx, http.MethodPost, previewPath(scope), struct {
		Desired DesiredAgentConfiguration `json:"desired"`
	}{Desired: desired}, &response)
	if err == nil {
		err = validatePreview(response)
	}
	return response, err
}

func (c *Client) ApplyAgentConfiguration(
	ctx context.Context,
	scope Scope,
	requestID string,
	desired DesiredAgentConfiguration,
	preview Preview,
	source Source,
) (ApplyResult, error) {
	request := struct {
		RequestID      string                    `json:"requestId"`
		Desired        DesiredAgentConfiguration `json:"desired"`
		ManifestDigest string                    `json:"manifestDigest"`
		Preconditions  Preconditions             `json:"preconditions"`
		Source         Source                    `json:"source"`
	}{
		RequestID:      requestID,
		Desired:        desired,
		ManifestDigest: preview.ManifestDigest,
		Preconditions:  preview.Preconditions,
		Source:         source,
	}
	var response ApplyResult
	err := c.request(ctx, http.MethodPost, applyPath(scope), request, &response)
	if err != nil && ctx.Err() == nil {
		var ambiguous *transportError
		if errors.As(err, &ambiguous) {
			response = ApplyResult{}
			err = c.request(ctx, http.MethodPost, applyPath(scope), request, &response)
		}
	}
	if err == nil {
		err = validateApplyResult(response, scope, requestID, desired.AgentDefinitionID)
	}
	return response, err
}

func (c *Client) GetAgentConfiguration(
	ctx context.Context,
	scope Scope,
	agentDefinitionID string,
) (AgentConfiguration, error) {
	var response AgentConfiguration
	err := c.request(ctx, http.MethodGet, configurationPath(scope, agentDefinitionID), nil, &response)
	if err == nil {
		err = validateAgentConfiguration(response, scope, agentDefinitionID)
	}
	return response, err
}

func (c *Client) GetConfigurationRevision(
	ctx context.Context,
	scope Scope,
	configurationRevisionID string,
) (ConfigurationRevision, error) {
	var response ConfigurationRevision
	err := c.request(ctx, http.MethodGet, configurationRevisionPath(scope, configurationRevisionID), nil, &response)
	if err == nil {
		err = validateConfigurationRevision(response, scope, configurationRevisionID)
	}
	return response, err
}

func validatePreview(preview Preview) error {
	if err := validateDigest(preview.ManifestDigest); err != nil {
		return fmt.Errorf("validate Management API preview: %w", err)
	}
	for _, action := range preview.Actions {
		switch action {
		case "createAgentDefinition", "updateAgentDefinition", "createTestAgent", "publishAgentVersion",
			"reuseAgentVersion", "activateTestRelease", "reuseTestRelease":
		default:
			return fmt.Errorf("validate Management API preview: unknown action %q", action)
		}
	}
	if preview.Preconditions.TestReleaseGeneration < 0 {
		return fmt.Errorf("validate Management API preview: negative Test release generation")
	}
	for _, identifier := range []*string{
		preview.Preconditions.AgentDefinitionID,
		preview.Preconditions.DefinitionKey,
		preview.Preconditions.TestAgentPrincipalID,
		preview.Preconditions.CurrentManifestDigest,
	} {
		if identifier != nil && *identifier == "" {
			return fmt.Errorf("validate Management API preview: empty precondition value")
		}
	}
	return nil
}

func validateApplyResult(result ApplyResult, scope Scope, requestID string, expectedAgentDefinitionID *string) error {
	if err := validateConfigurationRevision(result.Revision, scope, result.Revision.ConfigurationRevisionID); err != nil {
		return fmt.Errorf("validate Management API apply: %w", err)
	}
	if result.Revision.RequestID != requestID {
		return fmt.Errorf("validate Management API apply: invalid ConfigurationRevision identity")
	}
	if result.Revision.AgentDefinitionID == nil ||
		*result.Revision.AgentDefinitionID != result.Configuration.AgentDefinitionID {
		return fmt.Errorf("validate Management API apply: ConfigurationRevision Definition mismatch")
	}
	if expectedAgentDefinitionID != nil && result.Configuration.AgentDefinitionID != *expectedAgentDefinitionID {
		return fmt.Errorf("validate Management API apply: unexpected Agent Definition identity")
	}
	if result.Revision.Status != "Succeeded" {
		return fmt.Errorf("validate Management API apply: unexpected ConfigurationRevision status %q", result.Revision.Status)
	}
	for _, step := range []string{
		result.Revision.Progress.AgentDefinition,
		result.Revision.Progress.TestAgent,
		result.Revision.Progress.AgentVersion,
		result.Revision.Progress.TestRelease,
	} {
		if step != "Succeeded" {
			return fmt.Errorf("validate Management API apply: unexpected ConfigurationRevision step status %q", step)
		}
	}
	return validateAgentConfiguration(result.Configuration, scope, result.Configuration.AgentDefinitionID)
}

func validateConfigurationRevision(revision ConfigurationRevision, scope Scope, expectedID string) error {
	if revision.ConfigurationRevisionID == "" || revision.ConfigurationRevisionID != expectedID || revision.RequestID == "" {
		return fmt.Errorf("invalid ConfigurationRevision identity")
	}
	if revision.OrganizationID != scope.OrganizationID || revision.ProjectID != scope.ProjectID {
		return fmt.Errorf("ConfigurationRevision scope mismatch")
	}
	switch revision.Status {
	case "Pending", "Running", "Succeeded", "Failed":
	default:
		return fmt.Errorf("unknown ConfigurationRevision status %q", revision.Status)
	}
	for _, step := range []string{
		revision.Progress.AgentDefinition,
		revision.Progress.TestAgent,
		revision.Progress.AgentVersion,
		revision.Progress.TestRelease,
	} {
		if step != "Pending" && step != "Succeeded" {
			return fmt.Errorf("unknown ConfigurationRevision step status %q", step)
		}
	}
	return nil
}

func validateAgentConfiguration(configuration AgentConfiguration, scope Scope, expectedAgentDefinitionID string) error {
	if configuration.OrganizationID != scope.OrganizationID || configuration.ProjectID != scope.ProjectID {
		return fmt.Errorf("validate Management API Agent Configuration: scope mismatch")
	}
	if configuration.AgentDefinitionID == "" || configuration.AgentDefinitionID != expectedAgentDefinitionID ||
		configuration.Key == "" || configuration.DisplayName == "" {
		return fmt.Errorf("validate Management API Agent Configuration: invalid Definition identity")
	}
	if configuration.Status != "active" && configuration.Status != "disabled" {
		return fmt.Errorf("validate Management API Agent Configuration: unknown Definition status %q", configuration.Status)
	}
	if configuration.ConfigurationAuthority != "ApiManaged" && configuration.ConfigurationAuthority != "DashboardManaged" {
		return fmt.Errorf(
			"validate Management API Agent Configuration: unknown configuration authority %q",
			configuration.ConfigurationAuthority,
		)
	}
	if configuration.TestReleaseGeneration < 0 || configuration.TestReleaseETag == "" {
		return fmt.Errorf("validate Management API Agent Configuration: invalid Test release generation")
	}
	if configuration.CurrentManifestDigest != nil {
		if err := validateDigest(*configuration.CurrentManifestDigest); err != nil {
			return fmt.Errorf("validate Management API Agent Configuration: %w", err)
		}
	}
	if configuration.CurrentManifest != nil {
		if err := validateManifest(*configuration.CurrentManifest); err != nil {
			return fmt.Errorf("validate Management API Agent Configuration: %w", err)
		}
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != "config.cantora.ai/agent-version/v1alpha1" || manifest.Models.Classifier == "" ||
		manifest.Models.Worker == "" {
		return fmt.Errorf("invalid Agent Version manifest")
	}
	if err := validateDependencyKeys(manifest.Tools); err != nil {
		return err
	}
	if err := validateDependencyKeys(manifest.RequiredBindings); err != nil {
		return err
	}
	budgets := manifest.Budgets
	if !between(budgets.MaxModelSteps, 1, 100) || !between(budgets.MaxToolCalls, 0, 100) ||
		!between(budgets.TotalWallTimeMS, 1_000, 300_000) ||
		!between(budgets.ModelStepTimeMS, 1_000, 300_000) ||
		!between(budgets.StreamStallTimeMS, 1_000, 300_000) ||
		!between(budgets.MaxInputTokens, 1, 10_000_000) ||
		!between(budgets.MaxOutputTokens, 1, 1_000_000) ||
		!between(budgets.MaxReasoningTokens, 0, 1_000_000) ||
		!between(budgets.MaxTotalTokens, 1, 10_000_000) ||
		!between(budgets.MaxToolResultBytes, 1, 10_000_000) ||
		!between(budgets.MaxEstimatedCostMicros, 1, 1_000_000_000) ||
		budgets.ModelStepTimeMS > budgets.TotalWallTimeMS ||
		budgets.StreamStallTimeMS > budgets.ModelStepTimeMS ||
		budgets.MaxTotalTokens < max(budgets.MaxInputTokens, budgets.MaxOutputTokens, budgets.MaxReasoningTokens) {
		return fmt.Errorf("invalid Agent Version manifest budgets")
	}
	return nil
}

func validateDependencyKeys(keys []string) error {
	if len(keys) > 100 {
		return fmt.Errorf("invalid Agent Version manifest dependency count")
	}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if len(key) > 100 || !dependencyKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid Agent Version manifest dependency key")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate Agent Version manifest dependency key")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func between(value int64, minimum int64, maximum int64) bool {
	return value >= minimum && value <= maximum
}

func validateDigest(value string) error {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid SHA-256 manifest digest")
	}
	return nil
}

func (c *Client) request(ctx context.Context, method string, path string, body any, response any) error {
	var encoded io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Management API request: %w", err)
		}
		encoded = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, encoded)
	if err != nil {
		return fmt.Errorf("create Management API request: %w", err)
	}
	requestIdentifier, err := requestID()
	if err != nil {
		return fmt.Errorf("create Management API request identifier: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("X-Request-ID", requestIdentifier)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	httpResponse, err := c.httpClient.Do(request)
	if err != nil {
		return &transportError{cause: err}
	}
	defer httpResponse.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, 1_048_577))
	if err != nil {
		return &transportError{cause: fmt.Errorf("read Management API response: %w", err)}
	}
	if len(responseBody) > 1_048_576 {
		return fmt.Errorf("management API response exceeded 1048576 bytes")
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		var failure struct {
			Tag                   string  `json:"_tag"`
			Resource              string  `json:"resource"`
			Reason                string  `json:"reason"`
			CurrentAgentReleaseID *string `json:"currentAgentReleaseId"`
			CurrentETag           string  `json:"currentEtag"`
		}
		_ = json.Unmarshal(responseBody, &failure)
		failure.Reason = c.redactCredential(failure.Reason)
		return &APIError{
			StatusCode:            httpResponse.StatusCode,
			Tag:                   failure.Tag,
			Resource:              failure.Resource,
			Reason:                failure.Reason,
			CurrentAgentReleaseID: failure.CurrentAgentReleaseID,
			CurrentETag:           failure.CurrentETag,
			Body:                  c.redactCredential(strings.TrimSpace(string(responseBody))),
		}
	}
	if response == nil || len(responseBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(responseBody, response); err != nil {
		return fmt.Errorf("decode Management API response: %w", err)
	}
	return nil
}

func (c *Client) redactCredential(value string) string {
	return strings.ReplaceAll(value, c.apiKey, "[redacted]")
}

func projectPath(scope Scope) string {
	return fmt.Sprintf("/v1/organizations/%s/projects/%s", url.PathEscape(scope.OrganizationID), url.PathEscape(scope.ProjectID))
}

func previewPath(scope Scope) string {
	return projectPath(scope) + "/agent-configuration-previews"
}

func applyPath(scope Scope) string {
	return projectPath(scope) + "/agent-configuration-applies"
}

func configurationPath(scope Scope, agentDefinitionID string) string {
	return projectPath(scope) + "/agent-configurations/" + url.PathEscape(agentDefinitionID)
}

func configurationRevisionPath(scope Scope, configurationRevisionID string) string {
	return projectPath(scope) + "/configuration-revisions/" + url.PathEscape(configurationRevisionID)
}

func requestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		value[0:4],
		value[4:6],
		value[6:8],
		value[8:10],
		value[10:16],
	), nil
}
