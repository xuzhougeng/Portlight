package remoteviewer

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type AuthMethod string

const (
	AuthMethodKey      AuthMethod = "key"
	AuthMethodPassword AuthMethod = "password"

	sessionTTL                     = 30 * time.Minute
	sessionCleanupInterval         = 5 * time.Minute
	largeTextPreviewThresholdBytes = 10 * 1024 * 1024
	largeTextPreviewMaxBytes       = 256 * 1024
	largeTextPreviewMaxLines       = 120
	tablePreviewMaxRows            = 10
	terminalExecTimeout            = 20 * time.Second
	terminalOutputLimitBytes       = 128 * 1024
)

var (
	htmlBaseHrefRe = regexp.MustCompile(`(?i)<base\b([^>]*?)href=(["'])(.*?)\2([^>]*)>`)
	htmlSrcsetRe   = regexp.MustCompile(`(?i)\bsrcset=(["'])(.*?)\1`)
	htmlAttrRe     = regexp.MustCompile(`(?i)\b(src|href|poster|action)=(["'])(.*?)\2`)
	htmlCSSURLRe   = regexp.MustCompile(`(?i)url\((["']?)(/[^)"']*)\1\)`)
)

type ServerOptions struct {
	FrontendDistDir string
}

type Server struct {
	apiHandler              http.Handler
	apiOnce                 sync.Once
	cleanupDone             chan struct{}
	cleanupStop             chan struct{}
	frontendDistDir         string
	mu                      sync.RWMutex
	sessions                map[string]*sessionRecord
	storageDir              string
	savedProfilesPath       string
	legacySavedProfilesPath string
}

type appError struct {
	Message    string `json:"error"`
	StatusCode int    `json:"-"`
}

func (e *appError) Error() string {
	return e.Message
}

type connectionConfig struct {
	AuthMethod       AuthMethod `json:"authMethod"`
	Host             string     `json:"host"`
	Password         string     `json:"password"`
	PrivateKey       string     `json:"privateKey"`
	Port             string     `json:"port"`
	RememberPassword bool       `json:"rememberPassword"`
	RootPath         string     `json:"rootPath"`
	Username         string     `json:"username"`
}

type connectionSessionResponse struct {
	Alias      string     `json:"alias,omitempty"`
	AuthMethod AuthMethod `json:"authMethod"`
	Hostname   string     `json:"hostname"`
	Port       int        `json:"port"`
	SessionID  string     `json:"sessionId"`
	Username   string     `json:"username"`
}

type remoteEntry struct {
	Extension string `json:"extension,omitempty"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
}

type savedConnectionProfile struct {
	AuthMethod       AuthMethod `json:"authMethod"`
	CreatedAt        int64      `json:"createdAt"`
	Host             string     `json:"host"`
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Port             string     `json:"port"`
	PrivateKey       string     `json:"privateKey"`
	RememberPassword bool       `json:"rememberPassword"`
	RootPath         string     `json:"rootPath"`
	UpdatedAt        int64      `json:"updatedAt"`
	Username         string     `json:"username"`
}

type sessionRecord struct {
	authMethod AuthMethod
	client     *ssh.Client
	createdAt  time.Time
	hostname   string
	id         string
	lastUsedAt time.Time
	port       int
	username   string
}

type terminalExecRequest struct {
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
}

type terminalExecResult struct {
	Command         string  `json:"command"`
	Cwd             string  `json:"cwd"`
	ExitCode        *int    `json:"exitCode"`
	Signal          *string `json:"signal"`
	Stderr          string  `json:"stderr"`
	StderrTruncated bool    `json:"stderrTruncated"`
	Stdout          string  `json:"stdout"`
	StdoutTruncated bool    `json:"stdoutTruncated"`
	TimedOut        bool    `json:"timedOut"`
}

type textPreviewPayload struct {
	Content        string     `json:"content,omitempty"`
	Kind           string     `json:"kind"`
	Message        string     `json:"message,omitempty"`
	Notice         string     `json:"notice,omitempty"`
	PreviewedBytes int        `json:"previewedBytes,omitempty"`
	Rows           [][]string `json:"rows,omitempty"`
	TotalSize      int64      `json:"totalSize"`
	Truncated      bool       `json:"truncated,omitempty"`
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if b.limit <= 0 {
		b.truncated = true
		return written, nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return written, nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return written, nil
	}
	_, _ = b.buf.Write(p)
	return written, nil
}

func NewServer(options ServerOptions) *Server {
	storageDir := filepath.Join(userHomeDir(), ".portlight")
	legacyStorageDir := filepath.Join(userHomeDir(), ".remote-viewer")
	server := &Server{
		cleanupDone:             make(chan struct{}),
		cleanupStop:             make(chan struct{}),
		frontendDistDir:         options.FrontendDistDir,
		savedProfilesPath:       filepath.Join(storageDir, "profiles.json"),
		legacySavedProfilesPath: filepath.Join(legacyStorageDir, "profiles.json"),
		sessions:                make(map[string]*sessionRecord),
		storageDir:              storageDir,
	}
	go server.cleanupLoop()
	return server
}

func (s *Server) Close() error {
	select {
	case <-s.cleanupStop:
	default:
		close(s.cleanupStop)
	}
	<-s.cleanupDone

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, session := range s.sessions {
		delete(s.sessions, id)
		_ = session.client.Close()
	}
	return nil
}

func (s *Server) APIHandler() http.Handler {
	s.apiOnce.Do(func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/health", s.handleHealth)
		mux.HandleFunc("/api/ssh/hosts", s.handleDisabledSSHConfig)
		mux.HandleFunc("/api/ssh/resolve", s.handleDisabledSSHConfig)
		mux.HandleFunc("/api/profiles", s.handleProfiles)
		mux.HandleFunc("/api/profiles/", s.handleProfileDelete)
		mux.HandleFunc("/api/session", s.handleSession)
		mux.HandleFunc("/api/session/", s.handleSessionDelete)
		mux.HandleFunc("/api/terminal/exec", s.handleTerminalExec)
		mux.HandleFunc("/api/list", s.handleList)
		mux.HandleFunc("/api/file", s.handlePreviewFile)
		mux.HandleFunc("/api/download", s.handleDownload)
		mux.HandleFunc("/api/text", s.handleTextPreview)
		mux.HandleFunc("/api/upload", s.handleUpload)
		mux.HandleFunc("/api/html-preview/", s.handleHTMLPreview)
		s.apiHandler = mux
	})
	return s.apiHandler
}

func (s *Server) HTTPDevMux() http.Handler {
	api := s.APIHandler()
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		distDir := s.frontendDistDir
		indexPath := filepath.Join(distDir, "index.html")
		cleanPath := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if cleanPath == "." || cleanPath == "/" {
			http.ServeFile(w, r, indexPath)
			return
		}
		targetPath := filepath.Join(distDir, filepath.FromSlash(cleanPath))
		if info, err := os.Stat(targetPath); err == nil && !info.IsDir() {
			http.ServeFile(w, r, targetPath)
			return
		}
		http.ServeFile(w, r, indexPath)
	})
	return mux
}

func (s *Server) SaveRemoteFileToPath(sessionID string, remotePath string, localPath string) error {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}

	sftpClient, err := sftp.NewClient(session.client)
	if err != nil {
		return asAppError(err, "SFTP 初始化失败")
	}
	defer sftpClient.Close()

	resolvedPath, err := sftpClient.RealPath(remotePath)
	if err != nil {
		return asAppError(err, "远程文件读取失败")
	}
	info, err := sftpClient.Stat(resolvedPath)
	if err != nil {
		return asAppError(err, "远程文件读取失败")
	}
	if !info.Mode().IsRegular() {
		return &appError{Message: "目标不是可下载文件", StatusCode: http.StatusBadRequest}
	}

	remoteFile, err := sftpClient.Open(resolvedPath)
	if err != nil {
		return asAppError(err, "远程文件读取失败")
	}
	defer remoteFile.Close()

	localFile, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer localFile.Close()

	if _, err := io.Copy(localFile, remoteFile); err != nil {
		return asAppError(err, "远程文件下载失败")
	}
	return nil
}

func (s *Server) cleanupLoop() {
	ticker := time.NewTicker(sessionCleanupInterval)
	defer func() {
		ticker.Stop()
		close(s.cleanupDone)
	}()

	for {
		select {
		case <-ticker.C:
			s.expireSessions()
		case <-s.cleanupStop:
			return
		}
	}
}

func (s *Server) expireSessions() {
	now := time.Now()
	var expired []*sessionRecord

	s.mu.Lock()
	for id, session := range s.sessions {
		if now.Sub(session.lastUsedAt) <= sessionTTL {
			continue
		}
		delete(s.sessions, id)
		expired = append(expired, session)
	}
	s.mu.Unlock()

	for _, session := range expired {
		_ = session.client.Close()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDisabledSSHConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	writeAppError(w, &appError{
		Message:    "已停用本机 SSH 配置读取，请直接填写主机地址，或使用应用内保存的连接配置。",
		StatusCode: http.StatusGone,
	})
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		profiles, err := s.readSavedConnectionProfiles()
		if err != nil {
			writeAppError(w, asAppError(err, "应用内 SSH 配置读取失败"))
			return
		}
		writeJSON(w, http.StatusOK, map[string][]savedConnectionProfile{
			"profiles": profiles,
		})
	case http.MethodPost:
		var draft savedConnectionProfile
		if err := decodeJSONBody(r, &draft); err != nil {
			writeAppError(w, &appError{Message: err.Error(), StatusCode: http.StatusBadRequest})
			return
		}
		profile, statusCode, err := s.saveProfile(draft)
		if err != nil {
			writeAppError(w, asAppError(err, "应用内 SSH 配置保存失败"))
			return
		}
		writeJSON(w, statusCode, map[string]savedConnectionProfile{
			"profile": profile,
		})
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleProfileDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeMethodNotAllowed(w, http.MethodDelete)
		return
	}
	profileID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/profiles/"))
	if profileID == "" {
		writeAppError(w, &appError{Message: "缺少配置 ID", StatusCode: http.StatusBadRequest})
		return
	}
	if err := s.deleteProfile(profileID); err != nil {
		writeAppError(w, asAppError(err, "应用内 SSH 配置删除失败"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	var request connectionConfig
	if err := decodeJSONBody(r, &request); err != nil {
		writeAppError(w, &appError{Message: err.Error(), StatusCode: http.StatusBadRequest})
		return
	}
	session, err := s.createSession(request)
	if err != nil {
		writeAppError(w, asAppError(err, "SSH 会话创建失败"))
		return
	}
	writeJSON(w, http.StatusOK, connectionSessionResponse{
		AuthMethod: session.authMethod,
		Hostname:   session.hostname,
		Port:       session.port,
		SessionID:  session.id,
		Username:   session.username,
	})
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeMethodNotAllowed(w, http.MethodDelete)
		return
	}
	sessionID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/session/"))
	if sessionID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.destroySession(sessionID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTerminalExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	var request terminalExecRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeAppError(w, &appError{Message: err.Error(), StatusCode: http.StatusBadRequest})
		return
	}
	request.Command = strings.TrimSpace(request.Command)
	request.Cwd = strings.TrimSpace(request.Cwd)
	request.SessionID = strings.TrimSpace(request.SessionID)
	if request.SessionID == "" {
		writeAppError(w, &appError{Message: "缺少会话 ID", StatusCode: http.StatusBadRequest})
		return
	}
	if request.Command == "" {
		writeAppError(w, &appError{Message: "请输入要执行的命令", StatusCode: http.StatusBadRequest})
		return
	}
	if request.Cwd == "" {
		request.Cwd = "."
	}
	session, err := s.requireSession(request.SessionID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	result, err := executeRemoteCommand(session, request)
	if err != nil {
		writeAppError(w, asAppError(err, "远程命令执行失败"))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	session, err := s.requireSession(strings.TrimSpace(r.URL.Query().Get("sessionId")))
	if err != nil {
		writeAppError(w, err)
		return
	}
	remoteDir := strings.TrimSpace(r.URL.Query().Get("dir"))
	if remoteDir == "" {
		remoteDir = "."
	}
	sftpClient, err := sftp.NewClient(session.client)
	if err != nil {
		writeAppError(w, asAppError(err, "SFTP 初始化失败"))
		return
	}
	defer sftpClient.Close()

	currentDir, err := sftpClient.RealPath(remoteDir)
	if err != nil {
		writeAppError(w, asAppError(err, "目录读取失败"))
		return
	}
	entries, err := sftpClient.ReadDir(currentDir)
	if err != nil {
		writeAppError(w, asAppError(err, "目录读取失败"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"currentDir": currentDir,
		"entries":    parseDirectoryListing(currentDir, entries),
	})
}

func (s *Server) handlePreviewFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	session, err := s.requireSession(strings.TrimSpace(r.URL.Query().Get("sessionId")))
	if err != nil {
		writeAppError(w, err)
		return
	}
	remotePath := strings.TrimSpace(r.URL.Query().Get("path"))
	if remotePath == "" {
		writeAppError(w, &appError{Message: "缺少文件路径", StatusCode: http.StatusBadRequest})
		return
	}
	s.streamRemoteFile(w, session, remotePath, streamModePreview)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	session, err := s.requireSession(strings.TrimSpace(r.URL.Query().Get("sessionId")))
	if err != nil {
		writeAppError(w, err)
		return
	}
	remotePath := strings.TrimSpace(r.URL.Query().Get("path"))
	if remotePath == "" {
		writeAppError(w, &appError{Message: "缺少文件路径", StatusCode: http.StatusBadRequest})
		return
	}
	s.streamRemoteFile(w, session, remotePath, streamModeDownload)
}

func (s *Server) handleTextPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	session, err := s.requireSession(strings.TrimSpace(r.URL.Query().Get("sessionId")))
	if err != nil {
		writeAppError(w, err)
		return
	}
	remotePath := strings.TrimSpace(r.URL.Query().Get("path"))
	if remotePath == "" {
		writeAppError(w, &appError{Message: "缺少文件路径", StatusCode: http.StatusBadRequest})
		return
	}

	sftpClient, err := sftp.NewClient(session.client)
	if err != nil {
		writeAppError(w, asAppError(err, "SFTP 初始化失败"))
		return
	}
	defer sftpClient.Close()

	resolvedPath, err := sftpClient.RealPath(remotePath)
	if err != nil {
		writeAppError(w, asAppError(err, "远程文本预览失败"))
		return
	}
	info, err := sftpClient.Stat(resolvedPath)
	if err != nil {
		writeAppError(w, asAppError(err, "远程文本预览失败"))
		return
	}
	if !info.Mode().IsRegular() {
		writeAppError(w, &appError{Message: "目标不是可读取文件", StatusCode: http.StatusBadRequest})
		return
	}

	isLargeText := info.Size() > largeTextPreviewThresholdBytes
	bytesToRead := info.Size()
	if isLargeText {
		bytesToRead = largeTextPreviewMaxBytes
	}
	previewBuffer, err := readRemoteFileSlice(sftpClient, resolvedPath, bytesToRead)
	if err != nil {
		writeAppError(w, asAppError(err, "远程文本预览失败"))
		return
	}
	if looksLikeBinaryContent(previewBuffer) {
		writeJSON(w, http.StatusOK, textPreviewPayload{
			Kind:      "binary",
			Message:   "二进制文件不支持预览",
			TotalSize: info.Size(),
		})
		return
	}

	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(resolvedPath)), ".")
	rawText := string(previewBuffer)

	if extension == "csv" || extension == "tsv" {
		delimiter := ","
		if extension == "tsv" {
			delimiter = "\t"
		}
		rows, truncated := buildDelimitedPreview(rawText, delimiter, tablePreviewMaxRows)
		writeJSON(w, http.StatusOK, textPreviewPayload{
			Kind:           "table",
			Notice:         fmt.Sprintf("%s 预览仅显示前 %d 行", strings.ToUpper(extension), tablePreviewMaxRows),
			PreviewedBytes: len(previewBuffer),
			Rows:           rows,
			TotalSize:      info.Size(),
			Truncated:      isLargeText || truncated,
		})
		return
	}

	content := rawText
	truncated := false
	if isLargeText {
		content, truncated = takeLeadingLines(rawText, largeTextPreviewMaxLines)
	}

	writeJSON(w, http.StatusOK, textPreviewPayload{
		Content:        content,
		Kind:           "text",
		Notice:         ternary(isLargeText, fmt.Sprintf("文件超过 10 MB，仅展示前 %d 行", largeTextPreviewMaxLines), ""),
		PreviewedBytes: len(previewBuffer),
		TotalSize:      info.Size(),
		Truncated:      isLargeText || truncated,
	})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	session, err := s.requireSession(strings.TrimSpace(r.URL.Query().Get("sessionId")))
	if err != nil {
		writeAppError(w, err)
		return
	}
	remoteDir := strings.TrimSpace(r.URL.Query().Get("dir"))
	if remoteDir == "" {
		writeAppError(w, &appError{Message: "缺少上传目录", StatusCode: http.StatusBadRequest})
		return
	}
	fileName, err := normalizeUploadFilename(r.URL.Query().Get("filename"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	overwrite := strings.TrimSpace(r.URL.Query().Get("overwrite")) == "1"

	sftpClient, err := sftp.NewClient(session.client)
	if err != nil {
		writeAppError(w, asAppError(err, "SFTP 初始化失败"))
		return
	}
	defer sftpClient.Close()

	resolvedDir, err := sftpClient.RealPath(remoteDir)
	if err != nil {
		writeAppError(w, asAppError(err, "远程文件上传失败"))
		return
	}
	dirInfo, err := sftpClient.Stat(resolvedDir)
	if err != nil {
		writeAppError(w, asAppError(err, "远程文件上传失败"))
		return
	}
	if !dirInfo.IsDir() {
		writeAppError(w, &appError{Message: "目标不是可写目录", StatusCode: http.StatusBadRequest})
		return
	}

	targetPath := path.Join(resolvedDir, fileName)
	if !overwrite {
		exists, err := sftpPathExists(sftpClient, targetPath)
		if err != nil {
			writeAppError(w, asAppError(err, "远程文件上传失败"))
			return
		}
		if exists {
			writeAppError(w, &appError{Message: fmt.Sprintf("远程已存在同名文件: %s", fileName), StatusCode: http.StatusConflict})
			return
		}
	}

	remoteFile, err := sftpClient.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		writeAppError(w, asAppError(err, "远程文件上传失败"))
		return
	}

	var copied int64
	copyErr := func() error {
		defer remoteFile.Close()
		n, err := io.Copy(remoteFile, r.Body)
		copied = n
		return err
	}()
	if copyErr != nil {
		_ = sftpClient.Remove(targetPath)
		writeAppError(w, asAppError(copyErr, "远程文件上传失败"))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"fileName": fileName,
		"path":     targetPath,
		"size":     copied,
	})
}

func (s *Server) handleHTMLPreview(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		setHTMLPreviewHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet:
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodOptions)
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/api/html-preview/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		writeAppError(w, &appError{Message: "缺少 HTML 预览路径", StatusCode: http.StatusBadRequest})
		return
	}
	sessionID := strings.TrimSpace(parts[0])
	requestedPath := strings.TrimSpace(parts[1])
	if sessionID == "" || requestedPath == "" {
		writeAppError(w, &appError{Message: "缺少 HTML 预览路径", StatusCode: http.StatusBadRequest})
		return
	}
	session, err := s.requireSession(sessionID)
	if err != nil {
		writeAppError(w, err)
		return
	}

	sftpClient, err := sftp.NewClient(session.client)
	if err != nil {
		writeAppError(w, asAppError(err, "SFTP 初始化失败"))
		return
	}
	defer sftpClient.Close()

	remotePath := normalizeRemoteRoutePath(requestedPath)
	resolvedPath, err := sftpClient.RealPath(remotePath)
	if err != nil {
		writeAppError(w, asAppError(err, "HTML 资源读取失败"))
		return
	}
	info, err := sftpClient.Stat(resolvedPath)
	if err != nil {
		writeAppError(w, asAppError(err, "HTML 资源读取失败"))
		return
	}
	if !info.Mode().IsRegular() {
		writeAppError(w, &appError{Message: "目标不是可读取文件", StatusCode: http.StatusBadRequest})
		return
	}

	setHTMLPreviewHeaders(w)
	w.Header().Set("Cache-Control", "no-store")

	extension := strings.ToLower(filepath.Ext(resolvedPath))
	if extension == ".html" || extension == ".htm" {
		content, err := readRemoteFileSlice(sftpClient, resolvedPath, info.Size())
		if err != nil {
			writeAppError(w, asAppError(err, "HTML 资源读取失败"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(rewriteHTMLPreviewDocument(string(content), session.id, resolvedPath)))
		return
	}

	file, err := sftpClient.Open(resolvedPath)
	if err != nil {
		writeAppError(w, asAppError(err, "HTML 资源读取失败"))
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", getRemoteContentType(resolvedPath))
	if _, err := io.Copy(w, file); err != nil {
		return
	}
}

func (s *Server) saveProfile(draft savedConnectionProfile) (savedConnectionProfile, int, error) {
	normalized, err := normalizeSavedConnectionProfile(draft)
	if err != nil {
		return savedConnectionProfile{}, 0, err
	}
	profiles, err := s.readSavedConnectionProfiles()
	if err != nil {
		return savedConnectionProfile{}, 0, err
	}

	now := time.Now().UnixMilli()
	statusCode := http.StatusCreated
	existingIndex := -1
	for i, profile := range profiles {
		if normalized.ID != "" && profile.ID == normalized.ID {
			existingIndex = i
			break
		}
	}

	nextProfile := normalized
	if existingIndex >= 0 {
		statusCode = http.StatusOK
		nextProfile.CreatedAt = profiles[existingIndex].CreatedAt
		nextProfile.ID = profiles[existingIndex].ID
		nextProfile.UpdatedAt = now
		profiles[existingIndex] = nextProfile
	} else {
		nextProfile.ID = randomID()
		nextProfile.CreatedAt = now
		nextProfile.UpdatedAt = now
		profiles = append(profiles, nextProfile)
	}

	sortProfiles(profiles)
	if err := s.writeSavedConnectionProfiles(profiles); err != nil {
		return savedConnectionProfile{}, 0, err
	}
	return nextProfile, statusCode, nil
}

func (s *Server) deleteProfile(profileID string) error {
	profiles, err := s.readSavedConnectionProfiles()
	if err != nil {
		return err
	}
	nextProfiles := make([]savedConnectionProfile, 0, len(profiles))
	found := false
	for _, profile := range profiles {
		if profile.ID == profileID {
			found = true
			continue
		}
		nextProfiles = append(nextProfiles, profile)
	}
	if !found {
		return &appError{Message: "应用内 SSH 配置不存在", StatusCode: http.StatusNotFound}
	}
	return s.writeSavedConnectionProfiles(nextProfiles)
}

func (s *Server) createSession(config connectionConfig) (*sessionRecord, error) {
	target, err := normalizeConnection(config)
	if err != nil {
		return nil, err
	}
	port, err := resolvePort(target.Port)
	if err != nil {
		return nil, err
	}
	sshConfig, err := buildSSHClientConfig(target)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(target.Host, strconv.Itoa(port))
	client, err := ssh.Dial("tcp", address, sshConfig)
	if err != nil {
		return nil, explainSSHConnectError(err, target.AuthMethod)
	}

	session := &sessionRecord{
		authMethod: target.AuthMethod,
		client:     client,
		createdAt:  time.Now(),
		hostname:   target.Host,
		id:         randomID(),
		lastUsedAt: time.Now(),
		port:       port,
		username:   target.Username,
	}

	s.mu.Lock()
	s.sessions[session.id] = session
	s.mu.Unlock()

	return session, nil
}

func (s *Server) requireSession(sessionID string) (*sessionRecord, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, &appError{Message: "缺少会话 ID", StatusCode: http.StatusBadRequest}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, &appError{Message: "SSH 会话不存在或已过期，请重新连接", StatusCode: http.StatusUnauthorized}
	}
	session.lastUsedAt = time.Now()
	return session, nil
}

func (s *Server) destroySession(sessionID string) {
	s.mu.Lock()
	session, ok := s.sessions[sessionID]
	if ok {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()
	if ok {
		_ = session.client.Close()
	}
}

type streamMode string

const (
	streamModePreview  streamMode = "preview"
	streamModeDownload streamMode = "download"
)

func (s *Server) streamRemoteFile(w http.ResponseWriter, session *sessionRecord, remotePath string, mode streamMode) {
	sftpClient, err := sftp.NewClient(session.client)
	if err != nil {
		writeAppError(w, asAppError(err, "SFTP 初始化失败"))
		return
	}
	defer sftpClient.Close()

	resolvedPath, err := sftpClient.RealPath(remotePath)
	if err != nil {
		writeAppError(w, asAppError(err, "远程文件读取失败"))
		return
	}
	info, err := sftpClient.Stat(resolvedPath)
	if err != nil {
		writeAppError(w, asAppError(err, ternary(mode == streamModeDownload, "远程文件下载失败", "远程文件读取失败")))
		return
	}
	if !info.Mode().IsRegular() {
		writeAppError(w, &appError{
			Message:    ternary(mode == streamModeDownload, "目标不是可下载文件", "目标不是可读取文件"),
			StatusCode: http.StatusBadRequest,
		})
		return
	}

	extension := strings.ToLower(filepath.Ext(resolvedPath))
	if mode == streamModePreview && !isSupportedPreviewExtension(extension) {
		writeAppError(w, &appError{Message: "仅支持 PDF 和常见图片文件", StatusCode: http.StatusBadRequest})
		return
	}

	file, err := sftpClient.Open(resolvedPath)
	if err != nil {
		writeAppError(w, asAppError(err, ternary(mode == streamModeDownload, "远程文件下载失败", "远程文件读取失败")))
		return
	}
	defer file.Close()

	filename := filepath.Base(resolvedPath)
	contentType := getRemoteContentType(resolvedPath)
	dispositionType := "inline"
	if mode == streamModeDownload {
		dispositionType = "attachment"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", buildContentDispositionHeader(filename, dispositionType))
	w.Header().Set("Cache-Control", "no-store")
	if mode == streamModeDownload {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	if _, err := io.Copy(w, file); err != nil {
		return
	}
}

func (s *Server) ensureAppStorageDir() error {
	return os.MkdirAll(s.storageDir, 0o700)
}

func (s *Server) readSavedConnectionProfiles() ([]savedConnectionProfile, error) {
	var targetPath string
	if _, err := os.Stat(s.savedProfilesPath); err == nil {
		targetPath = s.savedProfilesPath
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	} else if _, legacyErr := os.Stat(s.legacySavedProfilesPath); legacyErr == nil {
		targetPath = s.legacySavedProfilesPath
	} else if !errors.Is(legacyErr, fs.ErrNotExist) {
		return nil, legacyErr
	}

	if targetPath == "" {
		return []savedConnectionProfile{}, nil
	}

	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Profiles []savedConnectionProfile `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	profiles := payload.Profiles
	for index := range profiles {
		if profiles[index].AuthMethod != AuthMethodPassword {
			profiles[index].RememberPassword = false
		}
	}
	sortProfiles(profiles)
	return profiles, nil
}

func (s *Server) writeSavedConnectionProfiles(profiles []savedConnectionProfile) error {
	if err := s.ensureAppStorageDir(); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(map[string]any{
		"profiles": profiles,
	}, "", "  ")
	if err != nil {
		return err
	}
	tempPath := fmt.Sprintf("%s.%d.tmp", s.savedProfilesPath, time.Now().UnixNano())
	if err := os.WriteFile(tempPath, append(payload, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tempPath, s.savedProfilesPath)
}

func normalizeSavedConnectionProfile(draft savedConnectionProfile) (savedConnectionProfile, error) {
	authMethod := normalizeAuthMethod(string(draft.AuthMethod))
	name := strings.TrimSpace(draft.Name)
	host := strings.TrimSpace(draft.Host)
	port := strings.TrimSpace(draft.Port)
	username := strings.TrimSpace(draft.Username)
	rootPath := strings.TrimSpace(draft.RootPath)
	if port == "" {
		port = "22"
	}
	if rootPath == "" {
		rootPath = "/"
	}
	if name == "" {
		return savedConnectionProfile{}, &appError{Message: "请填写配置名称", StatusCode: http.StatusBadRequest}
	}
	if host == "" {
		return savedConnectionProfile{}, &appError{Message: "请填写 SSH 主机地址", StatusCode: http.StatusBadRequest}
	}
	if username == "" {
		return savedConnectionProfile{}, &appError{Message: "请填写 SSH 用户名", StatusCode: http.StatusBadRequest}
	}
	if authMethod == AuthMethodKey && strings.TrimSpace(draft.PrivateKey) == "" {
		return savedConnectionProfile{}, &appError{Message: "SSH Key 配置需要填写私钥内容", StatusCode: http.StatusBadRequest}
	}
	return savedConnectionProfile{
		AuthMethod:       authMethod,
		CreatedAt:        draft.CreatedAt,
		Host:             host,
		ID:               strings.TrimSpace(draft.ID),
		Name:             name,
		Port:             port,
		PrivateKey:       ternary(authMethod == AuthMethodKey, draft.PrivateKey, ""),
		RememberPassword: authMethod == AuthMethodPassword && draft.RememberPassword,
		RootPath:         rootPath,
		UpdatedAt:        draft.UpdatedAt,
		Username:         username,
	}, nil
}

func normalizeConnection(config connectionConfig) (connectionConfig, error) {
	authMethod := normalizeAuthMethod(string(config.AuthMethod))
	host := strings.TrimSpace(config.Host)
	username := strings.TrimSpace(config.Username)
	port := strings.TrimSpace(config.Port)
	if host == "" {
		return connectionConfig{}, &appError{Message: "缺少 SSH 主机信息", StatusCode: http.StatusBadRequest}
	}
	if username == "" {
		return connectionConfig{}, &appError{Message: "缺少 SSH 用户名", StatusCode: http.StatusBadRequest}
	}
	if authMethod == AuthMethodPassword && config.Password == "" {
		return connectionConfig{}, &appError{Message: "密码登录需要填写密码", StatusCode: http.StatusBadRequest}
	}
	if authMethod == AuthMethodKey && strings.TrimSpace(config.PrivateKey) == "" {
		return connectionConfig{}, &appError{Message: "SSH Key 登录需要提供私钥内容", StatusCode: http.StatusBadRequest}
	}
	if port == "" {
		port = "22"
	}
	return connectionConfig{
		AuthMethod: authMethod,
		Host:       host,
		Password:   config.Password,
		PrivateKey: config.PrivateKey,
		Port:       port,
		RootPath:   strings.TrimSpace(config.RootPath),
		Username:   username,
	}, nil
}

func resolvePort(rawPort string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(rawPort))
	if err != nil || port <= 0 || port > 65535 {
		return 0, &appError{Message: "SSH 端口不合法", StatusCode: http.StatusBadRequest}
	}
	return port, nil
}

func buildSSHClientConfig(config connectionConfig) (*ssh.ClientConfig, error) {
	var auth []ssh.AuthMethod
	switch config.AuthMethod {
	case AuthMethodPassword:
		password := config.Password
		auth = append(auth,
			ssh.Password(password),
			ssh.KeyboardInteractive(func(_ string, _ string, _ []string, _ []bool) ([]string, error) {
				return []string{password}, nil
			}),
		)
	default:
		signer, err := ssh.ParsePrivateKey([]byte(config.PrivateKey))
		if err != nil {
			return nil, &appError{Message: "SSH 私钥解析失败，请确认私钥格式正确且未加密。", StatusCode: http.StatusBadRequest}
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	return &ssh.ClientConfig{
		User:            config.Username,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}, nil
}

func explainSSHConnectError(err error, authMethod AuthMethod) error {
	message := strings.ToLower(err.Error())
	if authMethod == AuthMethodPassword &&
		(strings.Contains(message, "unable to authenticate") ||
			strings.Contains(message, "permission denied") ||
			strings.Contains(message, "handshake failed")) {
		return errors.New("用户名或密码错误，SSH 认证失败，请检查后重试。")
	}
	if strings.Contains(message, "timed out") || strings.Contains(message, "timeout") {
		return errors.New("SSH 连接超时，请确认主机、端口和网络是否可达。")
	}
	if strings.Contains(message, "connection refused") {
		return errors.New("SSH 连接被拒绝，请确认端口是否正确且远端 SSH 服务已启动。")
	}
	if strings.Contains(message, "no such host") ||
		strings.Contains(message, "getaddrinfo") ||
		strings.Contains(message, "network is unreachable") ||
		strings.Contains(message, "no route to host") {
		return errors.New("无法连接到目标主机，请检查主机地址、DNS 和网络配置。")
	}
	return err
}

func executeRemoteCommand(session *sessionRecord, request terminalExecRequest) (terminalExecResult, error) {
	sshSession, err := session.client.NewSession()
	if err != nil {
		return terminalExecResult{}, err
	}
	defer sshSession.Close()

	stdoutPipe, err := sshSession.StdoutPipe()
	if err != nil {
		return terminalExecResult{}, err
	}
	stderrPipe, err := sshSession.StderrPipe()
	if err != nil {
		return terminalExecResult{}, err
	}

	stdoutBuf := &limitedBuffer{limit: terminalOutputLimitBytes}
	stderrBuf := &limitedBuffer{limit: terminalOutputLimitBytes}
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(stdoutBuf, stdoutPipe)
	}()
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(stderrBuf, stderrPipe)
	}()

	shellCommand := fmt.Sprintf("sh -lc %s", escapePosixShellArgument(
		fmt.Sprintf("cd -- %s && %s", escapePosixShellArgument(request.Cwd), request.Command),
	))
	if err := sshSession.Start(shellCommand); err != nil {
		return terminalExecResult{}, err
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- sshSession.Wait()
	}()

	timedOut := false
	waitErr := error(nil)

	select {
	case waitErr = <-waitCh:
	case <-time.After(terminalExecTimeout):
		timedOut = true
		_ = sshSession.Signal(ssh.SIGTERM)
		_ = sshSession.Close()
		waitErr = <-waitCh
	}

	copyWG.Wait()

	var exitCode *int
	var signal *string

	if waitErr != nil {
		var exitErr *ssh.ExitError
		if errors.As(waitErr, &exitErr) {
			status := exitErr.ExitStatus()
			exitCode = &status
			if sig := exitErr.Signal(); sig != "" {
				signal = &sig
			}
		} else if !timedOut {
			return terminalExecResult{}, waitErr
		}
	}

	if waitErr == nil && exitCode == nil {
		zero := 0
		exitCode = &zero
	}

	return terminalExecResult{
		Command:         request.Command,
		Cwd:             request.Cwd,
		ExitCode:        exitCode,
		Signal:          signal,
		Stderr:          stderrBuf.buf.String(),
		StderrTruncated: stderrBuf.truncated,
		Stdout:          stdoutBuf.buf.String(),
		StdoutTruncated: stdoutBuf.truncated,
		TimedOut:        timedOut,
	}, nil
}

func parseDirectoryListing(currentDir string, entries []os.FileInfo) []remoteEntry {
	result := make([]remoteEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			result = append(result, remoteEntry{
				Kind: "directory",
				Name: entry.Name(),
				Path: path.Join(currentDir, entry.Name()),
				Size: entry.Size(),
			})
			continue
		}
		if !entry.Mode().IsRegular() {
			continue
		}
		result = append(result, remoteEntry{
			Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(entry.Name())), "."),
			Kind:      "file",
			Name:      entry.Name(),
			Path:      path.Join(currentDir, entry.Name()),
			Size:      entry.Size(),
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind == "directory"
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}

func readRemoteFileSlice(client *sftp.Client, remotePath string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return []byte{}, nil
	}
	file, err := client.Open(remotePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maxBytes))
}

func looksLikeBinaryContent(buffer []byte) bool {
	if len(buffer) == 0 {
		return false
	}
	sample := buffer
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	suspiciousBytes := 0
	for _, b := range sample {
		if b == 0 {
			return true
		}
		if b < 7 || (b > 14 && b < 32) {
			suspiciousBytes++
		}
	}
	return float64(suspiciousBytes)/float64(len(sample)) > 0.1
}

func takeLeadingLines(content string, maxLines int) (string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content, false
	}
	return strings.Join(lines[:maxLines], "\n"), true
}

func splitDelimitedLine(line string, delimiter string) []string {
	cells := make([]string, 0, 8)
	var current strings.Builder
	inQuotes := false
	for i := 0; i < len(line); i++ {
		character := line[i]
		if character == '"' {
			nextChar := byte(0)
			if i+1 < len(line) {
				nextChar = line[i+1]
			}
			if inQuotes && nextChar == '"' {
				current.WriteByte('"')
				i++
				continue
			}
			inQuotes = !inQuotes
			continue
		}
		if string(character) == delimiter && !inQuotes {
			cells = append(cells, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(character)
	}
	cells = append(cells, current.String())
	return cells
}

func buildDelimitedPreview(content string, delimiter string, maxRows int) ([][]string, bool) {
	allLines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	previewLines := allLines
	if len(previewLines) > maxRows {
		previewLines = previewLines[:maxRows]
	}
	rows := make([][]string, 0, len(previewLines))
	for _, line := range previewLines {
		rows = append(rows, splitDelimitedLine(line, delimiter))
	}
	return rows, len(allLines) > maxRows
}

func normalizeRemoteRoutePath(routePath string) string {
	normalized := routePath
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func normalizeUploadFilename(rawFileName string) (string, error) {
	normalized := strings.TrimSpace(strings.ReplaceAll(rawFileName, "\\", "/"))
	fileName := path.Base(normalized)
	if fileName == "" || fileName == "." || fileName == ".." {
		return "", &appError{Message: "上传文件名不合法", StatusCode: http.StatusBadRequest}
	}
	return fileName, nil
}

func buildContentDispositionHeader(filename string, dispositionType string) string {
	fallback := sanitizeFilename(filename)
	return fmt.Sprintf("%s; filename=\"%s\"; filename*=UTF-8''%s", dispositionType, fallback, pathEscape(filename))
}

func sanitizeFilename(filename string) string {
	replacer := strings.NewReplacer(`"`, "_", `\`, "_")
	cleaned := replacer.Replace(filename)
	var builder strings.Builder
	for _, r := range cleaned {
		if r >= 0x20 && r <= 0x7E {
			builder.WriteRune(r)
			continue
		}
		builder.WriteRune('_')
	}
	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "download"
	}
	return result
}

func buildHTMLPreviewProxyPath(sessionID string, remotePath string) string {
	segments := strings.Split(normalizeRemoteRoutePath(remotePath), "/")
	for i := 1; i < len(segments); i++ {
		segments[i] = pathEscape(segments[i])
	}
	return "/api/html-preview/" + pathEscape(sessionID) + strings.Join(segments, "/")
}

func isSpecialURLReference(value string) bool {
	if value == "" || strings.HasPrefix(value, "#") || strings.HasPrefix(value, "//") {
		return true
	}
	return regexp.MustCompile(`^[a-z][a-z\d+\-.]*:`).MatchString(strings.ToLower(value))
}

func rewriteHTMLAbsoluteReference(value string, sessionID string) string {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return value
	}
	return buildHTMLPreviewProxyPath(sessionID, value)
}

func rewriteHTMLBaseReference(value string, sessionID string, currentDir string) string {
	if isSpecialURLReference(value) {
		return value
	}
	trailingSlash := strings.HasSuffix(value, "/")
	resolvedPath := value
	if strings.HasPrefix(value, "/") {
		resolvedPath = normalizeRemoteRoutePath(value)
	} else {
		resolvedPath = path.Clean(path.Join(currentDir, value))
	}
	proxiedPath := buildHTMLPreviewProxyPath(sessionID, resolvedPath)
	if trailingSlash && !strings.HasSuffix(proxiedPath, "/") {
		return proxiedPath + "/"
	}
	return proxiedPath
}

func rewriteHTMLSrcset(value string, sessionID string) string {
	parts := strings.Split(value, ",")
	for index, entry := range parts {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		fields[0] = rewriteHTMLAbsoluteReference(fields[0], sessionID)
		parts[index] = strings.Join(fields, " ")
	}
	return strings.Join(parts, ", ")
}

func rewriteHTMLPreviewDocument(content string, sessionID string, remotePath string) string {
	currentDir := path.Dir(remotePath)
	content = replaceAllRegexpString(content, htmlBaseHrefRe, func(match []string) string {
		return fmt.Sprintf("<base%shref=%s%s%s%s>", match[1], match[2], rewriteHTMLBaseReference(match[3], sessionID, currentDir), match[2], match[4])
	})
	content = replaceAllRegexpString(content, htmlSrcsetRe, func(match []string) string {
		return fmt.Sprintf("srcset=%s%s%s", match[1], rewriteHTMLSrcset(match[2], sessionID), match[1])
	})
	content = replaceAllRegexpString(content, htmlAttrRe, func(match []string) string {
		return fmt.Sprintf("%s=%s%s%s", match[1], match[2], rewriteHTMLAbsoluteReference(match[3], sessionID), match[2])
	})
	content = replaceAllRegexpString(content, htmlCSSURLRe, func(match []string) string {
		target := match[2]
		if strings.HasPrefix(target, "//") {
			return match[0]
		}
		return fmt.Sprintf("url(%s%s%s)", match[1], buildHTMLPreviewProxyPath(sessionID, target), match[1])
	})
	return content
}

func getRemoteContentType(remotePath string) string {
	extension := strings.ToLower(filepath.Ext(remotePath))
	switch extension {
	case ".css":
		return "text/css; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".gif":
		return "image/gif"
	case ".htm", ".html":
		return "text/html; charset=utf-8"
	case ".ico":
		return "image/x-icon"
	case ".jpeg", ".jpg":
		return "image/jpeg"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	case ".tsv":
		return "text/tab-separated-values; charset=utf-8"
	case ".txt", ".log":
		return "text/plain; charset=utf-8"
	case ".wasm":
		return "application/wasm"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		if detected := mime.TypeByExtension(extension); detected != "" {
			return detected
		}
		return "application/octet-stream"
	}
}

func isSupportedPreviewExtension(extension string) bool {
	switch extension {
	case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return true
	default:
		return false
	}
}

func decodeJSONBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("请求体解析失败: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAppError(w http.ResponseWriter, err error) {
	appErr := asAppError(err, "请求失败")
	if appErr == nil {
		appErr = &appError{Message: "请求失败", StatusCode: http.StatusInternalServerError}
	}
	writeJSON(w, appErr.StatusCode, map[string]string{
		"error": appErr.Message,
	})
}

func writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	if len(methods) > 0 {
		w.Header().Set("Allow", strings.Join(methods, ", "))
	}
	writeAppError(w, &appError{Message: "请求方法不支持", StatusCode: http.StatusMethodNotAllowed})
}

func asAppError(err error, fallback string) *appError {
	if err == nil {
		return nil
	}
	var appErr *appError
	if errors.As(err, &appErr) {
		return appErr
	}
	return &appError{
		Message:    ternary(strings.TrimSpace(err.Error()) != "", err.Error(), fallback),
		StatusCode: http.StatusBadGateway,
	}
}

func sftpPathExists(client *sftp.Client, remotePath string) (bool, error) {
	_, err := client.Stat(remotePath)
	if err == nil {
		return true, nil
	}
	if isSFTPNotFound(err) {
		return false, nil
	}
	return false, err
}

func isSFTPNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var statusErr *sftp.StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Code == 2
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such file") || strings.Contains(message, "not exist")
}

func sortProfiles(profiles []savedConnectionProfile) {
	sort.Slice(profiles, func(i, j int) bool {
		left := strings.ToLower(profiles[i].Name)
		right := strings.ToLower(profiles[j].Name)
		if left == right {
			return profiles[i].ID < profiles[j].ID
		}
		return left < right
	})
}

func randomID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func normalizeAuthMethod(value string) AuthMethod {
	if strings.TrimSpace(strings.ToLower(value)) == string(AuthMethodPassword) {
		return AuthMethodPassword
	}
	return AuthMethodKey
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "."
	}
	return home
}

func setHTMLPreviewHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func replaceAllRegexpString(input string, re *regexp.Regexp, replacer func([]string) string) string {
	return re.ReplaceAllStringFunc(input, func(match string) string {
		submatches := re.FindStringSubmatch(match)
		if submatches == nil {
			return match
		}
		return replacer(submatches)
	})
}

func pathEscape(value string) string {
	escaped := make([]string, 0, len(value))
	for _, segment := range strings.Split(value, "/") {
		escaped = append(escaped, url.PathEscape(segment))
	}
	return strings.Join(escaped, "/")
}

func escapePosixShellArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\"'\"'`) + "'"
}

func ternary[T any](condition bool, whenTrue T, whenFalse T) T {
	if condition {
		return whenTrue
	}
	return whenFalse
}
