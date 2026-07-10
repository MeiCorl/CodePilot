package web

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	ProjectGitStatusModified  = "modified"
	ProjectGitStatusAdded     = "added"
	ProjectGitStatusDeleted   = "deleted"
	ProjectGitStatusRenamed   = "renamed"
	ProjectGitStatusUntracked = "untracked"

	ProjectGitReasonGitUnavailable = "git_unavailable"
	ProjectGitReasonNotRepository  = "not_repository"
	ProjectGitReasonNotChanged     = "not_changed"
)

// ProjectGitChange describes one changed file in the project Git view.
type ProjectGitChange struct {
	Path         string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
	Status       string `json:"status"`
	XY           string `json:"xy,omitempty"`
	Size         int64  `json:"size"`
	Previewable  bool   `json:"previewable"`
	Language     string `json:"language,omitempty"`
	RenderType   string `json:"render_type,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// ProjectGitStatusResult is the changed-file list returned to WebUI.
type ProjectGitStatusResult struct {
	OK        bool               `json:"ok"`
	Reason    string             `json:"reason,omitempty"`
	Entries   []ProjectGitChange `json:"entries"`
	Truncated bool               `json:"truncated"`
}

// ProjectGitDiffResult contains the before/after text for one changed file.
type ProjectGitDiffResult struct {
	Found        bool             `json:"found"`
	OK           bool             `json:"ok"`
	Reason       string           `json:"reason,omitempty"`
	Change       ProjectGitChange `json:"change"`
	Path         string           `json:"path"`
	OriginalPath string           `json:"original_path,omitempty"`
	Status       string           `json:"status,omitempty"`
	Before       string           `json:"before,omitempty"`
	After        string           `json:"after,omitempty"`
	Language     string           `json:"language,omitempty"`
	RenderType   string           `json:"render_type,omitempty"`
}

// ProjectGitBrowser provides read-only Git status and diff content for WebUI.
type ProjectGitBrowser struct {
	workdir      string
	maxEntries   int
	maxFileBytes int64
	files        *ProjectFileBrowser
}

// NewProjectGitBrowser creates a Git browser with the project file defaults.
func NewProjectGitBrowser(workdir string) *ProjectGitBrowser {
	return NewProjectGitBrowserWithLimits(workdir, ProjectFileDefaultMaxEntries, ProjectFileDefaultMaxBytes)
}

// NewProjectGitBrowserWithLimits creates a Git browser with explicit limits.
func NewProjectGitBrowserWithLimits(workdir string, maxEntries int, maxFileBytes int64) *ProjectGitBrowser {
	if maxEntries <= 0 {
		maxEntries = ProjectFileDefaultMaxEntries
	}
	if maxFileBytes <= 0 {
		maxFileBytes = ProjectFileDefaultMaxBytes
	}
	return &ProjectGitBrowser{
		workdir:      workdir,
		maxEntries:   maxEntries,
		maxFileBytes: maxFileBytes,
		files:        NewProjectFileBrowserWithLimits(workdir, maxEntries, maxFileBytes),
	}
}

// Status returns changed files under the current project workdir.
func (b *ProjectGitBrowser) Status() (ProjectGitStatusResult, error) {
	ctx, err := b.gitContext()
	if err != nil {
		return ProjectGitStatusResult{OK: false, Reason: projectGitErrorReason(err)}, err
	}

	args := []string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--"}
	args = append(args, ctx.pathspec())
	out, err := b.runGit(ctx.repoRoot, args...)
	if err != nil {
		return ProjectGitStatusResult{OK: false, Reason: projectGitStatusCommandReason(err)}, err
	}

	records := parseGitStatusPorcelainZ(out)
	result := ProjectGitStatusResult{OK: true, Entries: make([]ProjectGitChange, 0, len(records))}
	for _, record := range records {
		change, err := b.changeFromStatusRecord(ctx, record)
		if err != nil {
			continue
		}
		result.Entries = append(result.Entries, change)
	}
	sort.SliceStable(result.Entries, func(i, j int) bool {
		return strings.ToLower(result.Entries[i].Path) < strings.ToLower(result.Entries[j].Path)
	})
	if len(result.Entries) > b.maxEntries {
		result.Entries = result.Entries[:b.maxEntries]
		result.Truncated = true
		result.Reason = ProjectFileReasonEntryLimit
	}
	return result, nil
}

// ReadDiff returns before/after text for one changed project file.
func (b *ProjectGitBrowser) ReadDiff(relPath string) (ProjectGitDiffResult, error) {
	_, cleanRel, err := b.files.resolveProjectPath(relPath)
	if err != nil {
		return ProjectGitDiffResult{Found: false, OK: false, Reason: projectPathErrorReason(err)}, err
	}

	status, err := b.Status()
	if err != nil {
		return ProjectGitDiffResult{Found: false, OK: false, Reason: status.Reason}, err
	}
	var change ProjectGitChange
	found := false
	for _, entry := range status.Entries {
		if entry.Path == cleanRel {
			change = entry
			found = true
			break
		}
	}
	if !found {
		return ProjectGitDiffResult{
			Found:  false,
			OK:     false,
			Reason: ProjectGitReasonNotChanged,
			Path:   cleanRel,
		}, nil
	}
	result := ProjectGitDiffResult{
		Found:        true,
		Change:       change,
		Path:         change.Path,
		OriginalPath: change.OriginalPath,
		Status:       change.Status,
		Language:     change.Language,
		RenderType:   change.RenderType,
	}
	if !change.Previewable {
		result.Reason = change.Reason
		return result, nil
	}

	ctx, err := b.gitContext()
	if err != nil {
		result.Reason = projectGitErrorReason(err)
		return result, err
	}
	beforePath := change.Path
	if change.OriginalPath != "" {
		beforePath = change.OriginalPath
	}
	if change.Status != ProjectGitStatusAdded && change.Status != ProjectGitStatusUntracked {
		before, reason, err := b.readGitBlob(ctx.repoRoot, ctx.toRepoPath(beforePath))
		if reason != "" {
			result.Reason = reason
			return result, nil
		}
		if err != nil {
			result.Reason = projectGitStatusCommandReason(err)
			return result, err
		}
		result.Before = before
	}
	if change.Status != ProjectGitStatusDeleted {
		after, reason, err := b.readWorktreeFile(change.Path)
		if reason != "" {
			result.Reason = reason
			return result, nil
		}
		if err != nil {
			result.Reason = ProjectFileReasonReadError
			return result, err
		}
		result.After = after
	}
	result.OK = true
	return result, nil
}

func (b *ProjectGitBrowser) changeFromStatusRecord(ctx projectGitContext, record gitStatusRecord) (ProjectGitChange, error) {
	projectPath, ok := ctx.toProjectPath(record.path)
	if !ok {
		return ProjectGitChange{}, fmt.Errorf("git path %q is outside project", record.path)
	}
	originalPath := ""
	if record.originalPath != "" {
		var originalOK bool
		originalPath, originalOK = ctx.toProjectPath(record.originalPath)
		if !originalOK {
			originalPath = ""
		}
	}
	status := gitStatusKind(record.xy)
	change := ProjectGitChange{
		Path:         projectPath,
		OriginalPath: originalPath,
		Status:       status,
		XY:           record.xy,
	}
	change.RenderType, change.Language = DetectProjectFileRender(projectPath, false)
	change.Previewable = true
	if status == ProjectGitStatusDeleted {
		return change, nil
	}

	absPath, _, err := b.files.resolveProjectPath(projectPath)
	if err != nil {
		change.Previewable = false
		change.Reason = projectPathErrorReason(err)
		return change, nil
	}
	info, err := os.Stat(absPath)
	if err != nil {
		change.Previewable = false
		change.Reason = ProjectFileReasonReadError
		return change, nil
	}
	change.Size = info.Size()
	if info.IsDir() {
		change.Previewable = false
		change.Reason = ProjectFileReasonIsDirectory
		return change, nil
	}
	if info.Size() > b.maxFileBytes {
		change.Previewable = false
		change.Reason = ProjectFileReasonTooLarge
		return change, nil
	}
	binary, err := isProjectBinaryFile(absPath)
	if err != nil {
		change.Previewable = false
		change.Reason = ProjectFileReasonReadError
		return change, nil
	}
	if binary {
		change.Previewable = false
		change.RenderType = ProjectRenderTypeBinary
		change.Language = ""
		change.Reason = ProjectFileReasonBinary
	}
	return change, nil
}

func (b *ProjectGitBrowser) readWorktreeFile(projectPath string) (string, string, error) {
	absPath, _, err := b.files.resolveProjectPath(projectPath)
	if err != nil {
		return "", projectPathErrorReason(err), err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", ProjectFileReasonReadError, err
	}
	if info.Size() > b.maxFileBytes {
		return "", ProjectFileReasonTooLarge, nil
	}
	binary, err := isProjectBinaryFile(absPath)
	if err != nil {
		return "", ProjectFileReasonReadError, err
	}
	if binary {
		return "", ProjectFileReasonBinary, nil
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", ProjectFileReasonReadError, err
	}
	return string(data), "", nil
}

func (b *ProjectGitBrowser) readGitBlob(repoRoot, repoPath string) (string, string, error) {
	sizeOut, err := b.runGit(repoRoot, "cat-file", "-s", "HEAD:"+repoPath)
	if err != nil {
		return "", "", err
	}
	sizeText := strings.TrimSpace(string(sizeOut))
	var size int64
	if _, err := fmt.Sscan(sizeText, &size); err != nil {
		return "", ProjectFileReasonReadError, err
	}
	if size > b.maxFileBytes {
		return "", ProjectFileReasonTooLarge, nil
	}

	data, err := b.runGit(repoRoot, "show", "HEAD:"+repoPath)
	if err != nil {
		return "", "", err
	}
	if !isProjectTextContent(repoPath, data[:minInt(len(data), projectFileSniffBytes)]) {
		return "", ProjectFileReasonBinary, nil
	}
	return string(data), "", nil
}

func (b *ProjectGitBrowser) gitContext() (projectGitContext, error) {
	root, err := b.files.projectRoot()
	if err != nil {
		return projectGitContext{}, err
	}
	repoOut, err := b.runGit(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return projectGitContext{}, projectGitError{reason: projectGitStatusCommandReason(err), err: err}
	}
	repoRoot := filepath.Clean(strings.TrimSpace(string(repoOut)))
	if repoRoot == "" {
		return projectGitContext{}, projectGitError{reason: ProjectGitReasonNotRepository, err: errors.New("empty git toplevel")}
	}
	if realRepo, err := filepath.EvalSymlinks(repoRoot); err == nil {
		repoRoot = filepath.Clean(realRepo)
	}
	rel, err := filepath.Rel(repoRoot, root)
	if err != nil {
		return projectGitContext{}, projectGitError{reason: ProjectFileReasonReadError, err: err}
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		rel = ""
	}
	if isOutsideRelativePath(rel) {
		return projectGitContext{}, projectGitError{reason: ProjectGitReasonNotRepository, err: fmt.Errorf("workdir %q is outside repo %q", root, repoRoot)}
	}
	return projectGitContext{
		projectRoot: root,
		repoRoot:    repoRoot,
		projectRel:  filepath.ToSlash(rel),
	}, nil
}

func (b *ProjectGitBrowser) runGit(dir string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, projectGitError{reason: ProjectGitReasonGitUnavailable, err: err}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, projectGitError{reason: projectGitCommandReason(out, err), err: fmt.Errorf("git %v: %w: %s", args, err, bytes.TrimSpace(out))}
	}
	return out, nil
}

type projectGitContext struct {
	projectRoot string
	repoRoot    string
	projectRel  string
}

func (c projectGitContext) pathspec() string {
	if c.projectRel == "" {
		return "."
	}
	return c.projectRel
}

func (c projectGitContext) toProjectPath(repoPath string) (string, bool) {
	repoPath = path.Clean(filepath.ToSlash(repoPath))
	if repoPath == "." {
		repoPath = ""
	}
	if c.projectRel == "" {
		return repoPath, true
	}
	prefix := c.projectRel + "/"
	if repoPath == c.projectRel {
		return "", true
	}
	if !strings.HasPrefix(repoPath, prefix) {
		return "", false
	}
	return strings.TrimPrefix(repoPath, prefix), true
}

func (c projectGitContext) toRepoPath(projectPath string) string {
	projectPath = path.Clean(filepath.ToSlash(projectPath))
	if projectPath == "." {
		projectPath = ""
	}
	if c.projectRel == "" {
		return projectPath
	}
	if projectPath == "" {
		return c.projectRel
	}
	return path.Join(c.projectRel, projectPath)
}

type gitStatusRecord struct {
	xy           string
	path         string
	originalPath string
}

func parseGitStatusPorcelainZ(raw []byte) []gitStatusRecord {
	parts := bytes.Split(raw, []byte{0})
	records := make([]gitStatusRecord, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		part := string(parts[i])
		if part == "" {
			continue
		}
		if len(part) < 4 {
			continue
		}
		record := gitStatusRecord{
			xy:   part[:2],
			path: part[3:],
		}
		if strings.Contains(record.xy, "R") || strings.Contains(record.xy, "C") {
			if i+1 < len(parts) {
				record.originalPath = string(parts[i+1])
				i++
			}
		}
		records = append(records, record)
	}
	return records
}

func gitStatusKind(xy string) string {
	if xy == "??" {
		return ProjectGitStatusUntracked
	}
	if strings.Contains(xy, "R") {
		return ProjectGitStatusRenamed
	}
	if strings.Contains(xy, "A") {
		return ProjectGitStatusAdded
	}
	if strings.Contains(xy, "D") {
		return ProjectGitStatusDeleted
	}
	return ProjectGitStatusModified
}

type projectGitError struct {
	reason string
	err    error
}

func (e projectGitError) Error() string {
	return e.err.Error()
}

func (e projectGitError) Unwrap() error {
	return e.err
}

func projectGitErrorReason(err error) string {
	var gitErr projectGitError
	if errors.As(err, &gitErr) {
		return gitErr.reason
	}
	return projectPathErrorReason(err)
}

func projectGitStatusCommandReason(err error) string {
	var gitErr projectGitError
	if errors.As(err, &gitErr) {
		return gitErr.reason
	}
	return ProjectFileReasonReadError
}

func projectGitCommandReason(out []byte, err error) string {
	text := strings.ToLower(string(out))
	if strings.Contains(text, "not a git repository") || strings.Contains(text, "not a git repo") {
		return ProjectGitReasonNotRepository
	}
	if errors.Is(err, exec.ErrNotFound) {
		return ProjectGitReasonGitUnavailable
	}
	return ProjectFileReasonReadError
}
