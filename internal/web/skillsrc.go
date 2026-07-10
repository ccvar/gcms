package web

import _ "embed"

// 技能包 CLI 脚本单一来源：真实 .js 文件（可 node --check、可直接编辑），
// 经 go:embed 进二进制。历史教训：脚本曾以 Go 字符串内嵌 + repo skills/ 目录各存一份，
// 两份多次漂移（发出去的和仓库里的不一致）。现在 skillsrc/ 是唯一事实，
// skills/gcms-content-assistant/scripts/gcms.js 只是它的镜像拷贝，由
// TestSkillScriptMirrorsInSync 强制保持一致。

//go:embed skillsrc/gcms_single.js
var skillScriptSingle string

//go:embed skillsrc/gcms_platform.js
var skillScriptPlatform string
