package runtime

import "example.com/tbcg/server/internal/content/catalog"

// DialogAction 是 NPC 对话选项触发的领域动作；npc 与 option 均来自当前 Release。
type DialogAction func(c *Ctx, npc *catalog.NPC, option catalog.DialogOption, key string) (any, *ProtocolError)

var dialogActions = map[string]DialogAction{}

// RegisterDialogAction 由领域模块注册对话动作处理器。
func RegisterDialogAction(kind string, fn DialogAction) {
	registryMu.Lock()
	defer registryMu.Unlock()
	dialogActions[kind] = fn
}

// LookupDialogAction 返回动作处理器。
func LookupDialogAction(kind string) (DialogAction, bool) {
	fn, ok := dialogActions[kind]
	return fn, ok
}

// ConditionChecker 判断对话条件；由任务模块注册（需要读取任务状态）。
var ConditionChecker func(c *Ctx, condition *catalog.Condition) bool
