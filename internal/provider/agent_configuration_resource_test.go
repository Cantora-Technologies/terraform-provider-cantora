package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Cantora-Technologies/terraform-provider-cantora/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const (
	testAPIKey            = "test-api-key"
	testOrganizationID    = "org_test"
	testProjectID         = "proj_test"
	testEnvironmentID     = "env_test"
	testAgentDefinitionID = "agentdefinition_test"
	testAgentPrincipalID  = "principal_test_agent"
)

func TestPlannedRequestIDIsStableAndContentBound(t *testing.T) {
	scope := client.Scope{OrganizationID: testOrganizationID, ProjectID: testProjectID}
	desired := testDesired("Reviewed instructions", "Reviewed activation")
	preview := client.Preview{ManifestDigest: manifestDigest(desired.Manifest), Preconditions: client.Preconditions{}}
	source := client.Source{Repository: "Cantora-Technologies/configuration", Commit: "0123456789abcdef"}

	first, diagnostics := plannedRequestID(scope, desired, preview, source)
	if diagnostics.HasError() {
		t.Fatalf("create first request identifier: %v", diagnostics)
	}
	second, diagnostics := plannedRequestID(scope, desired, preview, source)
	if diagnostics.HasError() {
		t.Fatalf("create second request identifier: %v", diagnostics)
	}
	if first != second {
		t.Fatalf("expected stable request identifier, got %q and %q", first, second)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(first) {
		t.Fatalf("unexpected request identifier %q", first)
	}

	desired.TestRelease.Reason = "Changed activation"
	changed, diagnostics := plannedRequestID(scope, desired, preview, source)
	if diagnostics.HasError() {
		t.Fatalf("create changed request identifier: %v", diagnostics)
	}
	if changed == first {
		t.Fatal("expected changed apply content to produce a different request identifier")
	}
}

func TestAccAgentConfigurationLifecycle(t *testing.T) {
	api := newFakeManagementAPI()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories(server),
		CheckDestroy:             api.checkRetained(2, 3),
		Steps: []resource.TestStep{
			{
				Config: providerConfig(server.URL, "run-1") + agentConfigurationConfig("First instructions", "First reviewed activation"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "agent_definition_id", testAgentDefinitionID),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "agent_version_id", "agentversion_1"),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "test_agent_release_id", "agentrelease_1"),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "release_generation", "1"),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "configuration_revision_status", "Succeeded"),
					api.checkCalls(1),
				),
			},
			{
				Config: providerConfig(server.URL, "run-2") + agentConfigurationConfig("First instructions", "First reviewed activation"),
				PreConfig: func() {
					api.setLatestRevisionStatus("Failed")
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "configuration_revision_status", "Failed"),
					api.checkCalls(1),
				),
			},
			{
				Config: providerConfig(server.URL, "run-3") + agentConfigurationConfig("First instructions", "Updated rationale"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "agent_version_id", "agentversion_1"),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "test_agent_release_id", "agentrelease_2"),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "release_generation", "2"),
					api.checkCalls(2),
				),
			},
			{
				Config: providerConfig(server.URL, "run-4") + agentConfigurationConfig("Second instructions", "Updated behavior"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "agent_version_id", "agentversion_2"),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "test_agent_release_id", "agentrelease_3"),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "release_generation", "3"),
					api.checkCalls(3),
				),
			},
		},
	})
}

func TestAccAgentConfigurationRejectsInvalidLocalStructure(t *testing.T) {
	api := newFakeManagementAPI()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	invalid := strings.Replace(
		agentConfigurationConfig("Reviewed instructions", "Reviewed activation"),
		"max_model_steps           = 12",
		"max_model_steps           = 0",
		1,
	)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories(server),
		Steps: []resource.TestStep{{
			Config:      providerConfig(server.URL, "run-1") + invalid,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile("Invalid finite budget"),
		}},
	})
}

func TestAccAgentConfigurationDefersPreviewUntilDependenciesAreKnown(t *testing.T) {
	api := newFakeManagementAPI()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories(server),
		CheckDestroy:             api.checkRetained(1, 1),
		Steps: []resource.TestStep{{
			Config: configurationWithUnknownDependencies(server.URL),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("cantora_agent_configuration.test", "organization_id", testOrganizationID),
				resource.TestCheckResourceAttr("cantora_agent_configuration.test", "test_release.environment_id", testEnvironmentID),
				api.checkCalls(1),
			),
		}},
	})
}

func configurationWithUnknownDependencies(endpoint string) string {
	return providerConfig(endpoint, "run-1") + `
resource "terraform_data" "scope" {
  input = {
    organization_id = "org_test"
    project_id      = "proj_test"
    environment_id  = "env_test"
  }
}
` + strings.NewReplacer(
		fmt.Sprintf("organization_id = %q", testOrganizationID), "organization_id = terraform_data.scope.output.organization_id",
		fmt.Sprintf("project_id      = %q", testProjectID), "project_id      = terraform_data.scope.output.project_id",
		fmt.Sprintf("environment_id = %q", testEnvironmentID), "environment_id = terraform_data.scope.output.environment_id",
	).Replace(agentConfigurationConfig("Reviewed instructions", "Reviewed activation"))
}

func TestAccAgentConfigurationDetectsAndCorrectsDrift(t *testing.T) {
	api := newFakeManagementAPI()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	config := providerConfig(server.URL, "run-1") + agentConfigurationConfig("Reviewed instructions", "Reviewed activation")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories(server),
		CheckDestroy:             api.checkRetained(2, 3),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:          func() { api.activateOutOfBand("Out-of-band instructions") },
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "manifest.instructions", "Reviewed instructions"),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "agent_version_id", "agentversion_1"),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "release_generation", "3"),
				),
			},
		},
	})
}

func TestAccAgentConfigurationRequiresExplicitImport(t *testing.T) {
	api := newFakeManagementAPI()
	api.seedExisting()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	config := providerConfig(server.URL, "run-1") + agentConfigurationConfig("Imported instructions", "Imported activation")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories(server),
		CheckDestroy:             api.checkRetained(1, 1),
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile("import it explicitly"),
			},
			{
				ResourceName:      "cantora_agent_configuration.test",
				ImportState:       true,
				ImportStateId:     strings.Join([]string{testOrganizationID, testProjectID, testAgentDefinitionID}, "/"),
				ImportStateVerify: false,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "agent_definition_id", testAgentDefinitionID),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "manifest.instructions", "Imported instructions"),
					resource.TestCheckResourceAttr("cantora_agent_configuration.test", "test_release.reason", "Imported activation"),
				),
			},
		},
	})
}

func providerFactories(server *httptest.Server) map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"cantora": providerserver.NewProtocol6WithError(NewWithHTTPClient("test", server.Client())()),
	}
}

func providerConfig(endpoint string, run string) string {
	return fmt.Sprintf(`
provider "cantora" {
  endpoint          = %q
  api_key           = %q
	request_timeout   = "5s"
  source_repository = "Cantora-Technologies/configuration"
  source_commit     = "0123456789abcdef"
  source_path       = "agents/aria.tf"
  source_workflow   = "terraform"
  source_run        = %q
  source_actor      = "acceptance-test"
}
`, endpoint, testAPIKey, run)
}

func agentConfigurationConfig(instructions string, reason string) string {
	return fmt.Sprintf(`
resource "cantora_agent_configuration" "test" {
  organization_id = %q
  project_id      = %q
  key             = "aria"
  display_name    = "Aria"

  manifest {
    schema_version              = "config.cantora.ai/agent-version/v1alpha1"
    instructions                = %q
    classifier_model_release_id = "model_release_classifier"
    worker_model_release_id     = "model_release_worker"
    tools                       = ["run_code", "google_drive"]
    required_bindings           = []

    budgets {
      max_model_steps           = 12
      max_tool_calls            = 8
      total_wall_time_ms        = 60000
      model_step_time_ms        = 30000
      stream_stall_time_ms      = 10000
      max_input_tokens          = 100000
      max_output_tokens         = 10000
      max_reasoning_tokens      = 5000
      max_total_tokens          = 110000
      max_tool_result_bytes     = 1000000
      max_estimated_cost_micros = 500000
    }
  }

  test_release {
    environment_id = %q
    reason          = %q
  }
}
`, testOrganizationID, testProjectID, instructions, testEnvironmentID, reason)
}

type applyRequest struct {
	RequestID      string                           `json:"requestId"`
	Desired        client.DesiredAgentConfiguration `json:"desired"`
	ManifestDigest string                           `json:"manifestDigest"`
	Preconditions  client.Preconditions             `json:"preconditions"`
	Source         client.Source                    `json:"source"`
}

type fakeManagementAPI struct {
	mu               sync.Mutex
	configuration    *client.AgentConfiguration
	manifestByDigest map[string]client.Manifest
	versionByDigest  map[string]string
	requestResults   map[string]client.ApplyResult
	versionCount     int
	releaseCount     int
	applyCalls       int
}

func newFakeManagementAPI() *fakeManagementAPI {
	return &fakeManagementAPI{
		manifestByDigest: make(map[string]client.Manifest),
		versionByDigest:  make(map[string]string),
		requestResults:   make(map[string]client.ApplyResult),
	}
}

func (api *fakeManagementAPI) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	api.mu.Lock()
	defer api.mu.Unlock()
	if request.Header.Get("Authorization") != "Bearer "+testAPIKey {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"_tag": "Unauthorized"})
		return
	}
	base := fmt.Sprintf("/v1/organizations/%s/projects/%s", testOrganizationID, testProjectID)
	switch {
	case request.Method == http.MethodPost && request.URL.Path == base+"/agent-configuration-previews":
		var body struct {
			Desired client.DesiredAgentConfiguration `json:"desired"`
		}
		if !decode(response, request, &body) {
			return
		}
		preview, status, failure := api.preview(body.Desired)
		if status != http.StatusOK {
			writeJSON(response, status, failure)
			return
		}
		writeJSON(response, http.StatusOK, preview)
	case request.Method == http.MethodPost && request.URL.Path == base+"/agent-configuration-applies":
		var body applyRequest
		if !decode(response, request, &body) {
			return
		}
		result, status, failure := api.apply(body)
		if status != http.StatusOK {
			writeJSON(response, status, failure)
			return
		}
		writeJSON(response, http.StatusOK, result)
	case request.Method == http.MethodGet && request.URL.Path == base+"/agent-configurations/"+testAgentDefinitionID:
		if api.configuration == nil {
			writeJSON(response, http.StatusNotFound, map[string]any{"_tag": "NotFound", "resource": "agentConfiguration"})
			return
		}
		writeJSON(response, http.StatusOK, api.configuration)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, base+"/configuration-revisions/"):
		revisionID := strings.TrimPrefix(request.URL.Path, base+"/configuration-revisions/")
		for _, result := range api.requestResults {
			if result.Revision.ConfigurationRevisionID == revisionID {
				writeJSON(response, http.StatusOK, result.Revision)
				return
			}
		}
		writeJSON(response, http.StatusNotFound, map[string]any{"_tag": "NotFound", "resource": "configurationRevision"})
	default:
		writeJSON(response, http.StatusNotFound, map[string]any{"_tag": "NotFound", "resource": "route"})
	}
}

func (api *fakeManagementAPI) preview(desired client.DesiredAgentConfiguration) (client.Preview, int, any) {
	if desired.Intent == "create" && api.configuration != nil {
		return client.Preview{}, http.StatusConflict, map[string]any{
			"_tag": "Conflict", "resource": "agentDefinition", "reason": "this Project already has this key; import it explicitly",
		}
	}
	if desired.Intent == "update" && (api.configuration == nil || desired.AgentDefinitionID == nil || *desired.AgentDefinitionID != testAgentDefinitionID) {
		return client.Preview{}, http.StatusNotFound, map[string]any{"_tag": "NotFound", "resource": "agentDefinition"}
	}
	digest := manifestDigest(desired.Manifest)
	actions := []string{"reuseAgentVersion", "reuseTestRelease"}
	if api.configuration == nil {
		actions = []string{"createAgentDefinition", "createTestAgent", "publishAgentVersion", "activateTestRelease"}
	} else if api.configuration.CurrentManifestDigest == nil || *api.configuration.CurrentManifestDigest != digest {
		actions = []string{"publishAgentVersion", "activateTestRelease"}
	} else if api.configuration.CurrentTestAgentReleaseReason == nil || *api.configuration.CurrentTestAgentReleaseReason != desired.TestRelease.Reason {
		actions = []string{"reuseAgentVersion", "activateTestRelease"}
	}
	return client.Preview{
		ManifestDigest: digest,
		Actions:        actions,
		Preconditions:  api.preconditions(),
		Warnings:       []client.Warning{},
	}, http.StatusOK, nil
}

func (api *fakeManagementAPI) apply(request applyRequest) (client.ApplyResult, int, any) {
	api.applyCalls++
	if prior, ok := api.requestResults[request.RequestID]; ok {
		return prior, http.StatusOK, nil
	}
	preview, status, failure := api.preview(request.Desired)
	if status != http.StatusOK {
		return client.ApplyResult{}, status, failure
	}
	if request.ManifestDigest != preview.ManifestDigest || !reflect.DeepEqual(request.Preconditions, preview.Preconditions) {
		return client.ApplyResult{}, http.StatusPreconditionFailed, map[string]any{
			"_tag": "PreconditionFailed", "resource": "agentConfiguration", "currentEtag": generationETag(api.generation()),
		}
	}
	digest := request.ManifestDigest
	versionID := api.versionByDigest[digest]
	if versionID == "" {
		api.versionCount++
		versionID = "agentversion_" + strconv.Itoa(api.versionCount)
		api.manifestByDigest[digest] = request.Desired.Manifest
		api.versionByDigest[digest] = versionID
	}
	releaseID := ""
	releaseGeneration := api.generation()
	if slicesContain(preview.Actions, "activateTestRelease") {
		api.releaseCount++
		releaseGeneration = int64(api.releaseCount)
		releaseID = "agentrelease_" + strconv.Itoa(api.releaseCount)
	} else if api.configuration != nil && api.configuration.CurrentTestAgentReleaseID != nil {
		releaseID = *api.configuration.CurrentTestAgentReleaseID
	}
	revisionID := "configurationrevision_" + strconv.Itoa(api.applyCalls)
	definitionID := testAgentDefinitionID
	principalID := testAgentPrincipalID
	environmentID := request.Desired.TestRelease.EnvironmentID
	reason := request.Desired.TestRelease.Reason
	manifest := request.Desired.Manifest
	api.configuration = &client.AgentConfiguration{
		OrganizationID:                testOrganizationID,
		ProjectID:                     testProjectID,
		AgentDefinitionID:             definitionID,
		Key:                           request.Desired.Key,
		DisplayName:                   request.Desired.DisplayName,
		Status:                        "active",
		ConfigurationAuthority:        "ApiManaged",
		TestEnvironmentID:             &environmentID,
		TestAgentPrincipalID:          &principalID,
		CurrentAgentVersionID:         &versionID,
		CurrentManifest:               &manifest,
		CurrentManifestDigest:         &digest,
		CurrentTestAgentReleaseID:     &releaseID,
		CurrentTestAgentReleaseReason: &reason,
		TestReleaseGeneration:         releaseGeneration,
		TestReleaseETag:               generationETag(releaseGeneration),
		LatestConfigurationRevisionID: &revisionID,
	}
	result := client.ApplyResult{
		Revision: client.ConfigurationRevision{
			ConfigurationRevisionID: revisionID,
			RequestID:               request.RequestID,
			OrganizationID:          testOrganizationID,
			ProjectID:               testProjectID,
			AgentDefinitionID:       &definitionID,
			Status:                  "Succeeded",
			Progress: client.RevisionProgress{
				AgentDefinition: "Succeeded", TestAgent: "Succeeded", AgentVersion: "Succeeded", TestRelease: "Succeeded",
			},
		},
		Configuration: *api.configuration,
	}
	api.requestResults[request.RequestID] = result
	return result, http.StatusOK, nil
}

func (api *fakeManagementAPI) preconditions() client.Preconditions {
	if api.configuration == nil {
		return client.Preconditions{}
	}
	return client.Preconditions{
		AgentDefinitionID:      stringPointer(api.configuration.AgentDefinitionID),
		DefinitionKey:          stringPointer(api.configuration.Key),
		DefinitionDisplayName:  stringPointer(api.configuration.DisplayName),
		DefinitionStatus:       stringPointer(api.configuration.Status),
		ConfigurationAuthority: stringPointer(api.configuration.ConfigurationAuthority),
		TestAgentPrincipalID:   api.configuration.TestAgentPrincipalID,
		TestReleaseGeneration:  api.configuration.TestReleaseGeneration,
		CurrentManifestDigest:  api.configuration.CurrentManifestDigest,
	}
}

func (api *fakeManagementAPI) generation() int64 {
	if api.configuration == nil {
		return 0
	}
	return api.configuration.TestReleaseGeneration
}

func (api *fakeManagementAPI) seedExisting() {
	api.mu.Lock()
	defer api.mu.Unlock()
	desired := testDesired("Imported instructions", "Imported activation")
	preview, _, _ := api.preview(desired)
	_, _, _ = api.apply(applyRequest{
		RequestID: "seed-request", Desired: desired, ManifestDigest: preview.ManifestDigest, Preconditions: preview.Preconditions,
	})
}

func (api *fakeManagementAPI) activateOutOfBand(instructions string) {
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.configuration == nil {
		panic("cannot drift an absent configuration")
	}
	manifest := testManifest(instructions)
	digest := manifestDigest(manifest)
	api.versionCount++
	api.releaseCount++
	versionID := "agentversion_" + strconv.Itoa(api.versionCount)
	releaseID := "agentrelease_" + strconv.Itoa(api.releaseCount)
	reason := "Out-of-band activation"
	api.manifestByDigest[digest] = manifest
	api.versionByDigest[digest] = versionID
	api.configuration.CurrentManifest = &manifest
	api.configuration.CurrentManifestDigest = &digest
	api.configuration.CurrentAgentVersionID = &versionID
	api.configuration.CurrentTestAgentReleaseID = &releaseID
	api.configuration.CurrentTestAgentReleaseReason = &reason
	api.configuration.TestReleaseGeneration = int64(api.releaseCount)
	api.configuration.TestReleaseETag = generationETag(int64(api.releaseCount))
}

func (api *fakeManagementAPI) setLatestRevisionStatus(status string) {
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.configuration == nil || api.configuration.LatestConfigurationRevisionID == nil {
		panic("cannot update an absent ConfigurationRevision")
	}
	for requestID, result := range api.requestResults {
		if result.Revision.ConfigurationRevisionID == *api.configuration.LatestConfigurationRevisionID {
			result.Revision.Status = status
			api.requestResults[requestID] = result
			return
		}
	}
	panic("latest ConfigurationRevision could not be found")
}

func (api *fakeManagementAPI) checkCalls(want int) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.applyCalls != want {
			return fmt.Errorf("expected %d apply calls, got %d", want, api.applyCalls)
		}
		return nil
	}
}

func (api *fakeManagementAPI) checkRetained(versions int, releases int) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.versionCount != versions || api.releaseCount != releases {
			return fmt.Errorf("expected %d retained Versions and %d Releases, got %d and %d", versions, releases, api.versionCount, api.releaseCount)
		}
		return nil
	}
}

func testDesired(instructions string, reason string) client.DesiredAgentConfiguration {
	return client.DesiredAgentConfiguration{
		Intent: "create", Key: "aria", DisplayName: "Aria", Manifest: testManifest(instructions),
		TestRelease: client.TestRelease{EnvironmentID: testEnvironmentID, Reason: reason},
	}
}

func testManifest(instructions string) client.Manifest {
	return client.Manifest{
		SchemaVersion: "config.cantora.ai/agent-version/v1alpha1", Instructions: instructions,
		Models: client.Models{Classifier: "model_release_classifier", Worker: "model_release_worker"},
		Tools:  []string{"google_drive", "run_code"}, RequiredBindings: []string{},
		Budgets: client.Budgets{
			MaxModelSteps: 12, MaxToolCalls: 8, TotalWallTimeMS: 60000, ModelStepTimeMS: 30000,
			StreamStallTimeMS: 10000, MaxInputTokens: 100000, MaxOutputTokens: 10000,
			MaxReasoningTokens: 5000, MaxTotalTokens: 110000, MaxToolResultBytes: 1000000,
			MaxEstimatedCostMicros: 500000,
		},
	}
}

func manifestDigest(manifest client.Manifest) string {
	payload, _ := json.Marshal(manifest)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func generationETag(generation int64) string { return `"` + strconv.FormatInt(generation, 10) + `"` }
func stringPointer(value string) *string     { return &value }

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func decode(response http.ResponseWriter, request *http.Request, target any) bool {
	if err := json.NewDecoder(request.Body).Decode(target); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]any{"_tag": "BadRequest", "reason": err.Error()})
		return false
	}
	return true
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
