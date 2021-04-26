// Copyright © 2019 by PACE Telematics GmbH. All rights reserved.
// Created at 2019/10/18 Charlotte Pröller

package servicehealthcheck

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/caarlos0/env"
	"github.com/pace/bricks/maintenance/errors"
	"github.com/pace/bricks/maintenance/log"
)

// HealthCheck is a health check that is registered once and that is performed
// periodically and/or spontaneously.
type HealthCheck interface {
	HealthCheck(ctx context.Context) HealthCheckResult
}

type HealthCheckFunc func(ctx context.Context) HealthCheckResult

func (hcf HealthCheckFunc) HealthCheck(ctx context.Context) HealthCheckResult {
	return hcf(ctx)
}

// Initializable is used to mark that a health check needs to be initialized
type Initializable interface {
	Init(ctx context.Context) error
}

type config struct {
	// Amount of time to cache the last init
	HealthCheckInitResultErrorTTL time.Duration `env:"HEALTH_CHECK_INIT_RESULT_ERROR_TTL" envDefault:"10s"`
	// Amount of time to wait before failing the health check
	HealthCheckMaxWait time.Duration `env:"HEALTH_CHECK_MAX_WAIT" envDefault:"5s"`
	// Amount of time that a health result checked in background is valid
	HealthCheckResultTTL time.Duration `env:"HEALTH_CHECK_RESULT_TTL" envDefault:"90s"`
	// How often should a background health check be performed?
	// Should be at most HealthCheckResult - HealthCheckMaxWait
	HealthCheckBackgroundInterval time.Duration `env:"HEALTH_CHECK_BACKGROUND_CHECK_INTERAL" envDefault:"1m"`
}

var cfg config

// requiredChecks contains all required registered Health Checks - key:Name
var requiredChecks sync.Map

// optionalChecks contains all optional registered Health Checks - key:Name
var optionalChecks sync.Map

type initError struct {
	state ConnectionState
}

// initErrors map with all err ConnectionState that happened in the initialization of any health check - key:Name
var initErrors sync.Map

// HealthState describes if a any error or warning occurred during the health check of a service
type HealthState string

const (
	// Err State of a service, if an error occurred during the health check of the service
	Err HealthState = "ERR"
	// Warn State of a service, if a warning occurred during the health check of the service
	Warn HealthState = "WARN"
	// Ok State of a service, if no warning or error occurred during the health check of the service
	Ok HealthState = "OK"
)

// HealthCheckResult describes the result of a health check, contains the state of a service and a message that
// describes the state. If the state is Ok the description can be empty.
// The description should contain the error message if any error or warning occurred during the health check.
type HealthCheckResult struct {
	State HealthState
	Msg   string
}

func init() {
	err := env.Parse(&cfg)
	if err != nil {
		log.Fatalf("Failed to parse health check environment: %v", err)
	}
}

// checkActively checks all heal checks in the map actively and waits for them
// to return or run into a timeout
// nolint: deadcode // TODO remove in future revision if not used by 2021/07/01
func checkActively(ctx context.Context, hcs *sync.Map) map[string]HealthCheckResult {
	ctx, cancel := context.WithTimeout(ctx, cfg.HealthCheckMaxWait)

	result := make(map[string]HealthCheckResult)
	var resultSync sync.Map
	var wg sync.WaitGroup

	hcs.Range(func(key, value interface{}) bool {
		name := key.(string)
		hc := value.(HealthCheck)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer errors.HandleWithCtx(ctx, fmt.Sprintf("HealthCheck %s", name))

			// If it was not possible to initialize this health check, then show the initialization error message
			if val, isIn := initErrors.Load(name); isIn {
				state := val.(*ConnectionState)
				if done := reInitHealthCheck(ctx, state, name, hc.(Initializable)); done {
					resultSync.Store(name, state.GetState())
					return
				}
			}
			// this is the actual health check
			resultSync.Store(name, hc.HealthCheck(ctx))
		}()
		return true
	})
	wg.Wait()
	cancel()
	resultSync.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(HealthCheckResult)
		return true
	})

	return result
}

func reInitHealthCheck(ctx context.Context, conState *ConnectionState, name string, initHc Initializable) bool {
	if time.Since(conState.LastChecked()) < cfg.HealthCheckInitResultErrorTTL {
		return true
	}
	err := initHc.Init(ctx)
	if err != nil {
		conState.SetErrorState(err)
		return true
	}
	return false
}

func writeResult(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	if _, err := fmt.Fprint(w, body); err != nil {
		log.Warnf("could not write output: %s", err)
	}
}

// RegisterHealthCheck registers a required HealthCheck. The name
// must be unique. If the health check satisfies the Initializable interface, it
// is initialized before it is added.
// It is not possible to add a health check with the same name twice, even if one is required and one is optional
func RegisterHealthCheck(name string, hc HealthCheck) {
	registerHealthCheck(&requiredChecks, hc, name)
}

// RegisterHealthCheckFunc registers a required HealthCheck. The name
// must be unique.  It is not possible to add a health check with the same name twice,
// even if one is required and one is optional
func RegisterHealthCheckFunc(name string, f HealthCheckFunc) {
	RegisterHealthCheck(name, f)
}

// RegisterOptionalHealthCheck registers a HealthCheck like RegisterHealthCheck(hc HealthCheck, name string)
// but the health check is only checked for /health/check and not for /health/
func RegisterOptionalHealthCheck(hc HealthCheck, name string) {
	registerHealthCheck(&optionalChecks, hc, name)
}

func registerHealthCheck(checks *sync.Map, hc HealthCheck, name string) {
	ctx := log.Logger().WithContext(context.Background())

	// check both lists, because
	if _, inReq := requiredChecks.Load(name); inReq {
		log.Warnf("tried to register health check with name %q twice", name)
		return
	}
	if _, inOpt := optionalChecks.Load(name); inOpt {
		log.Warnf("tried to register health check with name %q twice", name)
		return
	}
	if initHC, ok := hc.(Initializable); ok {
		if err := initHC.Init(ctx); err != nil {
			log.Warnf("error initializing health check %q: %s", name, err)
			initErrors.Store(name, &ConnectionState{
				lastCheck: time.Now(),
				result: HealthCheckResult{
					State: Err,
					Msg:   err.Error(),
				},
			})
		}
	}
	// save the length of the longest health check name, for the width of the column in /health/check
	if len(name) > longestCheckName {
		longestCheckName = len(name)
	}
	checks.Store(name, hc)
	// start doing the check in background
	startBackgroundHealthCheck(ctx, name, hc, cfg.HealthCheckBackgroundInterval)
}

func deleteHealthCheck(name string) {
	cancelBackgroundHealthCheck(name)
	requiredChecks.Delete(name)
	optionalChecks.Delete(name)
}

// HealthHandler returns the health endpoint for transactional processing. This Handler only checks
// the required health checks and returns ERR and 503 or OK and 200.
func HealthHandler() http.Handler {
	return &healthHandler{}
}

// ReadableHealthHandler returns the health endpoint with all details about service health. This handler checks
// all health checks. The response body contains two tables (for required and optional health checks)
// with the detailed results of the health checks.
func ReadableHealthHandler() http.Handler {
	return &readableHealthHandler{}
}

// JSONHealthHandler return health endpoint with all details about service health. This handler checks
// all health checks. The response body contains a JSON formatted array with every service (required or optional)
// and the detailed health checks about them.
func JSONHealthHandler() http.Handler {
	return &jsonHealthHandler{}
}
