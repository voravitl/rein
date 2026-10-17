package routing

import "github.com/voravitl/rein/internal/providers"

// QuotaPool is the canonical cooldown scope; billing pools track funding separately.
func QuotaPool(p providers.Provider) (string, error) {
	_, pool, err := identity(p)
	return pool, err
}
