package permission

// hardDenylist returns the code-built-in non-overridable rules. R4
// §7.4 fixed the *position* in code; T2.4 expanded the *contents*
// while bash tool implementation was settled. New patterns must come
// with a test in hard_denylist_test.go demonstrating the pattern
// fires even under ModeYes.
//
// The function returns a fresh slice on every call so callers (boot
// path, tests) can mutate the slice without trampling shared state.
//
// Match strategy (see internal/permission/matcher.go):
//
//   - For bash patterns, two passes run in order:
//       1. literal equality after whitespace normalisation (catches
//          patterns containing characters doublestar treats as glob
//          metacharacters but that we want literal — chiefly the
//          fork-bomb spellings ":(){:|:&};:" which contain "{").
//       2. doublestar glob match. Patterns intended to match a
//          family of commands flow through here.
//   - For path-based patterns (ToolName empty), we substitute
//     ${cwd}/${home} via Substitutor before matching.
//
// Patterns marked as "literal-equality only" do NOT contain glob
// metacharacters; patterns marked as "glob" do. A literal pattern
// like "rm -rf /" only matches the exact string "rm -rf /" and will
// NOT fire on "rm -rf /home". Use the "*" suffix variant
// ("rm -rf /*") to match "rm -rf /<anything>".
func hardDenylist() []*HardDenyRule {
	return []*HardDenyRule{
		// ============================================================
		// Filesystem destruction (rm -rf variants)
		// ============================================================
		// "rm -rf /" — literal-equality (no glob meta). Catches the
		// canonical command exactly. The "/**" variant below is for
		// "rm -rf /<anything>" — we use ** rather than * because the
		// doublestar engine treats "/" as a separator and "*" never
		// crosses it; "**" does.
		{ToolName: "bash", Pattern: "rm -rf /", Reason: "destroys root filesystem"},
		{ToolName: "bash", Pattern: "rm -rf /**", Reason: "destroys root filesystem"},
		{ToolName: "bash", Pattern: "rm -fr /", Reason: "destroys root filesystem (flag order variant)"},
		{ToolName: "bash", Pattern: "rm -fr /**", Reason: "destroys root filesystem (flag order variant)"},
		{ToolName: "bash", Pattern: "rm -rf --no-preserve-root /**", Reason: "explicit root destruction bypass"},
		// Home directory variants.
		{ToolName: "bash", Pattern: "rm -rf ~", Reason: "destroys home directory"},
		{ToolName: "bash", Pattern: "rm -rf ~/**", Reason: "destroys home directory"},
		{ToolName: "bash", Pattern: "rm -rf $HOME", Reason: "destroys home directory"},
		{ToolName: "bash", Pattern: "rm -rf $HOME/**", Reason: "destroys home directory"},
		{ToolName: "bash", Pattern: "rm -rf ${HOME}", Reason: "destroys home directory (braced var)"},
		{ToolName: "bash", Pattern: "rm -rf ${HOME}/**", Reason: "destroys home directory (braced var)"},

		// ============================================================
		// Block device / partition destruction
		// ============================================================
		// dd writing to a raw device. "**" lets the pattern cross
		// path separators in flag values like "if=/dev/zero".
		{ToolName: "bash", Pattern: "dd ** of=/dev/sd**", Reason: "writes to raw block device (data loss)"},
		{ToolName: "bash", Pattern: "dd ** of=/dev/nvme**", Reason: "writes to NVMe device (data loss)"},
		{ToolName: "bash", Pattern: "dd ** of=/dev/hd**", Reason: "writes to legacy IDE device (data loss)"},
		{ToolName: "bash", Pattern: "dd ** of=/dev/disk**", Reason: "writes to macOS raw disk (data loss)"},
		// mkfs / wipefs on real devices.
		{ToolName: "bash", Pattern: "mkfs** /dev/sd**", Reason: "reformats block device"},
		{ToolName: "bash", Pattern: "mkfs** /dev/nvme**", Reason: "reformats NVMe device"},
		{ToolName: "bash", Pattern: "wipefs** /dev/sd**", Reason: "wipes filesystem signatures"},
		{ToolName: "bash", Pattern: "wipefs** /dev/nvme**", Reason: "wipes filesystem signatures"},
		// Direct shred of raw devices.
		{ToolName: "bash", Pattern: "shred ** /dev/sd**", Reason: "shreds block device"},
		{ToolName: "bash", Pattern: "shred ** /dev/nvme**", Reason: "shreds NVMe device"},

		// ============================================================
		// Fork bomb (literal-equality; doublestar would parse "{")
		// ============================================================
		{ToolName: "bash", Pattern: ":(){:|:&};:", Reason: "fork bomb"},
		{ToolName: "bash", Pattern: ":(){ :|:& };:", Reason: "fork bomb"},
		// "yes | head" patterns are noisy but not catastrophic; we
		// don't blacklist them. A user who really needs `:(){...}` can
		// always run it outside the agent.

		// ============================================================
		// System credential / boot path mutations via redirect/tee
		// ============================================================
		// The leading "**" catches "echo X > /etc/passwd", "tee /etc/passwd",
		// "cat foo >> /etc/sudoers", etc. We use ** so the prefix
		// portion can include path separators (URLs, flag values).
		{ToolName: "bash", Pattern: "** > /etc/passwd**", Reason: "modifies system credentials"},
		{ToolName: "bash", Pattern: "** >> /etc/passwd**", Reason: "modifies system credentials"},
		{ToolName: "bash", Pattern: "** > /etc/sudoers**", Reason: "modifies sudoers"},
		{ToolName: "bash", Pattern: "** >> /etc/sudoers**", Reason: "modifies sudoers"},
		{ToolName: "bash", Pattern: "** > /etc/shadow**", Reason: "modifies password shadow file"},
		{ToolName: "bash", Pattern: "** >> /etc/shadow**", Reason: "modifies password shadow file"},
		{ToolName: "bash", Pattern: "** > /etc/hosts**", Reason: "rewrites system hosts file"},
		// tee variants (less common but a real bypass route).
		{ToolName: "bash", Pattern: "** | tee /etc/passwd**", Reason: "modifies system credentials"},
		{ToolName: "bash", Pattern: "** | tee /etc/sudoers**", Reason: "modifies sudoers"},
		{ToolName: "bash", Pattern: "** | tee /etc/shadow**", Reason: "modifies password shadow file"},
		{ToolName: "bash", Pattern: "** | tee -a /etc/passwd**", Reason: "appends to system credentials"},
		{ToolName: "bash", Pattern: "** | tee -a /etc/sudoers**", Reason: "appends to sudoers"},

		// ============================================================
		// "Pipe-to-shell" remote code execution patterns
		// ============================================================
		// `curl <url> | sh` and friends — the canonical "trust the
		// internet" anti-pattern. We use ** so the URL (which contains
		// "/") matches.
		{ToolName: "bash", Pattern: "curl ** | sh**", Reason: "pipes remote content to shell"},
		{ToolName: "bash", Pattern: "curl ** | bash**", Reason: "pipes remote content to bash"},
		{ToolName: "bash", Pattern: "wget ** | sh**", Reason: "pipes remote content to shell"},
		{ToolName: "bash", Pattern: "wget ** | bash**", Reason: "pipes remote content to bash"},
		{ToolName: "bash", Pattern: "curl ** -o - | sh**", Reason: "pipes remote content to shell"},
		{ToolName: "bash", Pattern: "wget ** -O - | sh**", Reason: "pipes remote content to shell"},

		// ============================================================
		// Permission / ownership chaos on system trees
		// ============================================================
		// chmod -R 777 / and friends. We list the obvious roots only;
		// user-installed paths are expected to be safe enough to chmod.
		{ToolName: "bash", Pattern: "chmod -R 777 /", Reason: "world-writes the entire root filesystem"},
		{ToolName: "bash", Pattern: "chmod -R 777 /**", Reason: "world-writes the entire root filesystem"},
		{ToolName: "bash", Pattern: "chmod -R 000 /", Reason: "removes all access from root filesystem"},
		{ToolName: "bash", Pattern: "chmod -R 000 /**", Reason: "removes all access from root filesystem"},
		{ToolName: "bash", Pattern: "chown -R ** /", Reason: "rewrites ownership of root filesystem"},
		{ToolName: "bash", Pattern: "chown -R ** /**", Reason: "rewrites ownership of root filesystem"},

		// ============================================================
		// Git "destroy your work" combos
		// ============================================================
		// We are intentionally narrow here: many destructive git
		// operations are legitimate (`git reset --hard`). We only
		// blacklist the "I'm going to destroy upstream too" ones.
		{ToolName: "bash", Pattern: "git push --force-with-lease origin main**", Reason: "force pushes to main branch"},
		{ToolName: "bash", Pattern: "git push --force origin main**", Reason: "force pushes to main branch"},
		{ToolName: "bash", Pattern: "git push -f origin main**", Reason: "force pushes to main branch"},
		{ToolName: "bash", Pattern: "git push --force origin master**", Reason: "force pushes to master branch"},
		{ToolName: "bash", Pattern: "git push -f origin master**", Reason: "force pushes to master branch"},

		// ============================================================
		// Filesystem tool path-based bans
		// ============================================================
		// Apply to every write tool (write_file / edit_file /
		// delete_file). ToolName="" iterates every write-class tool;
		// see matchHardDeny in matcher.go.
		{ToolName: "", Pattern: "/etc/passwd", Reason: "writing to system credentials is forbidden"},
		{ToolName: "", Pattern: "/etc/sudoers", Reason: "writing to sudoers is forbidden"},
		{ToolName: "", Pattern: "/etc/shadow", Reason: "writing to password shadow file is forbidden"},
		{ToolName: "", Pattern: "/etc/hosts", Reason: "writing to system hosts file is forbidden"},
		// Boot configuration.
		{ToolName: "", Pattern: "/boot/**", Reason: "writing to boot partition is forbidden"},
		// SSH keys are sensitive — agent should never silently rewrite them.
		{ToolName: "", Pattern: "/root/.ssh/**", Reason: "writing to root's SSH config is forbidden"},
	}
}
