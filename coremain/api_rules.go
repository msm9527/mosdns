package coremain

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

var diversionRuleProfiles = []struct {
	Type string
	Tag  string
}{
	{Type: "geositecn", Tag: "geosite_cn"},
	{Type: "geositenocn", Tag: "geosite_no_cn"},
	{Type: "geoipcn", Tag: "geoip_cn"},
	{Type: "cuscn", Tag: "cuscn"},
	{Type: "cusnocn", Tag: "cusnocn"},
}

func RegisterRulesAPI(router *chi.Mux, m *Mosdns) {
	router.Route("/api/v1/rules", func(r chi.Router) {
		r.Route("/adguard", func(r chi.Router) {
			r.Get("/", func(w http.ResponseWriter, r *http.Request) {
				handleListAdguardRules(w, r, m)
			})
			r.Post("/", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
				handleCreateAdguardRule(w, r, m)
			}))
			r.Put("/{id}", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
				handleUpdateAdguardRule(w, r, m)
			}))
			r.Delete("/{id}", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
				handleDeleteAdguardRule(w, r, m)
			}))
			r.Post("/update", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
				handleTriggerAdguardUpdate(w, r, m)
			}))
		})

		r.Route("/diversion", func(r chi.Router) {
			r.Get("/", func(w http.ResponseWriter, r *http.Request) {
				handleListDiversionRules(w, r, m)
			})
			r.Put("/{type}/{name}", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
				handleUpsertDiversionRule(w, r, m)
			}))
			r.Delete("/{type}/{name}", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
				handleDeleteDiversionRule(w, r, m)
			}))
			r.Post("/{type}/{name}/update", WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
				handleTriggerDiversionRuleUpdate(w, r, m)
			}))
		})
	})
}

func handleListAdguardRules(w http.ResponseWriter, _ *http.Request, m *Mosdns) {
	controller, ok := m.GetPlugin("adguard").(AdguardRuleController)
	if !ok || controller == nil {
		writeAPIError(w, http.StatusNotFound, "RULES_ADGUARD_UNAVAILABLE", "adguard rules controller not available")
		return
	}

	items, err := controller.ListAdguardRules()
	if err != nil {
		writeRuleError(w, "RULES_ADGUARD_LIST_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func handleCreateAdguardRule(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	controller, ok := m.GetPlugin("adguard").(AdguardRuleController)
	if !ok || controller == nil {
		writeAPIError(w, http.StatusNotFound, "RULES_ADGUARD_UNAVAILABLE", "adguard rules controller not available")
		return
	}

	var item AdguardRuleItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		writeAPIError(w, http.StatusBadRequest, "RULES_ADGUARD_INVALID_BODY", "invalid request body")
		return
	}

	created, err := controller.CreateAdguardRule(item)
	if err != nil {
		writeRuleError(w, "RULES_ADGUARD_CREATE_FAILED", err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func handleUpdateAdguardRule(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	controller, ok := m.GetPlugin("adguard").(AdguardRuleController)
	if !ok || controller == nil {
		writeAPIError(w, http.StatusNotFound, "RULES_ADGUARD_UNAVAILABLE", "adguard rules controller not available")
		return
	}

	var item AdguardRuleItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		writeAPIError(w, http.StatusBadRequest, "RULES_ADGUARD_INVALID_BODY", "invalid request body")
		return
	}

	updated, err := controller.UpdateAdguardRule(chi.URLParam(r, "id"), item)
	if err != nil {
		writeRuleError(w, "RULES_ADGUARD_UPDATE_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func handleDeleteAdguardRule(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	controller, ok := m.GetPlugin("adguard").(AdguardRuleController)
	if !ok || controller == nil {
		writeAPIError(w, http.StatusNotFound, "RULES_ADGUARD_UNAVAILABLE", "adguard rules controller not available")
		return
	}

	if err := controller.DeleteAdguardRule(chi.URLParam(r, "id")); err != nil {
		writeRuleError(w, "RULES_ADGUARD_DELETE_FAILED", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleTriggerAdguardUpdate(w http.ResponseWriter, _ *http.Request, m *Mosdns) {
	controller, ok := m.GetPlugin("adguard").(AdguardRuleController)
	if !ok || controller == nil {
		writeAPIError(w, http.StatusNotFound, "RULES_ADGUARD_UNAVAILABLE", "adguard rules controller not available")
		return
	}

	if err := controller.TriggerAdguardUpdate(); err != nil {
		writeRuleError(w, "RULES_ADGUARD_TRIGGER_FAILED", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "accepted",
		"message": "已开始在后台更新所有启用的拦截规则。",
	})
}

func handleListDiversionRules(w http.ResponseWriter, _ *http.Request, m *Mosdns) {
	items := make([]DiversionRuleItem, 0)
	for _, profile := range diversionRuleProfiles {
		controller, ok := m.GetPlugin(profile.Tag).(DiversionRuleController)
		if !ok || controller == nil {
			continue
		}
		rules, err := controller.ListDiversionRules()
		if err != nil {
			writeRuleError(w, "RULES_DIVERSION_LIST_FAILED", err)
			return
		}
		for i := range rules {
			if strings.TrimSpace(rules[i].Type) == "" {
				rules[i].Type = profile.Type
			}
		}
		items = append(items, rules...)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Type == items[j].Type {
			return items[i].Name < items[j].Name
		}
		return items[i].Type < items[j].Type
	})
	writeJSON(w, http.StatusOK, items)
}

func handleUpsertDiversionRule(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	ruleType := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "type")))
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	controller, ok := resolveDiversionRuleController(m, ruleType)
	if !ok || controller == nil {
		writeAPIError(w, http.StatusNotFound, "RULES_DIVERSION_TYPE_NOT_FOUND", "diversion rule type not found")
		return
	}

	var item DiversionRuleItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		writeAPIError(w, http.StatusBadRequest, "RULES_DIVERSION_INVALID_BODY", "invalid request body")
		return
	}
	item.Name = name
	item.Type = ruleType

	updated, created, err := controller.UpsertDiversionRule(name, item)
	if err != nil {
		writeRuleError(w, "RULES_DIVERSION_UPSERT_FAILED", err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, updated)
}

func handleDeleteDiversionRule(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	ruleType := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "type")))
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	controller, ok := resolveDiversionRuleController(m, ruleType)
	if !ok || controller == nil {
		writeAPIError(w, http.StatusNotFound, "RULES_DIVERSION_TYPE_NOT_FOUND", "diversion rule type not found")
		return
	}

	if err := controller.DeleteDiversionRule(name); err != nil {
		writeRuleError(w, "RULES_DIVERSION_DELETE_FAILED", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleTriggerDiversionRuleUpdate(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	ruleType := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "type")))
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	controller, ok := resolveDiversionRuleController(m, ruleType)
	if !ok || controller == nil {
		writeAPIError(w, http.StatusNotFound, "RULES_DIVERSION_TYPE_NOT_FOUND", "diversion rule type not found")
		return
	}

	if err := controller.TriggerDiversionRuleUpdate(name); err != nil {
		writeRuleError(w, "RULES_DIVERSION_TRIGGER_FAILED", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "accepted",
		"message": "规则更新任务已在后台启动。",
	})
}

func resolveDiversionRuleController(m *Mosdns, ruleType string) (DiversionRuleController, bool) {
	for _, profile := range diversionRuleProfiles {
		if profile.Type != ruleType {
			continue
		}
		controller, ok := m.GetPlugin(profile.Tag).(DiversionRuleController)
		return controller, ok && controller != nil
	}
	return nil, false
}

func writeRuleError(w http.ResponseWriter, fallbackCode string, err error) {
	var apiErr *RuleAPIError
	if errors.As(err, &apiErr) {
		writeAPIError(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	writeAPIErrorFromErr(w, http.StatusInternalServerError, fallbackCode, err)
}
