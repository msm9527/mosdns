package coremain

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
	"github.com/go-chi/chi/v5"
)

func RegisterRulesAPI(router *chi.Mux, m *Mosdns) {
	router.Route("/api/v1/rules", func(r chi.Router) {
		registerRuleScopeRoutes(r, m, "adguard", rulesource.ScopeAdguard)
		registerRuleScopeRoutes(r, m, "diversion", rulesource.ScopeDiversion)
	})
}

func registerRuleScopeRoutes(r chi.Router, m *Mosdns, path string, scope rulesource.Scope) {
	service := newRuleSourceService(m, scope)
	r.Route("/"+path, func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
			items, err := service.List()
			if err != nil {
				writeRuleError(w, "RULE_SOURCE_LIST_FAILED", err)
				return
			}
			writeJSON(w, http.StatusOK, items)
		})
		r.Post("/", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
			item, err := decodeRuleSourceItem(r)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "RULE_SOURCE_INVALID_BODY", "invalid request body")
				return
			}
			created, err := service.Create(item)
			if err != nil {
				writeRuleError(w, "RULE_SOURCE_CREATE_FAILED", err)
				return
			}
			writeJSON(w, http.StatusCreated, created)
		}))
		r.Put("/{id}", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
			item, err := decodeRuleSourceItem(r)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "RULE_SOURCE_INVALID_BODY", "invalid request body")
				return
			}
			updated, err := service.Update(chi.URLParam(r, "id"), item)
			if err != nil {
				writeRuleError(w, "RULE_SOURCE_UPDATE_FAILED", err)
				return
			}
			writeJSON(w, http.StatusOK, updated)
		}))
		r.Delete("/{id}", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
			result, err := service.Delete(chi.URLParam(r, "id"))
			if err != nil {
				writeRuleError(w, "RULE_SOURCE_DELETE_FAILED", err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		r.Post("/update", WithAsyncGC(func(w http.ResponseWriter, _ *http.Request) {
			items, err := service.RefreshAll()
			if err != nil {
				writeRuleError(w, "RULE_SOURCE_REFRESH_FAILED", err)
				return
			}
			writeJSON(w, http.StatusOK, items)
		}))
		r.Post("/{id}/update", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
			item, err := service.RefreshOne(chi.URLParam(r, "id"))
			if err != nil {
				writeRuleError(w, "RULE_SOURCE_REFRESH_FAILED", err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		}))
	})
}

func decodeRuleSourceItem(r *http.Request) (RuleSourceItem, error) {
	var item RuleSourceItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		return RuleSourceItem{}, err
	}
	return item, nil
}

func writeRuleError(w http.ResponseWriter, fallbackCode string, err error) {
	var apiErr *RuleAPIError
	if errors.As(err, &apiErr) {
		writeAPIError(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	writeAPIErrorFromErr(w, http.StatusInternalServerError, fallbackCode, err)
}
