package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MeiCorl/CodePilot/src/internal/security"
)

const (
	ProjectFileTypeDirectory = "directory"
	ProjectFileTypeFile      = "file"

	ProjectRenderTypeMarkdown = "markdown"
	ProjectRenderTypeJSON     = "json"
	ProjectRenderTypeXML      = "xml"
	ProjectRenderTypeCode     = "code"
	ProjectRenderTypePlain    = "plain"
	ProjectRenderTypeBinary   = "binary"

	ProjectFileReasonEntryLimit     = "entry_limit"
	ProjectFileReasonEmptyWorkdir   = "empty_workdir"
	ProjectFileReasonInvalidPath    = "invalid_path"
	ProjectFileReasonOutsideWorkdir = "outside_workdir"
	ProjectFileReasonNotFound       = "not_found"
	ProjectFileReasonNotDirectory   = "not_directory"
	ProjectFileReasonIsDirectory    = "is_directory"
	ProjectFileReasonBinary         = "binary"
	ProjectFileReasonTooLarge       = "too_large"
	ProjectFileReasonReadError      = "read_error"
)

const (
	ProjectFileDefaultMaxEntries = 500
	ProjectFileDefaultMaxBytes   = 512 * 1024
	projectFileSniffBytes        = 512
)

// ProjectFileEntry describes one item in the current project directory level.
type ProjectFileEntry struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Type        string    `json:"type"`
	Size        int64     `json:"size"`
	ModTime     time.Time `json:"mod_time"`
	Previewable bool      `json:"previewable"`
	Language    string    `json:"language,omitempty"`
	RenderType  string    `json:"render_type"`
}

// ProjectBreadcrumb is one clickable segment in the project file panel path.
type ProjectBreadcrumb struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// ProjectDirResult is the response model for a single-level directory listing.
type ProjectDirResult struct {
	Path        string              `json:"path"`
	ParentPath  string              `json:"parent_path"`
	Breadcrumbs []ProjectBreadcrumb `json:"breadcrumbs"`
	Entries     []ProjectFileEntry  `json:"entries"`
	Truncated   bool                `json:"truncated"`
	Reason      string              `json:"reason,omitempty"`
}

// ProjectFileResult is the response model for reading a previewable project file.
type ProjectFileResult struct {
	Found   bool             `json:"found"`
	OK      bool             `json:"ok"`
	Reason  string           `json:"reason,omitempty"`
	File    ProjectFileEntry `json:"file"`
	Content string           `json:"content,omitempty"`
}

// ProjectFileBrowser contains read-only project file browsing rules for WebUI.
type ProjectFileBrowser struct {
	workdir      string
	maxEntries   int
	maxFileBytes int64
}

// NewProjectFileBrowser creates a browser with conservative default limits.
func NewProjectFileBrowser(workdir string) *ProjectFileBrowser {
	return NewProjectFileBrowserWithLimits(workdir, ProjectFileDefaultMaxEntries, ProjectFileDefaultMaxBytes)
}

// NewProjectFileBrowserWithLimits creates a browser with explicit limits.
func NewProjectFileBrowserWithLimits(workdir string, maxEntries int, maxFileBytes int64) *ProjectFileBrowser {
	if maxEntries <= 0 {
		maxEntries = ProjectFileDefaultMaxEntries
	}
	if maxFileBytes <= 0 {
		maxFileBytes = ProjectFileDefaultMaxBytes
	}
	return &ProjectFileBrowser{workdir: workdir, maxEntries: maxEntries, maxFileBytes: maxFileBytes}
}

// ListDir returns the direct children of relPath. It never scans recursively.
func (b *ProjectFileBrowser) ListDir(relPath string) (ProjectDirResult, error) {
	absPath, cleanRel, err := b.resolveProjectPath(relPath)
	result := ProjectDirResult{
		Path:        cleanRel,
		ParentPath:  parentProjectPath(cleanRel),
		Breadcrumbs: projectBreadcrumbs(cleanRel),
	}
	if err != nil {
		result.Reason = projectPathErrorReason(err)
		return result, err
	}

	info, err := os.Stat(absPath)
	if err != nil {
		result.Reason = ProjectFileReasonNotFound
		if !os.IsNotExist(err) {
			result.Reason = ProjectFileReasonReadError
		}
		return result, err
	}
	if !info.IsDir() {
		result.Reason = ProjectFileReasonNotDirectory
		return result, fmt.Errorf("project path %q is not a directory", cleanRel)
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		result.Reason = ProjectFileReasonReadError
		return result, err
	}
	root, err := b.projectRoot()
	if err != nil {
		result.Reason = ProjectFileReasonReadError
		return result, err
	}

	result.Entries = make([]ProjectFileEntry, 0, len(entries))
	for _, entry := range entries {
		item, err := b.entryFromDirEntry(absPath, cleanRel, root, entry)
		if err != nil {
			continue
		}
		result.Entries = append(result.Entries, item)
	}
	sortProjectFileEntries(result.Entries)
	if len(result.Entries) > b.maxEntries {
		result.Entries = result.Entries[:b.maxEntries]
		result.Truncated = true
		result.Reason = ProjectFileReasonEntryLimit
	}
	return result, nil
}

// ReadFile reads a text project file when it is safe, small, and previewable.
func (b *ProjectFileBrowser) ReadFile(relPath string) (ProjectFileResult, error) {
	absPath, cleanRel, err := b.resolveProjectPath(relPath)
	if err != nil {
		return ProjectFileResult{Found: false, OK: false, Reason: projectPathErrorReason(err)}, err
	}

	info, err := os.Stat(absPath)
	if err != nil {
		result := ProjectFileResult{Found: false, OK: false, Reason: ProjectFileReasonNotFound}
		if !os.IsNotExist(err) {
			result.Reason = ProjectFileReasonReadError
		}
		return result, nil
	}
	if info.IsDir() {
		return ProjectFileResult{
			Found:  true,
			OK:     false,
			Reason: ProjectFileReasonIsDirectory,
			File:   b.entryFromFileInfo(filepath.Base(absPath), cleanRel, info, false),
		}, nil
	}

	fileEntry := b.entryFromFileInfo(filepath.Base(absPath), cleanRel, info, false)
	if info.Size() > b.maxFileBytes {
		fileEntry.Previewable = false
		return ProjectFileResult{Found: true, OK: false, Reason: ProjectFileReasonTooLarge, File: fileEntry}, nil
	}

	binary, err := isProjectBinaryFile(absPath)
	if err != nil {
		return ProjectFileResult{Found: true, OK: false, Reason: ProjectFileReasonReadError, File: fileEntry}, err
	}
	if binary {
		fileEntry.RenderType = ProjectRenderTypeBinary
		fileEntry.Language = ""
		fileEntry.Previewable = false
		return ProjectFileResult{Found: true, OK: false, Reason: ProjectFileReasonBinary, File: fileEntry}, nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return ProjectFileResult{Found: true, OK: false, Reason: ProjectFileReasonReadError, File: fileEntry}, err
	}
	renderType, language := DetectProjectFileRender(cleanRel, false)
	fileEntry.RenderType = renderType
	fileEntry.Language = language
	fileEntry.Previewable = true
	return ProjectFileResult{Found: true, OK: true, File: fileEntry, Content: string(data)}, nil
}

func (b *ProjectFileBrowser) projectRoot() (string, error) {
	if b == nil || b.workdir == "" {
		return "", projectPathError{reason: ProjectFileReasonEmptyWorkdir, err: errors.New("workdir is empty")}
	}
	root, err := filepath.Abs(b.workdir)
	if err != nil {
		return "", projectPathError{reason: ProjectFileReasonEmptyWorkdir, err: fmt.Errorf("resolve workdir: %w", err)}
	}
	root = filepath.Clean(root)
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = filepath.Clean(realRoot)
	}
	return root, nil
}
func (b *ProjectFileBrowser) resolveProjectPath(relPath string) (string, string, error) {
	if strings.ContainsRune(relPath, '\x00') {
		return "", "", projectPathError{reason: ProjectFileReasonInvalidPath, err: errors.New("path contains NUL byte")}
	}

	root, err := b.projectRoot()
	if err != nil {
		return "", "", err
	}

	nativeRel := filepath.FromSlash(relPath)
	if filepath.IsAbs(nativeRel) || filepath.VolumeName(nativeRel) != "" {
		return "", "", projectPathError{reason: ProjectFileReasonInvalidPath, err: fmt.Errorf("absolute path is not allowed: %q", relPath)}
	}
	cleanNative := filepath.Clean(nativeRel)
	if cleanNative == "." {
		cleanNative = ""
	}
	if isOutsideRelativePath(cleanNative) {
		return "", filepath.ToSlash(cleanNative), projectPathError{reason: ProjectFileReasonOutsideWorkdir, err: fmt.Errorf("path escapes workdir: %q", relPath)}
	}

	absTarget := root
	if cleanNative != "" {
		absTarget = filepath.Join(root, cleanNative)
	}
	absTarget = filepath.Clean(absTarget)
	if !security.IsPathInside(absTarget, root) {
		return "", filepath.ToSlash(cleanNative), projectPathError{reason: ProjectFileReasonOutsideWorkdir, err: fmt.Errorf("path escapes workdir: %q", relPath)}
	}

	if _, err := os.Lstat(absTarget); err == nil {
		realTarget, err := filepath.EvalSymlinks(absTarget)
		if err != nil {
			return "", filepath.ToSlash(cleanNative), projectPathError{reason: ProjectFileReasonReadError, err: fmt.Errorf("eval symlink: %w", err)}
		}
		realTarget = filepath.Clean(realTarget)
		if !security.IsPathInside(realTarget, root) {
			return "", filepath.ToSlash(cleanNative), projectPathError{reason: ProjectFileReasonOutsideWorkdir, err: fmt.Errorf("symlink escapes workdir: %q", relPath)}
		}
		absTarget = realTarget
	}

	return absTarget, filepath.ToSlash(cleanNative), nil
}

func (b *ProjectFileBrowser) entryFromDirEntry(dirAbs, parentRel, rootAbs string, entry os.DirEntry) (ProjectFileEntry, error) {
	entryAbs := filepath.Join(dirAbs, entry.Name())
	info, isDir, safe := statProjectDirEntry(entryAbs, entry, rootAbs)
	if info == nil {
		return ProjectFileEntry{}, fmt.Errorf("stat entry %q failed", entry.Name())
	}
	entryRel := entry.Name()
	if parentRel != "" {
		entryRel = path.Join(parentRel, entry.Name())
	}
	item := b.entryFromFileInfo(entry.Name(), entryRel, info, isDir)
	if !safe {
		item.Previewable = false
	}
	return item, nil
}

func (b *ProjectFileBrowser) entryFromFileInfo(name, relPath string, info os.FileInfo, forceDir bool) ProjectFileEntry {
	isDir := forceDir || info.IsDir()
	item := ProjectFileEntry{
		Name:    name,
		Path:    filepath.ToSlash(relPath),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if isDir {
		item.Type = ProjectFileTypeDirectory
		item.RenderType = ""
		item.Previewable = false
		return item
	}
	item.Type = ProjectFileTypeFile
	item.RenderType, item.Language = DetectProjectFileRender(relPath, false)
	item.Previewable = info.Size() <= b.maxFileBytes && item.RenderType != ProjectRenderTypeBinary
	return item
}

func statProjectDirEntry(entryAbs string, entry os.DirEntry, rootAbs string) (os.FileInfo, bool, bool) {
	info, err := entry.Info()
	if err != nil {
		return nil, false, false
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return info, info.IsDir(), true
	}
	real, err := filepath.EvalSymlinks(entryAbs)
	if err != nil {
		return info, false, false
	}
	if !security.IsPathInside(real, rootAbs) {
		return info, false, false
	}
	realInfo, err := os.Stat(real)
	if err != nil {
		return info, false, false
	}
	return realInfo, realInfo.IsDir(), true
}

// DetectProjectFileRender maps a file path and binary flag to UI render metadata.
func DetectProjectFileRender(filePath string, binary bool) (string, string) {
	if binary {
		return ProjectRenderTypeBinary, ""
	}
	language := DetectLanguage(filePath)
	switch language {
	case "markdown":
		return ProjectRenderTypeMarkdown, language
	case "json":
		return ProjectRenderTypeJSON, language
	case "xml":
		return ProjectRenderTypeXML, language
	case "":
		return ProjectRenderTypePlain, ""
	default:
		return ProjectRenderTypeCode, language
	}
}

func isProjectBinaryFile(filePath string) (bool, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, projectFileSniffBytes)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	return !isProjectTextContent(filePath, buf[:n]), nil
}

func isProjectTextContent(filePath string, sample []byte) bool {
	if bytes.IndexByte(sample, 0) >= 0 {
		return false
	}
	if !utf8.Valid(sample) {
		return false
	}
	contentType := http.DetectContentType(sample)
	if strings.HasPrefix(contentType, "text/") || contentType == "application/json" || contentType == "application/xml" {
		return true
	}
	return isKnownTextExtension(filePath)
}

func isKnownTextExtension(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go", ".md", ".markdown", ".json", ".xml", ".py", ".ts", ".js", ".css", ".yaml", ".yml", ".html", ".htm", ".sql", ".sh", ".bash", ".txt", ".log", ".toml", ".ini", ".env", ".mod", ".sum":
		return true
	default:
		return false
	}
}

func sortProjectFileEntries(entries []ProjectFileEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		leftDir := entries[i].Type == ProjectFileTypeDirectory
		rightDir := entries[j].Type == ProjectFileTypeDirectory
		if leftDir != rightDir {
			return leftDir
		}
		left := strings.ToLower(entries[i].Name)
		right := strings.ToLower(entries[j].Name)
		if left == right {
			return entries[i].Name < entries[j].Name
		}
		return left < right
	})
}

func isOutsideRelativePath(cleanNative string) bool {
	if cleanNative == "" {
		return false
	}
	if cleanNative == ".." {
		return true
	}
	return strings.HasPrefix(cleanNative, ".."+string(filepath.Separator))
}

func parentProjectPath(relPath string) string {
	if relPath == "" {
		return ""
	}
	parent := path.Dir(filepath.ToSlash(relPath))
	if parent == "." {
		return ""
	}
	return parent
}

func projectBreadcrumbs(relPath string) []ProjectBreadcrumb {
	crumbs := []ProjectBreadcrumb{{Name: ".", Path: ""}}
	if relPath == "" {
		return crumbs
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	current := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		crumbs = append(crumbs, ProjectBreadcrumb{Name: part, Path: current})
	}
	return crumbs
}

type projectPathError struct {
	reason string
	err    error
}

func (e projectPathError) Error() string {
	return e.err.Error()
}

func (e projectPathError) Unwrap() error {
	return e.err
}

func projectPathErrorReason(err error) string {
	var pathErr projectPathError
	if errors.As(err, &pathErr) {
		return pathErr.reason
	}
	return ProjectFileReasonReadError
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
