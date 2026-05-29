package config

import (
	"gopkg.in/yaml.v3"
)

// maskedSecret is the placeholder we substitute in for any non-empty
// API key when rendering a Config for human eyes. We use eight stars
// (matching the OpenAI-style ********) to signal "real value present"
// without revealing length.
const maskedSecret = "********"

// String returns a YAML-serialized, secret-masked snapshot of the
// receiver — safe to log, dump in --version, or echo into Trace.
//
// Why it lives on *Config (D31): this is a private helper to copy and
// mutate; making it a value receiver would silently double-allocate
// the entire Config (~hundreds of bytes plus the slice headers).
func (c *Config) String() string {
	if c == nil {
		return "(nil config)"
	}
	masked := maskedClone(c)
	out, err := yaml.Marshal(&masked)
	if err != nil {
		// yaml.Marshal on a struct of plain types should never fail;
		// if it does, surface a debuggable string instead of panicking.
		return "(config: yaml marshal failed: " + err.Error() + ")"
	}
	return string(out)
}

// maskedClone returns a deep-enough copy of c with every api_key
// replaced by maskedSecret. We only deep-copy the parts that hold
// secrets — the rest can share backing arrays since we never mutate
// them.
func maskedClone(src *Config) Config {
	dst := *src

	if n := len(src.LLM.Providers.OpenAICompat); n > 0 {
		clones := make([]*OpenAICompatCfg, n)
		for i, p := range src.LLM.Providers.OpenAICompat {
			if p == nil {
				continue
			}
			cp := *p
			if cp.APIKey != "" {
				cp.APIKey = maskedSecret
			}
			clones[i] = &cp
		}
		dst.LLM.Providers.OpenAICompat = clones
	}

	if src.LLM.Providers.Anthropic != nil {
		cp := *src.LLM.Providers.Anthropic
		if cp.APIKey != "" {
			cp.APIKey = maskedSecret
		}
		dst.LLM.Providers.Anthropic = &cp
	}

	if src.LLM.Providers.Gemini != nil {
		cp := *src.LLM.Providers.Gemini
		if cp.APIKey != "" {
			cp.APIKey = maskedSecret
		}
		dst.LLM.Providers.Gemini = &cp
	}

	return dst
}
