# ok-gobot Architecture Contract

## 1. Scope

This document defines the active architecture contract for the chat/jobs runtime path.
For configuration, this document is the source of truth.

The legacy hub/subagent runtime (`internal/agent/runtime.go`, the Telegram
`hub_handler` flow, and legacy control-server sub-agent surfaces) is frozen for
compatibility only. New feature work must target the chat/jobs path backed by
`internal/runtime`, not the legacy runtime.

## 2. Active Runtime Contract

- Chat ingress and scheduled jobs are the product execution surfaces.
- `internal/runtime` owns mailbox scheduling, queueing, cancellation, and child completion routing.
- Different session keys execute in parallel.
- Each session key executes one run at a time.
- Transport layers submit work; they do not execute model logic directly.

## 3. Session Model

Canonical session keys:

- `agent:<agentId>:main`
- `agent:<agentId>:telegram:dm:<userId>` when DM scope is per-user
- `agent:<agentId>:telegram:group:<chatId>`
- `agent:<agentId>:telegram:group:<chatId>:thread:<topicId>`
- `agent:<agentId>:subagent:<runSlug>`

## 4. Adapters and Workers

- Telegram, control/TUI, and jobs are adapters over chat/jobs requests and runtime events.
- Adapters handle input/output rendering and acknowledgments.
- Execution, queueing, cancellation, and child completion routing stay in `internal/runtime`.

## 5. Control Plane

The control server provides loopback API/WS access for status, session operations,
abort, and chat/job control. Legacy sub-agent RPC surfaces remain available only
as frozen compatibility shims.

## 6. Legacy Freeze Policy

- `internal/agent.RuntimeHub` is legacy compatibility code.
- `browser_task`, `/task`, and legacy control-server sub-agent helpers may still depend on it today.
- Keep changes there limited to bug fixes or removal prep; do not add new product surface area.

## 7. Persistence

SQLite remains the persistence layer for sessions, messages, routes, and runtime metadata.

## 8. Memory

Memory remains markdown-first (`MEMORY.md` + `memory/*.md`) with semantic indexing for retrieval.

## 9. Configuration Reference (Canonical)

`config.schema.json` is generated from the canonical JSON block below. This section is the
single source of truth for configuration keys, types, defaults, and descriptions.

<!-- CONFIG_CANONICAL:START -->
```json
{
  "type": "object",
  "default": {},
  "description": "Root ok-gobot configuration.",
  "properties": {
    "telegram": {
      "type": "object",
      "default": {},
      "description": "Telegram transport settings.",
      "properties": {
        "token": {
          "type": "string",
          "default": "",
          "description": "Bot token from @BotFather. Required to run Telegram transport."
        },
        "webhook": {
          "type": "string",
          "default": "",
          "description": "Optional webhook URL. Empty means polling mode."
        }
      }
    },
    "ai": {
      "type": "object",
      "default": {},
      "description": "Primary model provider configuration.",
      "properties": {
        "provider": {
          "type": "string",
          "default": "openrouter",
          "enum": [
            "openrouter",
            "openai",
            "anthropic",
            "chatgpt",
            "droid",
            "custom"
          ],
          "description": "AI provider backend."
        },
        "api_key": {
          "type": "string",
          "default": "",
          "description": "API key for the selected provider."
        },
        "model": {
          "type": "string",
          "default": "moonshotai/kimi-k2.5",
          "description": "Default model identifier."
        },
        "base_url": {
          "type": "string",
          "default": "",
          "description": "Base URL for custom OpenAI-compatible providers."
        },
        "fallback_models": {
          "type": "array",
          "default": [],
          "description": "Ordered model fallbacks for automatic failover.",
          "items": {
            "type": "string",
            "default": "",
            "description": "Fallback model identifier."
          }
        },
        "default_thinking": {
          "type": "string",
          "default": "",
          "description": "Default thinking level for providers/models that support it."
        },
        "droid": {
          "type": "object",
          "default": {},
          "description": "Settings for factory.ai droid provider (provider=droid).",
          "properties": {
            "binary_path": {
              "type": "string",
              "default": "droid",
              "description": "Path to droid binary."
            },
            "auto_level": {
              "type": "string",
              "default": "",
              "enum": ["", "low", "medium", "high"],
              "description": "Droid autonomy level."
            },
            "work_dir": {
              "type": "string",
              "default": "",
              "description": "Working directory for droid execution."
            }
          }
        }
      }
    },
    "auth": {
      "type": "object",
      "default": {},
      "description": "Authorization and operator controls.",
      "properties": {
        "mode": {
          "type": "string",
          "default": "open",
          "enum": [
            "open",
            "allowlist",
            "pairing"
          ],
          "description": "Access mode for incoming users."
        },
        "allowed_users": {
          "type": "array",
          "default": [],
          "description": "Telegram user IDs allowed in allowlist mode.",
          "items": {
            "type": "integer",
            "default": 0,
            "description": "Telegram user ID."
          }
        },
        "admin_id": {
          "type": "integer",
          "default": 0,
          "description": "Telegram user ID allowed to manage auth."
        }
      }
    },
    "api": {
      "type": "object",
      "default": {},
      "description": "HTTP API server settings.",
      "properties": {
        "enabled": {
          "type": "boolean",
          "default": false,
          "description": "Enable HTTP API server."
        },
        "port": {
          "type": "integer",
          "default": 8080,
          "description": "HTTP API listen port."
        },
        "api_key": {
          "type": "string",
          "default": "",
          "description": "Bearer API key for HTTP endpoints."
        },
        "webhook_chat": {
          "type": "integer",
          "default": 0,
          "description": "Default chat ID for API webhook forwarding."
        }
      }
    },
    "control": {
      "type": "object",
      "default": {},
      "description": "Loopback control server settings.",
      "properties": {
        "enabled": {
          "type": "boolean",
          "default": false,
          "description": "Enable control server (disabled by default for security)."
        },
        "port": {
          "type": "integer",
          "default": 8787,
          "description": "Control server listen port."
        },
        "token": {
          "type": "string",
          "default": "",
          "description": "Bearer token for control endpoints."
        },
        "allow_loopback_without_token": {
          "type": "boolean",
          "default": true,
          "description": "Allow unauthenticated loopback access when token is empty."
        }
      }
    },
    "runtime": {
      "type": "object",
      "default": {},
      "description": "Mailbox runtime settings for the active chat/jobs path.",
      "properties": {
        "session_queue_limit": {
          "type": "integer",
          "default": 100,
          "description": "Per-session queue capacity for chat/jobs mailbox execution."
        }
      }
    },
    "session": {
      "type": "object",
      "default": {},
      "description": "Session-key resolution settings.",
      "properties": {
        "dm_scope": {
          "type": "string",
          "default": "main",
          "enum": [
            "main",
            "per_user"
          ],
          "description": "DM session scope: shared main session or per-user session keys."
        }
      }
    },
    "groups": {
      "type": "object",
      "default": {},
      "description": "Group-chat behavior settings.",
      "properties": {
        "default_mode": {
          "type": "string",
          "default": "standby",
          "enum": [
            "active",
            "standby"
          ],
          "description": "Default group mode for new groups."
        }
      }
    },
    "tts": {
      "type": "object",
      "default": {},
      "description": "Text-to-speech provider settings.",
      "properties": {
        "provider": {
          "type": "string",
          "default": "openai",
          "enum": [
            "openai",
            "edge"
          ],
          "description": "Default TTS provider."
        },
        "default_voice": {
          "type": "string",
          "default": "",
          "description": "Provider-specific default TTS voice."
        }
      }
    },
    "memory": {
      "type": "object",
      "default": {},
      "description": "Semantic memory and embeddings settings.",
      "properties": {
        "enabled": {
          "type": "boolean",
          "default": false,
          "description": "Enable semantic memory index and tools."
        },
        "embeddings_base_url": {
          "type": "string",
          "default": "https://api.openai.com/v1",
          "description": "Base URL for embeddings API."
        },
        "embeddings_api_key": {
          "type": "string",
          "default": "",
          "description": "Embeddings API key. Empty reuses ai.api_key."
        },
        "embeddings_model": {
          "type": "string",
          "default": "text-embedding-3-small",
          "description": "Embeddings model identifier."
        }
      }
    },
    "agents": {
      "type": "array",
      "default": [],
      "description": "Optional multi-agent profile definitions.",
      "items": {
        "type": "object",
        "default": {},
        "description": "Single agent profile.",
        "properties": {
          "name": {
            "type": "string",
            "default": "",
            "description": "Agent profile name used by /agent."
          },
          "soul_path": {
            "type": "string",
            "default": "",
            "description": "Directory containing this agent's personality files."
          },
          "model": {
            "type": "string",
            "default": "",
            "description": "Optional model override for this agent."
          },
          "allowed_tools": {
            "type": "array",
            "default": [],
            "description": "Tool allowlist for this agent. Empty means all tools.",
            "items": {
              "type": "string",
              "default": "",
              "description": "Tool name."
            }
          },
          "capabilities": {
            "type": "object",
            "default": {},
            "description": "Optional fine-grained capability policy. Empty/absent = fully permissive.",
            "properties": {
              "shell": {
                "type": "boolean",
                "default": true,
                "description": "Allow shell execution tools (local, ssh)."
              },
              "network": {
                "type": "boolean",
                "default": true,
                "description": "Allow network tools (web_fetch, search, browser)."
              },
              "network_allowlist": {
                "type": "array",
                "default": [],
                "description": "Allowed hostnames when network is true. Empty = all allowed.",
                "items": {
                  "type": "string",
                  "default": "",
                  "description": "Hostname."
                }
              },
              "cron": {
                "type": "boolean",
                "default": true,
                "description": "Allow cron scheduling."
              },
              "memory_write": {
                "type": "boolean",
                "default": true,
                "description": "Allow memory write tools."
              },
              "spawn": {
                "type": "boolean",
                "default": true,
                "description": "Allow sub-agent/job spawning (browser_task)."
              },
              "filesystem_roots": {
                "type": "array",
                "default": [],
                "description": "Allowed absolute filesystem paths. Empty = no restriction.",
                "items": {
                  "type": "string",
                  "default": "",
                  "description": "Absolute directory path."
                }
              },
              "file_write_scope": {
                "type": "string",
                "default": "full",
                "enum": ["full", "read_only"],
                "description": "File write scope: full allows read/write, read_only blocks writes."
              }
            }
          }
        }
      }
    },
    "model_aliases": {
      "type": "object",
      "default": {},
      "description": "Optional model alias overrides. Empty uses built-in aliases.",
      "additionalProperties": {
        "type": "string",
        "default": "",
        "description": "Model identifier for the alias key."
      }
    },
    "storage_path": {
      "type": "string",
      "default": "~/.ok-gobot/ok-gobot.db",
      "description": "SQLite database file path."
    },
    "log_level": {
      "type": "string",
      "default": "info",
      "enum": [
        "debug",
        "info",
        "warn",
        "error"
      ],
      "description": "Minimum log severity to emit."
    },
    "soul_path": {
      "type": "string",
      "default": "~/ok-gobot-soul",
      "description": "Default personality directory (deprecated; prefer agents[*].soul_path)."
    }
  }
}
```
<!-- CONFIG_CANONICAL:END -->

### 9.1 PRD Extensions

PRD adds rollout-specific configuration extensions to the canonical reference:

- `session.dm_scope`
- `runtime.session_queue_limit`

These keys remain part of the canonical schema above and must stay synchronized with PRD language.

### 9.2 Compatibility Notes

Legacy `runtime.mode` values (`hub`, `legacy`) are still accepted on load as
ignored compatibility aliases for older config files, but they are not canonical
keys and are intentionally excluded from the schema.

Legacy `openai.api_key` and `openai.model` are still accepted as migration aliases,
but they are not canonical keys and are intentionally excluded from the schema.
