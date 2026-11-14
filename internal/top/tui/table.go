package tui

import (
	"sort"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/top"
)

// SortKey orders the services pane.
type SortKey int

const (
	SortCPU SortKey = iota
	SortMem
	SortNet
	SortIO
	SortRestarts
	SortName
	sortKeyCount
)

func (k SortKey) String() string {
	return [...]string{"cpu", "mem", "net", "io", "restarts", "name"}[k]
}

// Next cycles to the following sort key.
func (k SortKey) Next() SortKey { return (k + 1) % sortKeyCount }

// ParseSortKey maps a flag value to a key (unknown values fall back to cpu).
func ParseSortKey(s string) SortKey {
	for k := SortKey(0); k < sortKeyCount; k++ {
		if strings.EqualFold(s, k.String()) {
			return k
		}
	}
	return SortCPU
}

// SortServices returns a sorted copy: heaviest first for numeric keys, A-Z for name.
// reverse flips the order. Ties break on service name so rows never jitter.
func SortServices(rows []top.Service, key SortKey, reverse bool) []top.Service {
	out := append([]top.Service(nil), rows...)
	metric := func(s top.Service) float64 {
		switch key {
		case SortMem:
			return float64(s.MemBytes)
		case SortNet:
			return s.NetRxPerSec + s.NetTxPerSec
		case SortIO:
			return s.BlockRPerSec + s.BlockWPerSec
		case SortRestarts:
			return float64(s.Restarts)
		}
		return s.CPUPercent
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if reverse {
			a, b = b, a
		}
		if key != SortName {
			if ma, mb := metric(a), metric(b); ma != mb {
				return ma > mb
			}
		}
		return a.Service < b.Service
	})
	return out
}

// FilterServices keeps rows whose service, container name, state, or health
// contains q (case-insensitive).
func FilterServices(rows []top.Service, q string) []top.Service {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return rows
	}
	var out []top.Service
	for _, r := range rows {
		hay := strings.ToLower(r.Service + " " + r.Name + " " + r.State + " " + r.Health)
		if strings.Contains(hay, q) {
			out = append(out, r)
		}
	}
	return out
}
