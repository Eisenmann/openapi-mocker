package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/loadstore"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/engine"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	// serverSecretPrefix is added to a plan's target.auth.ref when the server
	// runs the plan: a plan may only name LOAD_SECRET_<ref> variables, never
	// ADMIN_TOKEN or any other setting of the server.
	serverSecretPrefix = "LOAD_SECRET_"
	// loadDataDirName holds the data files (CSV, JSON) plans of the server read.
	loadDataDirName = "loaddata"
)

// loadServices are the load testing services of the server.
type loadServices struct {
	plans *usecase.LoadPlanService
	runs  *usecase.LoadRunService
}

// newLoadServices wires plan storage, the run store, the engine and the run
// service. Limits come from LOAD_MAX_RPS, LOAD_MAX_VUS, LOAD_MAX_DURATION and
// LOAD_MAX_CONNS, retention from LOAD_RUNS_KEEP and LOAD_RUNS_MAX_AGE.
func newLoadServices(
	ctx context.Context, dataDir string, store *jsonstore.Store, contracts *openapi.Engine, mockBase string,
) (*loadServices, error) {
	caps, err := usecase.LoadCapsFromEnv(os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("load limits: %w", err)
	}

	opts, err := loadstore.OptionsFromEnv(os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("load run retention: %w", err)
	}

	files, err := loadstore.New(dataDir, &opts)
	if err != nil {
		return nil, fmt.Errorf("initialize load store (%s): %w", dataDir, err)
	}

	cfg := new(engine.Config)
	cfg.Operations = contracts
	cfg.Data = os.DirFS(filepath.Join(dataDir, loadDataDirName))
	cfg.Version = version
	cfg.Secrets = func(ref string) string { return os.Getenv(serverSecretPrefix + ref) }
	cfg.NewValidator = func(raw []byte, every int) (usecase.ResponseValidator, error) {
		return contracts.NewResponseValidator(raw, every) //nolint:wrapcheck // the engine wraps it.
	}
	cfg.Contract = func(_ context.Context, projectID string) ([]byte, error) {
		c, getErr := store.GetActive(projectID)
		if getErr != nil {
			return nil, fmt.Errorf("project %s has no active contract: %w", projectID, getErr)
		}

		return []byte(c.Raw), nil
	}

	runs, err := usecase.NewLoadRunService(ctx, &usecase.LoadRunDeps{
		Engine: engine.New(cfg), Store: files, Caps: caps,
		MockBase: mockBase,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize load runs: %w", err)
	}

	return &loadServices{plans: usecase.NewLoadPlanService(files), runs: runs}, nil
}
