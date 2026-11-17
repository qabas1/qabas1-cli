package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/qabas1/qabas1-cli/internal/paths"
)

// CheckScalePoolBudget reads merged configuration without starting containers or
// printing its secret-bearing environment. Count worker pools separately from API pools.
func (r *Runtime) CheckScalePoolBudget(p paths.Paths, env []string, replicas int) error {
	var out, stderr bytes.Buffer
	if err := r.dockerOut(p, env, []string{"config", "--format", "json"}, &out, &stderr); err != nil {
		return fmt.Errorf("cannot render Compose configuration for the database connection budget")
	}
	return validateScalePoolBudget(out.Bytes(), replicas)
}

func validateScalePoolBudget(raw []byte, replicas int) error {
	var doc struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
			Deploy      struct {
				Replicas *int `json:"replicas"`
			} `json:"deploy"`
		} `json:"services"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("cannot decode Compose configuration for the database connection budget")
	}
	api, ok := doc.Services["api"]
	if !ok || replicas < 1 {
		return fmt.Errorf("missing API service or invalid API replica count")
	}
	read := func(values map[string]string, key string, fallback, maximum int) (int, error) {
		value := values[key]
		if value == "" {
			return fallback, nil
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > maximum {
			return 0, fmt.Errorf("invalid %s for the database connection budget", key)
		}
		return n, nil
	}
	apiMax, err := read(api.Environment, "PG_POOL_MAX", 10, 100)
	if err != nil {
		return err
	}
	budget, err := read(api.Environment, "DB_MAX_CONNECTIONS_BUDGET", 80, 10000)
	if err != nil {
		return err
	}
	apiConnections := replicas * apiMax
	total := apiConnections
	for _, name := range APIImageServices() {
		if name == "api" {
			continue
		}
		worker, exists := doc.Services[name]
		if !exists {
			continue
		}
		maximum, err := read(worker.Environment, "PG_POOL_MAX", 10, 100)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		count := 1
		if worker.Deploy.Replicas != nil {
			count = *worker.Deploy.Replicas
		}
		if count < 0 || count > 500 {
			return fmt.Errorf("invalid worker replica count for %s", name)
		}
		total += count * maximum
	}
	if total > budget {
		return fmt.Errorf("database connection budget exceeded: API and worker pools require up to %d connections, budget is %d", total, budget)
	}
	pool, ok := doc.Services["pgbouncer"]
	if !ok || pool.Environment["POOL_MODE"] != "session" {
		return fmt.Errorf("scaling requires PgBouncer session pooling")
	}
	servers, err := read(pool.Environment, "DEFAULT_POOL_SIZE", 40, 10000)
	if err != nil {
		return err
	}
	clients, err := read(pool.Environment, "MAX_CLIENT_CONN", 1000, 100000)
	if err != nil {
		return err
	}
	if servers < apiConnections || clients < apiConnections {
		return fmt.Errorf("PgBouncer capacity is below the API pool maximum (%d connections); size DEFAULT_POOL_SIZE and MAX_CLIENT_CONN before scaling", apiConnections)
	}
	return nil
}
