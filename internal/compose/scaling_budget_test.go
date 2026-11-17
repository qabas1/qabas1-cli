package compose

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScalePoolBudgetCountsDifferentWorkerAndAPIPools(t *testing.T) {
	doc := map[string]any{"services": map[string]any{
		"api":                  map[string]any{"environment": map[string]string{"PG_POOL_MAX": "5", "DB_MAX_CONNECTIONS_BUDGET": "50"}},
		"operations-worker":    map[string]any{"environment": map[string]string{"PG_POOL_MAX": "20"}},
		"notifications-worker": map[string]any{"environment": map[string]string{"PG_POOL_MAX": "10"}},
		"pgbouncer":            map[string]any{"environment": map[string]string{"POOL_MODE": "session", "DEFAULT_POOL_SIZE": "40", "MAX_CLIENT_CONN": "1000"}},
	}}
	encode := func() []byte {
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if err := validateScalePoolBudget(encode(), 4); err != nil {
		t.Fatal(err)
	}
	api := doc["services"].(map[string]any)["api"].(map[string]any)["environment"].(map[string]string)
	api["DB_MAX_CONNECTIONS_BUDGET"] = "49"
	if err := validateScalePoolBudget(encode(), 4); err == nil || !strings.Contains(err.Error(), "require up to 50") {
		t.Fatalf("different worker pool sizes were not counted: %v", err)
	}
	api["DB_MAX_CONNECTIONS_BUDGET"] = "80"
	pool := doc["services"].(map[string]any)["pgbouncer"].(map[string]any)["environment"].(map[string]string)
	pool["DEFAULT_POOL_SIZE"] = "19"
	if err := validateScalePoolBudget(encode(), 4); err == nil {
		t.Fatal("undersized session pool accepted")
	}
	pool["DEFAULT_POOL_SIZE"] = "40"
	pool["POOL_MODE"] = "transaction"
	if err := validateScalePoolBudget(encode(), 4); err == nil {
		t.Fatal("transaction pooling accepted")
	}
	pool["POOL_MODE"] = "session"
	api["PG_POOL_MAX"] = "not-a-number"
	if err := validateScalePoolBudget(encode(), 4); err == nil {
		t.Fatal("invalid pool setting accepted")
	}
}
