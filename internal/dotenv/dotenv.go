// Package dotenv loads KEY=VALUE pairs from a .env file into the process
// environment, as a fallback for variables not already set — a real
// environment variable (e.g. exported by the shell, or by bw-unlock)
// always wins over the file, never the other way around.
package dotenv

import (
	"bufio"
	"os"
	"strings"
)

// Load reads path (if it exists) and calls os.Setenv for every KEY=VALUE
// line whose KEY isn't already set in the environment. Missing file is not
// an error — .env is optional, real env vars are the primary source.
func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		os.Setenv(key, value)
	}
	return scanner.Err()
}
