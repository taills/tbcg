package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/internal/content/schema"
)

const (
	maxImportDefinitions = 200
	maxListedContent     = 2000
)

// ContentSummary 是稳定 ID 搜索结果：草稿状态与最新/已批准 revision。
type ContentSummary struct {
	ContentID        string    `json:"content_id"`
	ContentType      string    `json:"content_type"`
	DisplayName      string    `json:"display_name"`
	DraftID          string    `json:"draft_id,omitempty"`
	DraftVersion     string    `json:"draft_version,omitempty"`
	LatestRevision   int32     `json:"latest_revision"`
	ApprovedRevision int32     `json:"approved_revision"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ContentDefinition 是某来源（草稿或已批准）的一份定义。
type ContentDefinition struct {
	ContentID  string          `json:"content_id"`
	Source     string          `json:"source"`
	Revision   int32           `json:"revision"`
	Definition json.RawMessage `json:"definition"`
}

// ContentStore 为创作体验提供只读查询。
type ContentStore interface {
	ListContent(ctx context.Context, gameID string, limit int) ([]ContentSummary, error)
	ContentDefinitions(ctx context.Context, gameID, source string) ([]ContentDefinition, error)
	RevisionDefinition(ctx context.Context, gameID, contentID string, revision int32) (json.RawMessage, error)
}

// PreviewRunner 用与 runtime 完全相同的 Go 规则运行预览。
type PreviewRunner interface {
	PreviewBattle(content *catalog.Catalog, request json.RawMessage) (any, error)
}

type contentHandler struct {
	*handler
	content ContentStore
}

func registerContentRoutes(mux *http.ServeMux, api *handler) {
	content, ok := api.store.(ContentStore)
	if !ok {
		return
	}
	routes := &contentHandler{handler: api, content: content}
	mux.HandleFunc("GET /api/v1/games/{gameId}/content", routes.search)
	mux.HandleFunc("GET /api/v1/games/{gameId}/content-references", routes.references)
	mux.HandleFunc("GET /api/v1/games/{gameId}/content-diff", routes.diff)
	mux.HandleFunc("GET /api/v1/games/{gameId}/content-revisions", routes.revision)
	mux.HandleFunc("POST /api/v1/games/{gameId}/content-imports", routes.importDefinitions)
	mux.HandleFunc("POST /api/v1/games/{gameId}/validation-report", routes.validationReport)
	mux.HandleFunc("POST /api/v1/games/{gameId}/previews/battle", routes.previewBattle)
	mux.HandleFunc("GET /api/v1/content-types", routes.contentTypes)
}

func (h *contentHandler) contentTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAuthenticated(w, r); !ok {
		return
	}
	writeSuccess(w, http.StatusOK, schema.ContentTypes, nil)
}

func (h *contentHandler) search(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requireAuthenticated(w, r)
	if !ok || !h.requireRole(w, r, current, gameID, readableRoles...) {
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	contentType := r.URL.Query().Get("type")
	if len(query) > 128 || (contentType != "" && !knownContentType(contentType)) {
		writeFailure(w, http.StatusBadRequest, "invalid_query", "搜索条件无效", nil)
		return
	}
	all, err := h.content.ListContent(r.Context(), gameID, maxListedContent)
	if err != nil {
		h.internalError(w, "list content", err)
		return
	}
	matches := make([]ContentSummary, 0)
	for _, summary := range all {
		if contentType != "" && summary.ContentType != contentType {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(summary.ContentID), query) && !strings.Contains(strings.ToLower(summary.DisplayName), query) {
			continue
		}
		matches = append(matches, summary)
	}
	writeSuccess(w, http.StatusOK, matches, map[string]any{"total": len(matches)})
}

func knownContentType(value string) bool {
	for _, known := range schema.ContentTypes {
		if known == value {
			return true
		}
	}
	return false
}

type referenceHit struct {
	ContentID string `json:"content_id"`
	Pointer   string `json:"pointer"`
}

func (h *contentHandler) references(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requireAuthenticated(w, r)
	if !ok || !h.requireRole(w, r, current, gameID, readableRoles...) {
		return
	}
	target := r.URL.Query().Get("content_id")
	if !validContentID(target) {
		writeFailure(w, http.StatusBadRequest, "invalid_content_id", "必须指定合法的内容 ID", nil)
		return
	}
	definitions, err := h.content.ContentDefinitions(r.Context(), gameID, "draft")
	if err != nil {
		h.internalError(w, "read content for references", err)
		return
	}
	incoming := make([]referenceHit, 0)
	outgoing := make([]referenceHit, 0)
	known := map[string]bool{}
	for _, definition := range definitions {
		known[definition.ContentID] = true
	}
	for _, definition := range definitions {
		var value any
		if json.Unmarshal(definition.Definition, &value) != nil {
			continue
		}
		walkStrings(value, "", func(pointer, text string) {
			if pointer == "/content_id" || pointer == "/public/content_id" {
				return
			}
			if text == target && definition.ContentID != target {
				incoming = append(incoming, referenceHit{ContentID: definition.ContentID, Pointer: pointer})
			}
			if definition.ContentID == target && strings.Contains(text, ":") && strings.Contains(text, "/") && (known[text] || looksLikeContentID(text)) {
				outgoing = append(outgoing, referenceHit{ContentID: text, Pointer: pointer})
			}
		})
	}
	broken := make([]referenceHit, 0)
	for _, hit := range outgoing {
		if !known[hit.ContentID] {
			broken = append(broken, hit)
		}
	}
	writeSuccess(w, http.StatusOK, map[string]any{"content_id": target, "incoming": incoming, "outgoing": outgoing, "broken": broken}, nil)
}

func looksLikeContentID(text string) bool {
	_, err := schema.ContentTypeOf(text)
	return err == nil
}

func walkStrings(value any, pointer string, visit func(string, string)) {
	switch typed := value.(type) {
	case string:
		visit(pointer, typed)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walkStrings(typed[key], pointer+"/"+escapePointer(key), visit)
		}
	case []any:
		for index, item := range typed {
			walkStrings(item, pointer+"/"+strconv.Itoa(index), visit)
		}
	}
}

func escapePointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

type diffChange struct {
	Pointer string `json:"pointer"`
	Kind    string `json:"kind"`
	Before  any    `json:"before,omitempty"`
	After   any    `json:"after,omitempty"`
}

func (h *contentHandler) diff(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requireAuthenticated(w, r)
	if !ok || !h.requireRole(w, r, current, gameID, readableRoles...) {
		return
	}
	query := r.URL.Query()
	contentID := query.Get("content_id")
	if !validContentID(contentID) {
		writeFailure(w, http.StatusBadRequest, "invalid_content_id", "必须指定合法的内容 ID", nil)
		return
	}
	from, err := h.diffSide(r.Context(), gameID, contentID, query.Get("from"))
	if err == nil {
		var to json.RawMessage
		to, err = h.diffSide(r.Context(), gameID, contentID, query.Get("to"))
		if err == nil {
			var before, after any
			_ = json.Unmarshal(from, &before)
			_ = json.Unmarshal(to, &after)
			changes := make([]diffChange, 0)
			diffValues("", before, after, &changes)
			writeSuccess(w, http.StatusOK, map[string]any{"content_id": contentID, "from": query.Get("from"), "to": query.Get("to"), "changes": changes}, nil)
			return
		}
	}
	if errors.Is(err, ErrNotFound) {
		writeFailure(w, http.StatusNotFound, "not_found", "找不到要比较的 revision 或草稿", nil)
		return
	}
	if errors.Is(err, errInvalidDiffSide) {
		writeFailure(w, http.StatusBadRequest, "invalid_diff", "from/to 必须是正整数 revision 或 draft", nil)
		return
	}
	h.internalError(w, "diff content", err)
}

var errInvalidDiffSide = errors.New("invalid diff side")

func (h *contentHandler) diffSide(ctx context.Context, gameID, contentID, side string) (json.RawMessage, error) {
	if side == "draft" {
		definitions, err := h.content.ContentDefinitions(ctx, gameID, "draft")
		if err != nil {
			return nil, err
		}
		for _, definition := range definitions {
			if definition.ContentID == contentID && definition.Source == "draft" {
				return definition.Definition, nil
			}
		}
		return nil, ErrNotFound
	}
	revision, err := strconv.ParseInt(side, 10, 32)
	if err != nil || revision < 1 {
		return nil, errInvalidDiffSide
	}
	return h.content.RevisionDefinition(ctx, gameID, contentID, int32(revision))
}

func diffValues(pointer string, before, after any, changes *[]diffChange) {
	beforeMap, beforeIsMap := before.(map[string]any)
	afterMap, afterIsMap := after.(map[string]any)
	if beforeIsMap && afterIsMap {
		keys := map[string]bool{}
		for key := range beforeMap {
			keys[key] = true
		}
		for key := range afterMap {
			keys[key] = true
		}
		sorted := make([]string, 0, len(keys))
		for key := range keys {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		for _, key := range sorted {
			child := pointer + "/" + escapePointer(key)
			beforeValue, inBefore := beforeMap[key]
			afterValue, inAfter := afterMap[key]
			switch {
			case !inBefore:
				*changes = append(*changes, diffChange{Pointer: child, Kind: "added", After: afterValue})
			case !inAfter:
				*changes = append(*changes, diffChange{Pointer: child, Kind: "removed", Before: beforeValue})
			default:
				diffValues(child, beforeValue, afterValue, changes)
			}
		}
		return
	}
	if !reflect.DeepEqual(before, after) {
		*changes = append(*changes, diffChange{Pointer: pointer, Kind: "changed", Before: before, After: after})
	}
}

type importResult struct {
	ContentID string       `json:"content_id"`
	Status    string       `json:"status"`
	DraftID   string       `json:"draft_id,omitempty"`
	Errors    []fieldIssue `json:"errors"`
}

// importDefinitions 只创建新草稿：已有草稿的内容报告冲突，从不覆盖。
func (h *contentHandler) importDefinitions(w http.ResponseWriter, r *http.Request) {
	if !h.exactOrigin(w, r) {
		return
	}
	current, ok := h.requestSession(w, r, true)
	if !ok {
		return
	}
	gameID := r.PathValue("gameId")
	if !h.requireRole(w, r, current, gameID, RoleAuthor) {
		return
	}
	var input struct {
		DryRun      bool              `json:"dry_run"`
		Definitions []json.RawMessage `json:"definitions"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Definitions) == 0 || len(input.Definitions) > maxImportDefinitions {
		writeFailure(w, http.StatusBadRequest, "invalid_import", fmt.Sprintf("一次导入 1–%d 个定义", maxImportDefinitions), nil)
		return
	}
	results := make([]importResult, 0, len(input.Definitions))
	seen := map[string]bool{}
	for _, raw := range input.Definitions {
		var identity struct {
			ContentID string `json:"content_id"`
		}
		_ = json.Unmarshal(raw, &identity)
		result := importResult{ContentID: identity.ContentID, Errors: []fieldIssue{}}
		if !validContentID(identity.ContentID) || seen[identity.ContentID] {
			result.Status = "invalid"
			result.Errors = append(result.Errors, fieldIssue{Pointer: "/content_id", Code: "invalid_value", Message: "内容 ID 无效或在本批次重复"})
			results = append(results, result)
			continue
		}
		seen[identity.ContentID] = true
		definition, issues := h.prepareDefinition(gameID, identity.ContentID, 1, raw)
		if len(issues) != 0 {
			result.Status = "invalid"
			result.Errors = issues
			results = append(results, result)
			continue
		}
		existing, _, err := h.store.ListDrafts(r.Context(), gameID, identity.ContentID, 1, 0)
		if err != nil {
			h.internalError(w, "check import conflict", err)
			return
		}
		if len(existing) != 0 {
			result.Status = "conflict"
			result.DraftID = existing[0].DraftID
			results = append(results, result)
			continue
		}
		if input.DryRun {
			result.Status = "would_create"
			results = append(results, result)
			continue
		}
		draft, err := h.store.CreateDraft(r.Context(), gameID, identity.ContentID, definition, current.value.AdminID)
		if errors.Is(err, ErrConflict) {
			result.Status = "conflict"
			results = append(results, result)
			continue
		}
		if err != nil {
			h.internalError(w, "create imported draft", err)
			return
		}
		result.Status = "created"
		result.DraftID = draft.DraftID
		results = append(results, result)
	}
	if err := h.store.WriteAudit(r.Context(), AuditEvent{
		GameID: gameID, ActorID: current.value.AdminID, Action: "content.import", Resource: "content_import", ResourceID: gameID,
		Outcome: "success", DetailsJSON: []byte(`{"count":` + strconv.Itoa(len(results)) + `,"dry_run":` + strconv.FormatBool(input.DryRun) + `}`),
	}); err != nil {
		h.internalError(w, "audit content import", err)
		return
	}
	writeSuccess(w, http.StatusOK, results, map[string]any{"dry_run": input.DryRun})
}

func (h *contentHandler) loadCatalog(ctx context.Context, gameID, source string) (*catalog.Catalog, []catalog.Issue, error) {
	if h.registry == nil || h.extras.Geometry == nil {
		return nil, nil, errors.New("content registry is not configured")
	}
	definitions, err := h.content.ContentDefinitions(ctx, gameID, source)
	if err != nil {
		return nil, nil, err
	}
	entries := make([]catalog.Entry, 0, len(definitions))
	for _, definition := range definitions {
		contentType, err := schema.ContentTypeOf(definition.ContentID)
		if err != nil {
			continue
		}
		revision := definition.Revision
		if revision < 1 {
			revision = 1
		}
		var identity struct {
			Revision int32 `json:"revision"`
		}
		if json.Unmarshal(definition.Definition, &identity) == nil && identity.Revision > 0 {
			revision = identity.Revision
		}
		entries = append(entries, catalog.Entry{ContentID: definition.ContentID, ContentType: contentType, Revision: revision, Definition: definition.Definition})
	}
	loaded, issues := catalog.Load(gameID, entries, h.registry, h.extras.Geometry)
	return loaded, issues, nil
}

func (h *contentHandler) validationReport(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requestSession(w, r, false)
	if !ok || !h.exactOrigin(w, r) || !h.requireRole(w, r, current, gameID, readableRoles...) {
		return
	}
	var input struct {
		Source string `json:"source"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Source != "draft" && input.Source != "approved" {
		writeFailure(w, http.StatusBadRequest, "invalid_source", "source 必须是 draft 或 approved", nil)
		return
	}
	loaded, issues, err := h.loadCatalog(r.Context(), gameID, input.Source)
	if err != nil {
		h.internalError(w, "build validation report", err)
		return
	}
	if issues == nil {
		issues = []catalog.Issue{}
	}
	writeSuccess(w, http.StatusOK, map[string]any{"source": input.Source, "entry_count": len(loaded.Entries()), "valid": len(issues) == 0, "issues": issues}, nil)
}

func (h *contentHandler) previewBattle(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requestSession(w, r, false)
	if !ok || !h.exactOrigin(w, r) || !h.requireRole(w, r, current, gameID, readableRoles...) {
		return
	}
	if h.extras.Preview == nil {
		writeFailure(w, http.StatusServiceUnavailable, "preview_unavailable", "预览服务未启用", nil)
		return
	}
	var input struct {
		Source  string          `json:"source"`
		Request json.RawMessage `json:"request"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Source != "draft" && input.Source != "approved" {
		writeFailure(w, http.StatusBadRequest, "invalid_source", "source 必须是 draft 或 approved", nil)
		return
	}
	loaded, issues, err := h.loadCatalog(r.Context(), gameID, input.Source)
	if err != nil {
		h.internalError(w, "load preview content", err)
		return
	}
	if len(issues) != 0 {
		writeFailureWithData(w, http.StatusUnprocessableEntity, "content_invalid", "内容存在校验错误，无法预览", issues, nil)
		return
	}
	result, err := h.extras.Preview.PreviewBattle(loaded, input.Request)
	if err != nil {
		writeFailure(w, http.StatusBadRequest, "preview_rejected", err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, result, nil)
}

func (h *contentHandler) revision(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requireAuthenticated(w, r)
	if !ok || !h.requireRole(w, r, current, gameID, readableRoles...) {
		return
	}
	contentID := r.URL.Query().Get("content_id")
	revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 32)
	if !validContentID(contentID) || err != nil || revision < 1 {
		writeFailure(w, http.StatusBadRequest, "invalid_revision", "必须指定合法的内容 ID 与正整数 revision", nil)
		return
	}
	definition, err := h.content.RevisionDefinition(r.Context(), gameID, contentID, int32(revision))
	if errors.Is(err, ErrNotFound) {
		writeFailure(w, http.StatusNotFound, "not_found", "找不到该 revision", nil)
		return
	}
	if err != nil {
		h.internalError(w, "read content revision", err)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{"content_id": contentID, "revision": revision, "definition": definition}, nil)
}
