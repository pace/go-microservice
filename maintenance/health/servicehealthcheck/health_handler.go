// Copyright © 2019 by PACE Telematics GmbH. All rights reserved.
// Created at 2019/12/05 by Charlotte Pröller

package servicehealthcheck

import (
	"fmt"
	"net/http"

	"github.com/pace/bricks/maintenance/log"
)

type healthHandler struct{}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := log.WithContext(r.Context())

	var errors []string
	var warnings []string
	for name, res := range getRequiredResults() {
		if res.State == Err {
			errors = append(errors, fmt.Sprintf("%s: %s", name, res.Msg))
		} else if res.State == Warn {
			warnings = append(warnings, fmt.Sprintf("%s: %s", name, res.Msg))
		}
	}
	if len(errors) > 0 {
		log.Ctx(ctx).Info().Strs("errors", errors).Strs("warnings", warnings).Msg("Health check failed")
		msg := fmt.Sprintf("ERR: %d errors and %d warnings", len(errors), len(warnings))
		writeResult(w, http.StatusServiceUnavailable, msg)
		return
	}
	writeResult(w, http.StatusOK, string(Ok))
}
