package permission

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// UserRule is one entry from ~/.mini-agent/permissions.yaml. Matches
// the YAML keys verbatim through the custom UnmarshalYAML below — the
// raw YAML uses string labels ("allow"/"deny", "command"/"path"/
// "tool") while we want typed enums in memory.
//
// Per D31 we keep this a value type; rule slices use *UserRule when
// they get large (R4 §7.3.7 footnote).
type UserRule struct {
	Type        RuleType    `yaml:"-"`
	Granularity Granularity `yaml:"-"`
	ToolName    string      `yaml:"tool_name,omitempty"` // optional override for command/path rules
	Pattern     string      `yaml:"pattern"`
	Reason      string      `yaml:"reason,omitempty"`
}

// userRuleYAML is the on-disk shape. We keep it private so callers
// always see the typed UserRule.
type userRuleYAML struct {
	Type        string `yaml:"type"`
	Granularity string `yaml:"granularity"`
	ToolName    string `yaml:"tool_name,omitempty"`
	Pattern     string `yaml:"pattern"`
	Reason      string `yaml:"reason,omitempty"`
}

// UnmarshalYAML is defined on *UserRule (not the YAML helper) so
// callers can yaml.Unmarshal directly into UserRule values without an
// extra translation step.
func (u *UserRule) UnmarshalYAML(value *yaml.Node) error {
	var raw userRuleYAML
	if err := value.Decode(&raw); err != nil {
		return err
	}
	switch raw.Type {
	case "allow":
		u.Type = RuleAllow
	case "deny":
		u.Type = RuleDeny
	default:
		return fmt.Errorf("permission: invalid rule type %q (want allow|deny)", raw.Type)
	}
	switch raw.Granularity {
	case "command":
		u.Granularity = GranCommand
	case "path":
		u.Granularity = GranPath
	case "tool":
		u.Granularity = GranTool
	default:
		return fmt.Errorf("permission: invalid granularity %q (want command|path|tool)", raw.Granularity)
	}
	if raw.Pattern == "" {
		return errors.New("permission: rule.pattern is required")
	}
	u.ToolName = raw.ToolName
	u.Pattern = raw.Pattern
	u.Reason = raw.Reason
	return nil
}

// HardDenyRule is a code-built-in non-overridable rule. The agent
// installs these into RuleSet.HardDenylist at boot via hardDenylist().
// Patterns are matched against Operation.Command for bash and against
// Operation.Path for filesystem tools — see matchHardDeny.
type HardDenyRule struct {
	// ToolName scopes the pattern to a specific tool. Empty
	// matches every tool (rare; meant for global path-based bans).
	ToolName string

	// Pattern is interpreted as a glob (doublestar) against the
	// command string (when ToolName=="bash") or against the path
	// (otherwise). Variable substitution (${cwd}/${home}/~) does
	// NOT apply to hard rules since they're not user-supplied.
	Pattern string

	// Reason is the user-facing English string surfaced when the
	// rule fires. Should explain the danger ("destroys root
	// filesystem", "fork bomb", etc.).
	Reason string
}

// RuleSet holds both layers (hard blacklist + user rules) so the gate
// only has to track one struct.
//
// We use *HardDenyRule / *UserRule slices because the rule count can
// reach a few hundred in big setups and the field-by-field copy on
// every Check call would be wasteful (D31 footnote).
type RuleSet struct {
	UserRules    []*UserRule
	HardDenylist []*HardDenyRule
}

// LoadRules reads + parses the YAML rules file at rulesFile. A
// non-existent file is NOT an error — it simply produces a RuleSet
// with only the hard blacklist populated (R4 §7.3.1). This way the
// agent boots cleanly on a fresh machine.
//
// Empty rulesFile (no path configured) follows the same behaviour:
// hard blacklist only.
func LoadRules(rulesFile string) (*RuleSet, error) {
	rs := &RuleSet{HardDenylist: hardDenylist()}
	if rulesFile == "" {
		return rs, nil
	}
	data, err := os.ReadFile(rulesFile)
	if errors.Is(err, fs.ErrNotExist) {
		return rs, nil
	}
	if err != nil {
		return nil, fmt.Errorf("permission: read rules file %s: %w", rulesFile, err)
	}

	var doc struct {
		Rules []*UserRule `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("permission: parse rules file %s: %w", rulesFile, err)
	}
	rs.UserRules = doc.Rules
	return rs, nil
}
