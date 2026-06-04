// Package builtin 提供 CodePilot 的内置工具集。
// 本文件集中存放所有工具的 JSON Schema（手写），避免运行时反射
// 引入不可控的依赖行为。
//
// 每个 schema 是顶层 type=object 的标准 JSON Schema，可直接由
// Anthropic / OpenAI 等 LLM 供应商消费。
package builtin

import "encoding/json"

// readFileSchema — ReadFile 工具的 JSON Schema
var readFileSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "file_path": {
      "type": "string",
      "description": "要读取的文件路径（相对工作目录或绝对路径）"
    },
    "offset": {
      "type": "integer",
      "description": "起始行号（0-based），默认 0"
    },
    "limit": {
      "type": "integer",
      "description": "最多返回的行数，默认 2000"
    }
  },
  "required": ["file_path"]
}`)

// writeFileSchema — WriteFile 工具的 JSON Schema
var writeFileSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "file_path": {
      "type": "string",
      "description": "要写入的文件路径（相对工作目录或绝对路径）"
    },
    "content": {
      "type": "string",
      "description": "写入文件的完整内容（覆盖式写入）"
    }
  },
  "required": ["file_path", "content"]
}`)

// bashSchema — Bash 工具的 JSON Schema
var bashSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "要执行的 shell 命令字符串"
    },
    "timeout": {
      "type": "integer",
      "description": "单次执行超时秒数（覆盖全局默认 30s），0 表示使用默认"
    }
  },
  "required": ["command"]
}`)

// globSchema — Glob 工具的 JSON Schema
var globSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "glob 模式，支持 ** 递归（如 src/**/*.go）"
    },
    "path": {
      "type": "string",
      "description": "基准目录，相对工作目录解析，默认工作目录根"
    }
  },
  "required": ["pattern"]
}`)

// grepSchema — Grep 工具的 JSON Schema
var grepSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "正则表达式（Go RE2 语法）"
    },
    "path": {
      "type": "string",
      "description": "搜索根目录，相对工作目录解析，默认工作目录根"
    },
    "include": {
      "type": "string",
      "description": "文件名 glob 过滤（如 *.go），仅匹配此 glob 的文件会被扫描"
    }
  },
  "required": ["pattern"]
}`)

// editFileSchema — EditFile 工具的 JSON Schema
var editFileSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "file_path": {
      "type": "string",
      "description": "要编辑的文件路径（相对工作目录或绝对路径）"
    },
    "old_string": {
      "type": "string",
      "description": "要被替换的原文本（必须精确匹配，包括缩进）"
    },
    "new_string": {
      "type": "string",
      "description": "替换后的新文本（设为空字符串可删除 old_string）"
    }
  },
  "required": ["file_path", "old_string", "new_string"]
}`)
