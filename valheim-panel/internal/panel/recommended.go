package panel

import "context"

type recommendedDefinition struct {
	Key         string
	Owner       string
	Name        string
	Description string
	Icon        string
}

var recommendedDefinitions = []recommendedDefinition{
	{
		Key: "denikson-bepinexpack_valheim", Owner: "denikson", Name: "BepInExPack_Valheim",
		Description: "Valheim 模组加载框架，绝大多数 BepInEx 模组的前置。",
	},
	{
		Key: "valheimmodding-jotunn", Owner: "ValheimModding", Name: "Jotunn",
		Description: "Valheim 模组开发与运行框架，大量服务器模组的公共依赖。",
	},
	{
		Key: "blacks7ar-serversync", Owner: "blacks7ar", Name: "ServerSync",
		Description: "服务端配置同步组件，常用于统一客户端与服务端参数。",
	},
	{
		Key: "valheimplus-valheimplus", Owner: "ValheimPlus", Name: "ValheimPlus",
		Description: "大型服务器配置模组，包含建造、战斗、资源和联机规则调整。",
	},
	{
		Key: "advize-planteverything", Owner: "Advize", Name: "PlantEverything",
		Description: "扩展可种植植物和资源，适合长期经营型服务器。",
	},
	{
		Key: "azumatt-azuextendedplayerinventory", Owner: "Azumatt", Name: "AzuExtendedPlayerInventory",
		Description: "扩展玩家装备和背包栏位，常用于大型整合包。",
	},
	{
		Key: "randyknapp-epicloot", Owner: "RandyKnapp", Name: "EpicLoot",
		Description: "装备词条、魔法效果和掉落系统扩展，适合 RPG 风格服务器。",
	},
	{
		Key: "marf-betterui", Owner: "Marf", Name: "BetterUI",
		Description: "增强物品信息、耐久、食物和状态提示的常用客户端模组。",
	},
}

func (m *Manager) RecommendedMods(ctx context.Context, limit int) []Package {
	if limit <= 0 || limit > len(recommendedDefinitions) {
		limit = len(recommendedDefinitions)
	}
	result := make([]Package, 0, limit)
	for _, definition := range recommendedDefinitions[:limit] {
		pkg := Package{
			Key:         definition.Key,
			Owner:       definition.Owner,
			Name:        definition.Name,
			FullName:    definition.Owner + "-" + definition.Name,
			Description: definition.Description,
			Icon:        definition.Icon,
			Version:     "推荐",
		}
		if !m.demo {
			if fetched, err := m.thunder.FindPackage(ctx, definition.Key); err == nil {
				if fetched.Description != "" {
					pkg.Description = fetched.Description
				}
				pkg.Icon = fetched.Icon
				pkg.Version = fetched.Version
				pkg.DownloadURL = fetched.DownloadURL
				pkg.Dependencies = fetched.Dependencies
			}
		} else {
			if fetched, err := findDemoPackage(definition.Key); err == nil {
				pkg = fetched
				pkg.Description = definition.Description
			}
		}
		result = append(result, pkg)
	}
	return result
}
