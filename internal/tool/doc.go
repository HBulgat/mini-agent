// Package tool defines the unified Tool interface and supporting types
// (Result / Error / ErrorCode / Category / Mode / Registry) that every
// agent capability implements. See tool.go for the package-level doc.
//
// Implementations live under sub-packages by domain:
//   - fs      — read_file, write_file, edit_file, delete_file, list_dir
//   - search  — grep, glob
//   - shell   — bash
//   - plan    — write_plan
//   - task    — task (sub-agent)
//   - skill   — skill_tool
//   - web     — web_fetch, web_search
//   - ask     — ask_user
//
// Each sub-package follows the template in
// docs/system-design/10-tool-template-and-readfile.md.

package tool
