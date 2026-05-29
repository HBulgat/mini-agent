// Package config loads ~/.mini-agent/config.yaml via viper with the
// override order (D25):
//
//	built-in defaults  →  config file  →  CLI flags
//
// Environment variables are deliberately NOT read as a config source.
//
// Public API:
//
//	cfg, err := config.Load(path)            // path == "" → default location
//	cfg, err  = config.ApplyFlags(cfg, &fo)  // overlay CLI flags + validate
//	fmt.Println(cfg.String())                // secret-masked YAML dump
//
// Sensitive fields (every provider's api_key) are masked by Config.String();
// never log a *Config or its sub-structs directly.
package config
