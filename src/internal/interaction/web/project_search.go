package web

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	ProjectSearchReasonEmptyQuery      = "empty_query"
	ProjectSearchReasonInvalidRegex    = "invalid_regex"
	ProjectSearchReasonFileLimit       = "file_limit"
	ProjectSearchReasonTotalMatchLimit = "total_match_limit"
	ProjectSearchReasonFileMatchLimit  = "file_match_limit"
)

const (
	ProjectSearchDefaultMaxScanFiles      = 2000
	ProjectSearchDefaultMaxMatchesPerFile = 20
	ProjectSearchDefaultMaxTotalMatches   = 200
	ProjectSearchDefaultMaxSnippetRunes   = 240
)

// ProjectSearchRequest describes a bounded content search inside the project.
type ProjectSearchRequest struct {
	Query   string   `json:"query"`
	Path    string   `json:"path,omitempty"`
	Regex   bool     `json:"regex,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// ProjectSearchLimits reports the effective limits used by a search.
type ProjectSearchLimits struct {
	MaxScanFiles      int   `json:"max_scan_files"`
	MaxFileBytes      int64 `json:"max_file_bytes"`
	MaxMatchesPerFile int   `json:"max_matches_per_file"`
	MaxTotalMatches   int   `json:"max_total_matches"`
	MaxSnippetRunes   int   `json:"max_snippet_runes"`
}

// ProjectSearchLineMatch is one matched line in a searched file.
type ProjectSearchLineMatch struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Summary string `json:"summary"`
}

// ProjectSearchFileResult groups all returned line matches for one file.
type ProjectSearchFileResult struct {
	Path         string                   `json:"path"`
	Language     string                   `json:"language,omitempty"`
	RenderType   string                   `json:"render_type"`
	Lines        []ProjectSearchLineMatch `json:"lines"`
	TotalMatches int                      `json:"total_matches"`
	Truncated    bool                     `json:"truncated"`
	Reason       string                   `json:"reason,omitempty"`
}

// ProjectSearchResult is the response model for a project content search.
type ProjectSearchResult struct {
	OK           bool                      `json:"ok"`
	Reason       string                    `json:"reason,omitempty"`
	Query        string                    `json:"query"`
	Path         string                    `json:"path"`
	Regex        bool                      `json:"regex"`
	Files        []ProjectSearchFileResult `json:"files"`
	TotalMatches int                       `json:"total_matches"`
	ScannedFiles int                       `json:"scanned_files"`
	SkippedFiles int                       `json:"skipped_files"`
	Truncated    bool                      `json:"truncated"`
	TruncatedBy  string                    `json:"truncated_by,omitempty"`
	Limits       ProjectSearchLimits       `json:"limits"`
}

// ProjectSearcher contains read-only project content search rules for WebUI.
type ProjectSearcher struct {
	browser           *ProjectFileBrowser
	maxScanFiles      int
	maxFileBytes      int64
	maxMatchesPerFile int
	maxTotalMatches   int
	maxSnippetRunes   int
}

// NewProjectSearcher creates a searcher with conservative default limits.
func NewProjectSearcher(workdir string) *ProjectSearcher {
	return NewProjectSearcherWithLimits(
		workdir,
		ProjectSearchDefaultMaxScanFiles,
		ProjectFileDefaultMaxBytes,
		ProjectSearchDefaultMaxMatchesPerFile,
		ProjectSearchDefaultMaxTotalMatches,
		ProjectSearchDefaultMaxSnippetRunes,
	)
}

// NewProjectSearcherWithLimits creates a searcher with explicit limits.
func NewProjectSearcherWithLimits(workdir string, maxScanFiles int, maxFileBytes int64, maxMatchesPerFile int, maxTotalMatches int, maxSnippetRunes int) *ProjectSearcher {
	if maxScanFiles <= 0 {
		maxScanFiles = ProjectSearchDefaultMaxScanFiles
	}
	if maxFileBytes <= 0 {
		maxFileBytes = ProjectFileDefaultMaxBytes
	}
	if maxMatchesPerFile <= 0 {
		maxMatchesPerFile = ProjectSearchDefaultMaxMatchesPerFile
	}
	if maxTotalMatches <= 0 {
		maxTotalMatches = ProjectSearchDefaultMaxTotalMatches
	}
	if maxSnippetRunes <= 0 {
		maxSnippetRunes = ProjectSearchDefaultMaxSnippetRunes
	}
	return &ProjectSearcher{
		browser:           NewProjectFileBrowserWithLimits(workdir, ProjectFileDefaultMaxEntries, maxFileBytes),
		maxScanFiles:      maxScanFiles,
		maxFileBytes:      maxFileBytes,
		maxMatchesPerFile: maxMatchesPerFile,
		maxTotalMatches:   maxTotalMatches,
		maxSnippetRunes:   maxSnippetRunes,
	}
}

// Search scans text files under the requested project path.
func (s *ProjectSearcher) Search(req ProjectSearchRequest) (ProjectSearchResult, error) {
	result := ProjectSearchResult{
		Query:  req.Query,
		Regex:  req.Regex,
		Limits: s.limits(),
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		result.Reason = ProjectSearchReasonEmptyQuery
		return result, errors.New("project search query is empty")
	}

	matcher, err := newProjectSearchMatcher(query, req.Regex)
	if err != nil {
		result.Reason = ProjectSearchReasonInvalidRegex
		return result, err
	}

	absPath, cleanRel, err := s.browser.resolveProjectPath(req.Path)
	result.Path = cleanRel
	if err != nil {
		result.Reason = projectPathErrorReason(err)
		return result, err
	}
	root, err := s.browser.projectRoot()
	if err != nil {
		result.Reason = projectPathErrorReason(err)
		return result, err
	}

	excludes := normalizeProjectSearchExcludes(req.Exclude)
	filesByPath := make(map[string]int)
	walkErr := filepath.WalkDir(absPath, func(current string, entry fs.DirEntry, entryErr error) error {
		if entryErr != nil {
			return nil
		}

		rel, err := filepath.Rel(root, current)
		if err != nil {
			return nil
		}
		if rel == "." {
			rel = ""
		}
		rel = filepath.ToSlash(rel)

		if entry.IsDir() {
			if rel != "" && (isDefaultProjectSearchSkipDir(rel) || matchesProjectSearchExclude(rel, true, excludes)) {
				return filepath.SkipDir
			}
			return nil
		}
		if rel != "" && matchesProjectSearchExclude(rel, false, excludes) {
			result.SkippedFiles++
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			result.SkippedFiles++
			return nil
		}

		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if result.ScannedFiles >= s.maxScanFiles {
			result.Truncated = true
			result.TruncatedBy = ProjectSearchReasonFileLimit
			return filepath.SkipAll
		}
		if info.Size() > s.maxFileBytes {
			result.SkippedFiles++
			return nil
		}
		binary, err := isProjectBinaryFile(current)
		if err != nil || binary {
			result.SkippedFiles++
			return nil
		}

		result.ScannedFiles++
		fileResult, stop, err := s.searchFile(current, rel, matcher, &result)
		if err != nil {
			result.SkippedFiles++
			return nil
		}
		if fileResult.TotalMatches > 0 {
			idx, ok := filesByPath[fileResult.Path]
			if ok {
				result.Files[idx].Lines = append(result.Files[idx].Lines, fileResult.Lines...)
				result.Files[idx].TotalMatches += fileResult.TotalMatches
				result.Files[idx].Truncated = result.Files[idx].Truncated || fileResult.Truncated
				if result.Files[idx].Reason == "" {
					result.Files[idx].Reason = fileResult.Reason
				}
			} else {
				filesByPath[fileResult.Path] = len(result.Files)
				result.Files = append(result.Files, fileResult)
			}
		}
		if stop {
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		result.Reason = ProjectFileReasonReadError
		return result, walkErr
	}

	result.OK = true
	return result, nil
}

func (s *ProjectSearcher) limits() ProjectSearchLimits {
	if s == nil {
		return ProjectSearchLimits{}
	}
	return ProjectSearchLimits{
		MaxScanFiles:      s.maxScanFiles,
		MaxFileBytes:      s.maxFileBytes,
		MaxMatchesPerFile: s.maxMatchesPerFile,
		MaxTotalMatches:   s.maxTotalMatches,
		MaxSnippetRunes:   s.maxSnippetRunes,
	}
}

func (s *ProjectSearcher) searchFile(absPath, relPath string, matcher projectSearchMatcher, result *ProjectSearchResult) (ProjectSearchFileResult, bool, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return ProjectSearchFileResult{}, false, err
	}
	defer f.Close()

	renderType, language := DetectProjectFileRender(relPath, false)
	fileResult := ProjectSearchFileResult{
		Path:       relPath,
		Language:   language,
		RenderType: renderType,
		Lines:      make([]ProjectSearchLineMatch, 0),
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), scannerMaxBufferSize(s.maxFileBytes))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		column, ok := matcher.match(line)
		if !ok {
			continue
		}

		result.TotalMatches++
		fileResult.TotalMatches++
		if len(fileResult.Lines) < s.maxMatchesPerFile {
			fileResult.Lines = append(fileResult.Lines, ProjectSearchLineMatch{
				Line:    lineNo,
				Column:  column,
				Summary: projectSearchSnippet(line, column, s.maxSnippetRunes),
			})
		} else {
			fileResult.Truncated = true
			fileResult.Reason = ProjectSearchReasonFileMatchLimit
		}
		if result.TotalMatches >= s.maxTotalMatches {
			result.Truncated = true
			result.TruncatedBy = ProjectSearchReasonTotalMatchLimit
			return fileResult, true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fileResult, false, err
	}
	return fileResult, false, nil
}

type projectSearchMatcher interface {
	match(line string) (int, bool)
}

type projectSearchLiteralMatcher struct {
	query string
}

func (m projectSearchLiteralMatcher) match(line string) (int, bool) {
	idx := strings.Index(line, m.query)
	if idx < 0 {
		return 0, false
	}
	return utf8.RuneCountInString(line[:idx]) + 1, true
}

type projectSearchRegexMatcher struct {
	re *regexp.Regexp
}

func (m projectSearchRegexMatcher) match(line string) (int, bool) {
	loc := m.re.FindStringIndex(line)
	if loc == nil {
		return 0, false
	}
	return utf8.RuneCountInString(line[:loc[0]]) + 1, true
}

func newProjectSearchMatcher(query string, regex bool) (projectSearchMatcher, error) {
	if !regex {
		return projectSearchLiteralMatcher{query: query}, nil
	}
	re, err := regexp.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("compile project search regex: %w", err)
	}
	return projectSearchRegexMatcher{re: re}, nil
}

func scannerMaxBufferSize(maxFileBytes int64) int {
	if maxFileBytes <= 0 {
		return 64 * 1024
	}
	if maxFileBytes > int64(maxInt()-1024) {
		return maxInt()
	}
	return int(maxFileBytes) + 1024
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func projectSearchSnippet(line string, column int, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(line)
	if len(runes) <= maxRunes {
		return line
	}
	matchIdx := column - 1
	if matchIdx < 0 {
		matchIdx = 0
	}
	if matchIdx >= len(runes) {
		matchIdx = len(runes) - 1
	}
	start := matchIdx - maxRunes/2
	if start < 0 {
		start = 0
	}
	if start+maxRunes > len(runes) {
		start = len(runes) - maxRunes
	}
	snippet := string(runes[start : start+maxRunes])
	if start > 0 {
		snippet = "..." + snippet
	}
	if start+maxRunes < len(runes) {
		snippet += "..."
	}
	return snippet
}

func isDefaultProjectSearchSkipDir(relPath string) bool {
	name := path.Base(relPath)
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch strings.ToLower(name) {
	case "node_modules", "vendor", "dist", "build", "out", "target", "bin", "obj", "coverage", "tmp", "temp":
		return true
	default:
		return false
	}
}

func normalizeProjectSearchExcludes(patterns []string) []string {
	normalized := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		pattern = filepath.ToSlash(filepath.Clean(filepath.FromSlash(pattern)))
		if pattern == "." {
			continue
		}
		normalized = append(normalized, pattern)
	}
	sort.Strings(normalized)
	return normalized
}

func matchesProjectSearchExclude(relPath string, isDir bool, patterns []string) bool {
	relPath = filepath.ToSlash(relPath)
	base := path.Base(relPath)
	for _, pattern := range patterns {
		if pattern == relPath || pattern == base {
			return true
		}
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if relPath == prefix || strings.HasPrefix(relPath, prefix+"/") {
				return true
			}
		}
		if strings.HasSuffix(pattern, "/") {
			prefix := strings.TrimSuffix(pattern, "/")
			if relPath == prefix || strings.HasPrefix(relPath, prefix+"/") {
				return true
			}
		}
		if matched, _ := path.Match(pattern, relPath); matched {
			return true
		}
		if matched, _ := path.Match(pattern, base); matched {
			return true
		}
		if isDir && strings.HasPrefix(pattern, relPath+"/") {
			return true
		}
	}
	return false
}
