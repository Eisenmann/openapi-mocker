package usecase

import (
	"errors"
	"fmt"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Defaults of a generated plan.
const (
	DefaultGenRate         = 10
	DefaultGenRamp         = time.Minute
	DefaultGenHold         = 5 * time.Minute
	defaultGenGraceful     = 10 * time.Second
	genP95LimitMs          = 500
	genMaxErrorRate        = 0.01
	genReadWeight          = 9
	genWriteWeight         = 1
	genP95Metric           = "http.latency.p95"
	readScenarioName       = "read"
	writeScenarioName      = "write"
	generatedPlanNameSufix = " load test"
)

// ErrLoadNoOperations is returned when a contract has nothing to generate a
// plan from.
var ErrLoadNoOperations = errors.New("the contract has no operations to load test")

// GenerateOptions tunes GeneratePlan; zero values mean the defaults.
type GenerateOptions struct {
	// Name of the plan; default "<project> load test".
	Name string
	// ProjectID makes the plan target the project's own mock (mock://id)
	// unless BaseURL is set.
	ProjectID string
	BaseURL   string
	// IncludeUnsafe adds a scenario of POST/PUT/PATCH/DELETE operations. They
	// change data on the target, so it is off by default.
	IncludeUnsafe bool
	// Rate is the peak arrival rate in iterations per second.
	Rate int
	// Ramp is the time to reach Rate; Hold is how long it is then kept.
	Ramp time.Duration
	Hold time.Duration
}

// GeneratePlan builds a ready-to-run plan from a contract's operations: an
// open model that ramps to Rate and holds it, one scenario that calls every
// safe (GET/HEAD) operation, an optional scenario of the unsafe ones, and
// thresholds on latency, errors and contract violations.
func GeneratePlan(ops []LoadOperation, opts *GenerateOptions) (*domain.Plan, error) {
	if len(ops) == 0 {
		return nil, ErrLoadNoOperations
	}

	var safe, unsafe []domain.Step

	for i := range ops {
		step := generatedStep(&ops[i])
		if ops[i].Safe {
			safe = append(safe, step)
		} else {
			unsafe = append(unsafe, step)
		}
	}

	if len(safe) == 0 && !opts.IncludeUnsafe {
		return nil, fmt.Errorf("%w: it has only operations that change data (set includeUnsafe)", ErrLoadNoOperations)
	}

	plan := new(domain.Plan)
	plan.Name = opts.planName()
	plan.Target.BaseURL = opts.baseURL()
	plan.Scenarios = scenarioSet(safe, unsafe, opts.IncludeUnsafe)
	plan.Load = opts.loadModel()
	plan.GracefulStop = domain.Duration(defaultGenGraceful)
	plan.Thresholds = generatedThresholds()
	plan.Outputs = []domain.Output{{Format: domain.OutputHTML}, {Format: domain.OutputJSON}}

	err := plan.Validate()
	if err != nil {
		return nil, fmt.Errorf("generated plan is invalid: %w", err)
	}

	return plan, nil
}

// generatedStep calls the operation with contract examples and checks the
// response against the contract.
func generatedStep(op *LoadOperation) domain.Step {
	var s domain.Step

	s.Operation = op.Ref()
	s.Checks = []domain.Check{{Kind: domain.CheckContract}}

	return s
}

func scenarioSet(safe, unsafe []domain.Step, includeUnsafe bool) []domain.Scenario {
	var out []domain.Scenario

	readWeight := 1
	if includeUnsafe && len(unsafe) > 0 && len(safe) > 0 {
		readWeight = genReadWeight
	}

	out = append(out, chunkScenarios(readScenarioName, readWeight, safe)...)

	if includeUnsafe {
		out = append(out, chunkScenarios(writeScenarioName, genWriteWeight, unsafe)...)
	}

	return out
}

// chunkScenarios splits steps into scenarios of at most MaxLoadSteps; most
// contracts need only one.
func chunkScenarios(name string, weight int, steps []domain.Step) []domain.Scenario {
	var out []domain.Scenario

	for i := 0; i < len(steps); i += domain.MaxLoadSteps {
		end := min(i+domain.MaxLoadSteps, len(steps))

		var sc domain.Scenario

		sc.Name = name
		if i > 0 {
			sc.Name = fmt.Sprintf("%s-%d", name, i/domain.MaxLoadSteps+1)
		}

		sc.Weight = weight
		sc.Steps = steps[i:end]
		out = append(out, sc)
	}

	return out
}

func (o *GenerateOptions) planName() string {
	if o.Name != "" {
		return o.Name
	}

	if o.ProjectID != "" {
		return o.ProjectID + generatedPlanNameSufix
	}

	return "API" + generatedPlanNameSufix
}

func (o *GenerateOptions) baseURL() string {
	switch {
	case o.BaseURL != "":
		return o.BaseURL
	case o.ProjectID != "":
		return mockScheme + "://" + o.ProjectID
	default:
		return mockScheme + "://PROJECT_ID"
	}
}

func (o *GenerateOptions) loadModel() domain.LoadModel {
	rate := o.Rate
	if rate <= 0 {
		rate = DefaultGenRate
	}

	ramp, hold := o.Ramp, o.Hold
	if ramp <= 0 {
		ramp = DefaultGenRamp
	}

	if hold <= 0 {
		hold = DefaultGenHold
	}

	var m domain.LoadModel

	m.Kind = domain.LoadOpen
	m.Stages = []domain.Stage{
		{Duration: domain.Duration(ramp), Rate: rate},
		{Duration: domain.Duration(hold), Rate: rate},
	}

	return m
}

func generatedThresholds() []domain.Threshold {
	return []domain.Threshold{
		{Metric: genP95Metric, Op: domain.OpLess, Value: genP95LimitMs},
		{Metric: domain.MetricErrorsRate, Op: domain.OpLess, Value: genMaxErrorRate},
		{Metric: domain.MetricContractViolations, Op: domain.OpEqual, Value: 0},
	}
}
