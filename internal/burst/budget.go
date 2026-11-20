package burst

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Budget tracks VM-hours spent today (UTC), saved so a restart cannot reset
// the daily cap. Only VMs the provider lists are charged, so a create that
// made no VM costs nothing.
type Budget struct {
	path string
	Day  string  `json:"day"`
	Used float64 `json:"used_hours"`
}

func loadBudget(dir string) (*Budget, error) {
	b := &Budget{}
	raw, err := os.ReadFile(filepath.Join(dir, "budget.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, b); err != nil {
			return nil, err
		}
	}
	b.path = filepath.Join(dir, "budget.json")
	return b, nil
}

func (b *Budget) roll(now time.Time) {
	if day := now.UTC().Format(time.DateOnly); day != b.Day {
		b.Day, b.Used = day, 0
	}
}

// Accrue charges vms VMs for the interval d.
func (b *Budget) Accrue(now time.Time, d time.Duration, vms int) {
	b.roll(now)
	b.Used += d.Hours() * float64(vms)
}

// OK reports whether a new VM may be created today. capHours 0 = unlimited.
func (b *Budget) OK(now time.Time, capHours float64) bool {
	b.roll(now)
	return capHours <= 0 || b.Used < capHours
}

// Save writes the ledger atomically with mode 0600.
func (b *Budget) Save() error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}
