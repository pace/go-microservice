package servicehealthcheck

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/pace/bricks/maintenance/log"
)

type jsonHealthHandler struct {
	Name     string      `json: "name"`
	Status   HealthState `json: "status"`
	Required bool        `json: "required"`
	Error    string      `json: "error"`
}

func (h *jsonHealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := log.WithContext(r.Context())

	reqChecks := getRequiredResults()
	optChecks := getOptionalResults()
	var errors []string
	var warnings []string

	checkResponse := make([]jsonHealthHandler, len(reqChecks)+len(optChecks))
	index := 0

	status := http.StatusOK
	for name, res := range reqChecks {
		scr := jsonHealthHandler{
			Name:     name,
			Status:   res.State,
			Required: true,
			Error:    "",
		}
		if res.State == Err {
			scr.Error = res.Msg
			status = http.StatusServiceUnavailable
			errors = append(errors, fmt.Sprintf("%s: %s", name, res.Msg))
		} else if res.State == Warn {
			warnings = append(warnings, fmt.Sprintf("%s: %s", name, res.Msg))
		}
		checkResponse[index] = scr
		index++
	}
	if len(errors) > 0 {
		log.Ctx(ctx).Info().Strs("errors", errors).Strs("warnings", warnings).Msg("JSON HTTP handler: health check failed")
	}

	for name, res := range optChecks {
		scr := jsonHealthHandler{
			Name:     name,
			Status:   res.State,
			Required: false,
			Error:    "",
		}
		if res.State == Err {
			scr.Error = res.Msg
			status = http.StatusServiceUnavailable
		}
		checkResponse[index] = scr
		index++
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(checkResponse)
}
