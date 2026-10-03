@tool
class_name MapAnchor2D
extends Marker2D

@export var placement_id := ""
@export_enum("spawn", "npc", "portal", "trigger", "region") var kind := "spawn"
@export var extent := Vector2i.ONE
