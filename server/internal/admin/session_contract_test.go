package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionDataUsesArraysWithoutMutatingStoredRoles(t *testing.T) {
	for _, test := range []struct {
		name    string
		session Session
	}{
		{name: "匿名会话", session: Session{}},
		{name: "无角色的登录会话", session: Session{AdminID: "test-admin"}},
		{name: "空游戏角色", session: Session{AdminID: "test-admin", Roles: []GameRole{{GameID: "demo"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := (&handler{}).sessionData(test.session, "test-csrf")
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				GameRoles json.RawMessage `json:"game_roles"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatal(err)
			}
			if string(decoded.GameRoles) == "null" {
				t.Fatal("会话角色必须是 JSON 数组，不能为 null")
			}
			for _, role := range payload.GameRoles {
				if role.Roles == nil {
					t.Fatal("每个游戏的角色列表必须是非 nil 数组")
				}
			}
			if len(test.session.Roles) > 0 && test.session.Roles[0].Roles != nil {
				t.Fatal("生成会话响应不应修改存储快照")
			}
		})
	}
}

func TestAnonymousSessionHTTPReturnsEmptyGameRolesArray(t *testing.T) {
	store := &loginTestStore{handlerTestStore: &handlerTestStore{}, sessions: make(map[string]Session)}
	server := NewHandler(store, secureTestConfig(), nil, nil)
	request := httptest.NewRequest(http.MethodGet, "https://admin.local/api/v1/session", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("匿名会话状态码为 %d，期望 200", response.Code)
	}
	var payload struct {
		Data struct {
			GameRoles json.RawMessage `json:"game_roles"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload.Data.GameRoles) != "[]" {
		t.Fatalf("匿名角色 JSON 为 %s，期望 []", payload.Data.GameRoles)
	}
}
