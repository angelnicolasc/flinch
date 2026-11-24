package author

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
)

// Recognize root-level, always-loaded files and explicit Claude project imports.
// Conditional rules and arbitrary configured filenames need evidence of host loading.
func guideLoaded(ctx context.Context, root, ref, guide string, hosts []string) bool {
	if len(hosts) != 1 {
		return false
	}
	if _, err := gitutil.LocalPath(root, guide); err != nil {
		return false
	}
	switch hosts[0] {
	case "codex":
		if guide == "AGENTS.override.md" {
			return true
		}
		if guide != "AGENTS.md" {
			return false
		}
		override, err := gitutil.FileAtRef(ctx, root, ref, "AGENTS.override.md")
		if err == nil {
			return len(strings.TrimSpace(string(override))) == 0
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		// A developer can ignore a local override. The host still sees it even
		// though the Git snapshots intentionally leave it out.
		info, err := os.Lstat(filepath.Join(root, "AGENTS.override.md"))
		return errors.Is(err, os.ErrNotExist) || (err == nil && info.Mode().IsRegular() && info.Size() == 0)
	case "antigravity":
		switch guide {
		case "AGENTS.md", "GEMINI.md", ".agents/AGENTS.md", ".agents/GEMINI.md":
			return true
		}
	case "claude":
		for _, entry := range []string{"CLAUDE.md", ".claude/CLAUDE.md", "CLAUDE.local.md"} {
			if guide == entry {
				return true
			}
			contents, err := gitutil.FileAtRef(ctx, root, ref, entry)
			if err != nil {
				continue
			}
			var fence string
			inComment := false
			for _, line := range strings.Split(string(contents), "\n") {
				if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
					continue
				}
				line = strings.TrimSpace(line)
				if fence == "" && (inComment || strings.Contains(line, "<!--")) {
					inComment = !strings.Contains(line, "-->")
					continue
				}
				if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
					if fence == "" {
						fence = line[:3]
					} else if strings.HasPrefix(line, fence) {
						fence = ""
					}
					continue
				}
				if fence == "" && strings.HasPrefix(line, "@") {
					p := strings.TrimPrefix(line, "@")
					if !path.IsAbs(p) && path.Join(path.Dir(entry), p) == guide {
						return true
					}
				}
			}
		}
	}
	return false
}

// ManagedGuide returns the eligible shared instructions path for an authoring request.
func ManagedGuide(ctx context.Context, root, ref string, cfg config.Config, host string) string {
	guide := cfg.ContextGuide.File
	if guide == "" {
		guide = "AGENTS.md"
	}
	if !guideLoaded(ctx, root, ref, guide, []string{host}) {
		return ""
	}
	data, err := gitutil.FileAtRef(ctx, root, ref, guide)
	if err != nil {
		return ""
	}
	text := string(data)
	if strings.Count(text, ManagedStartMarker) != 1 || strings.Count(text, ManagedEndMarker) != 1 {
		return ""
	}
	start, end := -1, -1
	for i, line := range strings.Split(text, "\n") {
		switch strings.TrimSpace(line) {
		case ManagedStartMarker:
			start = i
		case ManagedEndMarker:
			end = i
		}
	}
	if start >= 0 && end > start && end-start-1 < cfg.ContextGuide.MaxLines {
		return guide
	}
	return ""
}
