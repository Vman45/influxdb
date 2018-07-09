package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/influxdata/chronograf"
)

type organizationConfigResponse struct {
	Links selfLinks `json:"links"`
	chronograf.OrganizationConfig
}

func newOrganizationConfigResponse(config chronograf.OrganizationConfig) *organizationConfigResponse {
	return &organizationConfigResponse{
		Links: selfLinks{
			Self: "/chronograf/v1/org_config",
		},
		OrganizationConfig: config,
	}
}

type logViewerConfigResponse struct {
	Links selfLinks `json:"links"`
	chronograf.LogViewerConfig
}

func newLogViewerConfigResponse(lv chronograf.LogViewerConfig) *logViewerConfigResponse {
	return &logViewerConfigResponse{
		Links: selfLinks{
			Self: "/chronograf/v1/org_config/logviewer",
		},
		LogViewerConfig: lv,
	}
}

// OrganizationConfig retrieves the organization-wide config settings
func (s *Service) OrganizationConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	orgID, ok := hasOrganizationContext(ctx)
	if !ok {
		Error(w, http.StatusBadRequest, "Organization not found on context", s.Logger)
		return
	}

	config, err := s.Store.OrganizationConfig(ctx).FindOrCreate(ctx, orgID)
	if err != nil {
		Error(w, http.StatusBadRequest, err.Error(), s.Logger)
		return
	}

	if config == nil {
		Error(w, http.StatusBadRequest, "Organization configuration object was nil", s.Logger)
		return
	}
	res := newOrganizationConfigResponse(*config)

	encodeJSON(w, http.StatusOK, res, s.Logger)
}

// LogViewerOrganizationConfig retrieves the log viewer UI section of the organization config
func (s *Service) LogViewerOrganizationConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	orgID, ok := hasOrganizationContext(ctx)
	if !ok {
		Error(w, http.StatusBadRequest, "Organization not found on context", s.Logger)
		return
	}

	config, err := s.Store.OrganizationConfig(ctx).FindOrCreate(ctx, orgID)
	if err != nil {
		Error(w, http.StatusBadRequest, err.Error(), s.Logger)
		return
	}

	if config == nil {
		Error(w, http.StatusBadRequest, "Organization configuration object was nil", s.Logger)
		return
	}

	res := newLogViewerConfigResponse(config.LogViewer)

	encodeJSON(w, http.StatusOK, res, s.Logger)
}

// ReplaceLogViewerOrganizationConfig replaces the log viewer UI section of the organization config
func (s *Service) ReplaceLogViewerOrganizationConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	orgID, ok := hasOrganizationContext(ctx)
	if !ok {
		Error(w, http.StatusBadRequest, "Organization not found on context", s.Logger)
		return
	}

	var logViewerConfig chronograf.LogViewerConfig
	if err := json.NewDecoder(r.Body).Decode(&logViewerConfig); err != nil {
		invalidJSON(w, s.Logger)
		return
	}
	if err := validLogViewerConfig(logViewerConfig); err != nil {
		Error(w, http.StatusBadRequest, err.Error(), s.Logger)
		return
	}

	config, err := s.Store.OrganizationConfig(ctx).FindOrCreate(ctx, orgID)
	if err != nil {
		Error(w, http.StatusBadRequest, err.Error(), s.Logger)
		return
	}
	if config == nil {
		Error(w, http.StatusBadRequest, "Organization configuration object was nil", s.Logger)
		return
	}
	config.LogViewer = logViewerConfig

	res := newLogViewerConfigResponse(config.LogViewer)
	if err := s.Store.OrganizationConfig(ctx).Update(ctx, config); err != nil {
		unknownErrorWithMessage(w, err, s.Logger)
		return
	}

	encodeJSON(w, http.StatusOK, res, s.Logger)
}

// validLogViewerConfig ensures that the request body log viewer UI config is valid
// to be valid, it must: not be empty, have at least one column, not have multiple
// columns with the same name or position value, each column must have a visbility
// of either "visible" or "hidden" and if a column is of type severity, it must have
// at least one severity format of type icon, text, or both
func validLogViewerConfig(cfg chronograf.LogViewerConfig) error {
	if len(cfg.Columns) == 0 {
		return fmt.Errorf("Invalid log viewer config: must have at least 1 column")
	}

	nameMatcher := map[string]bool{}
	positionMatcher := map[int32]bool{}

	for _, clm := range cfg.Columns {
		iconCount := 0
		textCount := 0
		visibility := 0

		// check that each column has a unique value for the name and position properties
		if _, ok := nameMatcher[clm.Name]; ok {
			return fmt.Errorf("Invalid log viewer config: Duplicate column name %s", clm.Name)
		}
		nameMatcher[clm.Name] = true
		if _, ok := positionMatcher[clm.Position]; ok {
			return fmt.Errorf("Invalid log viewer config: Multiple columns with same position value")
		}
		positionMatcher[clm.Position] = true

		for _, e := range clm.Encodings {
			if e.Type == "visibility" {
				visibility++
				if !(e.Value == "visible" || e.Value == "hidden") {
					return fmt.Errorf("Invalid log viewer config: invalid visibility in column %s", clm.Name)
				}
			}

			if clm.Name == "severity" {
				if e.Value == "icon" {
					iconCount++
				} else if e.Value == "text" {
					textCount++
				}
			}
		}

		if visibility != 1 {
			return fmt.Errorf("Invalid log viewer config: missing visibility encoding in column %s", clm.Name)
		}

		if clm.Name == "severity" {
			if iconCount+textCount == 0 || iconCount > 1 || textCount > 1 {
				return fmt.Errorf("Invalid log viewer config: invalid number of severity format encodings in column %s", clm.Name)
			}
		}
	}

	return nil
}
