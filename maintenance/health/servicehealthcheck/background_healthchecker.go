// Copyright © 2021 by PACE Telematics GmbH. All rights reserved.
// Created at 2021/04/26 by Florian Schäfer

package servicehealthcheck

import (
	"context"
	"fmt"
	"sync"
	"time"

	brickserrors "github.com/pace/bricks/maintenance/errors"
	"github.com/pace/bricks/maintenance/log"
)

type backgroundHealthCheckResult struct {
	lastCheckTime time.Time
	lastResult    HealthCheckResult
}

var (
	backgroundCheckResults sync.Map
	backgroundTickers      sync.Map
)

func startBackgroundHealthCheck(rootCtx context.Context, name string, hc HealthCheck, interval time.Duration) {
	// copy the context, so as to avoid memory leaks
	s := log.Sink{Silent: true}
	ctx := log.ContextWithSink(rootCtx, &s)
	checkOnce(ctx, name, hc)

	go func() {
		defer brickserrors.HandleWithCtx(ctx, fmt.Sprintf("health-check(%s)", name))
		defer func() {
			// if it should still be running (i.e. it was not cancelled), restart it
			if _, ok := backgroundTickers.Load(name); ok {
				startBackgroundHealthCheck(rootCtx, name, hc, interval)
			}
		}()

		healthTicker := time.NewTicker(interval)
		// use the existing ticker if it's in there (in case of restart)
		t, loaded := backgroundTickers.LoadOrStore(name, healthTicker)
		if loaded {
			healthTicker = t.(*time.Ticker)
		}
		for range healthTicker.C {
			checkOnce(ctx, name, hc)
		}
	}()
}

func cancelBackgroundHealthCheck(name string) {
	if t, ok := backgroundTickers.LoadAndDelete(name); ok {
		ticker := t.(*time.Ticker)
		backgroundTickers.Delete(name)
		ticker.Stop()
	}
}

func checkOnce(ctx context.Context, name string, hc HealthCheck) {
	timedCtx, cancel := context.WithTimeout(ctx, cfg.HealthCheckMaxWait)
	defer cancel()
	res := hc.HealthCheck(timedCtx)
	entry := &backgroundHealthCheckResult{
		lastCheckTime: time.Now(),
		lastResult:    res,
	}
	log.Logger().Debug().Str("name", name).Interface("hcResult", res).Str("result", string(res.State)).Msg("ran healthcheck")
	backgroundCheckResults.Store(name, entry)
}

func reInitializeHealthChecks() {
	var toUpdate []*ConnectionState
	var toDelete []string
	initErrors.Range(func(k, v interface{}) bool {
		name := k.(string)
		err := initHc.Init(ctx)
	}
				if done := reInitHealthCheck(ctx, state, name, hc.(Initializable)); done {
					resultSync.Store(name, state.GetState())
					return
				}
}

func reInitializeHealthCheck(ctx context.Context,  name string, initHc Initializable) error {
	err := initHc.Init(ctx)
	return err
}

func getBackgroundHealthCheckResult(name string) *backgroundHealthCheckResult {
	r, ok := backgroundCheckResults.Load(name)
	if !ok {
		return nil
	}
	return r.(*backgroundHealthCheckResult)
}

func getResults(checkMap *sync.Map) map[string]HealthCheckResult {
	m := make(map[string]HealthCheckResult)
	checkMap.Range(func(key, val interface{}) bool {
		name := key.(string)
		// first see if there was an init error for this check
		if res, ok := initErrors.Load(name); ok {
			m[name] = res.(*ConnectionState).GetState()
			return true
		}
		res := getBackgroundHealthCheckResult(name)
		if res == nil {
			m[name] = HealthCheckResult{
				Msg:   fmt.Sprintf("%s: no check performed yet", name),
				State: Err,
			}
			return true
		}
		if time.Since(res.lastCheckTime) > cfg.HealthCheckResultTTL {
			m[name] = HealthCheckResult{
				Msg:   fmt.Sprintf("%s: last health check result is too old. Was: %s", name, string(res.lastResult.State)),
				State: Err,
			}
			return true
		}
		m[name] = res.lastResult
		return true
	})
	return m
}

// getRequiredResults returns all the background check results of the required checks
func getRequiredResults() map[string]HealthCheckResult {
	getResults(requiredChecks)
}

// getOptionalREsults returns all the background check results of the optional checks
func getOptionalResults() map[string]HealthCheckResult {
	getResults(optionalChecks)
}
