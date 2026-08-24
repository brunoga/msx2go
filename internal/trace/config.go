package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// A cartridge's config is JSON, and every address in it is written the way a
// person writes addresses -- "0x404B" -- rather than as a decimal number
// nobody can read. Addr unmarshals either.

// UnmarshalJSON accepts "0x401A", "401Ah", "16410" or a bare number.
func (a *Addr) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	s = strings.Trim(s, `"`)
	s = strings.TrimSuffix(strings.TrimSuffix(s, "h"), "H")
	n, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		// A bare hex string with no 0x, which is how some tables are
		// written: "404b".
		n, err = strconv.ParseUint(s, 16, 32)
		if err != nil {
			return fmt.Errorf("trace: %q is not an address", s)
		}
	}
	*a = Addr(n)
	return nil
}

// LoadConfig reads a cartridge config. An empty path gives the zero config,
// which is what a cartridge that needs nothing said about it gets.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	if path == "" {
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}
