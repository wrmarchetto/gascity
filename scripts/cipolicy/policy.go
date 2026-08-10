// Package cipolicy validates the execution-affecting shape of required CI workflows.
package cipolicy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	setupGoAction = "actions/setup-go@4a3601121dd01d1626a1e23e37211e3254c1c06c"

	// Anchors for validateSPALintOrdering. The directory is the npm workspace
	// root, which is the working-directory every SPA step declares.
	spaWorkspaceDir       = "internal/api/dashboardspa/web"
	spaSharedBuildCommand = "npm run build:shared"

	// These are SHA-256 digests of the display-free JSON projections below.
	// Whole-workflow execution hashes deliberately pin shell text instead of
	// approximating shell semantics: any execution change requires explicit
	// policy review, while workflow, job, step, and input descriptions remain
	// free to change. A failure prints the projection and candidate digest.
	expectedCITriggersHash       = "d1a8bcd089019589658d8f154af9c26a70877285d84a384c2dcea299efc9554a"
	expectedCIExecutionHash      = "c032cdd66b620bb36e5ba9d3f205cb0c74ed176418ae1e0297bcfdf59d94c994"
	expectedNightlyTriggersHash  = "0a4400a09ac567e90adf8be1232eef1f14e36efd8dba3e143aa6e36f5b7a36f5"
	expectedNightlyExecutionHash = "80575ca368f28ba9f8b14bf72ce5767a7877ffe4dcadc136854ab4b0b5f1377a"
	expectedSetupActionHash      = "b7864038195cd054aee7fccfa903cab335b375bcab1a35239c17c5da7d32c07e"
)

var requiredFilterPaths = map[string][]string{
	"mail": {"internal/mail/**", "contrib/mail-scripts/**"},
	"docker": {
		"internal/session/**",
		"scripts/gc-session-docker",
		"scripts/test-docker-session",
		"contrib/session-scripts/**",
	},
	"k8s": {
		"internal/session/**",
		"contrib/session-scripts/gc-session-k8s*",
		"test/integration/session_k8s_test.go",
	},
	"beads": {
		"go.mod",
		"internal/beads/**",
		"test/acceptance/beads_cli_contract_test.go",
		"deps.env",
		".github/scripts/install-bd-archive.sh",
		"cmd/gc/init_provider_readiness.go",
	},
	"packs": {
		"examples/gastown/**",
		"internal/config/pack.go",
		"internal/config/compose.go",
		"cmd/gc/embed_builtin_packs.go",
		"scripts/update-bundled-gastown-pack",
	},
	"worker": {
		"go.mod",
		"go.sum",
		".github/workflows/**",
		"Makefile",
		"internal/worker/**",
		"internal/sessionlog/**",
		"internal/modelwindow/**",
		"internal/runtime/**",
		"internal/config/**",
		"cmd/gc/template_resolve*.go",
		"cmd/gc/session_*",
		"test/**worker**",
	},
	"worker_phase2": {
		"go.mod",
		"go.sum",
		".github/workflows/**",
		"Makefile",
		"internal/worker/**",
		"internal/sessionlog/**",
		"internal/modelwindow/**",
		"internal/runtime/**",
		"internal/config/**",
		"cmd/gc/**",
	},
	"cmd_gc_process": {
		"go.mod",
		"go.sum",
		".github/workflows/**",
		"Makefile",
		"cmd/gc/**",
		"internal/**",
		"examples/gastown/**",
	},
	"credential_provider": {
		"go.mod",
		"go.sum",
		"internal/credentialprovider/**",
		"internal/testenv/**",
		"internal/testutil/**",
	},
	"integration": {
		"go.mod",
		"go.sum",
		".github/workflows/**",
		"Makefile",
		"**/*.go",
		"scripts/test-integration-shard",
		"scripts/test-go-test-shard",
		"scripts/runtime-tmux-tests.manifest",
		"scripts/go-test-observable",
		"examples/gastown/**",
	},
	"openclaw_bridge": {"contrib/openclaw-bridge/**", ".github/workflows/**"},
	"shared": {
		"go.mod",
		"go.sum",
		"Makefile",
		".github/workflows/**",
		".github/actions/setup-gascity-ubuntu/**",
		".github/scripts/install-dolt-archive.sh",
		".github/scripts/install-bd-archive.sh",
		".github/scripts/install-claude-native.sh",
		"internal/beads/**",
		"internal/events/**",
		"internal/config/**",
	},
}

var (
	jobExecutionFields = []string{
		"needs",
		"if",
		"uses",
		"with",
		"secrets",
		"runs-on",
		"timeout-minutes",
		"env",
		"strategy",
		"outputs",
		"continue-on-error",
		"defaults",
		"permissions",
		"environment",
		"concurrency",
		"container",
		"services",
		"steps",
	}
	workflowExecutionFields = []string{
		"permissions",
		"env",
		"defaults",
		"concurrency",
	}
	stepExecutionFields = []string{
		"id",
		"if",
		"uses",
		"run",
		"with",
		"shell",
		"env",
		"continue-on-error",
		"timeout-minutes",
		"working-directory",
	}
)

func validate(ci, nightly, action map[string]any) error {
	if err := assertSemanticHash("CI triggers", projectTriggers(ci), expectedCITriggersHash); err != nil {
		return err
	}
	if err := validateChangesJob(ci); err != nil {
		return err
	}
	if err := validatePolicyWiring(ci); err != nil {
		return err
	}
	if err := validateSPALintOrdering(ci); err != nil {
		return err
	}
	if err := validatePRProviderOwnership(ci); err != nil {
		return err
	}
	if err := assertWorkflowExecution("CI", ci, expectedCIExecutionHash); err != nil {
		return err
	}
	if err := assertSemanticHash("setup action", projectAction(action), expectedSetupActionHash); err != nil {
		return err
	}
	if match, ok := findActionProviderSelector(projectAction(action), "setup-action"); ok {
		return fmt.Errorf(
			"setup action must not select a test provider: %s assigns %s",
			match.path,
			match.name,
		)
	}
	if err := assertSemanticHash(
		"nightly triggers",
		projectTriggers(nightly),
		expectedNightlyTriggersHash,
	); err != nil {
		return err
	}
	if err := validateNightlyProviderOwnership(nightly); err != nil {
		return err
	}
	return assertWorkflowExecution("nightly", nightly, expectedNightlyExecutionHash)
}

func validateChangesJob(workflow map[string]any) error {
	changeJob, err := workflowJob(workflow, "changes")
	if err != nil {
		return err
	}
	steps, err := mappingSlice(changeJob["steps"], "changes steps")
	if err != nil || len(steps) != 3 {
		if err != nil {
			return err
		}
		return fmt.Errorf("changes must contain exactly three execution steps")
	}
	with, ok := steps[1]["with"].(map[string]any)
	if !ok {
		return fmt.Errorf("changes paths-filter step must have a with mapping")
	}
	filterSource, ok := with["filters"].(string)
	if !ok {
		return fmt.Errorf("changes paths-filter input must be static YAML")
	}
	var filters map[string]any
	if err := yaml.Unmarshal([]byte(filterSource), &filters); err != nil {
		return fmt.Errorf("parse changes filters: %w", err)
	}
	filterNames := make([]string, 0, len(requiredFilterPaths))
	for filter := range requiredFilterPaths {
		filterNames = append(filterNames, filter)
	}
	sort.Strings(filterNames)
	for _, filter := range filterNames {
		required := requiredFilterPaths[filter]
		paths, ok := filters[filter].([]any)
		if !ok {
			return fmt.Errorf("changes filter %q must be a static path list", filter)
		}
		present := make(map[string]bool, len(paths))
		for _, path := range paths {
			text, ok := path.(string)
			if !ok {
				return fmt.Errorf("changes filter %q contains a non-string path", filter)
			}
			present[text] = true
		}
		for _, path := range required {
			if !present[path] {
				return fmt.Errorf("changes filter %q is missing required path %q", filter, path)
			}
		}
	}

	return nil
}

func validatePolicyWiring(workflow map[string]any) error {
	staticJob, err := workflowJob(workflow, "preflight-static")
	if err != nil {
		return err
	}
	steps, err := mappingSlice(staticJob["steps"], "preflight-static steps")
	if err != nil {
		return err
	}
	setupIndex := findStep(steps, "uses", setupGoAction)
	policyIndex := findStep(steps, "run", "make test-ci-policy")
	firstGuardIndex := findStep(steps, "run", "make check-gomod-replace")
	if setupIndex < 0 || policyIndex != setupIndex+1 || firstGuardIndex <= policyIndex {
		return fmt.Errorf(
			"preflight-static must run the focused CI policy immediately after setup-go and before other guards",
		)
	}
	want := map[string]any{"run": "make test-ci-policy"}
	if got := projectStep(steps[policyIndex]); !reflect.DeepEqual(got, want) {
		return fmt.Errorf("preflight-static CI policy step must be unconditional and blocking")
	}
	return nil
}

// The dashboard SPA's eslint config enables typescript-eslint's type-aware
// rules, so its lint has a BUILD dependency: those rules resolve the
// gas-city-dashboard-shared workspace through shared/dist, which is gitignored
// (`*/dist/` in that directory's .gitignore, zero tracked files) and is not
// produced by `npm ci`. Lint a fresh checkout before building it and every
// cross-workspace import reads as `unknown`, so restrict-template-expressions
// fails on a file nobody touched and the push stays red (bead ci-nwu2).
// Cheapest-step-first is correct for a syntax-only lint and wrong here.
//
// The Makefile already orders it right -- `dashboard-lint: dashboard-build` --
// which is why `make dashboard-ci` never broke. Leaving that as the only
// guarantee is what allowed a workflow calling the npm script directly to get
// it wrong, so the ordering is pinned here rather than trusted.
//
// NOT caught: an eslint run that declares no `working-directory` (a `cd` in
// the run body instead), and any lint outside .github/workflows. Widen
// spaLintsWorkspace below rather than adding a second gate.
func validateSPALintOrdering(workflow map[string]any) error {
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		return fmt.Errorf("workflow jobs must be a mapping")
	}
	for _, name := range sortedKeys(jobs) {
		job, ok := jobs[name].(map[string]any)
		if !ok {
			continue
		}
		steps, err := mappingSlice(job["steps"], name+" steps")
		if err != nil {
			// No steps list: a job that delegates to a reusable workflow. A
			// malformed one is left to the execution hash, which pins every
			// step field in the workflow and reports the whole projection.
			continue
		}
		for index, step := range steps {
			if !spaStepRuns(step, spaLintsWorkspace) {
				continue
			}
			if spaSharedBuiltBefore(steps[:index]) {
				continue
			}
			return fmt.Errorf(
				"job %q lints %s at step %d without building shared/dist first: run %q "+
					"in that working-directory beforehand, or eslint's type-aware rules "+
					"resolve every cross-workspace import as unknown",
				name, spaWorkspaceDir, index, spaSharedBuildCommand,
			)
		}
	}
	return nil
}

// Reports whether the step runs eslint over the SPA workspace. `npm run lint`
// is the shape ci.yml uses; the bare-eslint arm is there so hoisting the
// command out of the npm script does not silently drop the step from the gate.
func spaLintsWorkspace(run string) bool {
	return strings.Contains(run, "npm run lint") || strings.Contains(run, "eslint")
}

// Reports whether any earlier step produced shared/dist. Scans for a first
// match rather than requiring a unique one the way findStep does: the
// dashboard job legitimately runs `npm run build` again later for the bundle,
// so uniqueness would misread a correctly ordered job as ambiguous.
func spaSharedBuiltBefore(earlier []map[string]any) bool {
	for _, step := range earlier {
		if spaStepRuns(step, spaBuildsSharedTypes) {
			return true
		}
	}
	return false
}

// Reports whether the step produces shared/dist. Unsuffixed `npm run build`
// counts, since it is `build:shared && build:frontend` -- building the
// frontend too is wasteful before a lint, not incorrect. `npm run
// build:frontend` alone does NOT, which is why this cannot collapse into a
// single "npm run build" prefix match.
func spaBuildsSharedTypes(run string) bool {
	if strings.Contains(run, spaSharedBuildCommand) {
		return true
	}
	return strings.Contains(run, "npm run build") &&
		!strings.Contains(run, "npm run build:frontend")
}

func spaStepRuns(step map[string]any, pred func(string) bool) bool {
	if step["working-directory"] != spaWorkspaceDir {
		return false
	}
	run, ok := step["run"].(string)
	return ok && pred(run)
}

func validatePRProviderOwnership(workflow map[string]any) error {
	execution, err := projectWorkflowExecution(workflow)
	if err != nil {
		return err
	}
	if match, ok := findWorkflowProviderSelector(execution, "ci"); ok {
		return fmt.Errorf(
			"PR workflow must not select nightly-only test providers: %s assigns %s",
			match.path,
			match.name,
		)
	}
	return nil
}

func validateNightlyProviderOwnership(workflow map[string]any) error {
	if match, ok := findEnvField(workflow, "nightly"); ok {
		return fmt.Errorf(
			"nightly provider selection must be owned only by integration-sqlite-coordstore: %s assigns %s",
			match.path,
			match.name,
		)
	}

	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		return fmt.Errorf("nightly jobs must be a mapping")
	}
	jobNames := make([]string, 0, len(jobs))
	for name := range jobs {
		jobNames = append(jobNames, name)
	}
	sort.Strings(jobNames)
	for _, name := range jobNames {
		raw := jobs[name]
		job, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("nightly job %q must be a mapping", name)
		}
		if name == "integration-sqlite-coordstore" {
			continue
		}
		path := "nightly.jobs." + name
		if match, found := findJobProviderSelector(projectJob(job), path); found {
			return fmt.Errorf(
				"nightly provider selection must be owned only by integration-sqlite-coordstore: %s assigns %s",
				match.path,
				match.name,
			)
		}
	}
	return nil
}

func assertWorkflowExecution(label string, workflow map[string]any, expectedHash string) error {
	execution, err := projectWorkflowExecution(workflow)
	if err != nil {
		return err
	}
	return assertSemanticHash(label+" workflow", execution, expectedHash)
}

func assertSemanticHash(label string, got any, expectedHash string) error {
	encoded, err := json.Marshal(got)
	if err != nil {
		return fmt.Errorf("encode %s semantic policy: %w", label, err)
	}
	actualHash := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if actualHash == expectedHash {
		return nil
	}
	rendered, _ := json.MarshalIndent(got, "", "  ")
	return fmt.Errorf(
		"%s execution shape changed\nwant SHA-256: %s\ngot SHA-256:  %s\nsemantic projection:\n%s",
		label,
		expectedHash,
		actualHash,
		rendered,
	)
}

func workflowJob(workflow map[string]any, name string) (map[string]any, error) {
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workflow jobs must be a mapping")
	}
	job, ok := jobs[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workflow job %q must be a mapping", name)
	}
	return job, nil
}

func mappingSlice(value any, label string) ([]map[string]any, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", label)
	}
	result := make([]map[string]any, 0, len(items))
	for index, item := range items {
		mapping, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s item %d must be a mapping", label, index)
		}
		result = append(result, mapping)
	}
	return result, nil
}

func projectWorkflowExecution(workflow map[string]any) (map[string]any, error) {
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workflow jobs must be a mapping")
	}
	projectedJobs := make(map[string]any, len(jobs))
	for name, raw := range jobs {
		job, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("workflow job %q must be a mapping", name)
		}
		projectedJobs[name] = projectJob(job)
	}
	result := selectFields(workflow, workflowExecutionFields)
	result["jobs"] = projectedJobs
	return result, nil
}

func projectJob(job map[string]any) map[string]any {
	result := selectFields(job, jobExecutionFields)
	rawSteps, exists := job["steps"]
	if !exists {
		return result
	}
	steps, ok := rawSteps.([]any)
	if !ok {
		result["steps"] = rawSteps
		return result
	}
	projected := make([]any, 0, len(steps))
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			projected = append(projected, raw)
			continue
		}
		projected = append(projected, projectStep(step))
	}
	result["steps"] = projected
	return result
}

func projectStep(step map[string]any) map[string]any {
	return selectFields(step, stepExecutionFields)
}

func projectAction(action map[string]any) map[string]any {
	result := make(map[string]any)
	if inputs, ok := action["inputs"]; ok {
		result["inputs"] = projectInputs(inputs)
	}
	if outputs, ok := action["outputs"]; ok {
		result["outputs"] = projectInputs(outputs)
	}
	runs, ok := action["runs"].(map[string]any)
	if !ok {
		if raw, exists := action["runs"]; exists {
			result["runs"] = raw
		}
		return result
	}
	projectedRuns := selectFields(runs, []string{"using"})
	if rawSteps, exists := runs["steps"]; exists {
		if steps, ok := rawSteps.([]any); ok {
			projected := make([]any, 0, len(steps))
			for _, raw := range steps {
				if step, ok := raw.(map[string]any); ok {
					projected = append(projected, projectStep(step))
				} else {
					projected = append(projected, raw)
				}
			}
			projectedRuns["steps"] = projected
		} else {
			projectedRuns["steps"] = rawSteps
		}
	}
	result["runs"] = projectedRuns
	return result
}

func selectFields(source map[string]any, fields []string) map[string]any {
	result := make(map[string]any)
	for _, field := range fields {
		if value, ok := source[field]; ok {
			result[field] = copyValue(value)
		}
	}
	return result
}

func projectTriggers(workflow map[string]any) any {
	triggers := copyValue(workflow["on"])
	triggerMap, ok := triggers.(map[string]any)
	if !ok {
		return triggers
	}
	for _, triggerName := range []string{"workflow_call", "workflow_dispatch"} {
		trigger, ok := triggerMap[triggerName].(map[string]any)
		if !ok {
			continue
		}
		trigger["inputs"] = projectInputs(trigger["inputs"])
	}
	return triggerMap
}

func projectInputs(value any) any {
	inputs := copyValue(value)
	inputMap, ok := inputs.(map[string]any)
	if !ok {
		return inputs
	}
	for _, value := range inputMap {
		if input, ok := value.(map[string]any); ok {
			delete(input, "description")
		}
	}
	return inputMap
}

func copyValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[key] = copyValue(child)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for index, child := range value {
			result[index] = copyValue(child)
		}
		return result
	default:
		return value
	}
}

func findStep(steps []map[string]any, field, value string) int {
	found := -1
	for index, step := range steps {
		if step[field] != value {
			continue
		}
		if found >= 0 {
			return -1
		}
		found = index
	}
	return found
}

type providerSelectorMatch struct {
	name string
	path string
}

var providerSelectorNames = []string{
	"GC_BEADS",
	"GC_ACCEPTANCE_BEADS_PROVIDER",
}

func findWorkflowProviderSelector(workflow map[string]any, path string) (providerSelectorMatch, bool) {
	if match, ok := findEnvField(workflow, path); ok {
		return match, true
	}
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		return providerSelectorMatch{}, false
	}
	names := sortedKeys(jobs)
	for _, name := range names {
		job, ok := jobs[name].(map[string]any)
		if !ok {
			continue
		}
		if match, found := findJobProviderSelector(job, joinFieldPath(path, "jobs."+name)); found {
			return match, true
		}
	}
	return providerSelectorMatch{}, false
}

func findJobProviderSelector(job map[string]any, path string) (providerSelectorMatch, bool) {
	if match, ok := findEnvField(job, path); ok {
		return match, true
	}
	if container, ok := job["container"].(map[string]any); ok {
		if match, found := findEnvField(container, joinFieldPath(path, "container")); found {
			return match, true
		}
	}
	if services, ok := job["services"].(map[string]any); ok {
		for _, name := range sortedKeys(services) {
			service, ok := services[name].(map[string]any)
			if !ok {
				continue
			}
			servicePath := joinFieldPath(path, "services."+name)
			if match, found := findEnvField(service, servicePath); found {
				return match, true
			}
		}
	}
	return findStepsProviderSelector(job["steps"], joinFieldPath(path, "steps"))
}

func findActionProviderSelector(action map[string]any, path string) (providerSelectorMatch, bool) {
	runs, ok := action["runs"].(map[string]any)
	if !ok {
		return providerSelectorMatch{}, false
	}
	return findStepsProviderSelector(runs["steps"], joinFieldPath(path, "runs.steps"))
}

func findStepsProviderSelector(value any, path string) (providerSelectorMatch, bool) {
	steps, ok := value.([]any)
	if !ok {
		return providerSelectorMatch{}, false
	}
	for index, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		stepPath := fmt.Sprintf("%s[%d]", path, index)
		if match, found := findEnvField(step, stepPath); found {
			return match, true
		}
	}
	return providerSelectorMatch{}, false
}

func findEnvField(value map[string]any, path string) (providerSelectorMatch, bool) {
	env, exists := value["env"]
	if !exists {
		return providerSelectorMatch{}, false
	}
	return findProviderEnvKey(env, joinFieldPath(path, "env"))
}

func findProviderEnvKey(value any, path string) (providerSelectorMatch, bool) {
	env, ok := value.(map[string]any)
	if !ok {
		return providerSelectorMatch{}, false
	}
	for _, name := range providerSelectorNames {
		if _, exists := env[name]; exists {
			return providerSelectorMatch{name: name, path: joinFieldPath(path, name)}, true
		}
	}
	return providerSelectorMatch{}, false
}

func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinFieldPath(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}
