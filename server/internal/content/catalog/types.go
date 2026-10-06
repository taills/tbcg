// Package catalog 把已校验的 Release 内容解码为运行时只读类型并检查交叉引用。
package catalog

// Stats 是战斗属性；同一结构也用于可选加成（缺省为 0）。
type Stats struct {
	MaxHP   int64 `json:"max_hp,omitempty"`
	MaxMP   int64 `json:"max_mp,omitempty"`
	Attack  int64 `json:"attack,omitempty"`
	Defense int64 `json:"defense,omitempty"`
	Speed   int64 `json:"speed,omitempty"`
}

// Add 返回逐项相加后的新属性。
func (s Stats) Add(other Stats) Stats {
	return Stats{
		MaxHP: s.MaxHP + other.MaxHP, MaxMP: s.MaxMP + other.MaxMP, Attack: s.Attack + other.Attack,
		Defense: s.Defense + other.Defense, Speed: s.Speed + other.Speed,
	}
}

// Scale 返回加成乘以倍数后的新属性。
func (s Stats) Scale(times int64) Stats {
	return Stats{MaxHP: s.MaxHP * times, MaxMP: s.MaxMP * times, Attack: s.Attack * times, Defense: s.Defense * times, Speed: s.Speed * times}
}

type Stack struct {
	ItemID   string `json:"item_id"`
	Quantity int    `json:"quantity"`
}

type Rewards struct {
	Currency int64   `json:"currency"`
	Exp      int64   `json:"exp"`
	Items    []Stack `json:"items"`
}

type identity struct {
	GameID        string `json:"game_id"`
	ContentID     string `json:"content_id"`
	Revision      int32  `json:"revision"`
	SchemaVersion int    `json:"schema_version"`
}

type Item struct {
	identity
	Public struct {
		ItemType    string `json:"item_type"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
		IconID      string `json:"icon_id"`
		StackLimit  int    `json:"stack_limit"`
		PriceMinor  string `json:"price_minor"`
	} `json:"public"`
	Server struct {
		RewardValueMinor      string `json:"reward_value_minor"`
		DropWeightBasisPoints int    `json:"drop_weight_basis_points"`
	} `json:"server"`
}

type Portal struct {
	AnchorID        string `json:"anchor_id"`
	TargetMapID     string `json:"target_map_id"`
	TargetAnchorID  string `json:"target_anchor_id"`
	RequiredQuestID string `json:"required_quest_id,omitempty"`
}

type Zone struct {
	AnchorID    string `json:"anchor_id"`
	DisplayName string `json:"display_name"`
}

type Map struct {
	identity
	Public struct {
		DisplayName string         `json:"display_name"`
		Geometry    map[string]any `json:"geometry"`
		Portals     []Portal       `json:"portals"`
		Zones       []Zone         `json:"zones"`
	} `json:"public"`
	Server struct {
		SpawnAnchorID     string `json:"spawn_anchor_id"`
		MaxPathCells      int    `json:"max_path_cells"`
		MinStepIntervalMS int    `json:"min_step_interval_ms"`
	} `json:"server"`
}

type Archetype struct {
	identity
	Public struct {
		DisplayName  string `json:"display_name"`
		Description  string `json:"description"`
		AppearanceID string `json:"appearance_id"`
	} `json:"public"`
	Server struct {
		BaseStats       Stats    `json:"base_stats"`
		GrowthPerLevel  Stats    `json:"growth_per_level"`
		LevelExp        []int64  `json:"level_exp"`
		StartMapID      string   `json:"start_map_id"`
		StartAnchorID   string   `json:"start_anchor_id"`
		StartCurrency   int64    `json:"start_currency"`
		StartItems      []Stack  `json:"start_items"`
		DefaultSkillIDs []string `json:"default_skill_ids"`
	} `json:"server"`
}

type SkillUnlock struct {
	SkillID     string `json:"skill_id"`
	UnlockLevel int    `json:"unlock_level"`
}

type Sect struct {
	identity
	Public struct {
		DisplayName string        `json:"display_name"`
		Description string        `json:"description"`
		MinLevel    int           `json:"min_level"`
		Skills      []SkillUnlock `json:"skills"`
	} `json:"public"`
	Server struct {
		StatBonus Stats  `json:"stat_bonus"`
		JoinNPCID string `json:"join_npc_id"`
	} `json:"server"`
}

type Buff struct {
	Stat   string `json:"stat"`
	Amount int64  `json:"amount"`
	Turns  int    `json:"turns"`
}

type Skill struct {
	identity
	Public struct {
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
		Kind        string `json:"kind"`
		Target      string `json:"target"`
		MPCost      int64  `json:"mp_cost"`
	} `json:"public"`
	Server struct {
		PowerPercent int64 `json:"power_percent"`
		Hits         int   `json:"hits"`
		CritRateBP   int64 `json:"crit_rate_bp"`
		Buff         *Buff `json:"buff,omitempty"`
	} `json:"server"`
}

type Loot struct {
	ItemID   string `json:"item_id"`
	ChanceBP int64  `json:"chance_bp"`
	Quantity int    `json:"quantity"`
}

type Monster struct {
	identity
	Public struct {
		DisplayName  string `json:"display_name"`
		AppearanceID string `json:"appearance_id"`
		Level        int    `json:"level"`
	} `json:"public"`
	Server struct {
		Stats          Stats    `json:"stats"`
		SkillIDs       []string `json:"skill_ids"`
		SkillChanceBP  int64    `json:"skill_chance_bp"`
		ExpReward      int64    `json:"exp_reward"`
		CurrencyReward int64    `json:"currency_reward"`
		Loot           []Loot   `json:"loot"`
	} `json:"server"`
}

type EncounterMonster struct {
	MonsterID string `json:"monster_id"`
	Count     int    `json:"count"`
}

type Encounter struct {
	identity
	Public struct {
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
	} `json:"public"`
	Server struct {
		Monsters       []EncounterMonster `json:"monsters"`
		MaxRounds      int                `json:"max_rounds"`
		RoundTimeoutMS int                `json:"round_timeout_ms"`
		AllowFlee      bool               `json:"allow_flee"`
	} `json:"server"`
}

type Condition struct {
	QuestID     string `json:"quest_id,omitempty"`
	QuestStatus string `json:"quest_status,omitempty"`
	MinLevel    int    `json:"min_level,omitempty"`
	SectID      string `json:"sect_id,omitempty"`
	NoSect      bool   `json:"no_sect,omitempty"`
}

type Action struct {
	Kind     string `json:"kind"`
	TargetID string `json:"target_id,omitempty"`
	NextNode string `json:"next_node,omitempty"`
}

type DialogOption struct {
	OptionID  string     `json:"option_id"`
	Text      string     `json:"text"`
	Condition *Condition `json:"condition,omitempty"`
	Action    Action     `json:"action"`
}

type DialogNode struct {
	NodeID  string         `json:"node_id"`
	Text    string         `json:"text"`
	Options []DialogOption `json:"options"`
}

type NPC struct {
	identity
	Public struct {
		DisplayName  string `json:"display_name"`
		AppearanceID string `json:"appearance_id"`
		MapID        string `json:"map_id"`
		AnchorID     string `json:"anchor_id"`
	} `json:"public"`
	Server struct {
		InteractRangeCells int          `json:"interact_range_cells"`
		StartNode          string       `json:"start_node"`
		Nodes              []DialogNode `json:"nodes"`
	} `json:"server"`
}

// Node 按 ID 查找对话节点。
func (n *NPC) Node(id string) (DialogNode, bool) {
	for _, node := range n.Server.Nodes {
		if node.NodeID == id {
			return node, true
		}
	}
	return DialogNode{}, false
}

type Objective struct {
	Kind          string `json:"kind"`
	MapID         string `json:"map_id,omitempty"`
	AnchorID      string `json:"anchor_id,omitempty"`
	ItemID        string `json:"item_id,omitempty"`
	NPCID         string `json:"npc_id,omitempty"`
	EncounterID   string `json:"encounter_id,omitempty"`
	Count         int    `json:"count"`
	ProgressScope string `json:"progress_scope"`
}

type QuestStage struct {
	Objective Objective `json:"objective"`
}

type Quest struct {
	identity
	Public struct {
		DisplayName       string   `json:"display_name"`
		Description       string   `json:"description"`
		StageDescriptions []string `json:"stage_descriptions"`
		MinLevel          int      `json:"min_level"`
	} `json:"public"`
	Server struct {
		GiverNPCID    string       `json:"giver_npc_id"`
		TurnInNPCID   string       `json:"turn_in_npc_id"`
		Prerequisites []string     `json:"prerequisites"`
		RepeatPolicy  string       `json:"repeat_policy"`
		Stages        []QuestStage `json:"stages"`
		Rewards       Rewards      `json:"rewards"`
	} `json:"server"`
}

type Equipment struct {
	identity
	Public struct {
		ItemID   string   `json:"item_id"`
		Slot     string   `json:"slot"`
		MinLevel int      `json:"min_level"`
		SectIDs  []string `json:"sect_ids"`
		Stats    Stats    `json:"stats"`
	} `json:"public"`
}

type Effect struct {
	Kind     string `json:"kind"`
	Amount   int64  `json:"amount,omitempty"`
	TargetID string `json:"target_id,omitempty"`
}

type Consumable struct {
	identity
	Public struct {
		ItemID         string `json:"item_id"`
		Description    string `json:"description"`
		UsableInBattle bool   `json:"usable_in_battle"`
	} `json:"public"`
	Server struct {
		Effects []Effect `json:"effects"`
	} `json:"server"`
}

type DungeonStage struct {
	EncounterID string `json:"encounter_id"`
}

type Dungeon struct {
	identity
	Public struct {
		DisplayName string   `json:"display_name"`
		Description string   `json:"description"`
		MinPlayers  int      `json:"min_players"`
		MaxPlayers  int      `json:"max_players"`
		MinLevel    int      `json:"min_level"`
		DailyLimit  int      `json:"daily_limit"`
		StageNames  []string `json:"stage_names"`
	} `json:"public"`
	Server struct {
		EntryNPCID string         `json:"entry_npc_id"`
		Stages     []DungeonStage `json:"stages"`
		Rewards    Rewards        `json:"rewards"`
	} `json:"server"`
}

type Pet struct {
	identity
	Public struct {
		DisplayName  string   `json:"display_name"`
		AppearanceID string   `json:"appearance_id"`
		SkillIDs     []string `json:"skill_ids"`
	} `json:"public"`
	Server struct {
		BaseStats      Stats   `json:"base_stats"`
		GrowthPerLevel Stats   `json:"growth_per_level"`
		LevelExp       []int64 `json:"level_exp"`
	} `json:"server"`
}

type Partner struct {
	identity
	Public struct {
		DisplayName     string   `json:"display_name"`
		AppearanceID    string   `json:"appearance_id"`
		SkillIDs        []string `json:"skill_ids"`
		MinLevel        int      `json:"min_level"`
		RequiredQuestID string   `json:"required_quest_id,omitempty"`
		RecruitCost     int64    `json:"recruit_cost"`
	} `json:"public"`
	Server struct {
		Stats Stats `json:"stats"`
	} `json:"server"`
}

type Mount struct {
	identity
	Public struct {
		DisplayName  string `json:"display_name"`
		AppearanceID string `json:"appearance_id"`
		MinLevel     int    `json:"min_level"`
		StatBonus    Stats  `json:"stat_bonus"`
	} `json:"public"`
}

type Recipe struct {
	identity
	Public struct {
		DisplayName string  `json:"display_name"`
		Materials   []Stack `json:"materials"`
		Outputs     []Stack `json:"outputs"`
		MinLevel    int     `json:"min_level"`
		SectID      string  `json:"sect_id,omitempty"`
	} `json:"public"`
	Server struct {
		SuccessRateBP int64 `json:"success_rate_bp"`
	} `json:"server"`
}

type ShopGood struct {
	ItemID        string `json:"item_id"`
	Price         int64  `json:"price"`
	PurchaseLimit int    `json:"purchase_limit"`
}

type ShopStock struct {
	ItemID   string `json:"item_id"`
	Quantity int64  `json:"quantity"`
}

type Shop struct {
	identity
	Public struct {
		DisplayName  string     `json:"display_name"`
		NPCID        string     `json:"npc_id"`
		CurrencyCode string     `json:"currency_code"`
		Goods        []ShopGood `json:"goods"`
	} `json:"public"`
	Server struct {
		Stock []ShopStock `json:"stock"`
	} `json:"server"`
}

// Good 查找商品定义。
func (s *Shop) Good(itemID string) (ShopGood, bool) {
	for _, good := range s.Public.Goods {
		if good.ItemID == itemID {
			return good, true
		}
	}
	return ShopGood{}, false
}

type Social struct {
	identity
	Public struct {
		MaxFriends int `json:"max_friends"`
	} `json:"public"`
	Server struct {
		MaxPendingRequests int `json:"max_pending_requests"`
	} `json:"server"`
}

type Guild struct {
	identity
	Public struct {
		MaxMembers      int   `json:"max_members"`
		CreateCost      int64 `json:"create_cost"`
		CreateMinLevel  int   `json:"create_min_level"`
		NoticeMaxLength int   `json:"notice_max_length"`
	} `json:"public"`
	Server struct {
		OfficerCanInvite bool `json:"officer_can_invite"`
		OfficerCanKick   bool `json:"officer_can_kick"`
	} `json:"server"`
}

type Trade struct {
	identity
	Public struct {
		MaxItemSlots   int   `json:"max_item_slots"`
		TaxBP          int64 `json:"tax_bp"`
		TimeoutSeconds int   `json:"timeout_seconds"`
	} `json:"public"`
	Server struct {
		UntradableItemIDs []string `json:"untradable_item_ids"`
		MaxCurrency       int64    `json:"max_currency"`
	} `json:"server"`
}

type EventWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type EventObjective struct {
	Kind     string `json:"kind"`
	TargetID string `json:"target_id"`
	Count    int    `json:"count"`
}

type Event struct {
	identity
	Public struct {
		DisplayName        string         `json:"display_name"`
		Description        string         `json:"description"`
		Windows            []EventWindow  `json:"windows"`
		Objective          EventObjective `json:"objective"`
		MaxClaimsPerWindow int            `json:"max_claims_per_window"`
		Leaderboard        bool           `json:"leaderboard"`
	} `json:"public"`
	Server struct {
		Rewards Rewards `json:"rewards"`
	} `json:"server"`
}
