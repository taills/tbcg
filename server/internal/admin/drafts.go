package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/taills/tbcg/server/internal/content/schema"
)

var readableRoles = []Role{RoleReader, RoleAuthor, RoleReviewer, RolePublisher}

func (h *handler) createDraft(w http.ResponseWriter, r *http.Request) {
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
		ContentID            string          `json:"content_id"`
		ExpectedDraftVersion string          `json:"expected_draft_version"`
		Definition           json.RawMessage `json:"definition"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ExpectedDraftVersion != "0" || !validContentID(input.ContentID) {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "草稿创建请求无效", []fieldIssue{{Pointer: "/content_id", Code: "invalid_value", Message: "内容 ID 或初始版本无效"}})
		return
	}
	definition, issues := h.prepareDefinition(gameID, input.ContentID, 1, input.Definition)
	if len(issues) != 0 {
		h.writeValidationFailure(w, r, current, gameID, input.ContentID, issues)
		return
	}
	draft, err := h.store.CreateDraft(r.Context(), gameID, input.ContentID, definition, current.value.AdminID)
	if errors.Is(err, ErrConflict) {
		writeFailure(w, http.StatusConflict, "draft_exists", "该内容已有草稿，请打开现有草稿继续编辑", nil)
		return
	}
	if err != nil {
		h.internalError(w, "create item draft", err)
		return
	}
	writeSuccess(w, http.StatusCreated, draft, nil)
}

func (h *handler) listDrafts(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requireAuthenticated(w, r)
	if !ok || !h.requireRole(w, r, current, gameID, readableRoles...) {
		return
	}
	query := r.URL.Query()
	contentIDs := query["content_id"]
	if len(contentIDs) != 1 || !validContentID(contentIDs[0]) {
		writeFailure(w, http.StatusBadRequest, "invalid_content_id", "必须指定合法的精确内容 ID", nil)
		return
	}
	limit := 50
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeFailure(w, http.StatusBadRequest, "invalid_limit", "分页大小必须介于 1 和 100 之间", nil)
			return
		}
		limit = parsed
	}
	offset := 0
	if value := query.Get("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 1000000 {
			writeFailure(w, http.StatusBadRequest, "invalid_offset", "分页偏移必须介于 0 和 1000000 之间", nil)
			return
		}
		offset = parsed
	}
	drafts, total, err := h.store.ListDrafts(r.Context(), gameID, contentIDs[0], limit, offset)
	if err != nil {
		h.internalError(w, "list item drafts for recovery", err)
		return
	}
	writeSuccess(w, http.StatusOK, drafts, map[string]any{"total": total, "limit": limit, "offset": offset})
}

func (h *handler) getDraft(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requireAuthenticated(w, r)
	if !ok || !h.requireRole(w, r, current, gameID, readableRoles...) {
		return
	}
	draft, err := h.store.GetDraft(r.Context(), gameID, r.PathValue("draftId"))
	if errors.Is(err, ErrNotFound) {
		writeFailure(w, http.StatusNotFound, "not_found", "找不到该内容草稿", nil)
		return
	}
	if err != nil {
		h.internalError(w, "read item draft", err)
		return
	}
	writeSuccess(w, http.StatusOK, draft, nil)
}

func (h *handler) updateDraft(w http.ResponseWriter, r *http.Request) {
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
		ExpectedDraftVersion string          `json:"expected_draft_version"`
		Definition           json.RawMessage `json:"definition"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	expected, err := parseDraftVersion(input.ExpectedDraftVersion)
	if err != nil || expected == 0 {
		writeFailure(w, http.StatusBadRequest, "invalid_draft_version", "草稿版本必须是非零十进制字符串", []fieldIssue{{Pointer: "/expected_draft_version", Code: "invalid_value", Message: "草稿版本格式无效"}})
		return
	}
	currentDraft, err := h.store.GetDraft(r.Context(), gameID, r.PathValue("draftId"))
	if errors.Is(err, ErrNotFound) {
		writeFailure(w, http.StatusNotFound, "not_found", "找不到该内容草稿", nil)
		return
	}
	if err != nil {
		h.internalError(w, "read draft before update", err)
		return
	}
	definition, issues := h.prepareDefinition(gameID, currentDraft.ContentID, 1, input.Definition)
	if len(issues) != 0 {
		h.writeValidationFailure(w, r, current, gameID, currentDraft.ContentID, issues)
		return
	}
	draft, latest, err := h.store.CompareAndSwapDraft(r.Context(), gameID, currentDraft.DraftID, expected, definition, current.value.AdminID)
	if errors.Is(err, ErrConflict) {
		writeFailureWithData(w, http.StatusConflict, "draft_version_conflict", "草稿已被其他编辑者更新；请比较后明确重应用", latest, nil)
		return
	}
	if err != nil {
		h.internalError(w, "update item draft with CAS", err)
		return
	}
	writeSuccess(w, http.StatusOK, draft, nil)
}

func (h *handler) validateDraft(w http.ResponseWriter, r *http.Request) {
	if !h.exactOrigin(w, r) || !ensureEmptyBody(w, r) {
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
	draft, err := h.store.GetDraft(r.Context(), gameID, r.PathValue("draftId"))
	if errors.Is(err, ErrNotFound) {
		writeFailure(w, http.StatusNotFound, "not_found", "找不到该内容草稿", nil)
		return
	}
	if err != nil {
		h.internalError(w, "read draft for validation", err)
		return
	}
	_, issues := h.prepareDefinition(gameID, draft.ContentID, 1, draft.Definition)
	outcome := "success"
	if len(issues) != 0 {
		outcome = "failure"
	}
	if err := h.store.WriteAudit(r.Context(), AuditEvent{
		GameID: gameID, ActorID: current.value.AdminID, Action: "item.validation", Resource: "item_draft", ResourceID: draft.ContentID,
		Outcome: outcome, DetailsJSON: []byte(`{"issue_count":` + strconv.Itoa(len(issues)) + `}`),
	}); err != nil {
		h.internalError(w, "audit draft validation result", err)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{"valid": len(issues) == 0, "errors": issues}, nil)
}

func (h *handler) submitDraft(w http.ResponseWriter, r *http.Request) {
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
		ExpectedDraftVersion string `json:"expected_draft_version"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	expected, err := parseDraftVersion(input.ExpectedDraftVersion)
	if err != nil || expected == 0 {
		writeFailure(w, http.StatusBadRequest, "invalid_draft_version", "草稿版本必须是非零十进制字符串", []fieldIssue{{Pointer: "/expected_draft_version", Code: "invalid_value", Message: "草稿版本格式无效"}})
		return
	}
	draft, err := h.store.GetDraft(r.Context(), gameID, r.PathValue("draftId"))
	if errors.Is(err, ErrNotFound) {
		writeFailure(w, http.StatusNotFound, "not_found", "找不到该内容草稿", nil)
		return
	}
	if err != nil {
		h.internalError(w, "read draft before submit", err)
		return
	}
	if draft.DraftVersion != input.ExpectedDraftVersion {
		writeFailureWithData(w, http.StatusConflict, "draft_version_conflict", "草稿已被其他编辑者更新；请比较后明确重应用", &draft, nil)
		return
	}
	if _, issues := h.prepareDefinition(gameID, draft.ContentID, 1, draft.Definition); len(issues) != 0 {
		h.writeValidationFailure(w, r, current, gameID, draft.ContentID, issues)
		return
	}
	revision, err := h.store.SubmitRevision(r.Context(), gameID, draft.DraftID, expected, current.value.AdminID)
	if errors.Is(err, ErrConflict) {
		latest, latestErr := h.store.GetDraft(r.Context(), gameID, draft.DraftID)
		if latestErr != nil && !errors.Is(latestErr, ErrNotFound) {
			h.internalError(w, "read latest draft after submit conflict", latestErr)
			return
		}
		writeFailureWithData(w, http.StatusConflict, "draft_version_conflict", "该草稿版本已更新或已提交；请重新读取后继续", latest, nil)
		return
	}
	if err != nil {
		h.internalError(w, "submit immutable item revision", err)
		return
	}
	writeSuccess(w, http.StatusCreated, revision, nil)
}

func (h *handler) reviewQueue(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requireAuthenticated(w, r)
	if !ok || !h.requireRole(w, r, current, gameID, RoleReviewer) {
		return
	}
	limit, offset, ok := parsePage(w, r)
	if !ok {
		return
	}
	revisions, total, err := h.store.ReviewQueue(r.Context(), gameID, limit, offset)
	if err != nil {
		h.internalError(w, "list review queue", err)
		return
	}
	writeSuccess(w, http.StatusOK, revisions, map[string]any{"total": total, "limit": limit, "offset": offset})
}

func (h *handler) reviewRevision(w http.ResponseWriter, r *http.Request) {
	if !h.exactOrigin(w, r) {
		return
	}
	current, ok := h.requestSession(w, r, true)
	if !ok {
		return
	}
	gameID := r.PathValue("gameId")
	if !h.requireRole(w, r, current, gameID, RoleReviewer) {
		return
	}
	var input struct {
		ContentID string `json:"content_id"`
		Revision  int32  `json:"revision"`
		BodyHash  string `json:"body_hash"`
		Decision  string `json:"decision"`
		Comment   string `json:"comment"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	bodyHash, err := hex.DecodeString(input.BodyHash)
	if err != nil || len(bodyHash) != sha256.Size || input.Revision < 1 || !validContentID(input.ContentID) || (input.Decision != "approved" && input.Decision != "rejected") || len(input.Comment) > 2000 {
		writeFailure(w, http.StatusBadRequest, "invalid_review", "审核请求无效", []fieldIssue{{Pointer: "/revision", Code: "invalid_value", Message: "必须指定合法的 revision、hash 和审核结论"}})
		return
	}
	if input.Decision == "approved" && !h.reviewableRevision(w, r, gameID, input.ContentID, input.Revision) {
		return
	}
	approval, err := h.store.ReviewRevision(r.Context(), gameID, input.ContentID, input.Revision, bodyHash, input.Decision, input.Comment, current.value.AdminID)
	if errors.Is(err, ErrNotFound) {
		writeFailure(w, http.StatusNotFound, "not_found", "找不到待审核的确切 revision", nil)
		return
	}
	if errors.Is(err, ErrForbidden) {
		writeFailure(w, http.StatusForbidden, "forbidden", "当前账号没有 Reviewer 权限", nil)
		return
	}
	if errors.Is(err, ErrConflict) {
		writeFailure(w, http.StatusConflict, "revision_conflict", "revision 已处理或 hash 与不可变内容不匹配", nil)
		return
	}
	if err != nil {
		h.internalError(w, "store exact revision approval", err)
		return
	}
	writeSuccess(w, http.StatusOK, approval, nil)
}

func (h *handler) requireAuthenticated(w http.ResponseWriter, r *http.Request) (requestSession, bool) {
	return h.requestSession(w, r, false)
}

func (h *handler) prepareDefinition(gameID, contentID string, revision int32, input []byte) ([]byte, []fieldIssue) {
	contentType, err := schema.ContentTypeOf(contentID)
	if err != nil {
		return nil, []fieldIssue{{Pointer: "/content_id", Code: "invalid_value", Message: "内容 ID 必须形如 <game>:<类型>/<名称>，且类型受支持"}}
	}
	validator := h.validator
	if contentType != schema.ContentTypeItem {
		validator, err = h.registry.For(contentType)
		if err != nil {
			validator = nil
		}
	}
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(input, &definition); err != nil || definition == nil {
		return nil, []fieldIssue{{Pointer: "", Code: "invalid_json", Message: "物品定义必须是 JSON 对象"}}
	}
	public, ok := decodeObject(definition["public"])
	if !ok {
		return nil, []fieldIssue{{Pointer: "/public", Code: "invalid_value", Message: "公开物品字段必须是对象"}}
	}
	issues := make([]fieldIssue, 0)
	setIdentity := func(target map[string]json.RawMessage, field, expected, pointer string) {
		if existing, exists := target[field]; exists {
			var value string
			if field == "revision" {
				var number int32
				if json.Unmarshal(existing, &number) != nil || number < 1 || number != revision {
					issues = append(issues, fieldIssue{Pointer: pointer + "/" + field, Code: "server_owned_field", Message: "revision 由服务端分配，不可由客户端指定"})
				}
				return
			}
			if json.Unmarshal(existing, &value) != nil || value != expected {
				issues = append(issues, fieldIssue{Pointer: pointer + "/" + field, Code: "server_owned_field", Message: "内容身份字段必须与当前 Game 和内容匹配"})
			}
		}
		encoded, _ := json.Marshal(expected)
		target[field] = encoded
	}
	setRevision := func(target map[string]json.RawMessage, pointer string) {
		if existing, exists := target["revision"]; exists {
			var number int32
			if json.Unmarshal(existing, &number) != nil || number < 1 || number != revision {
				issues = append(issues, fieldIssue{Pointer: pointer + "/revision", Code: "server_owned_field", Message: "revision 由服务端分配，不可由客户端指定"})
			}
		}
		encoded, _ := json.Marshal(revision)
		target["revision"] = encoded
	}
	setIdentity(definition, "game_id", gameID, "")
	setIdentity(definition, "content_id", contentID, "")
	setRevision(definition, "")
	setIdentity(public, "game_id", gameID, "/public")
	setIdentity(public, "content_id", contentID, "/public")
	setRevision(public, "/public")
	encodedPublic, err := json.Marshal(public)
	if err != nil {
		return nil, []fieldIssue{{Pointer: "/public", Code: "invalid_value", Message: "公开物品字段无效"}}
	}
	definition["public"] = encodedPublic
	encoded, err := json.Marshal(definition)
	if err != nil {
		return nil, []fieldIssue{{Pointer: "", Code: "invalid_value", Message: "物品定义无效"}}
	}
	if len(issues) != 0 {
		return nil, issues
	}
	if validator == nil {
		return nil, []fieldIssue{{Pointer: "", Code: "validator_unavailable", Message: "内容校验器暂不可用"}}
	}
	for _, issue := range validator.ValidateDefinition(encoded) {
		issues = append(issues, fieldIssue{Pointer: issue.Path, Code: issue.Code, Message: validationMessage(issue.Code)})
	}
	if contentType != schema.ContentTypeItem {
		return encoded, issues
	}
	var value struct {
		Public struct {
			IconID string `json:"icon_id"`
		} `json:"public"`
	}
	if json.Unmarshal(encoded, &value) != nil {
		issues = append(issues, fieldIssue{Pointer: "", Code: "invalid_json", Message: "物品定义无效"})
		return nil, issues
	}
	issues = append(issues, validateIconReference(value.Public.IconID, assetsForGame(h.assets, gameID))...)
	return encoded, issues
}

func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var value map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, false
	}
	return value, true
}

func validContentID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("._:/-", char)
		if !valid || index == 0 && !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func validationMessage(code string) string {
	switch code {
	case schema.CodeMissingField:
		return "必填字段缺失"
	case schema.CodeUnknownField:
		return "字段不受支持"
	case "invalid_reference":
		return "引用不存在或缺少来源许可"
	case "server_owned_field":
		return "该身份字段由服务端管理"
	default:
		return "字段值不符合内容 Schema"
	}
}

func (h *handler) writeValidationFailure(w http.ResponseWriter, r *http.Request, current requestSession, gameID, contentID string, issues []fieldIssue) {
	if err := h.store.WriteAudit(r.Context(), AuditEvent{
		GameID: gameID, ActorID: current.value.AdminID, Action: "item.validation", Resource: "item", ResourceID: contentID,
		Outcome: "failure", DetailsJSON: []byte(`{"issue_count":` + strconv.Itoa(len(issues)) + `}`),
	}); err != nil {
		h.internalError(w, "audit item validation failure", err)
		return
	}
	writeFailure(w, http.StatusUnprocessableEntity, "validation_failed", "物品内容校验失败", issues)
}

func writeFailureWithData(w http.ResponseWriter, status int, code, message string, data any, fields []fieldIssue) {
	writeJSON(w, status, envelope{
		Success: false,
		Data:    data,
		Error:   &apiError{Code: code, Message: message, Fields: nonNilFields(fields)},
		Meta:    map[string]any{},
	})
}

// reviewableRevision 在批准前用该内容类型的可信 Schema 复核不可变 revision；类型与内容不符的 revision 不能被批准。
func (h *handler) reviewableRevision(w http.ResponseWriter, r *http.Request, gameID, contentID string, revision int32) bool {
	content, ok := h.store.(ContentStore)
	if !ok {
		return true
	}
	definition, err := content.RevisionDefinition(r.Context(), gameID, contentID, revision)
	if errors.Is(err, ErrNotFound) {
		writeFailure(w, http.StatusNotFound, "not_found", "找不到待审核的确切 revision", nil)
		return false
	}
	if err != nil {
		h.internalError(w, "read revision for review validation", err)
		return false
	}
	contentType, err := schema.ContentTypeOf(contentID)
	validator, validatorErr := h.validator, error(nil)
	if h.registry != nil {
		validator, validatorErr = h.registry.For(contentType)
	} else if contentType != schema.ContentTypeItem {
		validatorErr = schema.ErrUnknownContentType
	}
	if err != nil || validatorErr != nil || validator == nil {
		writeFailure(w, http.StatusUnprocessableEntity, "revision_invalid", "revision 的内容类型不受支持", nil)
		return false
	}
	if issues := validator.ValidateDefinition(definition); len(issues) != 0 {
		fields := make([]fieldIssue, 0, len(issues))
		for _, issue := range issues {
			fields = append(fields, fieldIssue{Pointer: issue.Path, Code: issue.Code, Message: validationMessage(issue.Code)})
		}
		writeFailure(w, http.StatusUnprocessableEntity, "revision_invalid", "revision 不符合其内容类型的 Schema，不能批准", fields)
		return false
	}
	return true
}
